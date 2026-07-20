package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	"github.com/sguldemond/goblin/agent/internal/messenger"
)

type PatchDeployment struct {
	ApprovalGate
	client          kubernetes.Interface
	targetNamespace string
}

func NewPatchDeployment(client kubernetes.Interface, targetNamespace string) *PatchDeployment {
	return &PatchDeployment{client: client, targetNamespace: targetNamespace}
}

func (t *PatchDeployment) Name() string { return "patchDeployment" }

func (t *PatchDeployment) Description() string {
	return "Dry-run a strategic merge patch against a Deployment and return a diff for human review. " +
		"This tool NEVER applies changes — it only computes what would change. " +
		"Containers are matched by 'name'; only include the fields you want to change — " +
		"unspecified fields are preserved automatically. " +
		"After calling this tool you will receive the diff as output: respond with Cause and Fix labels. " +
		"Call this only when confident in a fix; use escalate if unsure."
}

func (t *PatchDeployment) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"deploymentName": {"type": "string", "description": "Name of the Deployment to patch"},
			"namespace":      {"type": "string", "description": "Namespace of the Deployment"},
			"patch":          {"type": "object", "description": "Strategic merge patch body. For containers, include only 'name' (to identify) and the fields to change — all other fields are preserved automatically."}
		},
		"required": ["deploymentName", "namespace", "patch"]
	}`)
}

type patchDeploymentParams struct {
	DeploymentName string          `json:"deploymentName"`
	Namespace      string          `json:"namespace"`
	Patch          json.RawMessage `json:"patch"`
}

func (t *PatchDeployment) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var p patchDeploymentParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", fmt.Errorf("invalid params: %w", err)
	}
	if p.Namespace != t.targetNamespace {
		return "", fmt.Errorf("namespace guard: may only patch deployments in %q, got %q", t.targetNamespace, p.Namespace)
	}

	current, err := t.client.AppsV1().Deployments(p.Namespace).Get(ctx, p.DeploymentName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("getting deployment: %w", err)
	}

	dryRun, err := t.client.AppsV1().Deployments(p.Namespace).Patch(
		ctx, p.DeploymentName, types.StrategicMergePatchType, p.Patch,
		metav1.PatchOptions{DryRun: []string{"All"}},
	)
	if err != nil {
		return "", fmt.Errorf("dry-run rejected by API: %w", err)
	}

	before, _ := json.MarshalIndent(current.Spec.Template.Spec, "", "  ")
	after, _ := json.MarshalIndent(dryRun.Spec.Template.Spec, "", "  ")
	diff := lcsLineDiff(string(before), string(after), 2)

	if err := t.Stage(&StagedChange{
		Target: p.Namespace + "/" + p.DeploymentName,
		Diff:   diff,
		Apply: func(ctx context.Context) error {
			_, err := t.client.AppsV1().Deployments(p.Namespace).Patch(
				ctx, p.DeploymentName, types.StrategicMergePatchType, p.Patch, metav1.PatchOptions{},
			)
			return err
		},
		Verify: func(ctx context.Context, m messenger.Messenger) (string, bool) {
			return t.verifyRollout(ctx, p.Namespace, p.DeploymentName, m)
		},
	}); err != nil {
		return "", err
	}

	return diff, nil
}

// verifyRollout polls the Deployment's rollout status until the new revision is
// fully rolled out or timeout. It uses the Deployment's own status counters
// (the same signals as `kubectl rollout status`) rather than counting pods by
// selector, which would see old ready replicas and report success too early.
// It returns a human summary and whether the rollout actually settled; the
// caller decides what a settled rollout means for the incident.
func (t *PatchDeployment) verifyRollout(ctx context.Context, namespace, deployName string, m messenger.Messenger) (string, bool) {
	timeout := 2 * time.Minute
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	m.Send("Verifying rollout (timeout 2m)...") //nolint:errcheck
	for {
		deploy, err := t.client.AppsV1().Deployments(namespace).Get(ctx, deployName, metav1.GetOptions{})
		if err == nil {
			desired := int32(1)
			if deploy.Spec.Replicas != nil {
				desired = *deploy.Spec.Replicas
			}
			st := deploy.Status
			m.Send(fmt.Sprintf("  updated %d/%d, available %d", st.UpdatedReplicas, desired, st.AvailableReplicas)) //nolint:errcheck

			// Rollout is complete when the controller has observed the new spec,
			// every replica is the updated revision, none are stale, and all are available.
			rolledOut := st.ObservedGeneration >= deploy.Generation &&
				st.UpdatedReplicas == desired &&
				st.Replicas == desired &&
				st.AvailableReplicas == desired
			if rolledOut {
				m.Send(fmt.Sprintf("Rollout complete: %d/%d replicas updated and available.", //nolint:errcheck
					st.AvailableReplicas, desired))
				return fmt.Sprintf("Rollout complete: %d/%d replicas available.", st.AvailableReplicas, desired), true
			}
		}

		if time.Now().After(deadline) {
			return "Rollout timed out after 2 minutes. Pods may still be starting.", false
		}
		select {
		case <-ctx.Done():
			return "Context cancelled during rollout verification.", false
		case <-ticker.C:
		}
	}
}

// lcsLineDiff returns a unified-style diff of two texts, showing only changed
// lines plus ctx lines of context above and below each hunk.
func lcsLineDiff(before, after string, ctx int) string {
	a := strings.Split(before, "\n")
	b := strings.Split(after, "\n")

	// LCS table
	m, n := len(a), len(b)
	dp := make([][]int, m+1)
	for i := range dp {
		dp[i] = make([]int, n+1)
	}
	for i := 1; i <= m; i++ {
		for j := 1; j <= n; j++ {
			if a[i-1] == b[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else if dp[i-1][j] >= dp[i][j-1] {
				dp[i][j] = dp[i-1][j]
			} else {
				dp[i][j] = dp[i][j-1]
			}
		}
	}

	type edit struct {
		op   byte // '=' '-' '+'
		text string
	}
	edits := make([]edit, 0, m+n)
	i, j := m, n
	for i > 0 || j > 0 {
		switch {
		case i > 0 && j > 0 && a[i-1] == b[j-1]:
			edits = append(edits, edit{'=', a[i-1]})
			i--
			j--
		case j > 0 && (i == 0 || dp[i][j-1] >= dp[i-1][j]):
			edits = append(edits, edit{'+', b[j-1]})
			j--
		default:
			edits = append(edits, edit{'-', a[i-1]})
			i--
		}
	}
	// reverse
	for l, r := 0, len(edits)-1; l < r; l, r = l+1, r-1 {
		edits[l], edits[r] = edits[r], edits[l]
	}

	// mark lines to show (changed + ctx neighbours)
	show := make([]bool, len(edits))
	for idx, e := range edits {
		if e.op != '=' {
			for k := max(0, idx-ctx); k <= min(len(edits)-1, idx+ctx); k++ {
				show[k] = true
			}
		}
	}

	var sb strings.Builder
	skipped := 0
	for idx, e := range edits {
		if !show[idx] {
			skipped++
			continue
		}
		if skipped > 0 {
			fmt.Fprintf(&sb, "  ... (%d lines)\n", skipped)
			skipped = 0
		}
		switch e.op {
		case '=':
			fmt.Fprintf(&sb, "  %s\n", e.text)
		case '-':
			fmt.Fprintf(&sb, "- %s\n", e.text)
		case '+':
			fmt.Fprintf(&sb, "+ %s\n", e.text)
		}
	}
	if skipped > 0 {
		fmt.Fprintf(&sb, "  ... (%d lines)\n", skipped)
	}
	return strings.TrimRight(sb.String(), "\n")
}

var _ Tool = (*PatchDeployment)(nil)
var _ AfterTurnHook = (*PatchDeployment)(nil)
