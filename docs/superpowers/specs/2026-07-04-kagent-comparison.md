# kagent vs goblin-operator — Research Note
_2026-07-04_

## What kagent is

[kagent](https://github.com/kagent-dev/kagent) (CNCF sandbox, created by
Solo.io in 2025; v0.10.0-beta as of July 2026) is a **general-purpose
framework for building and running AI agents on Kubernetes**:

- **CRDs**: `Agent` (system prompt + tools + model), `ModelConfig` (LLM
  provider/keys), `ToolServer`/`RemoteMCPServer` (MCP tool servers). A Go
  controller reconciles these into running agents.
- **Engine**: agents execute on Google's ADK (Python default, Go variant);
  long-running with sessions, exposed via the A2A protocol (agents can call
  agents) and as MCP endpoints.
- **Tools**: everything is MCP. Built-in tool servers for Kubernetes
  (kubectl-style get/describe/patch/delete), Helm, Istio, Argo, Prometheus,
  Grafana, Cilium.
- **Interaction**: web UI dashboard, CLI, A2A. Chat-first.
- **Event-driven side**: [khook](https://github.com/kagent-dev/khook)
  (separate, early-stage — v0.0.4) adds a `Hook` CRD mapping five event
  types (`pod-restart`, `pod-pending`, `oom-kill`, `probe-failed`,
  `node-not-ready`) to an agent + prompt template, with a 10-minute dedup
  window. It fires a kagent session over REST/A2A and is done.
- **RBAC**: configured at install time (cluster-scoped or a namespace list);
  the agent's tools can do whatever its ServiceAccount can, all the time.
  Approval/read-only behaviour is prompt- and configuration-level — khook's
  example prompts literally say "analyze and fix immediately, never ask for
  permission".

## Where the projects overlap

The ambition overlaps heavily — in-cluster LLM agents that troubleshoot and
fix Kubernetes problems:

| Concern | kagent(+khook) | goblin |
|---|---|---|
| Agent runs in-cluster with k8s API tools | yes (MCP tool servers) | yes (Go tool loop) |
| Declarative "what to react to" | `Hook` CRD (5 event types) | detection controllers → planned `IncidentPolicy` CRD |
| Multi-provider LLM | ModelConfig CRD | env/config (Anthropic/OpenAI) |
| Chat with the agent | UI/CLI/A2A | Telegram / `kubectl attach` |
| Standing agent with sessions | yes, native | planned (2026-07-04 incident-case spec) |

khook's five event types are almost exactly goblin's issue catalog — OOM,
pending/unschedulable, probe failures, node-not-ready, crash restarts. The
detection *ambition* is convergent; the machinery around it is not.

## Where goblin differs

1. **Product vs platform.** kagent answers "how do I run any AI agent on
   Kubernetes" — framework, UI, multi-agent protocol, bring-your-own MCP
   tools. goblin answers "who fixes my cluster when it breaks, and why
   should I trust it" — one opinionated pipeline from detection to verified
   fix.
2. **Incidents are API objects.** goblin's Incident/Case CRs are a durable,
   kubectl-visible state machine: phases, attempts, outcomes, root cause,
   audit trail that outlives the agent. kagent has sessions and khook has
   hook status; there is no first-class incident record, no lifecycle, no
   `kubectl get incidents`.
3. **Safety is structural, not prompted.** goblin's dry-run → diff → human
   approval → verify pipeline is enforced in tool *code*; approval cannot be
   prompted away. kagent's equivalent is instructions and static RBAC —
   khook's examples push autonomous "never ask permission" remediation.
4. **Least privilege per incident, not per install.** kagent agents hold
   their full RBAC at all times. goblin's tier design binds permissions per
   incident class, escalates at runtime with the `bind`-verb guardrail, and
   revokes at case close — with the escalation history on the Case status.
5. **Correlation and absorption.** khook dedups identical events; nothing in
   kagent groups *different* symptoms into one diagnosis. goblin's Case
   absorption (many incidents, one root cause, `resolveIncidents` closing
   several at once) has no counterpart.
6. **Verification closes the loop.** goblin polls the rollout and re-checks
   every absorbed incident's target before marking anything resolved; khook
   is fire-and-forget.
7. **Weight.** Two small Go binaries vs controller + ADK engine + UI + CLI +
   MCP infrastructure.

## What's worth borrowing

- **MCP for tools.** Re-basing goblin's `Tool` interface on MCP (or adding an
  MCP client alongside it) would unlock kagent's whole tool-server ecosystem
  (Helm, Prometheus, Cilium) for free — the approval-gate wrapper can sit in
  front of MCP calls just as it does in front of native tools.
- **A2A exposure.** Once the standing scout exists, exposing it as an A2A/MCP
  endpoint makes goblin invocable from kagent and friends — goblin becomes
  the *remediation specialist* other platforms delegate to, rather than a
  competitor platform.
- **ModelConfig-style provider CRD** instead of env-var plumbing, eventually.

## Positioning takeaway

Don't compete with kagent as a platform — it has CNCF momentum, a UI, and an
ecosystem. goblin's defensible identity is the part kagent structurally
lacks: the **incident object model** (Incident/Case lifecycle, audit) and the
**structural trust story** (code-enforced approval, per-incident permission
tiers, verified outcomes). If anything, kagent's rise makes that gap more
valuable: khook proves demand for event-triggered agents, while its
"autonomous, never ask" defaults are exactly the trust problem goblin is
designed to solve.
