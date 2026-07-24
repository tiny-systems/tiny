// Package kube resolves a *rest.Config from the user's kubeconfig the same
// way kubectl does — honouring KUBECONFIG, ~/.kube/config, and an explicit
// --context override. Everything that touches the cluster (helm installs,
// the NATS token read) goes through here so `tiny` acts on exactly the
// cluster the user confirmed.
package kube

import (
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// runtimeGroupVersion is the operator's CRD API group, served only once the
// runtime is provisioned. A fresh cluster does not serve it.
const runtimeGroupVersion = "operator.tinysystems.io/v1alpha1"

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

// RuntimeInstalled reports whether the Tiny Systems runtime is provisioned on
// the cluster — i.e. whether the operator's CRD group is served. A fresh
// cluster has no CRDs, so the serve path can stop with "run `tiny up`" instead
// of prompting for a project whose CRD doesn't exist yet and then just
// listening. A false with nil error means "definitely not provisioned"; a
// non-nil error means the check itself couldn't run (callers should fail open).
func RuntimeInstalled(cfg *rest.Config) (bool, error) {
	c := rest.CopyConfig(cfg)
	c.Timeout = 8 * time.Second
	cs, err := kubernetes.NewForConfig(c)
	if err != nil {
		return false, err
	}
	if _, err := cs.Discovery().ServerResourcesForGroupVersion(runtimeGroupVersion); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil // group not served — runtime not provisioned
		}
		return false, err
	}
	return true, nil
}
