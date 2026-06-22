package tools

import (
	"context"

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
	onEscalate func(ctx context.Context, reason string) error,
	onApplied func(ctx context.Context) error,
) []Tool {
	return []Tool{
		NewGetResource(dynCli, mapper),
		NewGetPodLogs(client),
		NewPatchDeployment(client, targetNamespace, onApplied),
		NewUpdateRemediationStatus(dynCli, remediationName, remediationNamespace),
		NewEscalate(onEscalate),
		NewExit(),
	}
}
