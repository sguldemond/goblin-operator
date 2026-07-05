# IncidentPolicy by Example
_2026-07-05_

## What this is

The IncidentPolicy CRD made concrete: worked example manifests. Updates the
sketch in `2026-07-03-multi-resource-remediation-design.md` §1 to the
incident-only model, and settles the detection-expressiveness question with a
**ladder of four detect variants** — each rung more dynamic than the last,
and each policy uses exactly one:

| Rung | Field | For | Dynamic-ness |
|---|---|---|---|
| 1 | `detect.conditions` | anything with status conditions (most controllers) | declarative matchers |
| 2 | `detect.event` | Warning events — incl. targets with no pods/object state to inspect | reason + message regex + count threshold |
| 3 | `detect.expression` | arbitrary status fields, no condition to hook on | CEL over the object (k8s-native CEL, same as CRD validation rules) |
| 4 | `detect.builtin` | cross-object computed signals | named Go detector, policy toggles it |

Rungs 1–3 need zero goblin code changes to cover a new failure mode — that
is the "dynamic" requirement. Rung 4 exists so we never grow a YAML
programming language: when a signal needs *another* object to evaluate, it
gets a name in Go and stays toggleable/configurable from the policy.

The action side is one shared `response` block (renames the earlier
`remediation:`/`casework:` sketches — with Case gone, "response" is the
honest word).

---

## Rung 1 — `conditions`: the bread and butter

```yaml
apiVersion: ops.goblinoperator.io/v1alpha1
kind: IncidentPolicy
metadata:
  name: stalled-rollout
spec:
  target:
    apiVersion: apps/v1
    kind: Deployment
  detect:
    trigger: StalledRollout
    conditions:
      - type: Progressing
        status: "False"
        reason: ProgressDeadlineExceeded
        for: 2m                 # must hold this long — flap suppression
  response:
    tier: workload-editor
    maxAttempts: 2
    cooldown: 30m               # per-target re-fire suppression after closing
```

Node problems read the same way, plus cluster-wide correlation:

```yaml
apiVersion: ops.goblinoperator.io/v1alpha1
kind: IncidentPolicy
metadata:
  name: node-not-ready
spec:
  target:
    apiVersion: v1
    kind: Node                  # cluster-scoped target — no namespace scoping
  detect:
    trigger: NodeNotReady
    conditions:
      - type: Ready
        status: "False"         # matches Unknown too if listed twice
        for: 5m
  response:
    tier: node-operator
    correlate: cluster          # pod incidents may share a root cause with this
    maxAttempts: 1
    cooldown: 1h
```

## Rung 2 — `event`: when there is no object state to inspect

The quota case: the ReplicaSet creates zero pods; the only signal is a
Warning event. `count`/`window` turns noisy repeated events into one
deliberate incident:

```yaml
apiVersion: ops.goblinoperator.io/v1alpha1
kind: IncidentPolicy
metadata:
  name: quota-exceeded
spec:
  target:
    apiVersion: apps/v1
    kind: ReplicaSet            # the event's involvedObject kind
  detect:
    trigger: QuotaExceeded
    event:
      reason: FailedCreate
      messagePattern: "exceeded quota"    # RE2 against event message
  response:
    tier: capacity-admin
    maxAttempts: 1
    cooldown: 1h
---
apiVersion: ops.goblinoperator.io/v1alpha1
kind: IncidentPolicy
metadata:
  name: probe-flapping
spec:
  target:
    apiVersion: v1
    kind: Pod
  detect:
    trigger: ProbeFlapping
    event:
      reason: Unhealthy
      count: 5                  # at least 5 occurrences...
      window: 10m               # ...within 10 minutes → one incident
  response:
    tier: workload-editor
    maxAttempts: 2
    cooldown: 30m
```

## Rung 3 — `expression`: CEL for status fields that aren't conditions

OOMKilled is the motivating case — it lives in
`containerStatuses[].lastState`, which no condition matcher reaches. CEL is
already the Kubernetes expression language (CRD validation rules,
admission policies), so operators know it and the dependency is free:

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
      object.status.containerStatuses.exists(c,
        has(c.lastState.terminated) &&
        c.lastState.terminated.reason == 'OOMKilled')
  response:
    tier: workload-editor
    maxAttempts: 2
    cooldown: 30m
```

A compile error in the expression surfaces on the policy itself
(`status.conditions: type=Valid, status=False, reason=InvalidExpression`) —
never as silent non-detection.

## Rung 4 — `builtin`: computed, cross-object signals

"Service has no endpoints" requires comparing a Service's selector against
live Endpoints — that logic belongs in Go, but *whether it runs, where, and
what happens next* stays in the policy. Also shown: `mode: observe`, which
creates incidents and lets the scout diagnose, but pins the tier to readonly
(diagnose + escalate only) — the try-before-trust knob for any new policy:

```yaml
apiVersion: ops.goblinoperator.io/v1alpha1
kind: IncidentPolicy
metadata:
  name: service-without-endpoints
spec:
  target:
    apiVersion: v1
    kind: Service
  detect:
    trigger: ServiceWithoutEndpoints
    builtin:
      name: serviceWithoutEndpoints
      settings:                 # detector-specific knobs, schema per builtin
        graceSeconds: 120       # ignore services younger than this
  response:
    mode: observe               # observe | remediate (default)
    tier: readonly              # observe forces readonly regardless
    cooldown: 6h
```

## Scoping — the same knobs on every policy

Any policy can narrow where it applies; unscoped means everywhere the
detection controllers can see:

```yaml
apiVersion: ops.goblinoperator.io/v1alpha1
kind: IncidentPolicy
metadata:
  name: pvc-stuck-pending-data-teams
spec:
  target:
    apiVersion: v1
    kind: PersistentVolumeClaim
  namespaceSelector:
    matchLabels:
      team-kind: data           # only namespaces labeled like this
  objectSelector:
    matchExpressions:           # only PVCs matching this
      - {key: goblin/ignore, operator: DoesNotExist}
  detect:
    trigger: PVCStuckPending
    expression: object.status.phase == 'Pending'
    for: 10m                    # `for` is available on every rung
  response:
    tier: readonly
    mode: observe
```

(The existing `goblinoperator.io/no-auto-remediate` annotation remains a
target-side veto that overrides every policy.)

## What the policy's status tells you

```yaml
status:
  conditions:
    - type: Valid
      status: "True"
  incidentsCreated: 14
  openIncidents: 2
  lastTriggeredAt: "2026-07-05T09:41:12Z"
```

`kubectl get incidentpolicies` becomes the answer to "what is the goblin
watching right now, and is any of it misconfigured".

## Field reference (spec)

| Field | Req | Meaning |
|---|---|---|
| `target.apiVersion/kind` | ✓ | GVK to watch (drives dynamic informer registration) |
| `namespaceSelector` / `objectSelector` | | scope narrowing; omit = everywhere |
| `detect.trigger` | ✓ | free-form trigger name stamped on Incidents |
| `detect.conditions[]` \| `event` \| `expression` \| `builtin` | ✓ one | the ladder — exactly one variant per policy |
| `detect.*.for` | | signal must persist this long (flap suppression) |
| `detect.event.count`/`window` | | occurrence threshold for noisy events |
| `response.tier` | ✓ | permission tier → what the reconciler binds on claim |
| `response.mode` | | `remediate` (default) \| `observe` (readonly diagnosis) |
| `response.maxAttempts` | | default 2 |
| `response.cooldown` | | per-target suppression after an incident closes |
| `response.correlate` | | `namespace` (default) \| `cluster` \| `none` — correlation-id hinting |

## Deliberately absent

- Multiple detect variants in one policy — write two policies; composition
  in YAML breeds unreadable configs.
- Fix instructions in the policy (e.g. "patch memory to X") — the diagnosis
  belongs to the scout, the approval to the human. The policy decides *what
  is an incident* and *how much power the response gets*, nothing more.
- `approval: never` — still reserved, still not implemented.
