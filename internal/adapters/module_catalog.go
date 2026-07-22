package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tiny-systems/module/api/v1alpha1"
	sdktools "github.com/tiny-systems/module/pkg/tools"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/tiny-systems/tiny/internal/kube"
)

// ModuleCatalog implements sdktools.ModuleCatalog by reading TinyModule CRDs.
//
// When the module operator publishes per-component port schemas in
// TinyModule.Status.Components[].Ports (SDK commit 04944f7 and later),
// those surface on the returned ModuleInfo so get_component_info
// returns full upfront schemas. Older operators that don't yet publish
// ports yield ComponentInfo entries with empty port-detail slices;
// callers fall back to get_node_port_schema on placed nodes.
type ModuleCatalog struct {
	kube *kube.Client
}

func NewModuleCatalog(k *kube.Client) *ModuleCatalog {
	return &ModuleCatalog{kube: k}
}

// ListModules returns every TinyModule in the target namespace.
func (c *ModuleCatalog) ListModules(ctx context.Context) ([]sdktools.ModuleInfo, error) {
	list := &v1alpha1.TinyModuleList{}
	if err := c.kube.Client.List(ctx, list, client.InNamespace(c.kube.Namespace)); err != nil {
		return nil, wrapCRDError(fmt.Errorf("list TinyModules: %w", err))
	}

	out := make([]sdktools.ModuleInfo, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, moduleInfoFromCRD(&list.Items[i]))
	}
	return out, nil
}

// GetModule finds a single module by name. The lookup is permissive:
// it accepts the hosted platform's slash-qualified form
// ("tinysystems/common-module-v0"), the kubernetes resource form with
// dashes ("tinysystems-common-module-v0"), and bare names without any
// workspace prefix ("common-module-v0"). Case-insensitive.
func (c *ModuleCatalog) GetModule(ctx context.Context, name string) (*sdktools.ModuleInfo, error) {
	list := &v1alpha1.TinyModuleList{}
	if err := c.kube.Client.List(ctx, list, client.InNamespace(c.kube.Namespace)); err != nil {
		return nil, wrapCRDError(fmt.Errorf("list TinyModules: %w", err))
	}

	for i := range list.Items {
		if moduleNameMatches(name, &list.Items[i]) {
			info := moduleInfoFromCRD(&list.Items[i])
			return &info, nil
		}
	}
	return nil, nil // not found (no error per SDK contract)
}

// moduleInfoFromCRD converts a TinyModule CRD to the SDK's ModuleInfo.
// Per-component port metadata published in the CRD's status is surfaced
// as InputPortDetails / OutputPortDetails so get_component_info returns
// full schemas when the operator publishes them.
func moduleInfoFromCRD(m *v1alpha1.TinyModule) sdktools.ModuleInfo {
	name := m.Status.Name
	if name == "" {
		name = m.Name
	}

	info := sdktools.ModuleInfo{
		Name:    name,
		Version: m.Status.Version,
	}

	info.Components = make([]sdktools.ComponentInfo, 0, len(m.Status.Components))
	for _, c := range m.Status.Components {
		compInfo := sdktools.ComponentInfo{
			Name:        c.Name,
			Description: c.Description,
			Info:        c.Info,
		}
		for _, p := range c.Ports {
			if p.Source {
				compInfo.OutputPorts = append(compInfo.OutputPorts, p.Name)
				compInfo.OutputPortDetails = append(compInfo.OutputPortDetails, portDetailFromCRD(p))
				continue
			}
			compInfo.InputPorts = append(compInfo.InputPorts, p.Name)
			compInfo.InputPortDetails = append(compInfo.InputPortDetails, portDetailFromCRD(p))
		}
		info.Components = append(info.Components, compInfo)
	}
	return info
}

// portDetailFromCRD converts a TinyModuleComponentPort (static,
// component-level port info published by the module operator) into the
// SDK's PortDetail shape expected by the get_component_info tool.
//
// Schema bytes pass through as json.RawMessage so downstream callers
// can embed them directly in the tool output without an extra parse
// and re-marshal round-trip.
func portDetailFromCRD(p v1alpha1.TinyModuleComponentPort) sdktools.PortDetail {
	d := sdktools.PortDetail{
		Name:        p.Name,
		Description: p.Label,
	}
	if len(p.Schema) > 0 {
		d.Schema = json.RawMessage(p.Schema)
	}
	return d
}

// moduleNameMatches reports whether `wanted` (as supplied by the caller)
// identifies the given TinyModule. Slashes and dashes are treated as
// equivalent workspace separators and comparison is case-insensitive. The
// workspace prefix is optional in BOTH directions: a bare wanted name
// matches a qualified module, and a qualified wanted name matches a bare
// module (what decentralized installs produce).
func moduleNameMatches(wanted string, m *v1alpha1.TinyModule) bool {
	w := normalizeSeparators(wanted)
	if w == "" {
		return false
	}

	candidates := [2]string{
		normalizeSeparators(m.Status.Name),
		strings.ToLower(m.Name),
	}

	for _, c := range candidates {
		if c == "" {
			continue
		}
		if c == w {
			return true
		}
		// Bare name wanted, qualified module installed: "common-module-v0"
		// should match CRD "tinysystems-common-module-v0".
		if strings.HasSuffix(c, "-"+w) {
			return true
		}
		// Qualified name wanted, bare module installed: decentralized
		// installs name modules bare ("http-module-v0"), while the platform's
		// docs and examples use "tinysystems/http-module-v0". Both must
		// resolve, or an agent following get_instructions verbatim gets a
		// spurious "module not found".
		if strings.HasSuffix(w, "-"+c) {
			return true
		}
	}
	return false
}

// normalizeSeparators lowercases and replaces '/' with '-' so that
// workspace/module and workspace-module forms are equivalent.
func normalizeSeparators(s string) string {
	return strings.ToLower(strings.ReplaceAll(s, "/", "-"))
}

var _ sdktools.ModuleCatalog = (*ModuleCatalog)(nil)
