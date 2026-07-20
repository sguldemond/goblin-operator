package scout

import (
	"fmt"
	"strings"

	"github.com/sguldemond/goblin/agent/internal/tools"
)

const systemPrompt = `You are goblin-scout. A Kubernetes object has failed — investigate and propose one fix.
Do not apply changes without explicit human approval.
Plain text only — no markdown tables, no pipe characters, no ## headers.

Never describe a change you have not staged with a tool. Pick the tool that fits the object you
are fixing from the tools you have been given, call it, and then respond with exactly these two
labeled lines:
Cause: <root cause of the failure, one sentence>
Fix: <what the change does and why it solves the cause, one sentence>

Do not include the staged change or reasoning in your response — those are shown separately by the system.
For any other response (investigation, questions, escalation) keep it short and plain.`

// Incident holds the parsed CR fields the scout was dispatched for.
// TargetAPIVersion/TargetKind carry the target's own GVK so the scout can
// investigate kinds other than Pod.
type Incident struct {
	IncidentName     string
	Namespace        string
	TargetAPIVersion string
	TargetKind       string
	TargetName       string
	TargetNamespace  string
	Trigger          string
}

// BuildContext assembles the initial user message: incident header + full tool output.
func BuildContext(incident Incident, results []tools.ToolResult) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "Incident: %s on %s %s/%s\n",
		incident.Trigger, incident.TargetKind, incident.TargetNamespace, incident.TargetName)
	fmt.Fprintf(&sb, "Incident CR: apiVersion=ops.goblinoperator.io/v1alpha1 kind=Incident namespace=%s name=%s\n\n",
		incident.Namespace, incident.IncidentName)

	for _, r := range results {
		fmt.Fprintf(&sb, "--- %s ---\n", r.ToolName)
		if r.Err != nil {
			fmt.Fprintf(&sb, "error: %v\n", r.Err)
		} else {
			sb.WriteString(r.Output)
		}
		sb.WriteString("\n")
	}

	return sb.String()
}
