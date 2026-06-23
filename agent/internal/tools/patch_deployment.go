package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

type PatchDeployment struct {
	client          kubernetes.Interface
	targetNamespace string
	status          *UpdateRemediationStatus
	pending         *pendingApproval
}

type pendingApproval struct {
	deployName string
	namespace  string
	patch      json.RawMessage
	diff       string
}

func NewPatchDeployment(client kubernetes.Interface, targetNamespace string, status *UpdateRemediationStatus) *PatchDeployment {
	return &PatchDeployment{client: client, targetNamespace: targetNamespace, status: status}
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

	t.pending = &pendingApproval{
		deployName: p.DeploymentName,
		namespace:  p.Namespace,
		patch:      p.Patch,
		diff:       diff,
	}

	return diff, nil
}

func (t *PatchDeployment) Active() bool { return t.pending != nil }

func (t *PatchDeployment) AfterTurn(ctx context.Context, scanner *bufio.Scanner, out io.Writer) ([]string, bool, error) {
	p := t.pending

	fmt.Fprintf(out, "\nDiff: %s/%s\n", p.namespace, p.deployName)
	fmt.Fprintln(out, p.diff)
	fmt.Fprint(out, "\nApply? [y/n]: ")

	if !scanner.Scan() {
		t.pending = nil
		return nil, false, scanner.Err()
	}

	if strings.TrimSpace(strings.ToLower(scanner.Text())) == "y" {
		_, err := t.client.AppsV1().Deployments(p.namespace).Patch(
			ctx, p.deployName, types.StrategicMergePatchType, p.patch, metav1.PatchOptions{},
		)
		t.pending = nil
		if err != nil {
			return nil, false, fmt.Errorf("applying patch: %w", err)
		}
		fmt.Fprintln(out, "Patch applied.")
		verification := t.verifyRollout(ctx, p.namespace, p.deployName, out)
		return []string{"Patch applied. " + verification}, false, nil
	}

	fmt.Fprint(out, "Rejection reason (optional): ")
	reason := ""
	if scanner.Scan() {
		reason = strings.TrimSpace(scanner.Text())
	}
	t.pending = nil

	msg := "Human rejected the patch."
	if reason != "" {
		msg += " Reason: " + reason
	}
	return []string{msg}, false, nil
}

// verifyRollout polls the deployment's pods until all are ready or timeout.
func (t *PatchDeployment) verifyRollout(ctx context.Context, namespace, deployName string, out io.Writer) string {
	deploy, err := t.client.AppsV1().Deployments(namespace).Get(ctx, deployName, metav1.GetOptions{})
	if err != nil {
		return fmt.Sprintf("Could not verify rollout: %v", err)
	}

	selector := labels.Set(deploy.Spec.Selector.MatchLabels).String()
	timeout := 2 * time.Minute
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	fmt.Fprintln(out, "Verifying rollout (timeout 2m)...")
	for {
		pods, err := t.client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
		if err == nil && len(pods.Items) > 0 {
			ready := 0
			for _, pod := range pods.Items {
				for _, cond := range pod.Status.Conditions {
					if cond.Type == "Ready" && cond.Status == "True" {
						ready++
					}
				}
			}
			fmt.Fprintf(out, "  %d/%d pods ready\n", ready, len(pods.Items))
			if ready == len(pods.Items) {
				remStatus := "Applied"
				remNote := ""
				if t.status != nil {
					params, _ := json.Marshal(map[string]string{"phase": "Applied"})
					if _, err := t.status.Execute(ctx, params); err != nil {
						remStatus = "Applied (warning: status update failed)"
						remNote = fmt.Sprintf(" (%v)", err)
					}
				}
				names := make([]string, len(pods.Items))
				for i, p := range pods.Items {
					names[i] = p.Name
				}
				fmt.Fprintf(out, "Pod status: %d/%d ready — %s\n", ready, len(pods.Items), strings.Join(names, ", "))
				fmt.Fprintf(out, "Remediation status: %s%s\n", remStatus, remNote)
				return fmt.Sprintf("Pod status: %d/%d ready. Remediation status: %s.", ready, len(pods.Items), remStatus)
			}
		}

		if time.Now().After(deadline) {
			return "Rollout timed out after 2 minutes. Pods may still be starting."
		}
		select {
		case <-ctx.Done():
			return "Context cancelled during rollout verification."
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
			i--; j--
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
