package tools

import (
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// NewAll returns every available tool. Add new tools here.
func NewAll(
	client kubernetes.Interface,
	dynCli dynamic.Interface,
	mapper meta.RESTMapper,
	targetNamespace string,
	incidentName string,
	incidentNamespace string,
) []Tool {
	status := NewUpdateIncidentStatus(dynCli, incidentName, incidentNamespace)
	return []Tool{
		NewGetResource(dynCli, mapper),
		NewGetPodLogs(client),
		NewPatchDeployment(client, targetNamespace, status),
		status,
		NewEscalate(status),
		NewExit(),
	}
}
