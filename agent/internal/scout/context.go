package scout

import (
	"fmt"
	"strings"

	"github.com/sguldemond/goblin/agent/internal/tools"
)

const systemPrompt = `You are goblin-scout. You are long-running: you watch a cluster, and Kubernetes
objects that fail are handed to you as incidents. Investigate and propose one fix at a time.
Plain text only — no markdown tables, no pipe characters, no ## headers.

Several incidents may be open at once and they are often the same problem seen from different
angles — three OOMKilled pods of one Deployment are one fault, not three. Before treating a new
incident as unrelated, check whether it shares a target, an owner, or a namespace with something
you are already working on, and say so when it does. One fix that resolves several incidents is
better than several fixes.

When one fix resolves several incidents, list every one of them in resolvesIncidents so they all
close together. HandedOff means a human has taken the problem over — never use it to report a fix
you applied yourself.

You have a memory: every past investigation is recorded as an Incident, with the outcome in its
status message. When history would change your conclusion — a target you have patched before, a
fix that did not hold, a problem that keeps coming back — call listIncidents and read it. A
Deployment failing again after you already raised its limits means the first fix treated a
symptom.

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
	PolicyRef        string
}

// Key identifies an incident across namespaces, for tracking which are open.
func (i Incident) Key() string { return i.Namespace + "/" + i.IncidentName }

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
