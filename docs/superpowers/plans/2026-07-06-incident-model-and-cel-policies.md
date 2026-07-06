# Incident Model & CEL Policies — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace hardcoded pod-trigger detection with CEL-policy-driven detection: rename the `Remediation` CRD to `Incident`, add an `IncidentPolicy` CRD whose CEL `expression` decides what counts as an incident, and wire a controller that evaluates active policies against Pods and creates `Incident` CRs on match.

**Architecture:** An `IncidentPolicy` (cluster-scoped) names a target GVK, a `trigger` label, and a CEL `expression` over the target object. A `PolicyReconciler` compiles each policy's CEL and publishes it into a shared in-memory `Registry`. An `IncidentDetector` watches Pods; on each Pod change it evaluates every registered Pod-targeting policy's compiled program against the pod (as an unstructured map under the CEL variable `object`) and creates one `Incident` per matched target (idempotent by target-UID label). The old `PodReconciler`/`detectTrigger` is deleted; the two built-in triggers ship as preset CEL policies.

**Tech Stack:** Go 1.25, kubebuilder v4 / controller-runtime v0.23.3, `github.com/google/cel-go` v0.26.0 (already an indirect dep — promote to direct), envtest + Ginkgo for controller tests.

## Global Constraints

- Module path: `github.com/sguldemond/goblin` (operator) and `github.com/sguldemond/goblin/agent` (agent) — two Go modules.
- CRD group/version: `ops.goblinoperator.io/v1alpha1`, domain `goblinoperator.io`.
- After any change to `*_types.go` or kubebuilder markers, regenerate with `make manifests generate` from `operator/` before building/testing.
- CEL variable exposed to every expression is `object` (the full target object as `map[string]any`); expressions must evaluate to `bool`.
- Idempotency label on Incidents: `goblinoperator.io/target-uid` (value = target object UID).
- Opt-out annotation honored on every target: `goblinoperator.io/no-auto-remediate: "true"` suppresses Incident creation.
- Scope: **Pod targets only** for the detection controller in this plan. `IncidentPolicy.spec.target` is designed general (GVK), but only Pod-targeting policies are evaluated; arbitrary-GVK dynamic informers are the next plan.
- Out of scope: agent-side changes beyond the mechanical `Remediation`→`Incident` rename; permission tiers; `detect.conditions`/`event`/`builtin` rungs; `response` block. Only `detect.trigger` + `detect.expression`.

---

## Part 1 — Rename `Remediation` → `Incident`

Pure refactor. Keep the existing `PodReconciler` working through this part; it is deleted in Part 5. No behavior change — the proof is that the existing controller test suite and both Go modules still build and pass.

### Task 1: Rename the CRD Go types

**Files:**
- Rename: `operator/api/v1alpha1/remediation_types.go` → `operator/api/v1alpha1/incident_types.go`
- Modify: `operator/PROJECT`
- Regenerate: `operator/api/v1alpha1/zz_generated.deepcopy.go`

**Interfaces:**
- Produces: `Incident`, `IncidentSpec{ TargetRef corev1.ObjectReference; Trigger string; PolicyRef string }`, `IncidentStatus{ Phase IncidentPhase; Message string }`, `IncidentList`, `IncidentPhase` + phase consts (names unchanged: `PhaseDetected`, `PhaseAssessing`, `PhaseAwaitingApproval`, `PhaseApplied`, `PhaseRejected`, `PhaseEscalated`, `PhaseHandedOff`).

- [ ] **Step 1: Rewrite the types file** as `incident_types.go` (delete the old file):

```go
package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// +kubebuilder:validation:Enum=Detected;Assessing;AwaitingApproval;Applied;Rejected;Escalated;HandedOff
type IncidentPhase string

const (
	PhaseDetected         IncidentPhase = "Detected"
	PhaseAssessing        IncidentPhase = "Assessing"
	PhaseAwaitingApproval IncidentPhase = "AwaitingApproval"
	PhaseApplied          IncidentPhase = "Applied"
	PhaseRejected         IncidentPhase = "Rejected"
	PhaseEscalated        IncidentPhase = "Escalated"
	PhaseHandedOff        IncidentPhase = "HandedOff"
)

type IncidentSpec struct {
	// TargetRef points at the object the incident is about. Carries its own
	// apiVersion/kind, so it is generic across resource kinds.
	TargetRef corev1.ObjectReference `json:"targetRef"`

	// Trigger is the policy-defined name of what was detected (e.g. "OOMKilled").
	// +kubebuilder:validation:MinLength=1
	Trigger string `json:"trigger"`

	// PolicyRef is the name of the IncidentPolicy that fired, if any.
	PolicyRef string `json:"policyRef,omitempty"`
}

type IncidentStatus struct {
	Phase   IncidentPhase `json:"phase,omitempty"`
	Message string        `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Trigger",type=string,JSONPath=`.spec.trigger`
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.spec.targetRef.kind`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
type Incident struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   IncidentSpec   `json:"spec"`
	Status IncidentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type IncidentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Incident `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &Incident{}, &IncidentList{})
		return nil
	})
}
```

- [ ] **Step 2: Update `operator/PROJECT`** — change the `kind: Remediation` resource entry to `kind: Incident`.

- [ ] **Step 3: Regenerate deepcopy + manifests**

Run: `cd operator && make generate manifests`
Expected: `zz_generated.deepcopy.go` now has `Incident*` methods; `config/crd/bases/ops.goblinoperator.io_incidents.yaml` is created and the old `..._remediations.yaml` is removed (delete it manually if `make` leaves it behind).

- [ ] **Step 4: Delete stale CRD file** if present

Run: `cd operator && rm -f config/crd/bases/ops.goblinoperator.io_remediations.yaml && ls config/crd/bases/`
Expected: only `ops.goblinoperator.io_incidents.yaml` remains.

- [ ] **Step 5: Commit**

```bash
git add operator/api operator/PROJECT operator/config/crd
git commit -m "operator: rename Remediation CRD to Incident (targetRef, open trigger)"
```

### Task 2: Update the operator controllers to the Incident type

**Files:**
- Rename: `operator/internal/controller/remediation_controller.go` → `incident_controller.go`
- Modify: `operator/internal/controller/pod_controller.go`
- Modify: `operator/cmd/main.go`
- Modify: `operator/internal/controller/remediation_controller_test.go` → `incident_controller_test.go`

**Interfaces:**
- Consumes: `Incident`, `IncidentSpec`, `IncidentList` from Task 1.
- Produces: `IncidentReconciler` (was `RemediationReconciler`) with the same method set.

- [ ] **Step 1: Rename the reconciler.** In `incident_controller.go` replace throughout: `RemediationReconciler`→`IncidentReconciler`, `opsv1alpha1.Remediation`→`opsv1alpha1.Incident`, `rem`→`inc` (local vars), `remediationJobLabel` value stays but rename const to `incidentJobLabel = "goblinoperator.io/incident"`. Update all kubebuilder RBAC markers `resources=remediations` → `resources=incidents` (and `remediations/status`, `remediations/finalizers`). `SetupWithManager` uses `For(&opsv1alpha1.Incident{})` and `Named("incident")`. In `createScoutJob`, set env `INCIDENT_NAME`/`INCIDENT_NAMESPACE` (was `REMEDIATION_NAME`/`REMEDIATION_NAMESPACE`) and Job GenerateName prefix `"goblin-scout-" + inc.Name + "-"`. In `handleDetected`, drop the pod-specific `noAutoRemediate` pre-check block entirely (detection now vetoes annotated targets upstream) — go straight to the Job idempotency check.

- [ ] **Step 2: Update `pod_controller.go`** — replace `opsv1alpha1.RemediationList`→`opsv1alpha1.IncidentList`, `opsv1alpha1.Remediation`→`opsv1alpha1.Incident`, `opsv1alpha1.RemediationSpec`→`opsv1alpha1.IncidentSpec`, `PodRef:`→`TargetRef:`, RBAC markers `remediations`→`incidents`, label const `podUIDLabel`→`targetUIDLabel = "goblinoperator.io/target-uid"`. (This controller is deleted in Part 5; this keeps it compiling meanwhile.)

- [ ] **Step 3: Update `main.go`** — `controller.RemediationReconciler`→`controller.IncidentReconciler`; the setupLog `"controller", "remediation"`→`"incident"`.

- [ ] **Step 4: Update the controller test** — in `incident_controller_test.go`, `RemediationReconciler`→`IncidentReconciler`, `opsv1alpha1.Remediation`→`opsv1alpha1.Incident`, and give the created resource a valid spec so it passes MinLength validation:

```go
resource := &opsv1alpha1.Incident{
	ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: resourceNamespace},
	Spec: opsv1alpha1.IncidentSpec{
		TargetRef: corev1.ObjectReference{APIVersion: "v1", Kind: "Pod", Name: "sample-pod", Namespace: resourceNamespace},
		Trigger:   "OOMKilled",
	},
}
```
Add the `corev1 "k8s.io/api/core/v1"` import.

- [ ] **Step 5: Build + test**

Run: `cd operator && make test`
Expected: builds, `make manifests generate fmt vet` clean, controller suite passes.

- [ ] **Step 6: Commit**

```bash
git add operator/internal/controller operator/cmd/main.go
git commit -m "operator: switch controllers to Incident type"
```

### Task 3: Rename the agent's references

**Files:**
- Modify: `agent/internal/config/config.go`
- Rename: `agent/internal/tools/update_remediation_status.go` → `update_incident_status.go`
- Modify: `agent/internal/tools/all.go`, `agent/internal/scout/scout.go`, `agent/internal/scout/context.go`, `agent/internal/tools/patch_deployment.go`

**Interfaces:**
- Produces: `config.Config{ IncidentName, IncidentNamespace, ... }`; `tools.IncidentGVR` (resource `incidents`); `tools.UpdateIncidentStatus`.

- [ ] **Step 1: Config** — in `config.go`, rename struct fields `RemediationName`→`IncidentName`, `RemediationNamespace`→`IncidentNamespace`; read env `INCIDENT_NAME`/`INCIDENT_NAMESPACE`; update the error string.

- [ ] **Step 2: Status tool** — in `update_incident_status.go`, rename type `UpdateRemediationStatus`→`UpdateIncidentStatus`, constructor `NewUpdateRemediationStatus`→`NewUpdateIncidentStatus`, exported var `RemediationGVR`→`IncidentGVR` with `Resource: "incidents"`, and the doc comment.

- [ ] **Step 3: Propagate renames** — update `all.go` (`NewUpdateRemediationStatus`→`NewUpdateIncidentStatus`, `remediationName/Namespace` params→`incidentName/Namespace`), `scout.go` (`tools.RemediationGVR`→`tools.IncidentGVR`, `s.cfg.RemediationName/Namespace`→`IncidentName/Namespace`, `loadIncident` reads `spec.targetRef` instead of `spec.podRef`: `targetRef, _, _ := unstructuredMap(spec, "targetRef")` and populate `PodName`/`PodNamespace` from `targetRef.name`/`targetRef.namespace`), `context.go` (`*UpdateRemediationStatus`→`*UpdateIncidentStatus` if referenced; update the `Remediation CR:` header line to say `Incident CR ... kind=Incident`), `patch_deployment.go` (the `status *UpdateRemediationStatus` field type → `*UpdateIncidentStatus`).

- [ ] **Step 4: Build**

Run: `cd agent && go build ./... && go vet ./...`
Expected: clean.

- [ ] **Step 5: Commit**

```bash
git add agent/
git commit -m "agent: follow Remediation->Incident rename (targetRef, INCIDENT_* env)"
```

---

## Part 2 — `IncidentPolicy` CRD

### Task 4: Add the IncidentPolicy API type

**Files:**
- Create: `operator/api/v1alpha1/incidentpolicy_types.go`
- Modify: `operator/PROJECT`
- Regenerate: `zz_generated.deepcopy.go`, `config/crd/bases/`

**Interfaces:**
- Produces: `IncidentPolicy`, `IncidentPolicySpec{ Target TargetGVK; Detect DetectSpec }`, `TargetGVK{ APIVersion, Kind string }`, `DetectSpec{ Trigger, Expression string }`, `IncidentPolicyStatus{ Conditions []metav1.Condition; IncidentsCreated int64; LastTriggeredAt *metav1.Time }`, `IncidentPolicyList`.

- [ ] **Step 1: Write the type** (cluster-scoped):

```go
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// TargetGVK selects which kind of object a policy watches.
type TargetGVK struct {
	// +kubebuilder:validation:MinLength=1
	APIVersion string `json:"apiVersion"`
	// +kubebuilder:validation:MinLength=1
	Kind string `json:"kind"`
}

// DetectSpec is the CEL rung: an expression over `object` deciding whether
// the target is an incident of type Trigger.
type DetectSpec struct {
	// +kubebuilder:validation:MinLength=1
	Trigger string `json:"trigger"`
	// Expression is a CEL boolean over the variable `object` (the full target).
	// +kubebuilder:validation:MinLength=1
	Expression string `json:"expression"`
}

type IncidentPolicySpec struct {
	Target TargetGVK  `json:"target"`
	Detect DetectSpec `json:"detect"`
}

type IncidentPolicyStatus struct {
	// Conditions carries a Valid condition; reason InvalidExpression when CEL fails to compile.
	Conditions       []metav1.Condition `json:"conditions,omitempty"`
	IncidentsCreated int64              `json:"incidentsCreated,omitempty"`
	LastTriggeredAt  *metav1.Time       `json:"lastTriggeredAt,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.spec.target.kind`
// +kubebuilder:printcolumn:name="Trigger",type=string,JSONPath=`.spec.detect.trigger`
// +kubebuilder:printcolumn:name="Valid",type=string,JSONPath=`.status.conditions[?(@.type=="Valid")].status`
type IncidentPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   IncidentPolicySpec   `json:"spec"`
	Status IncidentPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type IncidentPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []IncidentPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &IncidentPolicy{}, &IncidentPolicyList{})
		return nil
	})
}
```

- [ ] **Step 2: Add the resource to `operator/PROJECT`** — append an entry mirroring the Incident one but `kind: IncidentPolicy` and `namespaced: false`.

- [ ] **Step 3: Regenerate**

Run: `cd operator && make generate manifests`
Expected: `IncidentPolicy` deepcopy methods added; `config/crd/bases/ops.goblinoperator.io_incidentpolicies.yaml` created.

- [ ] **Step 4: Commit**

```bash
git add operator/api operator/PROJECT operator/config/crd
git commit -m "operator: add IncidentPolicy CRD (target GVK + CEL expression)"
```

---

## Part 3 — CEL matcher library

Pure functions — the highest-value TDD unit. No Kubernetes, no envtest.

### Task 5: CEL compile + eval over an object map

**Files:**
- Create: `operator/internal/detection/cel.go`
- Test: `operator/internal/detection/cel_test.go`

**Interfaces:**
- Produces: `func Compile(expr string) (*Matcher, error)`; `type Matcher` with `func (m *Matcher) Eval(object map[string]any) (bool, error)`.

- [ ] **Step 1: Write failing tests**

```go
package detection

import "testing"

func TestCompileRejectsGarbage(t *testing.T) {
	if _, err := Compile("this is not )( cel"); err == nil {
		t.Fatal("expected compile error")
	}
}

func TestEvalOOMKilled(t *testing.T) {
	expr := `has(object.status.containerStatuses) && object.status.containerStatuses.exists(c, has(c.lastState.terminated) && c.lastState.terminated.reason == 'OOMKilled')`
	m, err := Compile(expr)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	oomed := map[string]any{"status": map[string]any{"containerStatuses": []any{
		map[string]any{"lastState": map[string]any{"terminated": map[string]any{"reason": "OOMKilled"}}},
	}}}
	got, err := m.Eval(oomed)
	if err != nil || !got {
		t.Fatalf("expected match, got %v err %v", got, err)
	}
	healthy := map[string]any{"status": map[string]any{"containerStatuses": []any{
		map[string]any{"ready": true},
	}}}
	if got, _ := m.Eval(healthy); got {
		t.Fatal("expected no match for healthy pod")
	}
}

func TestEvalNonBoolIsError(t *testing.T) {
	m, err := Compile(`object.status`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if _, err := m.Eval(map[string]any{"status": map[string]any{}}); err == nil {
		t.Fatal("expected non-bool result error")
	}
}
```

- [ ] **Step 2: Run — expect FAIL**

Run: `cd operator && go test ./internal/detection/ -run TestEval -v`
Expected: FAIL (package/functions undefined).

- [ ] **Step 3: Implement**

```go
package detection

import (
	"fmt"

	"github.com/google/cel-go/cel"
)

// Matcher is a compiled CEL program evaluating a boolean over `object`.
type Matcher struct {
	program cel.Program
}

// Compile builds a Matcher from a CEL expression. The expression sees one
// variable, `object`, of dynamic type (the target as map[string]any), and must
// return a bool.
func Compile(expr string) (*Matcher, error) {
	env, err := cel.NewEnv(cel.Variable("object", cel.DynType))
	if err != nil {
		return nil, fmt.Errorf("cel env: %w", err)
	}
	ast, iss := env.Compile(expr)
	if iss != nil && iss.Err() != nil {
		return nil, fmt.Errorf("compile: %w", iss.Err())
	}
	prg, err := env.Program(ast)
	if err != nil {
		return nil, fmt.Errorf("program: %w", err)
	}
	return &Matcher{program: prg}, nil
}

// Eval runs the program against object. A runtime error (e.g. missing field
// without a has() guard) or a non-bool result is returned as an error.
func (m *Matcher) Eval(object map[string]any) (bool, error) {
	out, _, err := m.program.Eval(map[string]any{"object": object})
	if err != nil {
		return false, fmt.Errorf("eval: %w", err)
	}
	b, ok := out.Value().(bool)
	if !ok {
		return false, fmt.Errorf("expression did not evaluate to bool (got %T)", out.Value())
	}
	return b, nil
}
```

- [ ] **Step 4: Tidy deps + run**

Run: `cd operator && go mod tidy && go test ./internal/detection/ -v`
Expected: `cel-go` moves to a direct require; all tests PASS.

- [ ] **Step 5: Commit**

```bash
git add operator/internal/detection operator/go.mod operator/go.sum
git commit -m "operator: CEL matcher (compile + eval over object map)"
```

---

## Part 4 — Policy registry + PolicyReconciler

### Task 6: Thread-safe policy registry

**Files:**
- Create: `operator/internal/detection/registry.go`
- Test: `operator/internal/detection/registry_test.go`

**Interfaces:**
- Produces: `type CompiledPolicy{ Name, Trigger string; Matcher *Matcher }`; `type Registry` with `NewRegistry() *Registry`, `Set(name string, gvk schema.GroupVersionKind, p CompiledPolicy)`, `Delete(name string)`, `ForGVK(gvk schema.GroupVersionKind) []CompiledPolicy`.

- [ ] **Step 1: Write failing test**

```go
package detection

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestRegistrySetForGVKDelete(t *testing.T) {
	r := NewRegistry()
	pod := schema.GroupVersionKind{Version: "v1", Kind: "Pod"}
	m, _ := Compile("true")
	r.Set("p1", pod, CompiledPolicy{Name: "p1", Trigger: "OOMKilled", Matcher: m})
	if got := r.ForGVK(pod); len(got) != 1 || got[0].Trigger != "OOMKilled" {
		t.Fatalf("expected 1 policy, got %#v", got)
	}
	// Set is upsert by name.
	r.Set("p1", pod, CompiledPolicy{Name: "p1", Trigger: "Changed", Matcher: m})
	if got := r.ForGVK(pod); len(got) != 1 || got[0].Trigger != "Changed" {
		t.Fatalf("expected upsert, got %#v", got)
	}
	r.Delete("p1")
	if got := r.ForGVK(pod); len(got) != 0 {
		t.Fatalf("expected 0 after delete, got %#v", got)
	}
}
```

- [ ] **Step 2: Run — expect FAIL.** `cd operator && go test ./internal/detection/ -run TestRegistry -v`

- [ ] **Step 3: Implement**

```go
package detection

import (
	"sync"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// CompiledPolicy is a validated IncidentPolicy ready to evaluate.
type CompiledPolicy struct {
	Name    string
	Trigger string
	Matcher *Matcher
}

// Registry maps target GVK -> active compiled policies, keyed by policy name.
type Registry struct {
	mu    sync.RWMutex
	byGVK map[schema.GroupVersionKind]map[string]CompiledPolicy
}

func NewRegistry() *Registry {
	return &Registry{byGVK: map[schema.GroupVersionKind]map[string]CompiledPolicy{}}
}

// Set upserts a policy under its GVK. If the policy's GVK changed since a prior
// Set, the caller must Delete first; in practice PolicyReconciler Deletes then Sets.
func (r *Registry) Set(name string, gvk schema.GroupVersionKind, p CompiledPolicy) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byGVK[gvk] == nil {
		r.byGVK[gvk] = map[string]CompiledPolicy{}
	}
	r.byGVK[gvk][name] = p
}

// Delete removes a policy by name from every GVK bucket.
func (r *Registry) Delete(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for gvk, m := range r.byGVK {
		delete(m, name)
		if len(m) == 0 {
			delete(r.byGVK, gvk)
		}
	}
}

// ForGVK returns a snapshot slice of active policies for a GVK.
func (r *Registry) ForGVK(gvk schema.GroupVersionKind) []CompiledPolicy {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]CompiledPolicy, 0, len(r.byGVK[gvk]))
	for _, p := range r.byGVK[gvk] {
		out = append(out, p)
	}
	return out
}
```

- [ ] **Step 4: Run — expect PASS.** `cd operator && go test ./internal/detection/ -v`

- [ ] **Step 5: Commit** — `git add operator/internal/detection && git commit -m "operator: policy registry (GVK -> compiled policies)"`

### Task 7: PolicyReconciler compiles policies into the registry

**Files:**
- Create: `operator/internal/controller/incidentpolicy_controller.go`
- Modify: `operator/cmd/main.go`

**Interfaces:**
- Consumes: `detection.NewRegistry`, `detection.Compile`, `detection.CompiledPolicy`, `opsv1alpha1.IncidentPolicy`.
- Produces: `PolicyReconciler{ Client; Registry *detection.Registry }` with `SetupWithManager`.

- [ ] **Step 1: Implement the reconciler.** On reconcile: fetch the policy (NotFound → `Registry.Delete(name)`, return). Parse `spec.target.apiVersion`+`kind` into a GVK. Compile `spec.detect.expression`. On compile error → `Registry.Delete(name)`, set `Valid=False, reason=InvalidExpression, message=<err>`, status update, return nil (don't requeue a bad expression). On success → `Registry.Delete(name)` then `Registry.Set(name, gvk, CompiledPolicy{Name, Trigger, Matcher})`, set `Valid=True, reason=Compiled`, status update.

```go
package controller

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	opsv1alpha1 "github.com/sguldemond/goblin/api/v1alpha1"
	"github.com/sguldemond/goblin/internal/detection"
)

// +kubebuilder:rbac:groups=ops.goblinoperator.io,resources=incidentpolicies,verbs=get;list;watch
// +kubebuilder:rbac:groups=ops.goblinoperator.io,resources=incidentpolicies/status,verbs=get;update;patch

type PolicyReconciler struct {
	client.Client
	Registry *detection.Registry
}

func (r *PolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := log.FromContext(ctx)

	var pol opsv1alpha1.IncidentPolicy
	if err := r.Get(ctx, req.NamespacedName, &pol); err != nil {
		if client.IgnoreNotFound(err) == nil {
			r.Registry.Delete(req.Name)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	gvk, gvkErr := parseGVK(pol.Spec.Target.APIVersion, pol.Spec.Target.Kind)
	var matcher *detection.Matcher
	var err error
	if gvkErr == nil {
		matcher, err = detection.Compile(pol.Spec.Detect.Expression)
	} else {
		err = gvkErr
	}

	if err != nil {
		r.Registry.Delete(pol.Name)
		l.Info("Policy invalid", "policy", pol.Name, "error", err)
		setValid(&pol, metav1.ConditionFalse, "InvalidExpression", err.Error())
		return ctrl.Result{}, r.Status().Update(ctx, &pol)
	}

	r.Registry.Delete(pol.Name)
	r.Registry.Set(pol.Name, gvk, detection.CompiledPolicy{
		Name: pol.Name, Trigger: pol.Spec.Detect.Trigger, Matcher: matcher,
	})
	setValid(&pol, metav1.ConditionTrue, "Compiled", "expression compiled")
	return ctrl.Result{}, r.Status().Update(ctx, &pol)
}

func parseGVK(apiVersion, kind string) (schema.GroupVersionKind, error) {
	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		return schema.GroupVersionKind{}, fmt.Errorf("bad target apiVersion %q: %w", apiVersion, err)
	}
	return gv.WithKind(kind), nil
}

func setValid(pol *opsv1alpha1.IncidentPolicy, status metav1.ConditionStatus, reason, msg string) {
	cond := metav1.Condition{
		Type: "Valid", Status: status, Reason: reason, Message: msg,
		LastTransitionTime: metav1.Now(), ObservedGeneration: pol.Generation,
	}
	for i, c := range pol.Status.Conditions {
		if c.Type == "Valid" {
			if c.Status == status && c.Reason == reason {
				cond.LastTransitionTime = c.LastTransitionTime
			}
			pol.Status.Conditions[i] = cond
			return
		}
	}
	pol.Status.Conditions = append(pol.Status.Conditions, cond)
}

func (r *PolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&opsv1alpha1.IncidentPolicy{}).
		Named("incidentpolicy").
		Complete(r)
}
```

- [ ] **Step 2: Wire in `main.go`** — construct the shared registry and register the reconciler:

```go
registry := detection.NewRegistry()

if err := (&controller.PolicyReconciler{
	Client:   mgr.GetClient(),
	Registry: registry,
}).SetupWithManager(mgr); err != nil {
	setupLog.Error(err, "Failed to create controller", "controller", "incidentpolicy")
	os.Exit(1)
}
```
Add the `"github.com/sguldemond/goblin/internal/detection"` import. Keep `registry` in scope — Task 8 passes it to the detector.

- [ ] **Step 3: Build**

Run: `cd operator && make manifests generate && go build ./...`
Expected: clean; RBAC role now includes `incidentpolicies`.

- [ ] **Step 4: Commit** — `git add operator && git commit -m "operator: PolicyReconciler compiles CEL policies into the registry"`

---

## Part 5 — Detection controller + preset policies

### Task 8: IncidentDetector — evaluate Pod policies, create Incidents

**Files:**
- Create: `operator/internal/controller/incident_detector.go`
- Test: add specs to `operator/internal/controller/` (new `incident_detector_test.go`, Ginkgo, uses the existing envtest suite)
- Modify: `operator/cmd/main.go`
- Delete: `operator/internal/controller/pod_controller.go`

**Interfaces:**
- Consumes: `detection.Registry`, `opsv1alpha1.Incident`, the Pod GVK.
- Produces: `IncidentDetector{ Client; Registry *detection.Registry; IncidentNamespace string }`.

- [ ] **Step 1: Implement the detector.** Watches Pods. On each Pod: skip if `no-auto-remediate` annotation is `"true"`; convert to unstructured; for each `Registry.ForGVK(Pod)` policy, `Eval`; on the first match, create an Incident (idempotent by `target-uid` label) with `TargetRef` = the pod, `Trigger` = policy trigger, `PolicyRef` = policy name; seed `Phase=Detected`.

```go
package controller

import (
	"context"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	opsv1alpha1 "github.com/sguldemond/goblin/api/v1alpha1"
	"github.com/sguldemond/goblin/internal/detection"
)

const targetUIDLabel = "goblinoperator.io/target-uid"
const noAutoRemediateAnnotation = "goblinoperator.io/no-auto-remediate"

var podGVK = schema.GroupVersionKind{Version: "v1", Kind: "Pod"}

// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=ops.goblinoperator.io,resources=incidents,verbs=get;list;watch;create
// +kubebuilder:rbac:groups=ops.goblinoperator.io,resources=incidents/status,verbs=update

type IncidentDetector struct {
	client.Client
	Registry          *detection.Registry
	IncidentNamespace string
}

func (r *IncidentDetector) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := log.FromContext(ctx)

	var pod corev1.Pod
	if err := r.Get(ctx, req.NamespacedName, &pod); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if pod.Annotations[noAutoRemediateAnnotation] == "true" {
		return ctrl.Result{}, nil
	}

	policies := r.Registry.ForGVK(podGVK)
	if len(policies) == 0 {
		return ctrl.Result{}, nil
	}

	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&pod)
	if err != nil {
		return ctrl.Result{}, err
	}

	for _, p := range policies {
		match, err := p.Matcher.Eval(obj)
		if err != nil {
			l.V(1).Info("policy eval error", "policy", p.Name, "pod", req.NamespacedName, "error", err)
			continue
		}
		if !match {
			continue
		}
		if err := r.createIncident(ctx, &pod, p); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil // one incident per pod
	}
	return ctrl.Result{}, nil
}

func (r *IncidentDetector) createIncident(ctx context.Context, pod *corev1.Pod, p detection.CompiledPolicy) error {
	l := log.FromContext(ctx)
	ns := r.IncidentNamespace
	if ns == "" {
		ns = pod.Namespace
	}

	var existing opsv1alpha1.IncidentList
	if err := r.List(ctx, &existing,
		client.InNamespace(ns),
		client.MatchingLabels{targetUIDLabel: string(pod.UID)},
	); err != nil {
		return err
	}
	if len(existing.Items) > 0 {
		return nil
	}

	inc := &opsv1alpha1.Incident{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: strings.ToLower(p.Trigger) + "-" + pod.Name + "-",
			Namespace:    ns,
			Labels:       map[string]string{targetUIDLabel: string(pod.UID)},
		},
		Spec: opsv1alpha1.IncidentSpec{
			TargetRef: corev1.ObjectReference{
				APIVersion: "v1", Kind: "Pod",
				Name: pod.Name, Namespace: pod.Namespace, UID: pod.UID,
			},
			Trigger:   p.Trigger,
			PolicyRef: p.Name,
		},
	}
	if err := r.Create(ctx, inc); err != nil {
		if errors.IsAlreadyExists(err) {
			return nil
		}
		return err
	}
	inc.Status.Phase = opsv1alpha1.PhaseDetected
	if err := r.Status().Update(ctx, inc); err != nil {
		return err
	}
	l.Info("Created Incident", "trigger", p.Trigger, "pod", pod.Name, "incident", inc.Name)
	return nil
}

func (r *IncidentDetector) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}).
		Named("incident-detector").
		Complete(r)
}
```

- [ ] **Step 2: Delete `pod_controller.go`** and its now-duplicated consts (`noAutoRemediateAnnotation` now lives in the detector; ensure it is not double-declared — remove the copy in `incident_controller.go` if present, keeping exactly one declaration in the package).

Run: `cd operator && rm internal/controller/pod_controller.go && go build ./... 2>&1 | head`
Fix any duplicate-const/undefined errors (the `incident_controller.go` still references `noAutoRemediateAnnotation`; it now resolves to the detector's copy — fine, one package).

- [ ] **Step 3: Wire in `main.go`** — replace the old `PodReconciler` block with:

```go
if err := (&controller.IncidentDetector{
	Client:            mgr.GetClient(),
	Registry:          registry,
	IncidentNamespace: "goblin",
}).SetupWithManager(mgr); err != nil {
	setupLog.Error(err, "Failed to create controller", "controller", "incident-detector")
	os.Exit(1)
}
```

- [ ] **Step 4: Write an envtest spec** proving policy-driven creation. In `incident_detector_test.go`: build a `detection.Registry`, `Set` a Pod policy compiled from the OOM CEL expression, construct `IncidentDetector{Client: k8sClient, Registry: registry, IncidentNamespace: "default"}`, create a Pod with an OOMKilled `lastState`, call `Reconcile`, and assert exactly one Incident with `Trigger=="OOMKilled"` and the `target-uid` label appears. Add a second case: a healthy pod produces no Incident.

- [ ] **Step 5: Run**

Run: `cd operator && make test`
Expected: PASS.

- [ ] **Step 6: Commit** — `git add operator && git commit -m "operator: CEL-policy incident detector; retire hardcoded pod triggers"`

### Task 9: Ship the built-in triggers as preset policies

**Files:**
- Create: `operator/config/samples/policies/oom-killed.yaml`, `operator/config/samples/policies/unschedulable.yaml`
- Create: `operator/config/samples/policies/kustomization.yaml` (lists the two)

- [ ] **Step 1: Write the preset policies**

`oom-killed.yaml`:
```yaml
apiVersion: ops.goblinoperator.io/v1alpha1
kind: IncidentPolicy
metadata:
  name: oom-killed
spec:
  target:
    apiVersion: v1
    kind: Pod
  detect:
    trigger: OOMKilled
    expression: >-
      has(object.status.containerStatuses) &&
      object.status.containerStatuses.exists(c,
        has(c.lastState.terminated) &&
        c.lastState.terminated.reason == 'OOMKilled')
```

`unschedulable.yaml`:
```yaml
apiVersion: ops.goblinoperator.io/v1alpha1
kind: IncidentPolicy
metadata:
  name: unschedulable
spec:
  target:
    apiVersion: v1
    kind: Pod
  detect:
    trigger: Unschedulable
    expression: >-
      object.status.phase == 'Pending' &&
      has(object.status.conditions) &&
      object.status.conditions.exists(c,
        c.type == 'PodScheduled' && c.status == 'False' && c.reason == 'Unschedulable')
```

`kustomization.yaml`:
```yaml
resources:
  - oom-killed.yaml
  - unschedulable.yaml
```

- [ ] **Step 2: Add a table test** for the two preset expressions in `operator/internal/detection/cel_test.go` — compile each expression string (paste them verbatim) and assert they compile and that the unschedulable one matches a Pending pod with a `PodScheduled=False/Unschedulable` condition and does not match a Running pod. This guards the shipped YAML against CEL typos.

Run: `cd operator && go test ./internal/detection/ -v`
Expected: PASS.

- [ ] **Step 3: Commit** — `git add operator/config/samples/policies operator/internal/detection && git commit -m "operator: preset OOMKilled + Unschedulable CEL policies"`

---

## Part 6 — Verify end to end

### Task 10: Live check on a cluster

- [ ] **Step 1:** `cd operator && make install` (applies both CRDs), then `kubectl apply -k config/samples/policies`.
- [ ] **Step 2:** `kubectl get incidentpolicies` — both show `Valid=True`.
- [ ] **Step 3:** `make run` (or deploy), then `kubectl apply -f ../scenarios/oom-killed.yaml`.
- [ ] **Step 4:** After the pod OOMKills, `kubectl get incidents -n goblin` shows an Incident with `Trigger=OOMKilled`, `Target=Pod`, `Phase=Detected`, and the `goblinoperator.io/target-uid` label.
- [ ] **Step 5:** Apply `scenarios/unschedulable-resources.yaml` and confirm a second Incident with `Trigger=Unschedulable` appears. Corrupt one policy's expression (`kubectl edit`) and confirm its `Valid` flips to `False` with reason `InvalidExpression`.

---

## Self-Review Notes

- **Rename coverage:** CRD types (T1), operator controllers + main + test (T2), agent config/tools/scout (T3). Env vars `INCIDENT_NAME`/`INCIDENT_NAMESPACE` set by the operator (T2 `createScoutJob`) and read by the agent (T3 config) — names match.
- **CEL variable name** `object` is identical in the matcher (T5), the preset policies (T9), and the tests.
- **Type/label consistency:** `targetUIDLabel = "goblinoperator.io/target-uid"` declared once in the detector (T8); `CompiledPolicy{Name,Trigger,Matcher}` fields identical across registry (T6), PolicyReconciler (T7), detector (T8).
- **Deferred (explicitly not in this plan):** arbitrary-GVK dynamic informers (detector watches Pods only), `detect.conditions`/`event`/`builtin`, `response`/tiers, cooldown/`for`, agent handling of non-Pod targets. A non-Pod `IncidentPolicy` compiles and registers but is never evaluated until the dynamic-informer follow-up.
