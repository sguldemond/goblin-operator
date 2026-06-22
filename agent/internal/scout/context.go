package scout

import (
	"fmt"
	"strings"

	"github.com/sguldemond/goblin/agent/internal/tools"
)


const systemPrompt = `You are goblin-scout. A pod has failed — investigate and propose one fix.
Keep replies short: what broke, why, what you recommend. No fluff, no restating context.
Do not apply changes without explicit human approval.
Plain text only — no markdown tables, no pipe characters, no ## headers.
Use simple bullet points or labeled lines (e.g. "Root cause: ...") when structure helps.`

// Incident holds the parsed CR fields the scout was dispatched for.
type Incident struct {
	RemediationName string
	Namespace       string
	PodName         string
	PodNamespace    string
	Trigger         string
}

// BuildContext assembles the initial user message: incident header + full tool output.
func BuildContext(incident Incident, results []tools.ToolResult) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("Incident: %s pod %s/%s\n", incident.Trigger, incident.PodNamespace, incident.PodName))
	sb.WriteString(fmt.Sprintf("Remediation CR: apiVersion=ops.goblinoperator.io/v1alpha1 kind=Remediation namespace=%s name=%s\n\n",
		incident.Namespace, incident.RemediationName))

	for _, r := range results {
		sb.WriteString(fmt.Sprintf("--- %s ---\n", r.ToolName))
		if r.Err != nil {
			sb.WriteString(fmt.Sprintf("error: %v\n", r.Err))
		} else {
			sb.WriteString(r.Output)
		}
		sb.WriteString("\n")
	}

	return sb.String()
}
