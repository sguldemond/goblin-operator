package tools

import (
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// NewAll returns every available tool, plus the status writer the scout loop
// uses to record tool outcomes on Incident CRs. Add new tools here.
//
// Tools no longer receive the status writer: one that concludes something about
// an incident implements OutcomeReporter and the loop does the writing.
func NewAll(
	client kubernetes.Interface,
	dynCli dynamic.Interface,
	mapper meta.RESTMapper,
) ([]Tool, *UpdateIncidentStatus) {
	status := NewUpdateIncidentStatus(dynCli)
	return []Tool{
		NewGetResource(dynCli, mapper),
		NewGetPodLogs(client),
		NewListIncidents(dynCli),
		NewPatchDeployment(client),
		status,
		NewEscalate(),
	}, status
}
