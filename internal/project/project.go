// Package project resolves the active project for a tiny session. A tiny
// session works inside one project: it's chosen (or created) at start, and it
// scopes both surfaces — the MCP endpoint defaults to it, and the editor opens
// to it. Projects are just TinyProject CRs in the namespace.
package project

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/tiny-systems/module/api/v1alpha1"
	"github.com/tiny-systems/module/pkg/resource"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
)

// ErrRuntimeNotInstalled means the TinyProject CRD isn't served by the cluster
// — the runtime hasn't been provisioned. Distinct so callers can say "run
// `tiny up`" instead of treating a missing runtime as "zero projects".
var ErrRuntimeNotInstalled = errors.New("tiny runtime not installed on cluster (no TinyProject CRD)")

// isNoCRD reports whether err is the cluster not serving the operator CRD group
// — meaning "not provisioned", not a real list failure. Covers the RESTMapper
// miss (NoKindMatchError), a 404, and the discovery aggregation error the SDK's
// manager surfaces on a fresh cluster: "unable to retrieve the complete list of
// server APIs: operator.tinysystems.io/v1alpha1: no matches ...". That last one
// is *discovery.ErrGroupDiscoveryFailed wrapped, so a type assert misses it;
// match on the unique group name in the message instead.
func isNoCRD(err error) bool {
	if err == nil {
		return false
	}
	if apimeta.IsNoMatchError(err) || apierrors.IsNotFound(err) {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "operator.tinysystems.io") &&
		(strings.Contains(s, "no matches") || strings.Contains(s, "could not find"))
}

// Ensure returns the project's resource name, creating the TinyProject CR when
// it doesn't exist yet — "select or create" in one call. Unlike the SDK's
// CreateProject (which generates a suffixed name), this uses the sanitized
// name verbatim so `--project demo` is a STABLE handle: the same command reuses
// the same project, and the name the user types is the name nodes are labeled
// with (so the MCP tools and the editor agree on it).
func Ensure(ctx context.Context, cfg *rest.Config, namespace, name string) (string, error) {
	rn := sanitizeName(name)
	if rn == "" {
		return "", fmt.Errorf("invalid project name %q", name)
	}
	mgr, err := resource.NewManagerFromConfig(cfg, namespace)
	if err != nil {
		return "", err
	}
	if existing, err := mgr.GetProject(ctx, rn, namespace); err == nil {
		// Backfill the display-name annotation on projects created before it was
		// set, so they too can be adopted if this cluster is connected to the
		// hosted platform. Best effort: a failure here must not stop the caller
		// from using a project that already exists.
		if existing.GetAnnotations()[v1alpha1.ProjectNameAnnotation] == "" {
			ann := existing.GetAnnotations()
			if ann == nil {
				ann = map[string]string{}
			}
			ann[v1alpha1.ProjectNameAnnotation] = name
			existing.SetAnnotations(ann)
			_ = mgr.GetK8sClient().Update(ctx, existing)
		}
		return rn, nil
	}
	// Carry the name the user typed as the display-name annotation. Nothing here
	// reads it — the resource name is the handle — but it is what the hosted
	// platform imports a cluster project by, and Project.Name is NOT NULL there,
	// so a project created without it cannot be adopted when someone later
	// connects this cluster to the platform.
	p := &v1alpha1.TinyProject{ObjectMeta: metav1.ObjectMeta{
		Name:        rn,
		Namespace:   namespace,
		Annotations: map[string]string{v1alpha1.ProjectNameAnnotation: name},
	}}
	if err := mgr.GetK8sClient().Create(ctx, p); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return rn, nil
		}
		return "", err
	}
	return rn, nil
}

// List returns the project resource names in the namespace — the choices for a
// startup picker.
func List(ctx context.Context, cfg *rest.Config, namespace string) ([]string, error) {
	mgr, err := resource.NewManagerFromConfig(cfg, namespace)
	if err != nil {
		return nil, err
	}
	projects, err := mgr.GetProjectList(ctx)
	if err != nil {
		if isNoCRD(err) {
			return nil, ErrRuntimeNotInstalled
		}
		return nil, err
	}
	names := make([]string, 0, len(projects))
	for _, p := range projects {
		names = append(names, p.Name)
	}
	return names, nil
}

// sanitizeName reduces a project handle to a valid RFC-1123 resource name:
// lowercase [a-z0-9-], no leading/trailing dash, capped at 63.
func sanitizeName(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 63 {
		out = strings.Trim(out[:63], "-")
	}
	return out
}
