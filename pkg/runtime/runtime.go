/*
Package runtime installs tiny's session runtime from a Go program outside
this module, the way `tiny init` does: an installer that sets up a cluster
for its own users and should not make them install the tiny CLI first.

	err := runtime.Init(ctx, cfg, "tiny")

Init is safe to re-run: everything is server-side applied, so an upgrade is
the same call as an install.

# Permissions

Init writes cluster-scoped objects (the namespace and two CRDs), so the
caller needs cluster-admin or close to it. Inside the namespace it writes a
ServiceAccount, the tiny-settings ConfigMap, the egress NetworkPolicy and
whatever add-ons the switchboard already has on.
*/
package runtime

import (
	"context"
	"fmt"

	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/tiny-systems/tiny/internal/kube"
	"github.com/tiny-systems/tiny/internal/runtimeinstall"
	"github.com/tiny-systems/tiny/internal/sessions"
)

// Init installs or upgrades the session runtime in namespace, creating the
// namespace if needed. A new namespace gets tiny's defaults (egress fenced);
// a configured one keeps its settings, and the add-ons it has on are
// re-applied from this module's specs.
//
// Unlike `tiny init`, a namespace left without its egress policy is an
// error here: an installer that hands people a cluster must not hand them
// one whose sessions can reach anything.
func Init(ctx context.Context, cfg *rest.Config, namespace string) error {
	if namespace == "" {
		return fmt.Errorf("namespace is required")
	}
	if err := runtimeinstall.Apply(ctx, cfg, namespace); err != nil {
		return fmt.Errorf("install the session runtime: %w", err)
	}
	k, err := kube.NewClientFromConfig(cfg, namespace)
	if err != nil {
		return err
	}
	store := &sessions.Store{Kube: k}
	if err := store.EnsureNamespaceDefaults(ctx); err != nil {
		return fmt.Errorf("apply the namespace defaults: %w", err)
	}
	if err := store.ReapplyAddons(ctx); err != nil {
		return fmt.Errorf("re-apply add-ons: %w", err)
	}
	// A namespace configured with the fence off keeps its setting; Init
	// reports it rather than turning it on behind its owner's back.
	pol := &networkingv1.NetworkPolicy{}
	if err := k.Client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: egressPolicy}, pol); err != nil {
		return fmt.Errorf("egress policy %s in %s: %w (turn egress on in the tiny-settings ConfigMap)",
			egressPolicy, namespace, err)
	}
	return nil
}

// egressPolicy is the NetworkPolicy the egress add-on maintains.
const egressPolicy = "tiny-session-egress"
