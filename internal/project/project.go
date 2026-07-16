// Package project resolves the active project for a tiny session. A tiny
// session works inside one project: it's chosen (or created) at start, and it
// scopes both surfaces — the MCP endpoint defaults to it, and the editor opens
// to it. Projects are just TinyProject CRs in the namespace.
package project

import (
	"context"

	"github.com/tiny-systems/module/pkg/resource"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/rest"
)

// Ensure returns the project's resource name, creating the TinyProject CR when
// it doesn't exist yet — "select or create" in one call.
func Ensure(ctx context.Context, cfg *rest.Config, namespace, name string) (string, error) {
	mgr, err := resource.NewManagerFromConfig(cfg, namespace)
	if err != nil {
		return "", err
	}
	if _, err := mgr.GetProject(ctx, name, namespace); err == nil {
		return name, nil
	}
	p, err := mgr.CreateProject(ctx, namespace, name)
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			return name, nil
		}
		return "", err
	}
	return p.Name, nil
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
