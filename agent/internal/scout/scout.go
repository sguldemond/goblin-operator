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
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"k8s.io/apimachinery/pkg/runtime/schema"

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
	incident, err := s.loadIncident(ctx)
	if err != nil {
		return fmt.Errorf("loading incident from CR: %w", err)
	}

	toolList := []tools.Tool{
		tools.NewGetPodDetails(s.client),
		tools.NewGetEvents(s.client),
		tools.NewGetPodLogs(s.client),
		tools.NewPatchMemoryLimit(s.client),
	}

	results := s.runTools(ctx, incident, toolList)
	contextMsg := BuildContext(*incident, results, toolList)

	c := llm.NewClient(s.cfg.APIKey)
	return s.runLoop(ctx, c.Send, contextMsg, toolList, os.Stdin, os.Stdout)
}

type sendFn func(ctx context.Context, req llm.Request) (llm.Response, error)

func (s *Scout) runLoop(ctx context.Context, send sendFn, contextMsg string, toolList []tools.Tool, in io.Reader, out io.Writer) error {
	// Build tool map for dispatch and tool defs for the API.
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

		// Append assistant turn to history.
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
				output, err := t.Execute(ctx, c.Input)
				if err != nil {
					toolResults = append(toolResults, llm.Content{
						Type:      "tool_result",
						ToolUseID: c.ID,
						IsError:   true,
						Content:   err.Error(),
					})
				} else {
					toolResults = append(toolResults, llm.Content{
						Type:      "tool_result",
						ToolUseID: c.ID,
						Content:   output,
					})
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

		// Prompt for human input — loop until we get a non-empty line.
		var line string
		for {
			fmt.Fprint(out, "\n> ")
			if !scanner.Scan() {
				return scanner.Err() // nil on clean EOF
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

func (s *Scout) runTools(ctx context.Context, incident *Incident, toolList []tools.Tool) []tools.ToolResult {
	baseParams, _ := json.Marshal(map[string]string{
		"podName":   incident.PodName,
		"namespace": incident.PodNamespace,
	})

	results := make([]tools.ToolResult, 0, len(toolList))
	for _, t := range toolList {
		out, err := t.Execute(ctx, baseParams)
		results = append(results, tools.ToolResult{ToolName: t.Name(), Output: out, Err: err})
	}
	return results
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
