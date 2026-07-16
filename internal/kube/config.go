// Package kube resolves a *rest.Config from the user's kubeconfig the same
// way kubectl does — honouring KUBECONFIG, ~/.kube/config, and an explicit
// --context override. Everything that touches the cluster (helm installs,
// the NATS token read) goes through here so `tiny` acts on exactly the
// cluster the user confirmed.
package kube

import (
	"fmt"
	"time"

	"k8s.io/client-go/kubernetes"
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

// Ping verifies the cluster is actually reachable and the credentials work —
// a real round-trip to the API server (which also exercises any exec
// credential plugin, e.g. gcloud). Returns the underlying error so callers can
// stop with a clear message instead of serving against a dead connection.
func Ping(cfg *rest.Config) error {
	c := rest.CopyConfig(cfg)
	c.Timeout = 8 * time.Second
	cs, err := kubernetes.NewForConfig(c)
	if err != nil {
		return err
	}
	if _, err := cs.Discovery().ServerVersion(); err != nil {
		return err
	}
	return nil
}
