package k8s

import (
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// NewClient builds an in-cluster client, falling back to KUBECONFIG or ~/.kube/config for dev.
// Returns both the typed client and the raw config (needed for dynamic clients).
func NewClient() (*rest.Config, kubernetes.Interface, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		// Respects KUBECONFIG env var; falls back to ~/.kube/config.
		loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
		clientCfg := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, nil)
		cfg, err = clientCfg.ClientConfig()
		if err != nil {
			return nil, nil, err
		}
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, nil, err
	}
	return cfg, client, nil
}
