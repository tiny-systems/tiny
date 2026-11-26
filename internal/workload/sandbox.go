package workload

import (
	"context"
	"fmt"

	nodev1 "k8s.io/api/node/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentsv1 "github.com/tiny-systems/tiny/api/v1alpha1"
	"github.com/tiny-systems/tiny/internal/settings"
)

// NoRuntimeClass is what a caller passes to opt one session out of the
// namespace's default sandbox.
const NoRuntimeClass = "none"

// NoSandboxAnnotation marks a session that opted out of the namespace
// sandbox, so the workload doesn't apply the default to it later.
const NoSandboxAnnotation = "tinysystems.io/no-sandbox"

// optedOut is NoSandboxAnnotation's value.
const optedOut = "true"

// ApplyDefaultRuntimeClass fills a new session's runtime class from the
// namespace settings when the session names none. It writes the class
// into the spec, so the session shows what it runs under and a later
// change of the setting never moves a running session. A session whose
// class is NoRuntimeClass is cleared and left on the node's runtime.
func ApplyDefaultRuntimeClass(ctx context.Context, c client.Client, s *agentsv1.Session) error {
	switch s.Spec.RuntimeClassName {
	case NoRuntimeClass:
		s.Spec.RuntimeClassName = ""
		if s.Annotations == nil {
			s.Annotations = map[string]string{}
		}
		s.Annotations[NoSandboxAnnotation] = optedOut
		return nil
	case "":
		if s.Annotations[NoSandboxAnnotation] == optedOut {
			return nil
		}
	default:
		return nil
	}
	cfg, err := settings.Load(ctx, c, s.Namespace)
	if err != nil {
		return fmt.Errorf("read settings: %w", err)
	}
	s.Spec.RuntimeClassName = cfg.RuntimeClass
	return nil
}

// RuntimeClassExists reports whether the cluster has a RuntimeClass. A
// caller who may not read RuntimeClasses gets (true, nil): they are
// cluster-scoped, and the pod reports a wrong name anyway.
func RuntimeClassExists(ctx context.Context, c client.Client, name string) (bool, error) {
	err := c.Get(ctx, types.NamespacedName{Name: name}, &nodev1.RuntimeClass{})
	switch {
	case err == nil, apierrors.IsForbidden(err):
		return true, nil
	case apierrors.IsNotFound(err):
		return false, nil
	default:
		return false, err
	}
}
