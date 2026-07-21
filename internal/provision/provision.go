// Package provision installs the Tiny Systems runtime and capability
// modules onto a cluster by embedding the Helm Go SDK (via mittwald's
// go-helm-client) — no shelling out to a `helm` binary, no hosted platform.
//
// A full runtime is four helm releases from the public chart repo
// https://tiny-systems.github.io/module/:
//
//	tinysystems-crd              CRDs (TinyModule/TinyNode/TinyFlow/…)
//	tinysystems-nats             NATS/JetStream broker — durable transport + run ledger
//	tinysystems-otel-collector   trace collector
//	tinysystems-operator         the module itself (one release per module, image-parameterised)
//
// The sequence and module values mirror the platform's install job so a
// `tiny`-provisioned cluster behaves identically to a hosted one — minus the
// multi-tenant machinery (no DB, no job locks, one namespace, one owner).
package provision

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	helmclient "github.com/mittwald/go-helm-client"
	"github.com/mittwald/go-helm-client/values"
	"helm.sh/helm/v3/pkg/repo"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/yaml"

	"github.com/tiny-systems/tiny/internal/catalog"
)

const (
	repoName = "tinysystems"
	repoURL  = "https://tiny-systems.github.io/module/"

	crdChart      = "tinysystems-crd"
	natsChart     = "tinysystems-nats"
	otelChart     = "tinysystems-otel-collector"
	operatorChart = "tinysystems-operator"

	// operatorVersion is pinned to the platform's validated version rather
	// than resolving "latest" off a helm cache — the exact reason the
	// platform pins it (a stale cache once shipped an operator predating the
	// port-manager RBAC http_server needs).
	operatorVersion = "0.2.10"

	natsService    = "tinysystems-nats"
	natsAuthSecret = "tinysystems-nats-auth"
	otelDSN        = "http://token@tinysystems-otel-collector:2345"

	// managedLabel dedicates a namespace to tinysystems. The operator
	// chart's pre-install hook refuses to install into a namespace without
	// it — a guardrail against dropping modules into a namespace shared with
	// unrelated workloads. Every module install fails until it's present.
	managedLabel = "tinysystems.io/managed"
)

// Client wraps a helm client bound to one cluster + namespace, with the
// tinysystems chart repo already added.
type Client struct {
	helm      helmclient.Client
	namespace string
	debug     io.Writer
}

// NewClient builds a helm client against cfg/namespace and registers the
// public chart repo. Pass a non-nil debug writer to surface helm's own
// (verbose) log; nil keeps installs quiet.
func NewClient(cfg *rest.Config, namespace string, debug io.Writer) (*Client, error) {
	cache, err := os.MkdirTemp("", "tiny-helm-")
	if err != nil {
		return nil, fmt.Errorf("helm cache dir: %w", err)
	}
	opt := &helmclient.RestConfClientOptions{
		Options: &helmclient.Options{
			RepositoryCache: cache,
			Namespace:       namespace,
			Debug:           debug != nil,
			DebugLog: func(format string, v ...interface{}) {
				if debug != nil {
					fmt.Fprintf(debug, format+"\n", v...)
				}
			},
		},
		RestConfig: cfg,
	}
	hc, err := helmclient.NewClientFromRestConf(opt)
	if err != nil {
		return nil, fmt.Errorf("helm client: %w", err)
	}
	if err := hc.AddOrUpdateChartRepo(repo.Entry{Name: repoName, URL: repoURL}); err != nil {
		return nil, fmt.Errorf("add chart repo: %w", err)
	}
	return &Client{helm: hc, namespace: namespace, debug: debug}, nil
}

// EnsureNamespace creates the target namespace if absent and labels it
// tinysystems.io/managed=true — the dedication marker the operator chart's
// pre-install hook requires. Must run before any module install. Idempotent:
// on an already-labeled namespace it's a single read.
func EnsureNamespace(ctx context.Context, cfg *rest.Config, namespace string) error {
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("kubernetes client: %w", err)
	}
	ns, err := cs.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   namespace,
				Labels: map[string]string{managedLabel: "true"},
			},
		}, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("create namespace %q: %w", namespace, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("get namespace %q: %w", namespace, err)
	}
	if ns.Labels[managedLabel] == "true" {
		return nil
	}
	if ns.Labels == nil {
		ns.Labels = map[string]string{}
	}
	ns.Labels[managedLabel] = "true"
	if _, err := cs.CoreV1().Namespaces().Update(ctx, ns, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("label namespace %q: %w", namespace, err)
	}
	return nil
}

// InstallCRDs installs the CRD chart. Cluster-scoped resources, but the
// release lives in the target namespace (single-owner cluster — none of the
// multi-tenant CRD-ownership dance the platform needs).
func (c *Client) InstallCRDs(ctx context.Context) error {
	return c.install(ctx, &helmclient.ChartSpec{
		ReleaseName:     crdChart,
		ChartName:       repoName + "/" + crdChart,
		Namespace:       c.namespace,
		CreateNamespace: true,
		Wait:            true,
		Timeout:         3 * time.Minute,
		Atomic:          true,
		Force:           true,
		CleanupOnFail:   true,
	})
}

// InstallBroker installs the NATS/JetStream broker. Deliberately no Force:
// the broker is a StatefulSet holding the durable run ledger, and forcing a
// replace would risk its persistent state.
func (c *Client) InstallBroker(ctx context.Context) error {
	return c.install(ctx, &helmclient.ChartSpec{
		ReleaseName:     natsChart,
		ChartName:       repoName + "/" + natsChart,
		Namespace:       c.namespace,
		CreateNamespace: true,
		Wait:            true,
		Timeout:         3 * time.Minute,
		Atomic:          true,
		CleanupOnFail:   true,
	})
}

// InstallOTEL installs the trace collector.
func (c *Client) InstallOTEL(ctx context.Context) error {
	return c.install(ctx, &helmclient.ChartSpec{
		ReleaseName:     otelChart,
		ChartName:       repoName + "/" + otelChart,
		Namespace:       c.namespace,
		CreateNamespace: true,
		Wait:            true,
		Timeout:         3 * time.Minute,
		Atomic:          true,
		Force:           true,
		CleanupOnFail:   true,
	})
}

// InstallModule installs one capability module as a tinysystems-operator
// release parameterised by the module's image. Returns the helm release
// name. natsURL wires the broker so durable execution is on out of the box;
// pass "" to leave the module in blocking-only mode.
func (c *Client) InstallModule(ctx context.Context, m *catalog.Module, natsURL string, settings Settings) (string, error) {
	release := SanitizeResourceName(m.FullName)
	spec := &helmclient.ChartSpec{
		ReleaseName:     release,
		ChartName:       repoName + "/" + operatorChart,
		Version:         operatorVersion,
		Namespace:       c.namespace,
		CreateNamespace: true,
		Wait:            true,
		Timeout:         5 * time.Minute,
		Atomic:          true,
		Force:           true,
		Replace:         true,
		CleanupOnFail:   true,
		ValuesOptions:   values.Options{Values: c.moduleValues(m, release, natsURL, settings)},
	}
	if err := c.install(ctx, spec); err != nil {
		return "", err
	}
	return release, nil
}

// moduleValues replicates the platform's operator.release_values for a
// single-owner local install: image, name/version/namespace args (also on
// the pre-install/pre-delete hook jobs, which don't get them from the chart
// otherwise), the durable-transport env, secret resolution, and the broker
// URL. --name is the workspace-qualified full name so the node IDs the agent
// builds resolve to this operator.
func (c *Client) moduleValues(m *catalog.Module, release, natsURL string, settings Settings) []string {
	v := []string{
		"controllerManager.manager.image.repository=" + m.Repo,
		"controllerManager.manager.image.tag=" + m.Tag,
		"fullnameOverride=" + release,
		"controllerManager.manager.args[0]=run",
		"controllerManager.manager.args[1]=--grpc-server-bind-address=:8483",
		"controllerManager.manager.args[2]=--health-probe-bind-address=:8081",
		"controllerManager.manager.args[3]=--metrics-bind-address=127.0.0.1:8080",
		"controllerManager.manager.args[4]=--name=" + m.FullName,
		"controllerManager.manager.args[5]=--version=" + m.Tag,
		"controllerManager.manager.args[6]=--namespace=" + c.namespace,
		"controllerManager.manager.deleteArgs[0]=pre-delete",
		"controllerManager.manager.deleteArgs[1]=--name=" + m.FullName,
		"controllerManager.manager.deleteArgs[2]=--namespace=" + c.namespace,
		"controllerManager.manager.installArgs[0]=pre-install",
		"controllerManager.manager.installArgs[1]=--name=" + m.FullName,
		"controllerManager.manager.installArgs[2]=--namespace=" + c.namespace,
		"controllerManager.manager.extraEnv[0].name=OTLP_DSN",
		"controllerManager.manager.extraEnv[0].value=" + otelDSN,
		// Durable wire by default: SDK reads TINY_NATS_TRANSPORT=jetstream and
		// uses the WorkQueue stream (pod-death survival + per-edge retry).
		"controllerManager.manager.extraEnv[1].name=TINY_NATS_TRANSPORT",
		"controllerManager.manager.extraEnv[1].value=jetstream",
		// Namespace-scoped secret reads so [[secret:name/key]] placeholders in
		// node settings resolve against Kubernetes Secrets.
		"secrets.enabled=true",
	}
	if natsURL != "" {
		v = append(v, "natsURL="+natsURL)
	}
	// Baseline RBAC (pods, services, ingresses) is needed both by modules that
	// manage cluster resources (RequiresKubernetesAccess) AND by any module
	// that exposes a port (RequiresIngress) — the port-manager must `get pods`
	// to find its own pod before it can expose/route the port. Without this,
	// http_server starts listening but logs "failed to expose port" and gets
	// no address.
	if m.RequiresKubernetesAccess || m.RequiresIngress {
		v = append(v, "rbac.enableKubernetesResourceAccess=true")
	}

	// Ingress: enable only when the module exposes HTTP and the cluster has an
	// ingress class set. Otherwise leave it off (reachable by port-forward,
	// the default on kind/minikube).
	if m.RequiresIngress && settings.IngressClass != "" {
		v = append(v,
			"managerIngress.ingress.enabled=true",
			"managerIngress.ingress.className="+settings.IngressClass,
		)
		if settings.DomainSuffix != "" {
			v = append(v, "global.defaultDomainSuffix="+settings.DomainSuffix)
		}
		if settings.Issuer != "" {
			// cert-manager TLS: annotate with the (cluster-)issuer. Dots in the
			// annotation key are escaped so helm --set treats them as key, not
			// nesting.
			key := "cert-manager\\.io/issuer"
			if settings.ClusterIssuer {
				key = "cert-manager\\.io/cluster-issuer"
			}
			v = append(v, "managerIngress.ingress.annotations."+key+"="+settings.Issuer)
		}
	} else {
		v = append(v, "managerIngress.ingress.enabled=false")
	}

	// Storage: wire the cluster's storage class for modules that need a PVC.
	if m.RequiresStorage && settings.StorageClass != "" {
		v = append(v,
			"storage.enabled=true",
			"storage.storageClassName="+settings.StorageClass,
			"storage.size="+settings.storageSizeOr(),
		)
	}
	return v
}

// UpgradeInstall installs or upgrades an arbitrary chart with a full values
// map — the generic helm primitive the repo-model installer drives. It makes
// *Client structurally satisfy repo.Helm (no import of the repo package needed;
// Go interfaces are structural). Mirrors the install flags used elsewhere
// (create-namespace, wait, atomic, cleanup-on-fail). Nothing calls it until the
// install/up cutover; adding it is non-breaking.
func (c *Client) UpgradeInstall(ctx context.Context, release, namespace, chart, version string, vals map[string]any) error {
	var valuesYaml string
	if len(vals) > 0 {
		b, err := yaml.Marshal(vals)
		if err != nil {
			return fmt.Errorf("marshal values for %s: %w", release, err)
		}
		valuesYaml = string(b)
	}
	return c.install(ctx, &helmclient.ChartSpec{
		ReleaseName:     release,
		ChartName:       chart,
		Version:         version,
		Namespace:       namespace,
		CreateNamespace: true,
		Wait:            true,
		Timeout:         5 * time.Minute,
		Atomic:          true,
		Force:           true,
		Replace:         true,
		CleanupOnFail:   true,
		ValuesYaml:      valuesYaml,
	})
}

func (c *Client) install(ctx context.Context, spec *helmclient.ChartSpec) error {
	if _, err := c.helm.InstallOrUpgradeChart(ctx, spec, nil); err != nil {
		return fmt.Errorf("install %s: %w", spec.ReleaseName, err)
	}
	return nil
}

// BrokerURL returns the authenticated broker URL clients connect with —
// nats://<token>@tinysystems-nats.<ns>.svc:4222 — reading the token the
// nats chart generated into its auth secret. Falls back to the tokenless URL
// (never errors) when the secret is absent: a pre-auth broker or a transient
// read still connects, so auth degrades gracefully instead of hard-failing.
func BrokerURL(ctx context.Context, cfg *rest.Config, namespace string) string {
	plain := fmt.Sprintf("nats://%s.%s.svc:4222", natsService, namespace)
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return plain
	}
	sec, err := cs.CoreV1().Secrets(namespace).Get(ctx, natsAuthSecret, metav1.GetOptions{})
	if err != nil {
		return plain
	}
	token := string(sec.Data["token"])
	if token == "" {
		return plain
	}
	return fmt.Sprintf("nats://%s@%s.%s.svc:4222", token, natsService, namespace)
}

// SanitizeResourceName lowercases and reduces an arbitrary name to a valid
// RFC-1123 helm release / resource name: [a-z0-9-], no leading/trailing dash,
// capped at 53 chars. "tinysystems/http-module-v0" → "tinysystems-http-module-v0".
func SanitizeResourceName(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 53 {
		out = strings.Trim(out[:53], "-")
	}
	return out
}
