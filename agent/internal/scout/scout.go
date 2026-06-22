package scout

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"

	"github.com/sguldemond/goblin/agent/internal/config"
	"github.com/sguldemond/goblin/agent/internal/llm"
	"github.com/sguldemond/goblin/agent/internal/tools"
)

var remediationGVR = schema.GroupVersionResource{
	Group:    "ops.goblinoperator.io",
	Version:  "v1alpha1",
	Resource: "remediations",
}

const model = "claude-sonnet-4-6"
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
	fmt.Printf(">> %s: %s/%s\n", incident.Trigger, incident.PodNamespace, incident.PodName)

	gr, err := restmapper.GetAPIGroupResources(s.client.Discovery())
	if err != nil {
		return fmt.Errorf("building REST mapper: %w", err)
	}
	mapper := restmapper.NewDiscoveryRESTMapper(gr)

	toolList := tools.NewAll(s.client, s.dynCli, mapper,
		incident.PodNamespace,
		s.cfg.RemediationName, s.cfg.RemediationNamespace,
		s.markEscalated, s.markApplied,
	)

	fmt.Println(">> gathering context...")
	getResource := tools.NewGetResource(s.dynCli, mapper)
	getPodLogs := tools.NewGetPodLogs(s.client)
	contextMsg := BuildContext(*incident, s.gatherContext(ctx, incident, getResource, getPodLogs))

	fmt.Println(">> waking the goblin...")

	c := llm.NewClient(s.cfg.APIKey)
	return s.runLoop(ctx, c.Send, contextMsg, toolList, os.Stdin, os.Stdout)
}

type sendFn func(ctx context.Context, req llm.Request) (llm.Response, error)

func (s *Scout) runLoop(
	ctx context.Context,
	send sendFn,
	contextMsg string,
	toolList []tools.Tool,
	in io.Reader,
	out io.Writer,
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

	scanner := bufio.NewScanner(in)

	for {
		resp, err := send(ctx, llm.Request{
			Model:     model,
			MaxTokens: maxTokens,
			System:    systemPrompt,
			Messages:  messages,
			Tools:     toolDefs,
		})
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
					if stop, err := h.AfterTool(ctx, out); stop || err != nil {
						return err
					}
				}
			}
			messages = append(messages, llm.Message{Role: "user", Content: toolResults})
			continue
		}

		// Print all text blocks from the assistant response.
		for _, c := range resp.Content {
			if c.Type == "text" {
				fmt.Fprintln(out, c.Text)
			}
		}

		// Let tools with an AfterTurnHook handle the prompt (e.g. approval flow).
		var turnHandled bool
		for _, t := range toolList {
			h, ok := t.(tools.AfterTurnHook)
			if !ok || !h.Active() {
				continue
			}
			msgs, stop, err := h.AfterTurn(ctx, scanner, out)
			if err != nil {
				return err
			}
			if stop {
				return nil
			}
			for _, m := range msgs {
				messages = append(messages, llm.Message{
					Role:    "user",
					Content: []llm.Content{{Type: "text", Text: m}},
				})
			}
			turnHandled = true
			break
		}
		if turnHandled {
			continue
		}

		// Normal stdin prompt.
		var line string
		for {
			fmt.Fprint(out, "\n> ")
			if !scanner.Scan() {
				return scanner.Err()
			}
			line = strings.TrimSpace(scanner.Text())
			if line != "" {
				break
			}
		}
		messages = append(messages, llm.Message{
			Role:    "user",
			Content: []llm.Content{{Type: "text", Text: line}},
		})
	}
}

func (s *Scout) markApplied(ctx context.Context) error {
	patch, _ := json.Marshal(map[string]any{
		"status": map[string]any{
			"phase": "Applied",
		},
	})
	_, err := s.dynCli.Resource(remediationGVR).
		Namespace(s.cfg.RemediationNamespace).
		Patch(ctx, s.cfg.RemediationName, types.MergePatchType, patch, metav1.PatchOptions{}, "status")
	return err
}

func (s *Scout) markEscalated(ctx context.Context, reason string) error {
	patch, _ := json.Marshal(map[string]any{
		"status": map[string]any{
			"phase":   "Escalated",
			"message": reason,
		},
	})
	_, err := s.dynCli.Resource(remediationGVR).
		Namespace(s.cfg.RemediationNamespace).
		Patch(ctx, s.cfg.RemediationName, types.MergePatchType, patch, metav1.PatchOptions{}, "status")
	return err
}

func (s *Scout) loadIncident(ctx context.Context) (*Incident, error) {
	obj, err := s.dynCli.Resource(remediationGVR).
		Namespace(s.cfg.RemediationNamespace).
		Get(ctx, s.cfg.RemediationName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	spec, _, _ := unstructuredMap(obj.Object, "spec")
	podRef, _, _ := unstructuredMap(spec, "podRef")

	incident := &Incident{
		RemediationName: s.cfg.RemediationName,
		Namespace:       s.cfg.RemediationNamespace,
		Trigger:         stringField(spec, "trigger"),
		PodName:         stringField(podRef, "name"),
		PodNamespace:    stringField(podRef, "namespace"),
	}

	if incident.PodName == "" {
		return nil, fmt.Errorf("podRef.name is empty in Remediation CR")
	}
	if incident.PodNamespace == "" {
		incident.PodNamespace = s.cfg.RemediationNamespace
	}

	return incident, nil
}

func (s *Scout) gatherContext(ctx context.Context, incident *Incident, getResource *tools.GetResource, getPodLogs *tools.GetPodLogs) []tools.ToolResult {
	call := func(t tools.Tool, params any) tools.ToolResult {
		raw, _ := json.Marshal(params)
		out, err := t.Execute(ctx, raw)
		return tools.ToolResult{ToolName: t.Name(), Output: out, Err: err}
	}

	return []tools.ToolResult{
		call(getResource, map[string]string{
			"apiVersion": "v1", "kind": "Pod",
			"name": incident.PodName, "namespace": incident.PodNamespace,
		}),
		call(getResource, map[string]string{
			"apiVersion":    "v1", "kind": "Event",
			"namespace":     incident.PodNamespace,
			"fieldSelector": "involvedObject.name=" + incident.PodName,
		}),
		call(getPodLogs, map[string]string{
			"podName": incident.PodName, "namespace": incident.PodNamespace,
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
