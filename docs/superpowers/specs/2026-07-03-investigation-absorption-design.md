# Investigations — Multi-Incident Absorption Design Spec
_2026-07-03_

## What this is

When several issues fire at once they might or might not share a root cause —
a node going bad evicts pods, evicted pods OOM elsewhere, the churn trips a
quota, a rollout stalls. Today's model (one Remediation → one scout Job) would
spawn a separate scout per symptom: N LLM loops paying for overlapping
context, N Telegram pings, and — worst — nobody holding the whole picture.

This spec designs an **Investigation**: a grouping object that lets a single
scout absorb multiple Remediations during one run, gain additional
permissions at runtime as new issue types join, diagnose the underlying
problem once, and resolve many symptoms with one fix.

Builds on `2026-07-03-multi-resource-remediation-design.md` (RemediationPolicy,
permission tiers). Assumes tiers exist (Phase 4 there).

---

## Design overview

```
Remediation created (by policy watchers)
  → operator looks for an OPEN Investigation matching the correlation key
      ├─ found:   attach Remediation to it (investigationRef)
      │           ensure the incident's tier is bound to the investigation's SA
      │           running scout picks it up at its next turn boundary
      └─ missing: create Investigation + dedicated SA + initial tier binding
                  + scout Job (owned by the Investigation)
```

The scout's conversation becomes the shared context: each absorbed incident is
injected as a new user message with its gathered context, and the LLM — not
the operator — makes the actual "related or unrelated" judgment, with a tool
to split unrelated incidents off into their own Investigation.

---

## 1. Investigation CRD

```yaml
apiVersion: ops.goblinoperator.io/v1alpha1
kind: Investigation
metadata:
  name: inv-default-a1b2c3
  namespace: goblin
spec:
  correlationKey: "ns/default"          # what grouped these (see §2)
  maxIncidents: 10                      # absorption cap; overflow starts a new Investigation
  lingerSeconds: 180                    # stay alive this long after last incident resolves
status:
  phase: Investigating                  # Open | Investigating | AwaitingApproval | Resolving | Closed
  serviceAccount: goblin-scout-inv-a1b2c3
  boundTiers:                           # audit trail of runtime escalations
    - tier: workload-editor
      boundAt: "2026-07-03T10:02:11Z"
      reason: "remediation oomkilled-api-7f9-x2 (OOMKilled)"
    - tier: capacity-admin
      boundAt: "2026-07-03T10:04:37Z"
      reason: "remediation quotaexceeded-api-rs (QuotaExceeded)"
  incidents:
    - remediationRef: oomkilled-api-7f9-x2
      state: absorbed                   # absorbed | released | resolved
    - remediationRef: quotaexceeded-api-rs
      state: absorbed
  rootCause: ""                         # written by the scout when diagnosed
```

Changes to **Remediation**: add `status.investigationRef` (set by the
operator when attached) and an `Absorbed` phase alongside the existing ones.
The scout Job moves from being owned by a Remediation to being owned by the
Investigation; `RemediationReconciler` keeps its phase bookkeeping but
delegates Job management to a new `InvestigationReconciler`.

---

## 2. Correlation: cheap heuristic first, LLM judgment second

The operator does **pre-grouping only** — it must be fast, deterministic, and
wrong-in-a-recoverable-way. Default correlation key, in priority order:

1. **Same node** — if the target (or its pods) is bound to a node that has an
   open node-class incident, join that Investigation. Node problems are the
   classic fan-out root cause.
2. **Same ownership chain** — target shares a Deployment/ReplicaSet/
   StatefulSet ancestor with an open incident.
3. **Same namespace within the window** — the fallback key (`ns/<name>`), with
   a configurable `correlationWindow` (default 5m): a Remediation joins an
   open Investigation in its namespace if that Investigation saw activity
   within the window.

If nothing matches → new Investigation. Cluster-scoped targets (Nodes) use a
`cluster` key. RemediationPolicy gets an optional `remediation.correlate:
none | namespace | cluster` override for policies whose incidents should never
be grouped (or always be).

**The scout corrects the heuristic.** Pre-grouping will sometimes glue
unrelated things together — that's fine, because the LLM sees full context and
gets a `releaseIncident` tool: releasing marks the incident `released`, and
the operator spins up a separate Investigation for it. The inverse mistake
(two Investigations that were actually one problem) is left to the human or a
future goblin-chief; merging running LLM conversations is not worth the
complexity.

---

## 3. Absorption mechanics in the agent

The scout loop (`agent/internal/scout/scout.go:runLoop`) is synchronous, so
absorption happens at **turn boundaries** — no goroutine surgery needed:

- A watch (or cheap list) on Remediations with
  `status.investigationRef == me` and phase `Absorbed`-pending runs before
  each LLM call and after each tool round.
- Each new incident gets the same treatment as the first one at startup:
  `gatherContext()` for its target kind, then injected as a user message:

```
--- NEW INCIDENT ABSORBED ---
Trigger: QuotaExceeded, target: apps/v1 ReplicaSet default/api-7f9d
This may or may not be related to what you are already investigating.
If unrelated, call releaseIncident. Otherwise fold it into your diagnosis.
<gathered context...>
```

- While blocked in a Telegram/TTY `Ask`, absorption waits for the answer
  (v1 limitation; acceptable because approval waits are short). A later
  iteration can make `Ask` selectable against an incident channel.

New/changed tools:

| Tool | What it does |
|---|---|
| `releaseIncident` | mark a Remediation `released`; operator re-routes it to a fresh Investigation |
| `resolveIncidents` | mark **multiple** Remediations resolved by one applied fix — the payoff tool: "uncordoned the node, that resolves these 8 unschedulable incidents". Verification then re-checks each listed target, not just the patched object |
| `updateInvestigation` | write `status.rootCause` + phase, alongside the existing per-Remediation status tool |
| `escalate` | unchanged, but now escalates the whole Investigation with the joint picture |

The system prompt grows one paragraph: "you may receive additional incidents
mid-run; prefer one root-cause diagnosis over per-symptom fixes; release
what does not belong."

---

## 4. Runtime permission escalation

The mechanism that makes "more permissions over time" work **without
restarting the pod**: a ServiceAccount's identity is fixed at pod creation,
but RBAC *bindings are evaluated live at request time*. So:

- Each Investigation gets a **dedicated, freshly created ServiceAccount**
  (`goblin-scout-inv-<hash>`), created before the Job.
- Absorbing an incident whose policy tier isn't bound yet → the operator
  creates a RoleBinding (or ClusterRoleBinding for the node tier) from that SA
  to the pre-existing tier ClusterRole. The running scout's next API call is
  authorized immediately — no token refresh, no pod restart.
- Closing the Investigation garbage-collects SA + bindings via owner
  references.

Guardrails:

- **Operator side**: the operator's own role gets the `bind` verb scoped to
  exactly the tier ClusterRoles (`goblin-tier-*`) — Kubernetes' escalation
  prevention then guarantees the operator can never grant anything beyond the
  declared tiers, even if compromised or buggy.
- **Audit**: every escalation appends to `status.boundTiers` with timestamp
  and the triggering Remediation (see CRD above), and emits a Kubernetes
  Event on the Investigation.
- **Tier ceiling** (optional, operator config): tier combinations above a
  threshold (e.g. `node-operator` + `capacity-admin` on one SA) require a
  human ack via the messenger before the binding is created — the scout keeps
  investigating read-only for that incident until approved.
- **Unchanged bedrock**: every write still goes through dry-run → diff →
  human approval. Escalation widens what the scout can *propose*, never what
  it can silently do.

Tool filtering follows bindings: the agent re-derives its enabled toolset from
the union of bound tiers each time it absorbs an incident, so the LLM's tool
list grows in lockstep with its RBAC.

---

## 5. Lifecycle & cost

- **Phases**: `Open` (Job starting) → `Investigating` → `AwaitingApproval` →
  `Resolving` → `Closed`.
- **Linger**: after the last incident resolves, the scout waits
  `lingerSeconds` (default 180s) for stragglers before exiting — cascading
  failures dribble in; killing the scout at first resolution wastes the warm
  context.
- **Caps**: `maxIncidents` (default 10) bounds context growth; overflow
  incidents start a sibling Investigation. Job TTL and `MaxAttempts`
  semantics carry over per Remediation.
- **Why this is cheaper, not just smarter**: one conversation reuses gathered
  context across incidents (N incidents ≪ N× tokens), one Telegram thread
  replaces N pings, and one fix + `resolveIncidents` replaces N approval
  round-trips.

---

## 6. Alternative considered: goblin-chief (supervisor agent)

A standing supervisor that reads all open Remediations and dispatches/
coordinates child scouts would also solve correlation — but it's a second
persistent LLM agent, a scheduling brain, and an inter-agent protocol.
Absorption gets ~90% of the value (shared context, joint diagnosis, one
human thread) inside the existing single-agent loop. Chief remains the future
answer for *cross-Investigation* correlation and fleet-level patterns; this
design deliberately leaves a `correlationKey` and audit trail it could later
consume.

---

## Phasing (continues the numbering from the multi-resource spec)

**Phase 6 — Investigation core.** Investigation CRD + InvestigationReconciler;
namespace-window correlation only; scout absorbs at turn boundaries;
`releaseIncident` + `resolveIncidents`. Single static tier per Investigation
(the first incident's) — no runtime escalation yet.
*Proof: apply `oom-killed.yaml` and `oom-leak.yaml` together → one scout, one
Telegram thread, two incidents resolved.*

**Phase 7 — Runtime tier escalation.** Per-Investigation SA + dynamic
bindings, `bind`-verb guardrail, `boundTiers` audit, toolset re-derivation.
*Proof: OOM incident running, then apply `quota-exceeded.yaml` in the same
namespace → scout gains `capacity-admin` mid-run and proposes a quota patch.*

**Phase 8 — Smarter correlation.** Node and ownership-chain keys; per-policy
`correlate` override; tier-ceiling human ack.
*Proof: cordon-and-break-a-node scenario → pod incidents across namespaces
land in the node's Investigation.*

## Out of scope

- Merging two running Investigations (human/goblin-chief territory).
- Interrupting a blocked `Ask` on incident arrival (v1 absorbs at turn
  boundaries only).
- Cross-cluster correlation.
