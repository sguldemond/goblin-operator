# Notes — ideas jotted down for later

Running notes file. Rough ideas land here first; a dated spec in `specs/`
only when something graduates to design.

---

## 2026-07-04 — `goblin` CLI: a Claude Code-like local interface

Add a local command-line tool next to Telegram: connect from a laptop to the
scout in the cluster and chat, Claude Code style.

- **It's just a third messenger.** `messenger.Messenger` (Send / Ask /
  StartThinking) already abstracts human I/O — TTY and Telegram exist. The
  CLI is another implementation on the scout side plus a thin client binary.
- **Transport**: today's `kubectl attach -it` to the job pod works but is
  clunky (raw TTY, one consumer). With the standing scout, expose a small
  session endpoint (WebSocket or SSE over HTTP) on the scout pod; the CLI
  reaches it via `port-forward` under the hood — no ingress, no new auth:
  **kubeconfig + RBAC on port-forward is the auth**. Whoever may
  port-forward to the goblin namespace may talk to the goblin.
- **UX sketch** (Claude Code feel):
  - `goblin` → opens the REPL, streaming responses, spinner while thinking.
  - Approval prompts render inline as y/n, same gate as Telegram buttons.
  - Slash commands over the same session: `/cases` (list open cases),
    `/incidents`, `/case <name>` (attach to that case's thread),
    `/approve` / `/reject`, `/escalations` (bound tiers).
  - Non-interactive one-shots for scripting: `goblin cases`,
    `goblin approve <case>`, `goblin ask "why is api slow"`.
- **Ship as** a standalone `goblin` binary (option: also a `kubectl-goblin`
  plugin shim so `kubectl goblin` works). Lives in the existing Go module,
  e.g. `agent/cmd/goblin/`.
- **Multi-consumer note**: unlike Telegram long-polling, the session endpoint
  naturally supports several CLI sessions at once; per-case threads (Phase 8)
  decide what each session is attached to.
- Depends on the standing scout (Phase 7); until then a `goblin attach`
  wrapper around the existing `kubectl attach` one-liner from the README is a
  cheap stopgap.

---

## 2026-07-04 — Per-target status watchers (goroutine per tracked resource)

For something like a pod in an error status: start a tiny goroutine that
keeps watching that resource's status and informs the scout when it changes.

- **What it gives the scout**: live eyes on its targets. Today the scout only
  sees a snapshot when it happens to call `getResource`; a watcher turns
  "state changed under you" into a message — pod recovered on its own (close
  the incident, no fix needed), error escalated to a different state
  (CrashLoopBackOff → ImagePullBackOff), or recurrence right after a fix.
- **Mechanism**: prefer a k8s watch (`dynCli.Resource(gvr).Watch` with a
  fieldSelector on the name) over polling — near-zero cost, one goroutine per
  tracked target. Fall back to a slow poll ticker only if watch perms are an
  issue. Emit compact status-diff events ("what changed"), not full objects.
- **Delivery reuses the absorption path**: watcher pushes onto a channel; the
  loop drains it at turn boundaries and injects a user message, exactly like
  newly absorbed incidents. In standing-scout mode a status change can also
  *wake* an idle case thread.
- **Lifecycle**: watcher starts when an incident is absorbed into a case,
  stops when the incident resolves/releases or the case closes. Cap watcher
  count per case (same spirit as `maxIncidents`).
- **Bonus**: this subsumes the hardcoded 2-minute `verifyRollout` polling in
  `patch_deployment.go` — verification becomes "watch the targets until
  stable or timeout", uniform across all fix tools.

---

## Direction snapshot (2026-07-04)

Where the design has landed, per the specs in `specs/`:

- k8s-native object model: IncidentPolicy → Incident (fact) → Case (process),
  dynamic incident types instead of two hardcoded triggers.
- Single standing scout absorbing incidents into developing cases, with
  cluster memory.
- Dynamic but strict RBAC: read-only at rest, case-scoped time-boxed tier
  bindings, `bind`-verb guardrail, audit on the Case.
- Code-enforced approval gate on every write, regardless of who's asking.
- kagent interop explored and parked (`2026-07-04-kagent-integration-design.md`)
  — revisit after the core matures.

This branch (`claude/k8s-analysis-expansion-shrnhq`) holds the designs for
later implementation.
