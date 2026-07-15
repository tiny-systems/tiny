package adapters

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode"

	"github.com/tiny-systems/module/api/v1alpha1"
	sdktools "github.com/tiny-systems/module/pkg/tools"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/tiny-systems/tiny/internal/kube"
)

// FlowLifecycle implements sdktools.FlowCreator and sdktools.FlowDeleter.
// Flows are thin label containers in the cluster — a TinyFlow CRD with a
// project-name label is all there is.
type FlowLifecycle struct {
	kube *kube.Client
}

func NewFlowLifecycle(k *kube.Client) *FlowLifecycle {
	return &FlowLifecycle{kube: k}
}

// CreateFlow creates a TinyFlow resource in the project.
//
// The input flowName may be a human-readable title ("Test Flow") or an
// already-valid resource name ("test-flow-abc12"). The returned string is
// always the k8s resource name of the created (or existing) flow. The
// original human name, if slugified, is preserved on the flow-description
// annotation so callers can surface it in UIs.
//
// Idempotent: if a valid-name flow already exists, the existing name is
// returned unchanged.
func (f *FlowLifecycle) CreateFlow(ctx context.Context, projectName, flowName string) (string, error) {
	if projectName == "" {
		return "", fmt.Errorf("project name required")
	}
	if flowName == "" {
		return "", fmt.Errorf("flow name required")
	}

	// If the caller passed a name that's already a valid DNS subdomain,
	// treat it as the resource name and try a direct get-or-create.
	if isValidDNSSubdomain(flowName) {
		existing, err := f.getFlow(ctx, flowName)
		if err != nil {
			return "", err
		}
		if existing != nil {
			return existing.Name, nil
		}
		return f.createFlow(ctx, projectName, flowName, flowName)
	}

	// Human-readable name — slugify and append a random suffix for uniqueness.
	resourceName := slugifyFlowName(flowName) + "-" + randAlphaSuffix(5)
	return f.createFlow(ctx, projectName, resourceName, flowName)
}

// createFlow performs the actual Create call.
func (f *FlowLifecycle) createFlow(ctx context.Context, projectName, resourceName, displayName string) (string, error) {
	flow := &v1alpha1.TinyFlow{
		ObjectMeta: metav1.ObjectMeta{
			Name:      resourceName,
			Namespace: f.kube.Namespace,
			Labels: map[string]string{
				v1alpha1.ProjectNameLabel: projectName,
			},
			Annotations: map[string]string{
				v1alpha1.FlowDescriptionAnnotation: displayName,
			},
		},
	}
	if err := f.kube.Client.Create(ctx, flow); err != nil {
		return "", wrapCRDError(fmt.Errorf("create TinyFlow: %w", err))
	}
	return flow.Name, nil
}

func (f *FlowLifecycle) getFlow(ctx context.Context, name string) (*v1alpha1.TinyFlow, error) {
	existing := &v1alpha1.TinyFlow{}
	err := f.kube.Client.Get(ctx, types.NamespacedName{
		Namespace: f.kube.Namespace,
		Name:      name,
	}, existing)
	if err == nil {
		return existing, nil
	}
	if k8serrors.IsNotFound(err) {
		return nil, nil
	}
	return nil, fmt.Errorf("lookup existing flow: %w", err)
}

// slugifyFlowName converts a human-readable name to a DNS-safe slug:
// lowercase, non-alphanumeric collapsed to single hyphens, trimmed.
// Empty input yields "flow".
func slugifyFlowName(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	prevHyphen := true
	for _, r := range strings.ToLower(name) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			prevHyphen = false
			continue
		}
		if !prevHyphen {
			b.WriteByte('-')
			prevHyphen = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return "flow"
	}
	// Kubernetes resource name max length is 253, but keep some headroom
	// for the "-" separator and the 5-char random suffix we append.
	const maxSlugLen = 240
	if len(slug) > maxSlugLen {
		slug = slug[:maxSlugLen]
	}
	return slug
}

// isValidDNSSubdomain reports whether s is a valid RFC 1123 subdomain as
// used by Kubernetes for resource names.
func isValidDNSSubdomain(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	prev := byte('.') // force first char to be alphanumeric
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9'):
			// ok
		case c == '-' || c == '.':
			if prev == '-' || prev == '.' {
				return false
			}
		default:
			return false
		}
		prev = c
	}
	return prev != '-' && prev != '.'
}

// randAlphaSuffix returns a hex-encoded random suffix of the given length.
func randAlphaSuffix(n int) string {
	buf := make([]byte, (n+1)/2)
	if _, err := rand.Read(buf); err != nil {
		return "00000"[:n]
	}
	return hex.EncodeToString(buf)[:n]
}

// DeleteFlow deletes a TinyFlow and all TinyNodes belonging to it.
// Nodes are identified via the flow-name label.
func (f *FlowLifecycle) DeleteFlow(ctx context.Context, projectName, flowName string) error {
	if err := f.deleteFlowNodes(ctx, flowName); err != nil {
		return fmt.Errorf("delete flow nodes: %w", err)
	}

	flow := &v1alpha1.TinyFlow{
		ObjectMeta: metav1.ObjectMeta{
			Name:      flowName,
			Namespace: f.kube.Namespace,
		},
	}
	if err := f.kube.Client.Delete(ctx, flow); err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("delete TinyFlow: %w", err)
	}
	return nil
}

func (f *FlowLifecycle) deleteFlowNodes(ctx context.Context, flowName string) error {
	return f.kube.Client.DeleteAllOf(ctx, &v1alpha1.TinyNode{},
		client.InNamespace(f.kube.Namespace),
		client.MatchingLabels{v1alpha1.FlowNameLabel: flowName},
	)
}

var (
	_ sdktools.FlowCreator = (*FlowLifecycle)(nil)
	_ sdktools.FlowDeleter = (*FlowLifecycle)(nil)
)
