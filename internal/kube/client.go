// Package kube wires up the Kubernetes client the session commands share: a
// controller-runtime client with the agents.tinysystems.io types registered,
// plus the resolved namespace and REST config.
package kube

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	agentsv1 "github.com/tiny-systems/tiny/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// defaultNS is where things land when nothing chose better.
const defaultNS = "default"

// Client bundles everything an adapter needs to talk to a cluster: a typed
// controller-runtime client, the REST config (for port-forwarding), and
// the target namespace.
type Client struct {
	Client     client.Client
	RESTConfig *rest.Config
	Namespace  string
	// ContextName is the kubeconfig context this client was built for,
	// kept so screens can say which cluster they show.
	ContextName string
}

// Options controls how the client is constructed.
type Options struct {
	// KubeconfigPath is an explicit path to a kubeconfig file. Empty means
	// fall back to $KUBECONFIG and then ~/.kube/config.
	KubeconfigPath string
	// Context overrides the current-context in the kubeconfig. Empty means
	// use whatever is set as current-context in the file.
	Context string
	// Namespace overrides the kubeconfig context's default namespace.
	// Empty means use the context's namespace, or "default" if none is set.
	Namespace string
}

// NewClient loads a kubeconfig, registers the TinySystems scheme, and
// returns a ready-to-use Client.
func NewClient(opts Options) (*Client, error) {
	restCfg, ns, err := loadConfig(opts.KubeconfigPath, opts.Context)
	if err != nil {
		return nil, err
	}
	if opts.Namespace != "" {
		ns = opts.Namespace
	}
	if ns == "" {
		ns = defaultNS
	}
	c, err := NewClientFromConfig(restCfg, ns)
	if err != nil {
		return nil, err
	}
	c.ContextName = opts.Context
	return c, nil
}

// NewClientFromConfig builds a Client from an already-resolved rest.Config —
// for callers (like the FlowService) that resolved the config themselves and
// just need the typed, scheme-aware client over it.
func NewClientFromConfig(restCfg *rest.Config, namespace string) (*Client, error) {
	if namespace == "" {
		namespace = defaultNS
	}

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(agentsv1.AddToScheme(scheme))

	c, err := client.New(restCfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("build controller-runtime client: %w", err)
	}

	return &Client{
		Client:     c,
		RESTConfig: restCfg,
		Namespace:  namespace,
	}, nil
}

// loadConfig resolves the kubeconfig and current context namespace.
func loadConfig(explicitPath, context string) (*rest.Config, string, error) {
	// Inside a pod (a runner job, for one) there is no kubeconfig — the
	// mounted serviceaccount is the identity.
	if explicitPath == "" && os.Getenv("KUBECONFIG") == "" {
		if cfg, err := rest.InClusterConfig(); err == nil {
			ns := defaultNS
			if raw, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); err == nil {
				ns = strings.TrimSpace(string(raw))
			}
			return cfg, ns, nil
		}
	}
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if explicitPath != "" {
		loadingRules.ExplicitPath = explicitPath
	} else if env := os.Getenv("KUBECONFIG"); env != "" {
		loadingRules.ExplicitPath = env
	} else if home, err := os.UserHomeDir(); err == nil {
		defaultPath := filepath.Join(home, ".kube", "config")
		if _, err := os.Stat(defaultPath); err == nil {
			loadingRules.ExplicitPath = defaultPath
		}
	}

	overrides := &clientcmd.ConfigOverrides{}
	if context != "" {
		overrides.CurrentContext = context
	}

	cfg := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loadingRules,
		overrides,
	)

	restCfg, err := cfg.ClientConfig()
	if err != nil {
		return nil, "", fmt.Errorf("load kubeconfig: %w", err)
	}

	ns, _, err := cfg.Namespace()
	if err != nil {
		return nil, "", fmt.Errorf("resolve namespace: %w", err)
	}

	return restCfg, ns, nil
}

// Key is client.ObjectKey with the argument order everyone means.
func Key(namespace, name string) client.ObjectKey {
	return client.ObjectKey{Namespace: namespace, Name: name}
}
