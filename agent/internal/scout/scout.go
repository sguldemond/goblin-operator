package scout

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"

	"github.com/sguldemond/goblin/agent/internal/config"
	"github.com/sguldemond/goblin/agent/internal/llm"
	"github.com/sguldemond/goblin/agent/internal/messenger"
	"github.com/sguldemond/goblin/agent/internal/tools"
)

const maxTokens = 8192

type Scout struct {
	cfg    *config.Config
	client kubernetes.Interface
	dynCli dynamic.Interface
}

func New(cfg *config.Config, restCfg *rest.Config, client kubernetes.Interface) (*Scout, error) {
	dynCli, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("building dynamic client: %w", err)
	}
	return &Scout{cfg: cfg, client: client, dynCli: dynCli}, nil
}

func (s *Scout) Run(ctx context.Context) error {
	fmt.Println(">> loading incident...")
	incident, err := s.loadIncident(ctx)
	if err != nil {
		return fmt.Errorf("loading incident from CR: %w", err)
	}
	fmt.Printf(">> %s: %s %s/%s\n", incident.Trigger, incident.TargetKind, incident.TargetNamespace, incident.TargetName)

	gr, err := restmapper.GetAPIGroupResources(s.client.Discovery())
	if err != nil {
		return fmt.Errorf("building REST mapper: %w", err)
	}
	mapper := restmapper.NewDiscoveryRESTMapper(gr)

	toolList, status := tools.NewAll(s.client, s.dynCli, mapper,
		incident.TargetNamespace,
		s.cfg.IncidentName, s.cfg.IncidentNamespace,
	)

	fmt.Println(">> gathering context...")
	getResource := tools.NewGetResource(s.dynCli, mapper)
	contextMsg := BuildContext(*incident, gatherContext(ctx, incident, getResource))

	fmt.Println(">> waking the goblin...")

	var m messenger.Messenger
	if token := os.Getenv("TELEGRAM_BOT_TOKEN"); token != "" {
		chatID, _ := strconv.ParseInt(os.Getenv("TELEGRAM_CHAT_ID"), 10, 64)
		tg, err := messenger.NewTelegram(token, chatID)
		if err != nil {
			return fmt.Errorf("telegram: %w", err)
		}
		m = tg
		fmt.Println(">> telegram mode")
		tg.Send(fmt.Sprintf("🔔 %s <code>%s/%s</code> — <b>%s</b>\n\nScout dispatched, investigating now.", incident.TargetKind, incident.TargetNamespace, incident.TargetName, incident.Trigger)) //nolint:errcheck
	} else {
		m = messenger.NewTTY(os.Stdin, os.Stdout)
	}

	var base llm.SendFunc
	switch s.cfg.Provider {
	case "openai":
		base = llm.NewOpenAI(s.cfg.APIKey)
	default:
		base = llm.NewAnthropic(s.cfg.APIKey)
	}
	send := llm.WithRetry(base, 5, func(attempt int, err error, delay time.Duration) {
		fmt.Printf("\r>> API overloaded, retrying in %s (attempt %d)...\n", delay.Round(time.Second), attempt)
	})
	fmt.Printf(">> provider: %s, model: %s\n", s.cfg.Provider, s.cfg.Model)
	if err := s.runLoop(ctx, send, contextMsg, toolList, status, m); err != nil {
		m.Send(fmt.Sprintf("❌ Scout failed: %v", err)) //nolint:errcheck
		return err
	}
	return nil
}

// runLoop is the heart of the agent: prompt the LLM, run whatever tools it
// asks for, feed the results back, and repeat. Strip away the hooks and the
// messenger and this is what every agent really is — a conversation that keeps
// going until the model (or the human) decides it's done.
//
// Each turn:
//  1. Send the running conversation to the LLM.
//  2. If it wants tools, run them and loop back with the results.
//  3. Otherwise show its reply and ask the human what's next.
//  4. Repeat until a tool, a hook, or the user ends the session.
func (s *Scout) runLoop(
	ctx context.Context,
	send llm.SendFunc,
	contextMsg string,
	toolList []tools.Tool,
	status *tools.UpdateIncidentStatus,
	m messenger.Messenger,
) error {
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

	messages := []llm.Message{
		{Role: "user", Content: []llm.Content{{Type: "text", Text: contextMsg}}},
	}

loop:
	for {
		stopThinking := m.StartThinking()
		resp, err := send(ctx, llm.Request{
			Model:     s.cfg.Model,
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
			var toolResults []llm.Content
			for _, c := range resp.Content {
				if c.Type != "tool_use" {
					continue
				}
				t, ok := toolMap[c.Name]
				if !ok {
					toolResults = append(toolResults, llm.Content{
						Type:      "tool_result",
						ToolUseID: c.ID,
						IsError:   true,
						Content:   fmt.Sprintf("unknown tool: %s", c.Name),
					})
					continue
				}
				output, execErr := t.Execute(ctx, c.Input)
				if execErr != nil {
					toolResults = append(toolResults, llm.Content{
						Type:      "tool_result",
						ToolUseID: c.ID,
						IsError:   true,
						Content:   execErr.Error(),
					})
				} else {
					toolResults = append(toolResults, llm.Content{
						Type:      "tool_result",
						ToolUseID: c.ID,
						Content:   output,
					})
				}
				if h, ok := t.(tools.AfterToolHook); ok {
					if stop, err := h.AfterTool(ctx, m); stop || err != nil {
						flushOutcomes(ctx, toolList, status, m)
						return err
					}
				}
			}
			flushOutcomes(ctx, toolList, status, m)
			messages = append(messages, llm.Message{Role: "user", Content: toolResults})
			continue
		}

		// Print all text blocks from the assistant response.
		for _, c := range resp.Content {
			if c.Type == "text" {
				m.Send(c.Text) //nolint:errcheck
			}
		}

		// Let tools with an AfterTurnHook handle the prompt (e.g. approval flow).
		var turnHandled bool
		for _, t := range toolList {
			h, ok := t.(tools.AfterTurnHook)
			if !ok || !h.Active() {
				continue
			}
			msgs, stop, err := h.AfterTurn(ctx, m)
			flushOutcomes(ctx, toolList, status, m)
			if err != nil {
				return err
			}
			if stop {
				return nil
			}
			for _, msg := range msgs {
				messages = append(messages, llm.Message{
					Role:    "user",
					Content: []llm.Content{{Type: "text", Text: msg}},
				})
			}
			turnHandled = true
			break
		}
		if turnHandled {
			continue
		}

		// Normal prompt — exit/bye shortcut bypasses the LLM.
		line, err := m.Ask(ctx, "", nil)
		if err != nil {
			return err
		}
		if lower := strings.ToLower(line); lower == "exit" || lower == "bye" {
			for _, t := range toolList {
				if t.Name() != "exit" {
					continue
				}
				t.Execute(ctx, nil) //nolint:errcheck
				h := t.(tools.AfterTurnHook)
				msgs, stop, err := h.AfterTurn(ctx, m)
				if err != nil {
					return err
				}
				if stop {
					return nil
				}
				for _, msg := range msgs {
					messages = append(messages, llm.Message{
						Role:    "user",
						Content: []llm.Content{{Type: "text", Text: msg}},
					})
				}
				continue loop
			}
		}

		messages = append(messages, llm.Message{
			Role:    "user",
			Content: []llm.Content{{Type: "text", Text: line}},
		})
	}
}

// flushOutcomes records any concluded tool outcome on the Incident CR. Tools
// report what happened; writing it is the loop's job, so no tool needs to know
// the CR exists. A failed write is surfaced but never fatal — losing the status
// update matters less than losing the session that produced it.
func flushOutcomes(ctx context.Context, toolList []tools.Tool, status *tools.UpdateIncidentStatus, m messenger.Messenger) {
	for _, t := range toolList {
		r, ok := t.(tools.OutcomeReporter)
		if !ok {
			continue
		}
		phase, message, reported := r.Outcome()
		if !reported {
			continue
		}
		params, _ := json.Marshal(map[string]string{"phase": phase, "message": message})
		if _, err := status.Execute(ctx, params); err != nil {
			m.Send(fmt.Sprintf("Warning: could not set incident status to %s: %v", phase, err)) //nolint:errcheck
			continue
		}
		m.Send(fmt.Sprintf("Incident status: %s.", phase)) //nolint:errcheck
	}
}

func (s *Scout) loadIncident(ctx context.Context) (*Incident, error) {
	obj, err := s.dynCli.Resource(tools.IncidentGVR).
		Namespace(s.cfg.IncidentNamespace).
		Get(ctx, s.cfg.IncidentName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	spec, _, _ := unstructuredMap(obj.Object, "spec")
	targetRef, _, _ := unstructuredMap(spec, "targetRef")

	incident := &Incident{
		IncidentName:     s.cfg.IncidentName,
		Namespace:        s.cfg.IncidentNamespace,
		Trigger:          stringField(spec, "trigger"),
		TargetAPIVersion: stringField(targetRef, "apiVersion"),
		TargetKind:       stringField(targetRef, "kind"),
		TargetName:       stringField(targetRef, "name"),
		TargetNamespace:  stringField(targetRef, "namespace"),
	}

	if incident.TargetName == "" {
		return nil, fmt.Errorf("targetRef.name is empty in Incident CR")
	}
	// Kind drives which context the scout gathers, so guessing it wrong is
	// worse than refusing: a mis-typed target would be investigated as the
	// wrong kind and silently produce nonsense.
	if incident.TargetKind == "" {
		return nil, fmt.Errorf("targetRef.kind is empty in Incident CR")
	}
	// Empty apiVersion means the core group, per ObjectReference convention.
	if incident.TargetAPIVersion == "" {
		incident.TargetAPIVersion = "v1"
	}
	if incident.TargetNamespace == "" {
		incident.TargetNamespace = s.cfg.IncidentNamespace
	}

	return incident, nil
}

// gatherContext seeds the conversation with the target object and the events
// about it — the two things that are meaningful for any kind. Everything else
// (logs, owners, children, node capacity) the scout fetches itself with the
// tools it has; pre-fetching more would mean teaching this function about
// specific kinds, which is exactly what makes it brittle.
func gatherContext(ctx context.Context, incident *Incident, getResource tools.Tool) []tools.ToolResult {
	call := func(t tools.Tool, params any) tools.ToolResult {
		raw, _ := json.Marshal(params)
		out, err := t.Execute(ctx, raw)
		return tools.ToolResult{ToolName: t.Name(), Output: out, Err: err}
	}

	return []tools.ToolResult{
		call(getResource, map[string]string{
			"apiVersion": incident.TargetAPIVersion, "kind": incident.TargetKind,
			"name": incident.TargetName, "namespace": incident.TargetNamespace,
		}),
		// Match on kind as well as name: names are only unique per kind, so a
		// Deployment and a Pod sharing a name would otherwise pick up each
		// other's events.
		call(getResource, map[string]string{
			"apiVersion": "v1", "kind": "Event",
			"namespace": incident.TargetNamespace,
			"fieldSelector": "involvedObject.name=" + incident.TargetName +
				",involvedObject.kind=" + incident.TargetKind,
		}),
	}
}

func unstructuredMap(m map[string]any, key string) (map[string]any, bool, error) {
	v, ok := m[key]
	if !ok {
		return nil, false, nil
	}
	nested, ok := v.(map[string]any)
	return nested, ok, nil
}

func stringField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, _ := m[key].(string)
	return v
}
