// Package catalog resolves a module NAME to the coordinates `tiny` needs to
// install it, using the public, unauthenticated Tiny Systems catalog at
// api.tinysystems.io. No account, no key.
//
// The key insight: every module installs from the SAME helm chart
// (tinysystems-operator). What varies per module is the container image
// (repo + tag) and whether it needs Kubernetes RBAC. So "resolve a module"
// means "look up its image + access flag" — the chart itself is constant.
package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// DefaultBaseURL is the public catalog. Overridable for on-prem mirrors.
const DefaultBaseURL = "https://api.tinysystems.io"

// Module is the resolved, install-ready view of a catalog module.
type Module struct {
	// Name is what the user typed (e.g. "http-module" or "http-module-v0").
	Name string
	// FullName is workspace-qualified (e.g. "tinysystems/http-module-v0").
	// This is what the operator runs as `--name`, so node IDs the agent
	// builds line up with the module that reconciles them.
	FullName string
	// Repo + Tag are the container image coordinates fed to the operator chart.
	Repo string
	Tag  string
	// RequiresKubernetesAccess turns on the chart's baseline RBAC (pods,
	// services, deployments, ingresses) for modules that manage cluster
	// resources — http-module's port exposure, kubernetes-module's ops.
	RequiresKubernetesAccess bool
}

// apiModule mirrors the fields we consume from GET /v1/modules/{name}.
// The response carries much more (component schemas, release notes); we
// decode only what an install needs.
type apiModule struct {
	FullName      string `json:"full_name"`
	Name          string `json:"name"`
	LatestVersion struct {
		Repo                     string `json:"repo"`
		Tag                      string `json:"tag"`
		RequiresKubernetesAccess bool   `json:"requires_kubernetes_access"`
	} `json:"latest_version"`
}

// Resolve looks up a module by name against the public catalog. Public
// module names carry a version suffix ("-v0"); as a convenience we accept
// the bare name and retry with the suffix so `tiny install http-module`
// works as well as `tiny install http-module-v0`.
func Resolve(ctx context.Context, name string) (*Module, error) {
	return resolve(ctx, DefaultBaseURL, name)
}

func resolve(ctx context.Context, baseURL, name string) (*Module, error) {
	candidates := []string{name}
	if !strings.HasSuffix(name, "-v0") {
		candidates = append(candidates, name+"-v0")
	}

	var lastErr error
	for _, cand := range candidates {
		m, err := fetch(ctx, baseURL, cand)
		if err == nil {
			return m, nil
		}
		lastErr = err
		if !isNotFound(err) {
			return nil, err // a real error (network, 5xx) — don't mask it behind the retry
		}
	}
	return nil, lastErr
}

func fetch(ctx context.Context, baseURL, name string) (*Module, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	url := fmt.Sprintf("%s/v1/modules/%s", strings.TrimRight(baseURL, "/"), name)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reach catalog: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, notFoundErr{name}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("catalog returned %s for %q", resp.Status, name)
	}

	var am apiModule
	if err := json.NewDecoder(resp.Body).Decode(&am); err != nil {
		return nil, fmt.Errorf("decode catalog response: %w", err)
	}
	if am.LatestVersion.Repo == "" || am.LatestVersion.Tag == "" {
		return nil, fmt.Errorf("module %q has no published image", name)
	}

	full := am.FullName
	if full == "" {
		full = am.Name
	}
	return &Module{
		Name:                     name,
		FullName:                 full,
		Repo:                     am.LatestVersion.Repo,
		Tag:                      am.LatestVersion.Tag,
		RequiresKubernetesAccess: am.LatestVersion.RequiresKubernetesAccess,
	}, nil
}

type notFoundErr struct{ name string }

func (e notFoundErr) Error() string { return fmt.Sprintf("module %q not found in catalog", e.name) }

func isNotFound(err error) bool {
	_, ok := err.(notFoundErr)
	return ok
}
