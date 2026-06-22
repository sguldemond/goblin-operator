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
	remediationName string,
	remediationNamespace string,
) []Tool {
	status := NewUpdateRemediationStatus(dynCli, remediationName, remediationNamespace)
	return []Tool{
		NewGetResource(dynCli, mapper),
		NewGetPodLogs(client),
		NewPatchDeployment(client, targetNamespace, status),
		status,
		NewEscalate(status),
		NewExit(),
	}
}
