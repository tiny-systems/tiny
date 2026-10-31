/*
Package session lets a Go program outside this module start and remove
tiny sessions: a CI runner, an operator, a service running in the cluster.

tiny has no manager. Creating a Session CR does nothing by itself; whoever
creates it also creates its workload (a workspace PVC and a one-replica
Deployment, both owned by the Session) using their own credentials. The
tiny CLI does this on `tiny new`. Create does the same for you, through the
same code the CLI uses.

	c, _ := client.New(cfg, client.Options{Scheme: scheme}) // scheme has corev1, appsv1 and agentsv1
	s := &agentsv1.Session{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tiny", Name: "fix-123"},
		Spec:       agentsv1.SessionSpec{Task: "fix issue 123", Repo: "git@github.com:acme/app.git"},
	}
	err := session.Create(ctx, c, s)

The client's scheme must know corev1, appsv1 and agents.tinysystems.io
v1alpha1 (agentsv1.AddToScheme): owner references are built from it.

# Permissions

The caller's identity needs these rights in the session's namespace. The
runner Role that tiny installs for its GitHub Actions add-on grants the
same set.

  - agents.tinysystems.io sessions: create, get (Create, and Ensure when
    the Session has no UID yet); delete (Delete).
  - persistentvolumeclaims: get, create (the workspace claim).
  - apps deployments: get, create (the session's workload).
  - configmaps: get (the tiny-settings ConfigMap: image overrides, egress
    allow-list and registry-cache switches).
  - services: get (the tiny-egress and tiny-zot ClusterIPs).
  - secrets: get, only for sessions carrying the
    tinysystems.io/env-secret annotation (the secret must exist and be
    labelled tinysystems.io/for-session=<session name>).

Read the last three as required, not optional. When a read fails, the
workload is still created, just without what that read would have added.
Without configmaps or services get, a session in a namespace with the
egress allow-list switched on is created without its proxy settings. The
namespace's NetworkPolicy then drops its traffic, so the agent cannot
reach its model API.

Clusters that enable the OwnerReferencesPermissionEnforcement admission
plugin also require update on sessions/finalizers, because the PVC and
Deployment carry blockOwnerDeletion owner references.

The session pod itself runs as the tiny-session ServiceAccount, which
`tiny init` (config/install/install.yaml) creates. The caller needs no
rights on it.

# Images

Unless overridden, sessions pull ghcr.io/tiny-systems images tagged
"main". A program that wants reproducible sessions should pin them once
at startup with SetImageTag. The tiny CLI passes its own release version.
Per-namespace overrides in tiny-settings (agentImage, sidecarImage) still
win over the tag, and spec.image chooses the session's toolchain image.
*/
package session

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentsv1 "github.com/tiny-systems/tiny/api/v1alpha1"
	"github.com/tiny-systems/tiny/internal/workload"
)

// SessionLabel is set to the session's name on its pods. Select on it to
// find or watch a session's pod.
const SessionLabel = workload.SessionLabel

// SetImageTag pins the default agent and sidecar images to a tiny
// release, given as "0.12.0" or "v0.12.0". Anything that is not a plain
// release version is ignored and the images stay on "main". It sets
// process-wide state: call it once, before creating sessions.
func SetImageTag(version string) { workload.SetDefaultImageTag(version) }

// Create creates the Session CR and materialises its workload. If a
// Session with s's name already exists, Create adopts it: s is overwritten
// with the live object (your spec is not applied) and its workload is
// ensured. A second call with the same name is therefore safe. With only
// GenerateName set, every call creates a new session.
func Create(ctx context.Context, c client.Client, s *agentsv1.Session) error {
	err := c.Create(ctx, s)
	if apierrors.IsAlreadyExists(err) && s.Name != "" {
		err = c.Get(ctx, client.ObjectKeyFromObject(s), s)
	}
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return Ensure(ctx, c, s)
}

// Ensure creates whatever is missing of an existing Session's workload
// (workspace PVC, Deployment). It never changes what is already there.
// When s has no UID it is first read from the API, since the workload's
// owner references need it.
func Ensure(ctx context.Context, c client.Client, s *agentsv1.Session) error {
	if s.UID == "" {
		if err := c.Get(ctx, client.ObjectKeyFromObject(s), s); err != nil {
			return fmt.Errorf("get session: %w", err)
		}
	}
	if err := workload.Ensure(ctx, c, s); err != nil {
		return fmt.Errorf("session %s exists but its workload failed: %w", s.Name, err)
	}
	return nil
}

// Delete removes a session. Its PVC, Deployment and pod go with it through
// garbage collection, so the workspace is lost. Deleting a session that
// does not exist is not an error.
func Delete(ctx context.Context, c client.Client, namespace, name string) error {
	s := &agentsv1.Session{}
	s.Namespace, s.Name = namespace, name
	return client.IgnoreNotFound(c.Delete(ctx, s))
}
