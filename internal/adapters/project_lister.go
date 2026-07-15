package adapters

import (
	"context"
	"fmt"
	"sort"

	"github.com/tiny-systems/module/api/v1alpha1"
	sdktools "github.com/tiny-systems/module/pkg/tools"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/tiny-systems/tiny/internal/kube"
)

// ProjectLister implements sdktools.ProjectLister by enumerating
// TinyProject CRDs in the configured namespace. The display name comes
// from the same annotation the platform uses
// (v1alpha1.ProjectNameAnnotation), falling back to the resource name
// when the annotation is missing.
type ProjectLister struct {
	kube *kube.Client
}

func NewProjectLister(k *kube.Client) *ProjectLister {
	return &ProjectLister{kube: k}
}

func (p *ProjectLister) ListProjects(ctx context.Context) ([]sdktools.ProjectInfo, error) {
	list := &v1alpha1.TinyProjectList{}
	if err := p.kube.Client.List(ctx, list, client.InNamespace(p.kube.Namespace)); err != nil {
		return nil, wrapCRDError(fmt.Errorf("list TinyProjects: %w", err))
	}
	out := make([]sdktools.ProjectInfo, 0, len(list.Items))
	for _, proj := range list.Items {
		displayName := proj.Annotations[v1alpha1.ProjectNameAnnotation]
		if displayName == "" {
			displayName = proj.Name
		}
		out = append(out, sdktools.ProjectInfo{
			ResourceName: proj.Name,
			DisplayName:  displayName,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ResourceName < out[j].ResourceName })
	return out, nil
}
