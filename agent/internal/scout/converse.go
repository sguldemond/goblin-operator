package scout

import (
	"context"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/sguldemond/goblin/agent/internal/llm"
	"github.com/sguldemond/goblin/agent/internal/tools"
)

const maxTokens = 8192

// approvedChange remembers what the human said yes to, so a re-proposal forced
// by the revalidation check does not ask them the same question twice.
type approvedChange struct{ fingerprint string }

// converse runs one conversation until every incident it covers is closed.
//
// It is the old single-incident loop with two additions: incidents can arrive
// mid-conversation and are injected as user turns, and an approved change is
// re-validated against newly arrived incidents before it is applied.
func (s *session) converse(ctx context.Context, incidents <-chan *Incident, opening []llm.Message) error {
	toolList, status := tools.NewAll(s.scout.client, s.scout.dynCli, s.mapper)

	var approved *approvedChange

	// Wire the gate to this session. The gate knows nothing about incidents; it
	// asks these hooks whether a change is still valid and whether the human
	// has already agreed to it.
	for _, t := range toolList {
		gated, ok := t.(interface{ Gate() *tools.ApprovalGate })
		if !ok {
			continue
		}
		g := gated.Gate()
		g.OnStage = func(c *tools.StagedChange) {
			c.Snapshot = s.openKeys()
			// AwaitingApproval is non-terminal, so the operator keeps the
			// scout's grants alive while the human decides. Without it an
			// incident sitting on an approval looks idle.
			s.markAwaitingApproval(ctx, status)
		}
		g.PreApproved = func(c *tools.StagedChange) bool {
			return approved != nil && approved.fingerprint == c.Fingerprint
		}
		g.OnApprove = func(ctx context.Context, c *tools.StagedChange) (bool, string, error) {
			snapshot, _ := c.Snapshot.([]string)
			arrived := s.arrivedSince(snapshot)
			if len(arrived) == 0 {
				approved = nil
				return true, "", nil
			}
			// Remember the approval so an unchanged re-proposal applies
			// straight away instead of asking again.
			approved = &approvedChange{fingerprint: c.Fingerprint}
			return false, fmt.Sprintf(
				"The human approved your change to %s, but these incidents arrived while they were deciding:\n%s"+
					"Re-evaluate before anything is applied. If your fix is still the right one, propose exactly the "+
					"same change again and it will be applied without asking them twice. If these are related and a "+
					"different change would address them together, propose that instead.",
				c.Target, describeArrivals(arrived)), nil
		}
	}

	toolMap := make(map[string]tools.Tool, len(toolList))
	toolDefs := make([]llm.ToolDef, len(toolList))
	for i, t := range toolList {
		toolMap[t.Name()] = t
		toolDefs[i] = llm.ToolDef{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: t.InputSchema(),
		}
	}

	// Seed with everything currently open, plus whatever the human asked if
	// they started this conversation themselves.
	var messages []llm.Message
	for _, inc := range s.open {
		messages = append(messages, llm.Message{
			Role:    "user",
			Content: []llm.Content{{Type: "text", Text: s.seedFor(ctx, inc)}},
		})
	}
	if len(s.open) > 0 {
		s.announce()
	}
	messages = append(messages, opening...)

	ticker := resyncTicker()
	defer ticker.Stop()

	for {
		stopThinking := s.m.StartThinking()
		resp, err := s.send(ctx, llm.Request{
			Model:     s.scout.cfg.Model,
			MaxTokens: maxTokens,
			System:    systemPrompt,
			Messages:  messages,
			Tools:     toolDefs,
		})
		stopThinking()
		if err != nil {
			return fmt.Errorf("LLM call failed: %w", err)
		}

		messages = append(messages, llm.Message{Role: "assistant", Content: resp.Content})

		if resp.StopReason == "tool_use" {
			results, stop, err := s.runTools(ctx, &resp, toolMap, toolList, status)
			if err != nil || stop {
				return err
			}
			messages = append(messages, llm.Message{Role: "user", Content: results})
			continue
		}

		for _, c := range resp.Content {
			if c.Type == "text" {
				s.m.Send(c.Text) //nolint:errcheck
			}
		}

		// Approval and other end-of-turn hooks.
		if msgs, handled, stop, err := s.runTurnHooks(ctx, toolList, status); err != nil || stop {
			return err
		} else if handled {
			messages = append(messages, asUser(msgs)...)
			continue
		}

		if err := s.reap(ctx); err != nil {
			return err
		}
		if len(s.open) == 0 {
			s.m.Send("Nothing open right now. I am still watching — ask me anything.") //nolint:errcheck
			return nil
		}

		// Wait for whichever comes first: the human, or a new incident. The
		// reader belongs to the session, so it survives this conversation
		// ending rather than leaking a goroutine that eats the next reply.
		s.listen(ctx)

		select {
		case <-ctx.Done():
			return nil

		case in := <-s.humanCh:
			s.asking = false
			if in.err != nil {
				return in.err
			}
			if lower := strings.ToLower(strings.TrimSpace(in.text)); lower == "exit" || lower == "bye" {
				s.m.Send("Staying up — I only stop when the pod does. Tell me to escalate if you want me to back off an incident.") //nolint:errcheck
				continue
			}
			messages = append(messages, llm.Message{
				Role:    "user",
				Content: []llm.Content{{Type: "text", Text: in.text}},
			})

		case inc, ok := <-incidents:
			if !ok {
				return nil
			}
			// Take the whole burst, not just the head of it: siblings queue up
			// while the scout is mid-investigation and belong in one turn.
			var admitted []*Incident
			for _, arrival := range append([]*Incident{inc}, drain(incidents)...) {
				before := len(s.open)
				s.admit(ctx, arrival)
				if len(s.open) > before {
					admitted = append(admitted, arrival)
				}
			}
			if len(admitted) == 0 {
				continue // duplicates, or over the cap
			}
			messages = append(messages, s.arrivalMessage(ctx, admitted))

		case <-ticker.C:
			// Catch anything a dropped watch missed.
			backlog, err := s.watcher.Backlog(ctx)
			if err != nil {
				continue
			}
			var added []*Incident
			for _, inc := range backlog {
				before := len(s.open)
				s.admit(ctx, inc)
				if len(s.open) > before {
					added = append(added, inc)
				}
			}
			if len(added) == 0 {
				continue
			}
			messages = append(messages, s.arrivalMessage(ctx, added))
		}
	}
}

type humanInput struct {
	text string
	err  error
}

func asUser(msgs []string) []llm.Message {
	out := make([]llm.Message, 0, len(msgs))
	for _, msg := range msgs {
		out = append(out, llm.Message{
			Role:    "user",
			Content: []llm.Content{{Type: "text", Text: msg}},
		})
	}
	return out
}

// runTools executes the tool calls in a response.
func (s *session) runTools(
	ctx context.Context,
	resp *llm.Response,
	toolMap map[string]tools.Tool,
	toolList []tools.Tool,
	status *tools.UpdateIncidentStatus,
) ([]llm.Content, bool, error) {
	var results []llm.Content
	for _, c := range resp.Content {
		if c.Type != "tool_use" {
			continue
		}
		t, ok := toolMap[c.Name]
		if !ok {
			results = append(results, llm.Content{
				Type: "tool_result", ToolUseID: c.ID, IsError: true,
				Content: fmt.Sprintf("unknown tool: %s", c.Name),
			})
			continue
		}
		output, execErr := t.Execute(ctx, c.Input)
		if execErr != nil {
			results = append(results, llm.Content{
				Type: "tool_result", ToolUseID: c.ID, IsError: true, Content: execErr.Error(),
			})
		} else {
			results = append(results, llm.Content{
				Type: "tool_result", ToolUseID: c.ID, Content: output,
			})
		}
		if h, ok := t.(tools.AfterToolHook); ok {
			if stop, err := h.AfterTool(ctx, s.m); stop || err != nil {
				s.flushOutcomes(ctx, toolList, status)
				return nil, true, err
			}
		}
	}
	s.flushOutcomes(ctx, toolList, status)
	return results, false, nil
}

// runTurnHooks gives tools a chance to own the end of a turn — the approval
// flow being the only one today.
func (s *session) runTurnHooks(
	ctx context.Context,
	toolList []tools.Tool,
	status *tools.UpdateIncidentStatus,
) (msgs []string, handled, stop bool, err error) {
	for _, t := range toolList {
		h, ok := t.(tools.AfterTurnHook)
		if !ok || !h.Active() {
			continue
		}
		msgs, hookStop, err := h.AfterTurn(ctx, s.m)
		s.flushOutcomes(ctx, toolList, status)
		if err != nil {
			return nil, false, false, err
		}
		if hookStop {
			return nil, false, true, nil
		}
		return msgs, true, false, nil
	}
	return nil, false, false, nil
}

// flushOutcomes records concluded outcomes on the Incident CRs.
//
// A tool names the incidents its conclusion covers, because one fix routinely
// resolves several: two replicas of one Deployment OOMKill and a single patch
// fixes both. When it names none, the outcome can only be attributed
// unambiguously if exactly one incident is open — otherwise it is dropped
// rather than guessed at.
func (s *session) flushOutcomes(ctx context.Context, toolList []tools.Tool, status *tools.UpdateIncidentStatus) {
	for _, t := range toolList {
		r, ok := t.(tools.OutcomeReporter)
		if !ok {
			continue
		}
		o, reported := r.Outcome()
		if !reported {
			continue
		}
		targets := s.resolveTargets(o.Incidents)
		if len(targets) == 0 {
			s.m.Send(fmt.Sprintf("Warning: %s reported %q but named no open incident, so nothing was recorded.",
				t.Name(), o.Phase)) //nolint:errcheck
			continue
		}
		for _, inc := range targets {
			if _, err := status.Set(ctx, inc.Namespace, inc.IncidentName, o.Phase, o.Message); err != nil {
				s.m.Send(fmt.Sprintf("Warning: could not set %s to %s: %v", inc.Key(), o.Phase, err)) //nolint:errcheck
				continue
			}
			delete(s.open, inc.Key())
			s.m.Send(fmt.Sprintf("%s: %s.", inc.Key(), o.Phase)) //nolint:errcheck
		}
	}
}

// resolveTargets maps reported incident names onto open incidents. Names that
// match nothing are ignored: the model may name an incident that has already
// closed, which is not worth failing over.
func (s *session) resolveTargets(names []string) []*Incident {
	if len(names) == 0 {
		// Unattributed: safe only when there is a single candidate.
		if len(s.open) != 1 {
			return nil
		}
		for _, inc := range s.open {
			return []*Incident{inc}
		}
		return nil
	}

	var out []*Incident
	for _, name := range names {
		for _, inc := range s.open {
			if inc.IncidentName == name || inc.Key() == name {
				out = append(out, inc)
				break
			}
		}
	}
	return out
}

// reap drops incidents that have reached a terminal phase, whoever closed them.
func (s *session) reap(ctx context.Context) error {
	for key, inc := range s.open {
		obj, err := s.scout.dynCli.Resource(tools.IncidentGVR).
			Namespace(inc.Namespace).
			Get(ctx, inc.IncidentName, metav1.GetOptions{})
		if err != nil {
			delete(s.open, key) // gone entirely
			continue
		}
		if isTerminalPhase(phaseOf(obj.Object)) {
			delete(s.open, key)
		}
	}
	return nil
}

func isTerminalPhase(p string) bool {
	switch p {
	case "Applied", "Rejected", "Escalated", "HandedOff":
		return true
	}
	return false
}

// announce tells the human what the scout is now working on.
func (s *session) announce() {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("🔔 Investigating %d incident(s):\n", len(s.open)))
	for _, key := range s.openKeys() {
		inc := s.open[key]
		fmt.Fprintf(&sb, "• <code>%s %s/%s</code> — %s\n",
			inc.TargetKind, inc.TargetNamespace, inc.TargetName, inc.Trigger)
	}
	s.m.Send(sb.String()) //nolint:errcheck
}

// markAwaitingApproval records that a change is staged and waiting on a human.
// With one incident open it is unambiguous; with several the phase would be a
// guess, so it is left alone and the diff itself tells the human what is
// pending.
func (s *session) markAwaitingApproval(ctx context.Context, status *tools.UpdateIncidentStatus) {
	if len(s.open) != 1 {
		return
	}
	for _, inc := range s.open {
		if _, err := status.Set(ctx, inc.Namespace, inc.IncidentName, "AwaitingApproval", ""); err != nil {
			fmt.Printf(">> could not set %s to AwaitingApproval: %v\n", inc.Key(), err)
		}
	}
}

// arrivalMessage renders incidents that turned up mid-conversation as one turn,
// so a burst of siblings is presented together rather than one at a time.
func (s *session) arrivalMessage(ctx context.Context, incs []*Incident) llm.Message {
	var sb strings.Builder
	if len(incs) == 1 {
		sb.WriteString("A new incident arrived while you were working:\n\n")
	} else {
		fmt.Fprintf(&sb, "%d new incidents arrived together while you were working. "+
			"Consider whether they share a root cause with each other, or with what you already have open:\n\n",
			len(incs))
	}
	for _, inc := range incs {
		sb.WriteString(s.seedFor(ctx, inc))
		sb.WriteString("\n")
	}
	return llm.Message{Role: "user", Content: []llm.Content{{Type: "text", Text: sb.String()}}}
}
