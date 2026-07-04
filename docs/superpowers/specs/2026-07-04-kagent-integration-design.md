# Joining the kagent Ecosystem — Integration Design
_2026-07-04_

## What this is

How goblin hooks into the kagent ecosystem **while keeping its own stack as
is**. Follows the comparison note (`2026-07-04-kagent-comparison.md`);
depends conceptually on the standing scout (`2026-07-04-incident-case-design.md`).

## The question, answered precisely

"Would the goblin scout become an agent to run inside kagent?" — there are
two very different ways to be "an agent in kagent":

1. **Declarative agent** (`Agent` CR with inline prompt/tools/model): kagent's
   ADK engine *executes* the agent. For goblin this would mean discarding the
   scout's own loop — and with it turn-boundary incident absorption, the
   code-enforced dry-run → diff → approve → verify pipeline, tier-filtered
   toolsets, and the Telegram approval flow. That is not "stack as is"; that
   is a rewrite onto their runtime, inheriting their prompt-level trust
   model. **Rejected.**

2. **BYO agent** (`Agent` CR with `spec.type: BYO`): the agent is a container
   *you* build, running your own framework, exposed to kagent via the A2A
   protocol. kagent registers it, shows it in the UI, and lets other agents
   and khook hooks call it — but never executes its reasoning loop. This is
   exactly how CrewAI/LangGraph agents join kagent. **This is the move.**

So: the goblin scout *appears in* kagent as an agent, but *runs as* goblin.
kagent becomes a front door, not an engine.

## Three integration seams (independent, all optional)

### Seam 1 — Goblin consumes MCP tools (inbound tools)

Add an MCP client adapter to the agent: each tool discovered from a
configured MCP server is wrapped as a `tools.Tool` (the existing interface —
Name/Description/InputSchema/Execute — is already shaped like an MCP tool, so
the adapter is thin). This unlocks kagent's tool-server ecosystem (Helm,
Prometheus, Grafana, Cilium, Istio) without writing Go tools for each.

Safety rules, non-negotiable:

- **Classification over trust**: MCP tools are declared read-only or write in
  goblin's config (allowlist per server; unknown tools default to *not
  loaded*, not to read-only).
- Write-classified MCP tools get the same `AfterTurnHook` approval wrapper as
  native tools — the human sees the call + args and approves before
  execution. No MCP tool bypasses the gate.
- Tier filtering applies: MCP toolsets are declared per tier like native
  tools.
- MCP servers run standalone — this seam works with **zero kagent
  installed**; kagent is just the likeliest source of good servers.

### Seam 2 — Goblin exposes A2A (outbound presence)

The standing scout grows an A2A server endpoint, and a small `Agent` CR
(`spec.type: BYO`) registers it in kagent. Two design decisions make this
cheap:

- **A2A is just another messenger.** The agent already abstracts human I/O
  behind `messenger.Messenger` (Send / Ask / StartThinking) with Telegram and
  TTY implementations. An A2A implementation is the third: an incoming A2A
  task opens a session, `Send` streams responses, and `Ask` (the approval
  prompt) maps onto A2A's `input-required` task state — so even another
  *agent* calling goblin hits the same structural approval gate, and a human
  in kagent's UI answers it. khook-style "never ask for permission" prompts
  cannot prompt the gate away, because it is not a prompt.
- **Every A2A invocation is casework.** A request like "investigate why api
  is slow" opens a Case (handler `a2a/<session>`), so work delegated from
  kagent gets the same audit trail, tier bindings, and verification as work
  originating from goblin's own detection. Incidents/Cases remain the system
  of record no matter who knocked.

Result: kagent users see "goblin" next to their other agents; khook Hooks can
target it (`agentRef: goblin`); other agents can delegate remediation to it —
while detection, correlation, approval, and RBAC stay 100% goblin.

### Seam 3 — Detection bridges (later, optional)

- **khook → goblin**: a Hook whose prompt says "create/describe the incident"
  is redundant once Seam 2 exists — khook can simply target the goblin agent.
  No code needed.
- **goblin → kagent agents**: `IncidentPolicy.casework.handler: a2a://...`
  could dispatch an incident to a *kagent* agent instead of the scout, making
  goblin's detection + incident model a front-end for arbitrary agents. Only
  worth building if a concrete use case shows up; noted for API-shape
  awareness (the `Case.status.handler` field from the incident-case spec
  already accommodates it).

## What explicitly does not change

Detection controllers, IncidentPolicy, Incident/Case CRs, permission tiers
and runtime bindings, the approval pipeline, Telegram, `kubectl attach`.
No hard dependency appears: without kagent (or with the A2A flag off), goblin
behaves exactly as before. kagent is beta (v0.10.0-beta) and A2A is young —
both integrations live in adapters at the edges (`internal/tools/mcp.go`,
`internal/messenger/a2a.go`), so protocol churn stays out of the core.

## Phasing (integration track, parallel to the main roadmap)

**I1 — MCP client tools.** Adapter + per-server allowlist/classification +
approval wrapper. Works with today's ephemeral scout; no kagent required.
*Proof: scout answers a Prometheus question via kagent's prometheus tool
server, and a write-classified Helm tool call triggers the approval prompt.*

**I2 — A2A messenger + BYO registration.** Requires the standing scout
(Phase 7). A2A server endpoint, `input-required` approval mapping, `Agent`
CR sample manifest in `config/samples/`.
*Proof: from the kagent UI, ask goblin to investigate a broken deployment;
approve its patch from the same UI; `kubectl get cases` shows the session.*

**I3 — Detection bridges.** Only on demonstrated need.

## Positioning restated

goblin does not become a kagent app; it becomes the **remediation specialist
in a kagent shop** — discoverable and callable like any agent, but bringing
its own incident lifecycle, its own least-privilege model, and an approval
gate that survives being called by other machines.
