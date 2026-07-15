// Package kube resolves a *rest.Config from the user's kubeconfig the same
// way kubectl does — honouring KUBECONFIG, ~/.kube/config, and an explicit
// --context override. Everything that touches the cluster (helm installs,
// the NATS token read) goes through here so `tiny` acts on exactly the
// cluster the user confirmed.
package kube

import (
	"fmt"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// RestConfig builds a client config from the local kubeconfig. An empty
// contextName means "use the kubeconfig's current-context" — the same
// default `tiny status` and the confirmation prompt already report.
//
// No client-side Timeout is set on purpose: helm waits on releases with its
// own per-operation timeout, and a short rest timeout here would sever those
// long installs mid-flight.
func RestConfig(contextName string) (*rest.Config, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	overrides := &clientcmd.ConfigOverrides{}
	if contextName != "" {
		overrides.CurrentContext = contextName
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	return cfg, nil
}
