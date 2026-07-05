# Incident-Only Model — Dropping Case
_2026-07-05_

## What this is

A revision of `2026-07-04-incident-case-design.md`: the **Case CRD is
dropped**. Remediation is still renamed to **Incident**, but no object owns
the scout pod, and the Incident CRD itself carries the status flow and acts
as the durable queue between detection and the standing scout.

## Why Case loses its value

In the ephemeral design, Case earned its keep structurally: it owned the
scout Job, the per-case ServiceAccount, and the tier bindings. With a
standing scout, all three owners disappear — the pod belongs to a Deployment,
the SA is the scout's own, and bindings hang off incidents' tiers. What
remained of Case was bookkeeping:

| Case gave | Replaced by |
|---|---|
| grouping related incidents | a shared `goblinoperator.io/correlation-id` label the scout stamps; `kubectl get incidents -l ...` still works |
| root-cause record | the scout writes the same `status.rootCause` to every incident it diagnoses together (denormalized, and fine) |
| escalation audit (`boundTiers`) | Kubernetes Events on the Incident + `status.grantedTier` |
| handler bookkeeping | there is one scout; `status.claimedBy` on the Incident suffices |
| conversation/attempt state | back on `Incident.status` — see below |

The fact/process split was solving a two-CRD ambiguity; with one CRD there is
no ambiguity to solve. Re-mixing process fields into Incident is deliberate
and cheap for a single-incident flow — it is what Remediation already did.
If ephemeral parallel scouts ever return (the old "mode C hands"), Case can
return with them. Not before.

## The runtime: an always-on scout, owned by nothing incident-shaped

The scout runs as a **Deployment with `replicas: 1`**, shipped in
`config/` alongside the operator (option: operator-reconciled later if
per-cluster agent config demands it — start static, it's fewer moving parts).
The pod's lifecycle is Kubernetes' problem now: crash → ReplicaSet replaces
it. No Job creation in the operator; `RemediationReconciler`'s job-spawning
role disappears entirely.

## Notification: the watch IS the notification

"When an incident is registered it notifies this always-on pod somehow" —
the *somehow* is nothing new: the scout runs an **informer on Incident CRs**.
Detection controllers just create Incidents and never care whether the scout
is up. The API server is the message bus, etcd is the queue, the informer is
the subscription, and status phases are the ack protocol.

### Claim protocol and the down-pod situation

Informers do **list-then-watch**, which makes crash recovery free:

1. Detection creates `Incident` with `status.phase: Detected`. Scout down?
   Nothing is lost — the Incident sits in etcd.
2. Scout starts (or restarts) → informer's initial **list** delivers every
   existing Incident; the watch delivers everything after. One code path
   serves both "live notification" and "catch up on what happened while I
   was down".
3. For each `Detected` incident, the scout **claims** it: patches
   `phase: Analyzing`, `status.claimedBy: <pod-name>`. Optimistic
   concurrency (resourceVersion) makes double-claiming impossible even
   during restart races or a future second replica.
4. Crash mid-analysis leaves incidents in `Analyzing`. With a single
   replica the recovery rule is trivial: **on startup, any incident in
   `Analyzing` is by definition orphaned → re-adopt and resume.** No leases,
   no heartbeats needed at replicas=1. (If multi-replica ever happens, add a
   Lease + stale-claim timeout then.)
5. Resuming means re-gathering context. The in-memory conversation is gone
   after a crash, so the scout persists a rolling
   **`status.diagnosisSummary`** on the incident as it works — restart
   resumes from the summary plus fresh context, not from zero.

### Status flow

The Incident phases (close to the original Remediation set, since process is
back on the CR):

```
Detected → Analyzing → AwaitingApproval → Applying → Resolved
                 │                                  ↘ Recurred → Analyzing (attempts++)
                 └────────────→ Escalated / Rejected
```

`maxAttempts`, `history`, `proposedAction`, `conversation` return to
`Incident.status` unchanged from today's Remediation. Correlated incidents
being worked as one diagnosis all sit in `Analyzing` with the same
correlation-id label, and one applied fix moves several of them to
`Applying`/`Resolved` together (`resolveIncidents` semantics survive, just
without a Case around them).

### Tier bindings without Case

The **IncidentReconciler updates the agent's RoleBindings as derived state**
— level-triggered reconciliation, not imperative bind/unbind calls:

```
desired bindings = union over all Incidents in {Analyzing, AwaitingApproval, Applying}
                   of (incident.spec.tier, incident.namespace)
```

On every Incident event the reconciler computes that set and syncs reality to
it: missing binding → create, no longer needed → delete. Idempotent,
self-healing (a manually deleted binding comes back on the next reconcile;
a stale one from a crashed run is garbage-collected), and refcounting falls
out for free — the binding exists exactly as long as at least one open
incident needs it.

Details that matter:

- **The operator binds, never the scout.** The scout's own RBAC contains no
  RoleBinding write access, so it cannot grant itself anything; the operator
  holds the `bind` verb scoped to the `goblin-tier-*` ClusterRoles only
  (guardrail unchanged).
- **Bindings are namespace-scoped where the tier allows.** Because the
  desired set is (tier, namespace) pairs, `workload-editor` for an incident
  in `team-a` is a namespaced RoleBinding *in* `team-a` — the scout can patch
  deployments only in namespaces that currently have open incidents, not
  cluster-wide. Only inherently cluster-scoped tiers (`node-operator`) use a
  ClusterRoleBinding.
- **Binding keys off the phase transition, not creation**: incidents in
  `Detected` grant nothing — while the scout is down, no permissions exist.
  Claiming (`Analyzing`) is what brings the tier up.
- **Claim-to-bind race**: the scout may claim and immediately attempt a read
  the tier gates before the binding lands. Tools already surface RBAC
  denials as tool errors the LLM can retry; optionally the scout waits for
  `status.grantedTier` (stamped by the reconciler) before proposing writes.
- Bindings carry `goblinoperator.io/managed-by` + tier labels; grants/revokes
  emit Events on the affected incidents.

## What this simplifies (phasing deltas)

- Phase 6 shrinks to: rename Remediation → Incident, phase flow above,
  `podRef` → `targetRef`. No second CRD.
- Phase 7 (standing scout): Deployment + informer + claim/re-adopt protocol +
  `diagnosisSummary` persistence.
- Phase 8: correlation-id grouping + tier refcounting.
- Gone: Case CRD, InvestigationReconciler, per-case SA creation,
  `releaseIncident` (grouping is agent-internal now — "releasing" is just
  stamping a different correlation-id).

## Trade-offs accepted

- Root cause is denormalized across correlated incidents instead of stored
  once. Acceptable; a label query reconstructs the group.
- No durable record of "the goblin considered these related but changed its
  mind" — correlation-id relabeling is visible in the audit log only.
- Multi-replica scouts need a claim-lease design later; explicitly deferred.
