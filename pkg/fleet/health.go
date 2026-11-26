package fleet

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/tiny-systems/tiny/internal/runtimeinstall"
	"github.com/tiny-systems/tiny/internal/settings"
	"github.com/tiny-systems/tiny/internal/workload"
	tinyruntime "github.com/tiny-systems/tiny/pkg/runtime"
)

// Health is what can quietly break a fleet, read in one pass.
type Health struct {
	// Nodes is one entry per node: pressure conditions and free disk.
	Nodes []NodeHealth `json:"nodes"`
	// RuntimeMissing lists Session fields this module knows that the
	// cluster's CRD lacks: the runtime is older than this code, and new
	// fields would be dropped. Empty means up to date.
	RuntimeMissing []string `json:"runtimeMissing,omitempty"`
	// SignIn is how Claude sessions sign in: "stored", "gateway",
	// "federation", or "none" when nothing is configured.
	SignIn string `json:"signIn"`
	// Codex reports whether a Codex credential is stored.
	Codex bool `json:"codex"`
	// Sandbox is the runtime class new sessions run under (tiny sandbox),
	// "" when they run on the node's default runtime.
	Sandbox string `json:"sandbox,omitempty"`
	// SandboxAvailable reports whether the cluster has a gvisor
	// RuntimeClass, so the sandbox can be switched on.
	SandboxAvailable bool `json:"sandboxAvailable"`
}

// How Claude sessions sign in, as Health.SignIn reports it.
const (
	SignInStored     = "stored"
	SignInGateway    = "gateway"
	SignInFederation = "federation"
	SignInNone       = "none"
)

// NodeHealth is one node's state.
type NodeHealth struct {
	Name           string  `json:"name"`
	Ready          bool    `json:"ready"`
	DiskPressure   bool    `json:"diskPressure"`
	MemoryPressure bool    `json:"memoryPressure"`
	DiskFreeGB     float64 `json:"diskFreeGB"`  // -1 when unknown
	DiskTotalGB    float64 `json:"diskTotalGB"` // -1 when unknown
}

// Health reads nodes, the runtime's version and the sign-in setup. Node
// disk comes from the kubelet's stats; without the rights to read them
// the numbers are -1 and only the pressure conditions are reported.
func (c *Client) Health(ctx context.Context) (Health, error) {
	var h Health
	nodes := &corev1.NodeList{}
	if err := c.Kube.Client.List(ctx, nodes); err == nil {
		for _, n := range nodes.Items {
			nh := NodeHealth{Name: n.Name, DiskFreeGB: -1, DiskTotalGB: -1}
			for _, cond := range n.Status.Conditions {
				on := cond.Status == corev1.ConditionTrue
				switch cond.Type {
				case corev1.NodeReady:
					nh.Ready = on
				case corev1.NodeDiskPressure:
					nh.DiskPressure = on
				case corev1.NodeMemoryPressure:
					nh.MemoryPressure = on
				}
			}
			nh.DiskFreeGB, nh.DiskTotalGB = c.nodeDisk(ctx, n.Name)
			h.Nodes = append(h.Nodes, nh)
		}
	}
	h.RuntimeMissing = runtimeinstall.MissingFields(ctx, c.Kube)

	cfg, err := settings.Load(ctx, c.Kube.Client, c.Kube.Namespace)
	if err != nil {
		return h, err
	}
	env := &corev1.Secret{}
	envErr := c.Kube.Client.Get(ctx, client.ObjectKey{Namespace: c.Kube.Namespace, Name: workload.AgentEnvSecret}, env)
	hasClaude := envErr == nil && (len(env.Data["CLAUDE_CODE_OAUTH_TOKEN"]) > 0 || len(env.Data["ANTHROPIC_API_KEY"]) > 0)
	h.Sandbox = cfg.RuntimeClass
	h.SandboxAvailable, _ = workload.RuntimeClassExists(ctx, c.Kube.Client, "gvisor")
	h.Codex = envErr == nil && (len(env.Data["TINY_CODEX_AUTH_JSON"]) > 0 || len(env.Data["OPENAI_API_KEY"]) > 0)
	switch {
	case cfg.Federation.On():
		h.SignIn = SignInFederation
	case cfg.ModelGateway != "":
		h.SignIn = SignInGateway
	case hasClaude:
		h.SignIn = SignInStored
	default:
		h.SignIn = SignInNone
	}
	return h, nil
}

// nodeDisk reads the node filesystem's free and total gigabytes from the
// kubelet's summary API, or -1, -1.
func (c *Client) nodeDisk(ctx context.Context, node string) (free, total float64) {
	rc, err := restClientFor(setGroupVersionV1(c.Kube.RESTConfig))
	if err != nil {
		return -1, -1
	}
	raw, err := rc.Get().AbsPath("/api/v1/nodes", node, "proxy", "stats", "summary").DoRaw(ctx)
	if err != nil {
		return -1, -1
	}
	var s struct {
		Node struct {
			FS struct {
				Available *float64 `json:"availableBytes"`
				Capacity  *float64 `json:"capacityBytes"`
			} `json:"fs"`
		} `json:"node"`
	}
	if json.Unmarshal(raw, &s) != nil || s.Node.FS.Available == nil || s.Node.FS.Capacity == nil {
		return -1, -1
	}
	const gb = 1 << 30
	return *s.Node.FS.Available / gb, *s.Node.FS.Capacity / gb
}

// UpgradeRuntime installs this module's runtime into the fleet's cluster
// and namespace, as `tiny init` does: new CRD fields, refreshed add-ons.
// The namespace's settings are kept.
func (c *Client) UpgradeRuntime(ctx context.Context) error {
	if err := tinyruntime.Init(ctx, c.Kube.RESTConfig, c.Kube.Namespace, tinyruntime.Options{}); err != nil {
		return fmt.Errorf("upgrade runtime: %w", err)
	}
	return nil
}

func restClientFor(cfg *rest.Config) (rest.Interface, error) { return rest.RESTClientFor(cfg) }

func setGroupVersionV1(cfg *rest.Config) *rest.Config {
	c := rest.CopyConfig(cfg)
	c.GroupVersion = &corev1.SchemeGroupVersion
	c.APIPath = "/api"
	c.NegotiatedSerializer = scheme.Codecs.WithoutConversion()
	return c
}
