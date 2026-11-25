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
	// Egress is the fence: a NetworkPolicy keeping sessions to their
	// namespace, the API server and (without the proxy) the internet on
	// 80/443.
	Egress bool
	// FenceWait holds a fenced session's agent until the fence blocks a
	// probe. For CNIs that program a new pod's policy after the pod starts
	// (kube-router, which k3s ships); elsewhere it only delays sessions.
	FenceWait bool
	// EgressProxy routes a session's outbound traffic through the
	// namespace's hostname allow-list instead of letting it reach any
	// address on 80/443.
	EgressProxy bool
	// ZotNodeTrust consents to the ONE cluster-touching piece: a DaemonSet
	// that installs the cache's CA certificate on every node so their
	// container runtimes accept it. Explicitly separate from Zot.
	ZotNodeTrust bool
	// ModelGateway keeps the Claude credential out of agent pods: the
	// egress proxy holds it and adds it to model requests on the way out.
	// The value names the credential's kind, GatewayOAuth (a subscription
	// token) or GatewayAPIKey, so agents send a placeholder of the same
	// kind. Empty is off. Needs EgressProxy.
	ModelGateway string
	// Federation, when its rule and organization are set, signs agents in
	// to the Claude API with Anthropic Workload Identity Federation: the
	// pod's projected service-account token is exchanged for a short-lived
	// access token, and no Claude credential is stored at all.
	Federation Federation
}

// Kinds of credential the model gateway can hold.
const (
	GatewayOAuth  = "oauth"
	GatewayAPIKey = "apikey"
)

// Federation is the Claude Console configuration a session needs to
// exchange its identity token: the IDs shown when the federation rule was
// created. None of them is a secret.
type Federation struct {
	RuleID           string
	OrganizationID   string
	ServiceAccountID string
	WorkspaceID      string // needed when the rule covers several workspaces
	// Audience the projected token is minted for; the rule must match it.
	Audience string
}

// DefaultFederationAudience is the audience Anthropic's guides use.
const DefaultFederationAudience = "https://api.anthropic.com"

// On reports whether federation is configured.
func (f Federation) On() bool { return f.RuleID != "" && f.OrganizationID != "" }

// Keys the switchboard stores the gateway and federation under.
const (
	KeyModelGateway             = "modelGateway"
	KeyFederationRule           = "federationRule"
	KeyFederationOrganization   = "federationOrganization"
	KeyFederationServiceAccount = "federationServiceAccount"
	KeyFederationWorkspace      = "federationWorkspace"
	KeyFederationAudience       = "federationAudience"
)

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
		Egress:       cm.Data["egress"] == on,
		FenceWait:    cm.Data["fenceWait"] == on,
		EgressProxy:  cm.Data["egressProxy"] == on,
		RunnerRepo:   cm.Data["runnerRepo"],
		ModelGateway: modelGateway(cm.Data[KeyModelGateway]),
		Federation: Federation{
			RuleID:           cm.Data[KeyFederationRule],
			OrganizationID:   cm.Data[KeyFederationOrganization],
			ServiceAccountID: cm.Data[KeyFederationServiceAccount],
			WorkspaceID:      cm.Data[KeyFederationWorkspace],
			Audience:         orDefault(cm.Data[KeyFederationAudience], DefaultFederationAudience),
		},
	}, nil
}

// modelGateway accepts only the kinds the workload knows how to fake; any
// other value is off rather than a pod with a placeholder of no kind.
func modelGateway(v string) string {
	if v == GatewayOAuth || v == GatewayAPIKey {
		return v
	}
	return ""
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}
