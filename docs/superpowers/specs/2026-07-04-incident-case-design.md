# Incidents & Cases — Naming, Relationship, and the Standing Scout
_2026-07-04_

> **Revised by `2026-07-05-incident-only-model.md`:** the Case CRD is
> dropped — Incident alone carries the status flow, the scout runs as a
> single-replica Deployment fed by an informer on Incidents, and grouping
> becomes a correlation-id label. The standing-scout recommendation (§3)
> stands; §1–2's two-CRD model does not.

## What this is

A refinement of the two 2026-07-03 specs, addressing three problems:

1. **"Remediation" and "Investigation" are semantically too similar.**
2. **Their relationship was murky** (bidirectional refs, unclear ownership).
3. **Open question: should the scout be always alive** — a standing LLM agent
   that receives context when errors occur and is chattable otherwise?

The detection design (policies, two watcher shapes), the tool/tier catalog,
and the absorption mechanics all carry over. What changes is the object
model's names and shape, and the scout's lifetime.

---

## 1. The root cause of the naming confusion

Remediation and Investigation felt interchangeable because today's
`Remediation` CR mixes two distinct things:

- **the symptom**: what was detected, where, when (`spec.targetRef`,
  `spec.trigger`) — a *fact*;
- **the work**: proposed actions, attempt history, conversation, approvals
  (`status.proposedAction`, `status.history`, `status.conversation`) — a
  *process*.

Then Investigation arrived as a second process-shaped object, and of course
the two overlapped. The fix is not a better synonym — it's splitting fact
from process, once, cleanly:

| Kind | Is | Replaces | Contains |
|---|---|---|---|
| **Incident** | a detected symptom (fact) | Remediation | targetRef, trigger, policyRef, detectedAt; phase + outcome |
| **Case** | the casework on 1..N incidents (process) | Investigation | diagnosis, proposed actions, attempt history, conversation, approvals, bound tiers, rootCause |
| **IncidentPolicy** | what to watch and how hard to act | RemediationPolicy | unchanged shape; `remediation:` section renamed `casework:` |

Incident/Case is the standard ITSM pairing (many incidents, one underlying
problem being worked). ITIL's literal `kind: Problem` was considered and
rejected — "Case" is shorter, reads as a unit of *work*, and suits a goblin
detective. Every process field moves off Incident: an Incident's status is
only `phase` (`Detected → Assigned → Resolved | Released | Escalated`) and,
when resolved, a pointer to the Case action that fixed it.

## 2. The relationship, made boring

One direction, Kubernetes idiom — child points to parent, parent queries by
label, no bidirectional bookkeeping to keep in sync:

```
IncidentPolicy ──fires──▶ Incident ──assigned to──▶ Case ──worked by──▶ scout
     (config)              (fact)                  (process)          (agent)
```

- Detection watchers create **Incidents**. The operator assigns each to a
  Case (correlation rules unchanged from the absorption spec): it stamps the
  label `goblinoperator.io/case: <name>` and `status.caseRef` on the
  Incident.
- A **Case never lists its incidents.** Children are found by label —
  `kubectl get incidents -l goblinoperator.io/case=inv-a1b2c3` — and
  `Case.status` carries only derived counts (`openIncidents`,
  `resolvedIncidents`). This kills the redundant `status.incidents[]` array
  from the absorption spec.
- **No ownerReferences between Incident and Case.** Incidents are the audit
  trail and must outlive Case deletion. The scout Job (in ephemeral mode) is
  owned by the Case; SA and RoleBindings hang off the Case too.
- Lifecycles:
  - Incident: `Detected → Assigned → Resolved | Released | Escalated`
  - Case: `Open → Diagnosing → AwaitingApproval → Fixing → Closed`
    (`status.rootCause` required to close)
  - `Released` (scout judged it unrelated) → operator opens a new Case and
    re-assigns; the Incident record itself never moves ownership silently —
    the label rewrite *is* the audit event.

## 3. The standing scout

### The three modes

| | A. Ephemeral (today's design) | B. Standing agent | C. Hybrid |
|---|---|---|---|
| Runs as | Job per Case | Deployment, always alive | Standing brain + ephemeral hands |
| Gets incidents | at spawn + turn-boundary absorption | watch on Incidents, injected into a case thread | brain triages, may delegate |
| Chat | only while a Case is open | anytime | anytime |
| Permissions | SA per Case, tiers bound at absorption | one SA, case-scoped time-boxed bindings | brain read-only, hands scoped |

### What standing buys

1. **It matches the product.** "An LLM agent that gets context when there are
   errors, otherwise you can still chat with it" — that's an ops copilot
   with an incident feed, not a batch job. You can ask "why is api slow?"
   with zero incidents open, and the goblin answers with `getResource` in
   hand. (It also matches the README lore: the goblin *lives in* the
   cluster; it shouldn't be summoned into existence per incident.)
2. **It dissolves a real conflict we already have.** Telegram long-polling
   (`getUpdates`) allows exactly one consumer per bot token — two concurrent
   scout Jobs answering different incidents fight over the same bot (409s,
   stolen updates). Absorption reduced the collision odds; a standing agent
   eliminates the class: one process owns the bot and routes messages to
   case threads.
3. **No cold starts, and memory across cases.** No Job scheduling/image pull
   per incident, and the agent can know that this is the *third* OOM on
   `api` this week — the limit is structurally too low, not transiently.
4. **A simpler operator.** No Job/SA/binding orchestration per Case; the
   InvestigationReconciler's spawn logic largely disappears.

### What standing costs, and the mitigations

1. **Least privilege.** A standing pod with union-of-all-tiers would be the
   opposite of the tier design. Mitigation: the runtime-binding mechanism
   from the absorption spec transfers unchanged — the standing agent's SA is
   **read-only at rest**, and the operator binds a tier RoleBinding when a
   Case needs it and **unbinds at Case close** (time-boxed, default 1h TTL as
   a backstop). Escalation audit stays on `Case.status.boundTiers`. The
   `bind`-verb guardrail on the operator is identical.
2. **Session management moves into the agent.** Per-case conversation
   threads, idle compaction, and restart survival become agent
   responsibilities instead of free Job semantics. Restart story: rebuild
   from the CRs — Incidents and Cases *are* the durable memory; the agent
   additionally writes a rolling `status.diagnosisSummary` to each open Case
   so a restarted agent resumes from the summary, not a blank slate. No PVC,
   no external store; the API server is the memory.
3. **Parallel storms serialize through one process.** Per-case threads are
   independent LLM conversations (concurrent API calls are fine), so this is
   a soft limit. The escape hatch is mode C: for an isolated heavy Case the
   standing agent dispatches an ephemeral Job scout — which is exactly the
   old model, demoted to a tool the brain can use. (Mode C is also where
   "goblin-chief" quietly arrives without ever being built as a separate
   project.)
4. **Blast radius.** A long-lived pod is a longer-lived target. Read-only
   baseline + per-write human approval + time-boxed bindings keep the
   standing agent's *steady-state* capability identical to a read-only
   scout's.
5. **Idle cost is a non-issue.** No events + no chat = no LLM calls; the
   idle cost is one small pod.

### Recommendation

Target **standing (B) as the end state**, keep the CRs mode-agnostic so
ephemeral remains a valid runner, and add hands (C) only when a real
parallelism ceiling is hit. Concretely mode-agnostic means: nothing in
Incident/Case references a Job; `Case.status.handler` records who is working
it (`job/<name>` or `agent/goblin-0`); the approval gate, tools, and tier
model are identical in both modes. The absorption mechanics (turn-boundary
injection, `releaseIncident`, `resolveIncidents`) carry over verbatim — in
standing mode "absorption" is simply assignment of an Incident to an
existing case thread.

---

## Revised phasing (replaces Phases 6–8 of the absorption spec)

**Phase 6 — Object model split.** Rename/split: Incident, Case,
IncidentPolicy; move process fields Remediation→Case; label-based
child lookup; lifecycles as in §2. Ephemeral runner still.
*Proof: existing scenarios pass; `kubectl get incidents -l
goblinoperator.io/case=X` shows grouping.*

**Phase 7 — Standing scout MVP.** Agent runs as a Deployment; watches
Incidents; one Case at a time; chat while idle (single Telegram consumer);
read-only SA + statically bound `workload-editor`.
*Proof: chat with the goblin with no incidents open; apply `oom-killed.yaml`
mid-conversation and watch it context-switch.*

**Phase 8 — Case-scoped escalation + threads.** Time-boxed tier bindings
bound/unbound at Case open/close; multiple concurrent case threads with
per-case compaction and `diagnosisSummary` restart recovery.
*Proof: quota + OOM Cases open simultaneously in different namespaces; tiers
bound per Case and revoked on close.*

**Phase 9 — Ephemeral hands (mode C).** Standing agent can dispatch a scoped
Job scout for an isolated Case; old ephemeral path becomes this tool.

## Out of scope

- Multi-replica standing agents (leader-elect one goblin; a clan is far off).
- External memory stores; the API server + Case summaries suffice.
- Merging open Cases (unchanged from the absorption spec).
