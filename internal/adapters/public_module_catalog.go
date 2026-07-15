package adapters

import (
	"context"
	"fmt"

	platformapi "github.com/tiny-systems/platform-api"
	sdktools "github.com/tiny-systems/module/pkg/tools"
)

// PublicModuleCatalog implements sdktools.PublicModuleCatalog by calling
// the Tiny Systems REST API at /v1/modules. When constructed without a
// dev key, only public modules are visible. With a dev key, the platform
// widens scope to include workspace-private modules owned by the key's
// workspace.
type PublicModuleCatalog struct {
	client *platformapi.ClientWithResponses
}

// NewPublicModuleCatalog builds an adapter against the given server URL
// and optional dev key. Pass an empty devKey for anonymous access.
func NewPublicModuleCatalog(serverURL, devKey string) (*PublicModuleCatalog, error) {
	client, err := NewPlatformClient(serverURL, devKey)
	if err != nil {
		return nil, fmt.Errorf("init platform-api client: %w", err)
	}
	return &PublicModuleCatalog{client: client}, nil
}

// SearchModules calls GET /v1/modules/search and returns summaries.
func (p *PublicModuleCatalog) SearchModules(ctx context.Context, keyword string, limit int) ([]sdktools.PublicModuleSummary, error) {
	params := &platformapi.SearchPublicModulesParams{}
	if keyword != "" {
		q := keyword
		params.Q = &q
	}
	if limit > 0 {
		l := limit
		params.Limit = &l
	}

	resp, err := p.client.SearchPublicModulesWithResponse(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("search modules: %w", err)
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("search modules: unexpected status %d", resp.StatusCode())
	}

	out := make([]sdktools.PublicModuleSummary, 0, len(resp.JSON200.Results))
	for _, r := range resp.JSON200.Results {
		out = append(out, summaryToSDK(r))
	}
	return out, nil
}

// GetPublicModule calls GET /v1/modules/{name} and returns the full
// PublicModuleDetails payload. Returns nil (no error) on 404 so callers
// can distinguish "not found" from transport errors.
func (p *PublicModuleCatalog) GetPublicModule(ctx context.Context, name string) (*sdktools.PublicModuleDetails, error) {
	resp, err := p.client.GetPublicModuleWithResponse(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("get module: %w", err)
	}
	if resp.StatusCode() == 404 {
		return nil, nil
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("get module: unexpected status %d", resp.StatusCode())
	}

	body := resp.JSON200
	details := &sdktools.PublicModuleDetails{
		Name:        body.Name,
		Description: body.Description,
	}
	if body.FullName != nil {
		details.FullName = *body.FullName
	}
	if body.Verified != nil {
		details.Verified = *body.Verified
	}
	if body.LatestVersion != nil {
		v := body.LatestVersion
		details.LatestVersion = v.Version
		if v.SdkVersion != nil {
			details.SDKVersion = *v.SdkVersion
		}
		if v.ReleaseNotes != nil {
			details.ReleaseNotes = *v.ReleaseNotes
		}
		if v.Repo != nil {
			details.Repo = *v.Repo
		}
		if v.Tag != nil {
			details.Tag = *v.Tag
		}
		if v.RequiresKubernetesAccess != nil {
			details.RequiresKubernetesAccess = *v.RequiresKubernetesAccess
		}
		details.Components = make([]sdktools.PublicModuleComponent, 0, len(v.Components))
		for _, c := range v.Components {
			details.Components = append(details.Components, componentToSDK(c))
		}
		if v.Permissions != nil {
			for _, perm := range *v.Permissions {
				details.Permissions = append(details.Permissions, permissionToSDK(perm))
			}
		}
	}
	if body.HelmInstall != nil {
		details.HelmInstall = helmInstallToSDK(body.HelmInstall)
	}

	return details, nil
}

func summaryToSDK(r platformapi.PublicModuleSummary) sdktools.PublicModuleSummary {
	sum := sdktools.PublicModuleSummary{
		Name:        r.Name,
		Description: r.Description,
	}
	if r.FullName != nil {
		sum.FullName = *r.FullName
	}
	if r.Verified != nil {
		sum.Verified = *r.Verified
	}
	if r.LatestVersion != nil {
		sum.LatestVersion = r.LatestVersion.Version
		if r.LatestVersion.SdkVersion != nil {
			sum.SDKVersion = *r.LatestVersion.SdkVersion
		}
		if r.LatestVersion.RequiresKubernetesAccess != nil {
			sum.RequiresKubernetesAccess = *r.LatestVersion.RequiresKubernetesAccess
		}
	}
	return sum
}

func componentToSDK(c platformapi.PublicModuleComponent) sdktools.PublicModuleComponent {
	comp := sdktools.PublicModuleComponent{
		Name: c.Name,
	}
	if c.Description != nil {
		comp.Description = *c.Description
	}
	if c.Info != nil {
		comp.Info = *c.Info
	}
	if c.Tags != nil {
		comp.Tags = *c.Tags
	}
	if c.Ports != nil {
		for _, p := range *c.Ports {
			comp.Ports = append(comp.Ports, portToSDK(p))
		}
	}
	return comp
}

func portToSDK(p platformapi.PublicModulePort) sdktools.PublicModulePort {
	out := sdktools.PublicModulePort{
		Name: p.Name,
		Type: string(p.Type),
	}
	if p.Description != nil {
		out.Description = *p.Description
	}
	if p.Schema != nil {
		out.Schema = *p.Schema
	}
	if p.DefaultData != nil {
		out.DefaultData = *p.DefaultData
	}
	return out
}

func permissionToSDK(p platformapi.PublicModulePermission) sdktools.PublicModulePermission {
	out := sdktools.PublicModulePermission{}
	if p.ApiGroups != nil {
		out.APIGroups = *p.ApiGroups
	}
	if p.Resources != nil {
		out.Resources = *p.Resources
	}
	if p.Verbs != nil {
		out.Verbs = *p.Verbs
	}
	return out
}

func helmInstallToSDK(h *platformapi.HelmInstallConfig) *sdktools.PublicModuleHelmInstall {
	if h == nil {
		return nil
	}
	out := &sdktools.PublicModuleHelmInstall{}
	if h.Command != nil {
		out.Command = *h.Command
	}
	if h.ChartRepo != nil {
		out.ChartRepo = *h.ChartRepo
	}
	if h.ChartName != nil {
		out.ChartName = *h.ChartName
	}
	if h.Prerequisites != nil {
		out.Prerequisites = *h.Prerequisites
	}
	if h.Warnings != nil {
		out.Warnings = *h.Warnings
	}
	if h.RequiresIngress != nil {
		out.RequiresIngress = *h.RequiresIngress
	}
	if h.RequiresStorage != nil {
		out.RequiresStorage = *h.RequiresStorage
	}
	if h.Fields != nil {
		for _, f := range *h.Fields {
			field := sdktools.PublicModuleHelmField{}
			if f.Name != nil {
				field.Name = *f.Name
			}
			if f.Label != nil {
				field.Label = *f.Label
			}
			if f.Description != nil {
				field.Description = *f.Description
			}
			if f.DefaultValue != nil {
				field.DefaultValue = *f.DefaultValue
			}
			if f.Placeholder != nil {
				field.Placeholder = *f.Placeholder
			}
			if f.Required != nil {
				field.Required = *f.Required
			}
			if f.Type != nil {
				field.Type = *f.Type
			}
			if f.Options != nil {
				field.Options = *f.Options
			}
			out.Fields = append(out.Fields, field)
		}
	}
	return out
}

var _ sdktools.PublicModuleCatalog = (*PublicModuleCatalog)(nil)
