// control-plane/internal/k8s/client.go
package k8s

import (
	"fmt"
	"os"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// NewDynamicClient returns a dynamic.Interface using in-cluster config when
// KUBERNETES_SERVICE_HOST is set, falling back to ~/.kube/config for local dev.
func NewDynamicClient() (dynamic.Interface, error) {
	cfg, err := restConfig()
	if err != nil {
		return nil, fmt.Errorf("build rest config: %w", err)
	}
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("dynamic.NewForConfig: %w", err)
	}
	return client, nil
}

func restConfig() (*rest.Config, error) {
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		return rest.InClusterConfig()
	}
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	configOverrides := &clientcmd.ConfigOverrides{}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loadingRules, configOverrides,
	).ClientConfig()
}
