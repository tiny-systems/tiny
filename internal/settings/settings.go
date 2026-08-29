/*
Package settings reads tiny's per-namespace configuration: the well-known
tiny-settings ConfigMap. A namespace is a group of agents — a team, a
project, one person — and this is that group's switchboard.
*/
package settings

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Name is the ConfigMap every piece of tiny agrees to look at.
const Name = "tiny-settings"

// on is the switchboard's true value.
const on = "true"

// Settings is the parsed switchboard. Absent map keys are false — every
// feature is opt-in.
type Settings struct {
	// Zot runs a per-namespace pull-through registry cache and rewrites
	// session images through it — one Docker Hub pull per image per
	// namespace instead of one per spawn.
	Zot bool
	// Minio runs a per-namespace S3 store so sessions can hand each other
	// artifacts — build outputs, datasets — without kubectl cp.
	Minio bool
	// RunnerRepo — "owner/name" for one repo, or just "owner" to register
	// the runner ORG-WIDE — runs a self-hosted GitHub Actions runner in
	// the namespace — the event-driven bridge: labeled issues trigger
	// workflows whose jobs land here and spawn sessions. Registration uses
	// the tiny-runner-pat Secret.
	RunnerRepo string
	// ZotNodeTrust consents to the ONE cluster-touching piece: a DaemonSet
	// that installs the cache's CA certificate on every node so their
	// container runtimes accept it. Explicitly separate from Zot.
	ZotNodeTrust bool
}

// Load reads the switchboard; a missing ConfigMap is all-off, not an error.
func Load(ctx context.Context, c client.Client, namespace string) (Settings, error) {
	cm := &corev1.ConfigMap{}
	err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: Name}, cm)
	if apierrors.IsNotFound(err) {
		return Settings{}, nil
	}
	if err != nil {
		return Settings{}, err
	}
	return Settings{
		Zot:          cm.Data["zot"] == on,
		ZotNodeTrust: cm.Data["zotNodeTrust"] == on,
		Minio:        cm.Data["minio"] == on,
		RunnerRepo:   cm.Data["runnerRepo"],
	}, nil
}
