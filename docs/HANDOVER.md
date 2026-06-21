# GoblinOperator — Context for Claude Code

## What this is

A Kubernetes operator that detects pod failures and investigates them via an
LLM-backed agent, with a strong bias toward bringing a human into the loop
rather than acting autonomously. Project name: **GoblinOperator** — a small
creature that lives inside the cluster, never leaves it (no standing
privilege beyond what it's explicitly given), and pokes its head out through
one narrow channel (eventually Telegram) when it needs a human.

This is a PoC / experiment, not a production-hardened system. The goal is to
validate, fast, whether an LLM agent with bounded tool access produces
useful judgment on real pod failures — not to build a polished product.

## Design principles (do not relitigate these without flagging it)

- **The CRD is the sole communication channel.** Agents, the operator, and
  (later) a human via Telegram all communicate by reading/writing fields on
  one `Remediation` custom resource. No agent talks to another agent
  directly, no separate message bus.
- **The tool list IS the safety boundary**, not a runtime permission check.
  What an agent can do is determined by what's in the tool list it was
  handed for that invocation — never by the model's judgment about what it
  should do. If an action shouldn't be possible, it should not be in the
  list, not merely discouraged in the prompt.
- **The reconciler is the only privileged, long-running actor.** Detection
  and the resolve agent should carry the minimum RBAC needed for their one
  job. The agent's actual cluster-mutating actions go through narrow, named
  functions (e.g. `patchMemoryLimit(podRef, newLimitMb int)`) that do their
  own parameter validation — this is a tighter boundary than RBAC verbs can
  express, since RBAC can't see inside a patch body.
- **Detection watches resources directly, not the Events API.** Events are
  short-TTL, deduplicated, and meant as human-readable narration, not
  canonical state. OOMKilled is read off
  `pod.status.containerStatuses[].lastState.terminated.reason`.
- **Detection is idempotent.** One `Remediation` CR per failing pod
  incident, never duplicated on repeated reconciles or repeated OOM events
  on the same pod.
- **Validation is deterministic, no LLM call.** "Did the same failure
  recur within the wait window" is a plain Go check against pod status.
- **Bias toward human-in-the-loop, not full autonomy.** Initial scenario
  survey showed most failure types don't have a safe one-click fix. The
  agent's job is to investigate with wide read access, propose a specific
  action with reasoning, and wait for approval before anything mutates.
  Full autonomy (skip approval) is something to earn per signature later,
  not the default.
- **No MCP server yet, no A2A protocol.** Tool execution is in-process
  (agent binary calls client-go directly) for now. An MCP server may be
  added later purely to let Claude Code or other tools point at the same
  action layer for manual testing — it is not required for the core
  architecture to work, and should not be built until asked for.
- **No multi-agent split (assess/resolve/validate as separate LLM calls).**
  One resolve agent does investigation + decision in a single reasoning
  loop with full context. Validation is separate but deterministic, not an
  LLM call.

## Full target architecture (for context — not all of this is in scope yet)

```
Pod status change
      │
      ▼
[detector controller] ── watches Pods directly, matches playbook registry
      │                   signatures (OOMKilled for now), creates CR
      ▼
[Remediation CR] ── spine/comms channel, phase state machine:
      │              Detected → Assessing → AwaitingApproval →
      │              Validating → Applied / Escalated / HandedOff
      ▼
[Remediation reconciler] ── drives phases, spins up resolve agent as a
      │                      one-shot Job, owns the deterministic stability
      │                      check, owns attempts/maxAttempts and history
      ▼
[goblin-scout agent Job] ── Go binary, calls Anthropic API directly (raw
      │                      HTTP, no SDK), wide read-only tool access,
      │                      narrow mutating tool access gated by approval
      ▼
[action/tool layer] ── named functions with their own parameter validation:
                         getPodLogs, getEvents, getResourceHistory (read),
                         patchMemoryLimit, restartPod (mutate, gated)

Later: a Telegram bridge ("goblin-horn") lets a human converse with the
agent mid-investigation, approve specific proposed actions, or hand off
entirely. The conversation becomes part of status.conversation on the CR.
```

## CURRENT SCOPE — this is what to build now

Trigger: **OOMKilled only.**
Possible remedy: **memory limit increase only**, via `patchMemoryLimit`.
No Telegram yet. No MCP server yet. No multi-signature registry yet — hardcode
for OOMKilled, structure the code so adding signatures later is additive, not
a rewrite, but don't build the generic registry abstraction prematurely.

The agent binary itself (the Anthropic API loop, tool-calling, the
interactive approval flow) is being prototyped **separately** as a CLI tool
for now — not part of this handover. For this phase, the Job the reconciler
spins up can run a placeholder/stub container (e.g. `busybox` running `echo
done && sleep 5`). Wiring in the real agent binary is a later phase once both
pieces work independently.

### Task 1 — Scaffold

- `kubebuilder init` + `kubebuilder create api` for `Remediation`
  (group/version e.g. `ops.goblinoperator.io/v1alpha1`)
- CRD Go types — see schema below
- Generate manifests (`make manifests`, `make install`), confirm the CRD
  installs on a test cluster (kind or k3s)
- Done check: hand-write a sample `Remediation` YAML, `kubectl apply` it,
  `kubectl get remediation` shows it with the printer columns working

### Task 2 — Detector controller

- Pod informer (`For(&corev1.Pod{})` or a separate controller/manager,
  your call on structure)
- Predicate: any container's `lastState.terminated.reason == "OOMKilled"`
- On match: create a `Remediation` CR referencing the pod, **idempotently**
  — check for an existing CR for this pod (e.g. via owner ref or a label
  like `goblinoperator.io/pod-uid`) before creating
- No mutating RBAC beyond create on Remediations; read-only on Pods
- Done check: deliberately OOM a test pod (busybox with a tiny memory
  limit running an allocation loop is a good fixture), confirm exactly one
  CR is created even if the pod OOMs multiple times

### Task 3 — Reconciler skeleton

- Phase state machine on the CR: `Detected → Assessing → <stub stops here
  for now>`
- On `Detected`: spin up a Job (stub container per above), set phase to
  `Assessing`
- On `Assessing`: watch Job completion (`Succeeded`/`Failed`), transition
  phase or set `Escalated` on failure
- Use `ctrl.SetControllerReference` so the Job is owned by the CR (cleanup
  for free, and enables `Owns(&batchv1.Job{})` so Job status changes
  trigger reconciles)
- Done check: create a `Remediation` CR by hand (or let the detector
  create one from a real OOM), watch a Job get created, watch phase
  advance once the stub Job completes

## CRD schema (canonical reference — implement this)

```go
package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:validation:Enum=Detected;Assessing;AwaitingApproval;Validating;Applied;Rejected;Escalated;HandedOff
type RemediationPhase string

const (
	PhaseDetected         RemediationPhase = "Detected"
	PhaseAssessing        RemediationPhase = "Assessing" // resolve agent Job running
	PhaseAwaitingApproval RemediationPhase = "AwaitingApproval"
	PhaseValidating       RemediationPhase = "Validating"
	PhaseApplied          RemediationPhase = "Applied"
	PhaseRejected         RemediationPhase = "Rejected"
	PhaseEscalated        RemediationPhase = "Escalated"
	PhaseHandedOff        RemediationPhase = "HandedOff" // human took over outside the loop
)

// +kubebuilder:validation:Enum=patchMemoryLimit;restartPod;scaleReplicas;flagForHuman
type ActionType string

const (
	ActionPatchMemoryLimit ActionType = "patchMemoryLimit"
	ActionRestartPod       ActionType = "restartPod"
	ActionScaleReplicas    ActionType = "scaleReplicas"
	ActionFlagForHuman     ActionType = "flagForHuman"
)

type RemediationSpec struct {
	PodRef corev1.ObjectReference `json:"podRef"`

	// +kubebuilder:validation:Enum=OOMKilled
	Trigger string `json:"trigger"`

	// +kubebuilder:default=2
	MaxAttempts int `json:"maxAttempts,omitempty"`
}

type ProposedAction struct {
	Action    ActionType        `json:"action"`
	Params    map[string]string `json:"params,omitempty"`
	Reasoning string            `json:"reasoning,omitempty"`
}

type AttemptRecord struct {
	Action    ProposedAction `json:"action"`
	AppliedAt metav1.Time    `json:"appliedAt"`
	// "stable" | "recurred" — written by the deterministic validate step only
	Outcome string `json:"outcome"`
}

type ConversationTurn struct {
	From      string      `json:"from"` // "agent" | "human"
	Text      string      `json:"text"`
	Timestamp metav1.Time `json:"timestamp"`
}

type RemediationStatus struct {
	Phase          RemediationPhase    `json:"phase,omitempty"`
	ProposedAction *ProposedAction     `json:"proposedAction,omitempty"`
	Attempts       int                 `json:"attempts,omitempty"`
	History        []AttemptRecord     `json:"history,omitempty"`
	Conversation   []ConversationTurn  `json:"conversation,omitempty"`
	Message        string              `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Trigger",type=string,JSONPath=`.spec.trigger`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Attempts",type=integer,JSONPath=`.status.attempts`
type Remediation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RemediationSpec   `json:"spec"`
	Status RemediationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type RemediationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Remediation `json:"items"`
}
```

Note vs. earlier drafts: `AwaitingApproval` and `HandedOff` phases were added
to reflect the human-in-the-loop direction (propose → human approves before
mutation, or human takes over entirely) — `ProposedAction` is no longer
auto-applied by the reconciler. `Conversation` was added for the future
Telegram exchange but the field can sit unused for now.

## Explicitly out of scope right now

Don't build these yet, even if they seem like natural next steps mid-task:

- Telegram bridge / `askHuman` tool
- MCP server
- Any trigger besides OOMKilled
- The deterministic validate/stability-check loop and `maxAttempts`
  enforcement (next phase after Task 3)
- The real agent binary wired into the Job (being prototyped separately)
- A generic playbook registry abstraction (hardcode OOMKilled handling for
  now; structure for extension, don't build the abstraction prematurely)
- Audit/printer-column polish beyond what's needed to debug Tasks 1–3

If something in these tasks seems to require one of the above, stop and
flag it rather than building it.