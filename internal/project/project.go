// Package project resolves the active project for a tiny session. A tiny
// session works inside one project: it's chosen (or created) at start, and it
// scopes both surfaces — the MCP endpoint defaults to it, and the editor opens
// to it. Projects are just TinyProject CRs in the namespace.
package project

import (
	"context"
	"fmt"
	"strings"

	"github.com/tiny-systems/module/api/v1alpha1"
	"github.com/tiny-systems/module/pkg/resource"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
)

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
	if _, err := mgr.GetProject(ctx, rn, namespace); err == nil {
		return rn, nil
	}
	p := &v1alpha1.TinyProject{ObjectMeta: metav1.ObjectMeta{Name: rn, Namespace: namespace}}
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
