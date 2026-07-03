# Multi-Resource Remediation — Exploration & Design Spec
_2026-07-03_

## What this is

An exploration of how to extend the goblin beyond pod-only issues: which
resources and issue types are worth detecting, how detection should be
architected, which tools the scout needs per issue class, and how RBAC scales
with those tools. The centerpiece is a new **RemediationPolicy CRD** that lets
the cluster operator declare, per resource / issue type, what the goblin
watches for and what it is allowed to do about it.

This is a design doc; implementation is phased at the end.

---

## Current state

### Detection

`operator/internal/controller/pod_controller.go` watches Pods and hardcodes
exactly two triggers in `detectTrigger()`:

| Trigger | Signal |
|---|---|
| `OOMKilled` | `containerStatuses[].lastState.terminated.reason == OOMKilled` |
| `Unschedulable` | `PodScheduled=False, reason=Unschedulable` while Pending |

Everything else is invisible. Notably, **two of the repo's own scenarios
cannot be detected today**:

- `scenarios/quota-exceeded.yaml` — the ReplicaSet fails to create *any* pods
  (only `FailedCreate` Warning events exist), so a pod watcher never fires.
- `scenarios/stalled-rollout.yaml` — pods run but never become Ready; no
  OOMKilled, no Unschedulable. The signal lives on the **Deployment**
  (`Progressing=False, reason=ProgressDeadlineExceeded`).

The scenario catalog already outstrips the detection layer. That is the
clearest evidence of where to extend first.

### The Remediation CRD is pod-shaped, but barely

`api/v1alpha1/remediation_types.go`:

- `spec.podRef` is a `corev1.ObjectReference` — it already carries
  `apiVersion`/`kind`, so it is structurally generic. Only the name is wrong.
- `spec.trigger` is a two-value enum (`OOMKilled;Unschedulable`).
- Idempotency uses a `goblinoperator.io/pod-uid` label.

### The agent is already half-generic

- **Read side is generic.** `getResource` (dynamic client + RESTMapper)
  fetches or lists *any* GVK, cluster- or namespace-scoped, with label/field
  selectors. Only `getPodLogs` and the initial `gatherContext()` in
  `scout/scout.go` assume a pod target.
- **Write side has one tool, but the right pattern.** `patchDeployment`
  implements: strategic-merge **dry-run** → LCS **diff** → **human approval**
  (via the `AfterTurnHook` messenger flow) → apply → **rollout verification**.
  That safety pipeline is the template for every future fix tool.

### RBAC is one-size-fits-all

`config/rbac/scout_role.yaml` is a single ClusterRole bound to the one
`goblin-scout` ServiceAccount. Every scout — whatever the incident — gets the
same permissions (read pods/logs/events/nodes/quotas/namespaces/deployments/
replicasets; **patch deployments** as the only write). Because each scout runs
as a fresh Job (`remediation_controller.go:createScoutJob`), the operator is
in a perfect position to hand each incident a ServiceAccount scoped to its
issue class instead.

---

## Issue catalog — what "all kinds of k8s issues" concretely means

Grouped by the resource that carries the signal, with the fix tool each class
needs. This is the menu; the RemediationPolicy CRD (below) decides which items
are active in a given cluster.

### Workloads

| Issue | Signal | Signal source | Typical fix (tool) |
|---|---|---|---|
| OOMKilled *(exists)* | lastState.terminated.reason | Pod status | patch memory limits (`patchWorkload`) |
| Unschedulable *(exists)* | PodScheduled=False | Pod condition | patch requests / nodeSelector (`patchWorkload`) |
| CrashLoopBackOff | waiting.reason=CrashLoopBackOff | Pod status | investigate logs; patch config/image; `deletePod` to kick |
| ImagePullBackOff / ErrImagePull | waiting.reason | Pod status | patch image ref (`patchWorkload`); escalate if registry auth |
| CreateContainerConfigError | waiting.reason | Pod status | missing ConfigMap/Secret key — usually escalate, or patch env ref |
| Stalled rollout | Progressing=False, ProgressDeadlineExceeded | **Deployment** condition | fix probe/image (`patchWorkload`), `rolloutRestart`, or rollback |
| Job failed | Failed condition, backoffLimit exceeded | **Job** condition | fix spec + recreate (`recreateJob`); escalate |
| CronJob failing repeatedly | last N Jobs failed | Job history | patch CronJob (`patchWorkload`) |
| StatefulSet stuck | updatedReplicas < replicas, no progress | STS status | `patchWorkload`, `deletePod` (STS pods don't self-heal on bad updates) |

### Scheduling & capacity

| Issue | Signal | Signal source | Typical fix (tool) |
|---|---|---|---|
| Quota exceeded | FailedCreate: "exceeded quota" | **Event** (ReplicaSet) | lower replicas/requests (`patchWorkload`, `scaleWorkload`) or raise quota (`patchQuota`) |
| LimitRange violation | FailedCreate: LimitRange | **Event** | patch workload resources |
| Node pressure evictions | Evicted pods, node conditions | Pod + **Node** | escalate; `cordonNode`; patch requests |

### Nodes

| Issue | Signal | Signal source | Typical fix (tool) |
|---|---|---|---|
| NotReady | Ready=False/Unknown | Node condition | escalate (infra); `cordonNode` to stop scheduling |
| Memory/Disk/PIDPressure | pressure conditions | Node condition | escalate; identify greedy pods; `deletePod` |
| Cordoned & forgotten | spec.unschedulable for > N hours | Node spec | `uncordonNode` (with human approval) |

### Storage

| Issue | Signal | Signal source | Typical fix (tool) |
|---|---|---|---|
| PVC stuck Pending | phase=Pending, no bound PV | PVC status + events | patch storageClassName/size; escalate |
| FailedMount / FailedAttachVolume | event reasons | **Event** | often node/CSI issue — investigate + escalate; `deletePod` to reschedule |

### Networking & config

| Issue | Signal | Signal source | Typical fix (tool) |
|---|---|---|---|
| Service with no endpoints | Endpoints empty while pods exist | Endpoints + Service | selector mismatch — patch Service or workload labels |
| Ingress → missing Service/Secret | events / backend checks | Event + Ingress | patch Ingress; escalate for TLS secrets |
| Dangling ConfigMap/Secret refs | CreateContainerConfigError | Pod status | escalate (never auto-create secrets) |

### Autoscaling & external CRDs

| Issue | Signal | Signal source | Typical fix (tool) |
|---|---|---|---|
| HPA can't fetch metrics | ScalingActive=False | HPA condition | escalate (metrics-server); patch HPA |
| HPA pinned at max, still saturated | currentReplicas==max + load | HPA status | raise max (`patchResource` on HPA) with approval |
| cert-manager Certificate not Ready | Ready=False | external CRD condition | escalate with diagnosis (issuer, DNS) |

Two architectural observations fall out of this table:

1. **Signals come in exactly two shapes**: a *status condition / field* on some
   object, or a *Warning Event* whose `involvedObject` may have no inspectable
   pods at all. Detection needs both a condition-watcher and an event-watcher.
2. **Fixes cluster into permission tiers**, not per-tool one-offs: workload
   patching, pod deletion, namespace capacity admin, node admin, read-only
   escalate. RBAC should follow the tier, not the individual tool.

---

## Design

### 1. RemediationPolicy CRD — declare what the goblin acts on

Instead of growing a hardcoded Go registry of detectors, detection becomes
**data**. A cluster operator installs the goblin, then applies policies for
whatever they want watched:

```yaml
apiVersion: ops.goblinoperator.io/v1alpha1
kind: RemediationPolicy
metadata:
  name: stalled-rollouts
spec:
  # WHAT to watch
  target:
    apiVersion: apps/v1
    kind: Deployment
  # WHERE (optional narrowing)
  namespaceSelector:
    matchNames: ["default", "team-*"]      # or matchLabels
  objectSelector:
    matchLabels: {}                         # optional label filter on targets
  # WHEN — one of `conditions` or `event` (the two signal shapes)
  detect:
    trigger: StalledRollout                 # free-form trigger name, lands in Remediation.spec.trigger
    conditions:
      - type: Progressing
        status: "False"
        reason: ProgressDeadlineExceeded
        duration: 2m                        # must hold this long before firing
  # HOW MUCH the scout may do about it
  remediation:
    tier: workload-editor                   # permission tier → ServiceAccount + toolset (see §3)
    maxAttempts: 2
    cooldown: 30m                           # per-target re-fire suppression
    approval: always                        # always | never (auto-apply) — start with `always` only
```

Event-shaped detection uses the same CRD:

```yaml
apiVersion: ops.goblinoperator.io/v1alpha1
kind: RemediationPolicy
metadata:
  name: quota-exceeded
spec:
  target:
    apiVersion: apps/v1
    kind: ReplicaSet          # the event's involvedObject kind
  detect:
    trigger: QuotaExceeded
    event:
      type: Warning
      reason: FailedCreate
      messagePattern: "exceeded quota"      # RE2, matched against event message
  remediation:
    tier: capacity-admin
    maxAttempts: 1
    cooldown: 1h
```

**Matcher expressiveness.** Condition matchers (`type`/`status`/`reason` +
`duration`) plus event matchers (`type`/`reason`/`messagePattern`) cover every
row in the issue catalog except a few computed ones (e.g. "Service has no
endpoints", "CronJob's last 3 Jobs failed"). For those, add a third detect
variant later: `detect.builtin: <name>` referencing a small library of
named Go detectors — policies still control *whether* they're active, Go
controls *how* they evaluate. Don't build a YAML expression language.
(A CEL `detect.expression` field is a natural v2 if builtins proliferate —
Kubernetes already ships CEL for CRD validation, so the dependency is cheap.)

**Ships with presets.** The current behaviour becomes two bundled policies
(`oom-killed.yaml`, `unschedulable.yaml`) in `config/samples/policies/` that
`make deploy` applies — so out-of-the-box behaviour is unchanged, but now
visible, editable, and deletable as CRs.

**Status.** `RemediationPolicy.status` tracks `active`, `remediationsCreated`,
`lastTriggeredAt`, and a `conditions` list (e.g. `InvalidPattern` when the
messagePattern fails to compile) so a bad policy is diagnosable with
`kubectl get remediationpolicies`.

### 2. Detection architecture: two generic watchers driven by policies

Replace the growing-per-kind-controller approach with two policy-driven
controllers in `operator/internal/controller/`:

- **PolicyReconciler** — watches RemediationPolicy CRs and maintains an
  in-memory index: `GVK → []compiledPolicy` and `eventReason →
  []compiledPolicy`. It also (re)registers dynamic informers for any GVK named
  by a condition-shaped policy, using `builder.Watches` with the dynamic
  client / `source.Kind` on unstructured objects — the same mechanism
  `getResource` already uses on the agent side.
- **TargetReconciler** — a single generic reconciler for condition-shaped
  policies: on any watched object change, evaluate matching compiled policies,
  and create a Remediation on match. The existing `PodReconciler` logic
  (idempotency-by-UID, no-auto-remediate annotation, GenerateName, phase seed)
  moves here nearly verbatim — it is already written generically enough.
- **EventReconciler** — watches `corev1.Event` (`type=Warning`), looks up
  policies by reason, matches `messagePattern`, resolves `involvedObject` to
  the target ref, and creates a Remediation. This is what makes the
  quota-exceeded scenario detectable, because it needs no pod to exist.

Noise control lives in one place: the target-UID idempotency label (exists
today as `goblinoperator.io/pod-uid` → rename `goblinoperator.io/target-uid`),
the `goblinoperator.io/no-auto-remediate` annotation (exists), plus the new
per-policy `cooldown` (needed for Events, which repeat) and `duration` on
condition matchers (avoid firing on transient flaps).

**Duration semantics**: condition matchers with `duration` are evaluated with
`RequeueAfter` — first sighting records nothing and requeues; the Remediation
is created only if the condition still matches on re-check. No extra state.

### 3. Remediation CRD generalization

Minimal, since `ObjectReference` is already generic:

- `spec.podRef` → `spec.targetRef` (v1alpha1, no conversion webhook needed —
  regenerate CRD, update the two consumers: `remediation_controller.go` and
  agent `scout.go:loadIncident`).
- `spec.trigger`: drop the enum, free-form string (validated `MinLength=1`).
  The trigger name is policy-defined now.
- Add `spec.policyRef` (name of the RemediationPolicy that fired) and
  `spec.tier` (copied from the policy at creation time so the scout Job spec
  doesn't need to re-resolve the policy).
- `status` unchanged; `ActionType` enum can be dropped or widened later — it
  is not currently load-bearing in the agent loop.

### 4. Tool expansion — one safety pattern, many tools

Extract the `patchDeployment` pipeline (dry-run → diff → `AfterTurnHook`
approval → apply → verify) into a shared helper in `agent/internal/tools/`,
then implement tools in risk order:

| Tool | What it does | Verify step | Notes |
|---|---|---|---|
| `patchWorkload` | strategic-merge patch on Deployment/StatefulSet/DaemonSet/CronJob (GVK allowlist) | rollout/pods-ready poll (exists) | supersedes `patchDeployment` |
| `scaleWorkload` | scale subresource | replicas==ready poll | cheaper + safer than a spec patch for replica fixes |
| `rolloutRestart` | patch `kubectl.kubernetes.io/restartedAt` template annotation | rollout poll | trivial once `patchWorkload` exists |
| `deletePod` | delete a single named pod | replacement pod Ready | destructive tier; refuse pods without a controller ownerRef |
| `patchQuota` | patch ResourceQuota / LimitRange | dry-run diff only | capacity-admin tier |
| `cordonNode` / `uncordonNode` | patch `spec.unschedulable` | node spec read-back | node tier; drain is out of scope (too destructive for v1) |
| `recreateJob` | delete + recreate a Job from patched spec | Job Complete condition | job tier |
| `getEvents` (read) | events for arbitrary involvedObject | — | already possible via `getResource` fieldSelector; keep |
| `getEndpoints` (read) | service selector vs endpoints diagnosis | — | cheap, useful for the networking class |

**Secrets rule**: the scout may read ConfigMap/Secret *names and key names*
(to diagnose `CreateContainerConfigError`) but never secret values, and no
tool creates or patches Secrets — those always end in `escalate`.

`gatherContext()` in `scout.go` also generalizes: switch on
`targetRef.kind` to seed the conversation (pod → pod+events+logs as today;
deployment → deployment+replicasets+events+pods; event-shaped → the event +
involvedObject + owner chain). Everything else the LLM pulls via
`getResource`.

### 5. Permission tiers — trigger class → ServiceAccount + toolset

The mapping the policy's `remediation.tier` field selects:

| Tier | ServiceAccount | Extra RBAC beyond read-only | Tools enabled |
|---|---|---|---|
| `readonly` | `goblin-scout-readonly` | none | read tools, `escalate`, `updateRemediationStatus`, `exit` |
| `workload-editor` | `goblin-scout-workload` | patch deployments/statefulsets/daemonsets/cronjobs, patch scale subresource | + `patchWorkload`, `scaleWorkload`, `rolloutRestart` |
| `pod-operator` | `goblin-scout-pods` | workload-editor + delete pods | + `deletePod` |
| `capacity-admin` | `goblin-scout-capacity` | workload-editor + patch resourcequotas/limitranges | + `patchQuota` |
| `node-operator` | `goblin-scout-nodes` | readonly + patch nodes | + `cordonNode`, `uncordonNode` |

Wiring is two small changes:

- **Operator**: `createScoutJob` sets `ServiceAccountName` from `spec.tier`
  (default `readonly` for unknown values — fail safe). SAs/Roles/Bindings live
  in `config/rbac/` next to the existing `scout_role.yaml`, which becomes the
  shared read-only base every tier's SA also binds.
- **Agent**: `tools.NewAll` takes the tier and returns only that tier's tools.
  Benefit beyond least privilege: the LLM sees a *smaller, incident-relevant*
  tool list, which measurably improves tool-choice quality and keeps the
  system prompt tight.

The human approval gate stays on **every** write tool regardless of tier —
`approval: never` in the policy is deliberately not implemented in v1; the
field exists so the API doesn't churn when auto-apply for low-risk fixes is
introduced later (gated per-policy, per-tier).

---

## Phased roadmap (each phase independently shippable)

**Phase 1 — Generalize the core.**
`podRef`→`targetRef`, open trigger string, target-uid label; extract shared
approval/diff helper; `patchDeployment`→`patchWorkload` (GVK allowlist);
generalize `gatherContext` by target kind. Hardcoded detection still in place.
*Proof: existing OOM + unschedulable scenarios still pass.*

**Phase 2 — RemediationPolicy CRD + condition watcher.**
New CRD (`kubebuilder create api --kind RemediationPolicy`), PolicyReconciler
+ generic TargetReconciler with dynamic informers; port the two pod triggers
into bundled preset policies; add the stalled-rollout policy.
*Proof: `scenarios/stalled-rollout.yaml` detected and fixed end-to-end.*

**Phase 3 — Event watcher.**
EventReconciler + event-shaped policies (FailedCreate/FailedScheduling/
FailedMount presets).
*Proof: `scenarios/quota-exceeded.yaml` detected with zero pods existing.*

**Phase 4 — Permission tiers.**
Per-tier SAs/Roles in `config/rbac/`, `spec.tier` plumbed operator→Job→agent
tool filtering.
*Proof: quota incident scout can patch quotas; OOM incident scout cannot.*

**Phase 5 — Long tail.**
Node, PVC, Job/CronJob, HPA policies + `deletePod`, `scaleWorkload`, cordon
tools + a new scenario per policy preset (the scenario suite is the de facto
acceptance test harness — keep growing it in lockstep).

---

## Out of scope

- Auto-apply without human approval (`approval: never`) — API reserved, not implemented.
- Node drain, PV surgery, Secret creation/patching — always escalate.
- A YAML/CEL expression language for detection in v1 — named builtins first.
- Cross-resource correlation (e.g. "these 5 incidents share one root cause") — future goblin-chief.
