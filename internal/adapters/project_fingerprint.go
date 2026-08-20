package adapters

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/tiny-systems/module/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/tiny-systems/tiny/internal/kube"
)

// ProjectFingerprint summarises everything about a project that could change
// what its flows do.
//
// Two sources, because breakage arrives from both. A node's generation moves
// when someone edits the flow — a changed setting, a new edge. A module's
// version moves when it is upgraded, which changes behaviour without touching
// the project at all: that is how a flow breaks while nobody is looking, and
// it is what nothing was watching for.
type ProjectFingerprint struct {
	kube *kube.Client
}

func NewProjectFingerprint(k *kube.Client) *ProjectFingerprint {
	return &ProjectFingerprint{kube: k}
}

// tinyMetadata summarises the labels and annotations this system gives
// meaning to. Everything else — kubectl's last-applied blob, a controller's
// bookkeeping — is noise that would re-run the suite for nothing.
func tinyMetadata(labels, annotations map[string]string) string {
	var parts []string
	for _, set := range []map[string]string{labels, annotations} {
		for k, v := range set {
			if strings.Contains(k, "tinysystems.io/") {
				parts = append(parts, k+"="+v)
			}
		}
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(fmt.Sprint(parts)))
	return hex.EncodeToString(sum[:4])
}

func (p *ProjectFingerprint) Fingerprint(ctx context.Context, projectName string) (string, error) {
	nodes := &v1alpha1.TinyNodeList{}
	if err := p.kube.Client.List(ctx, nodes,
		client.InNamespace(p.kube.Namespace),
		client.MatchingLabels{v1alpha1.ProjectNameLabel: projectName},
	); err != nil {
		return "", fmt.Errorf("list nodes: %w", err)
	}

	parts := make([]string, 0, len(nodes.Items)+8)
	for _, n := range nodes.Items {
		// Generation rather than resourceVersion: status updates bump the
		// latter constantly — a running flow would look like a changing one,
		// and the watch would never stop re-running.
		//
		// Generation alone is not enough either: it moves only on spec
		// changes, and several things that decide what a flow does live in
		// metadata — which flows a node is shared with, whether it is pinned
		// to the dashboard. Those would have been invisible.
		parts = append(parts, fmt.Sprintf("n/%s/%d/%s", n.Name, n.Generation, tinyMetadata(n.Labels, n.Annotations)))
	}

	modules := &v1alpha1.TinyModuleList{}
	if err := p.kube.Client.List(ctx, modules, client.InNamespace(p.kube.Namespace)); err != nil {
		return "", fmt.Errorf("list modules: %w", err)
	}
	for _, m := range modules.Items {
		parts = append(parts, fmt.Sprintf("m/%s/%s", m.Name, m.Status.Version))
	}

	sort.Strings(parts)
	sum := sha256.Sum256([]byte(fmt.Sprint(parts)))
	return hex.EncodeToString(sum[:8]), nil
}
