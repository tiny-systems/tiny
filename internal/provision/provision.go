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
	"path/filepath"
	"strings"
	"sync"
	"time"

	helmclient "github.com/mittwald/go-helm-client"
	"helm.sh/helm/v3/pkg/repo"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/yaml"
)

const (
	repoName = "tinysystems"
	repoURL  = "https://tiny-systems.github.io/module/"

	crdChart      = "tinysystems-crd"
	natsChart     = "tinysystems-nats"
	otelChart     = "tinysystems-otel-collector"
	operatorChart = "tinysystems-operator"

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
			// Isolate the repo config per client. Left unset it defaults to the
			// shared ~/.config/helm/repositories.yaml, which concurrent installs
			// race on — corrupting it so later installs fail "no cached repo found".
			RepositoryConfig: filepath.Join(cache, "repositories.yaml"),
			Namespace:        namespace,
			Debug:            debug != nil,
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
	if err := addChartRepo(hc); err != nil {
		return nil, fmt.Errorf("add chart repo: %w", err)
	}
	return &Client{helm: hc, namespace: namespace, debug: debug}, nil
}

// chartRepoMu serializes AddOrUpdateChartRepo across concurrent NewClient
// calls. Each client already gets an isolated RepositoryConfig + Cache, but
// serializing the add is belt-and-suspenders against go-helm-client's global
// helm settings and keeps parallel installs from hammering the repo-index
// fetch at once. Cheap — a single fast index download.
var chartRepoMu sync.Mutex

// addChartRepo registers the public chart repo, retrying the index fetch so a
// transient network blip doesn't hard-fail the install. The prior symptom was a
// bare "no cached repo found" when a raced or failed fetch left no index behind.
func addChartRepo(hc helmclient.Client) error {
	chartRepoMu.Lock()
	defer chartRepoMu.Unlock()
	entry := repo.Entry{Name: repoName, URL: repoURL}
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		if err = hc.AddOrUpdateChartRepo(entry); err == nil {
			return nil
		}
		time.Sleep(time.Duration(attempt) * 500 * time.Millisecond)
	}
	return err
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

// provisionTimeout bounds a helm install that has to pull images first. It is
// generous on purpose: the failure it prevents is a first run on a fresh
// cluster reporting "context deadline exceeded", which reads as broken rather
// than slow, and there is nothing to gain by giving up early — a genuinely
// stuck install is stuck for a reason the user has to look at either way.
const provisionTimeout = 10 * time.Minute

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
		Timeout:         provisionTimeout,
		Atomic:          true,
		Force:           true,
		CleanupOnFail:   true,
	})
}

// InstallBroker installs the NATS/JetStream broker. Deliberately no Force:
// the broker is a StatefulSet holding the durable run ledger, and forcing a
// replace would risk its persistent state.
//
// The timeout covers pulling images onto a cluster that has never seen them.
// Three minutes was enough on a machine with a warm cache and not enough on a
// cold CI runner, where it failed as "context deadline exceeded" — which reads
// as a broken install rather than a slow download, and is the first thing a
// new user would ever see.
func (c *Client) InstallBroker(ctx context.Context) error {
	return c.install(ctx, &helmclient.ChartSpec{
		ReleaseName:     natsChart,
		ChartName:       repoName + "/" + natsChart,
		Namespace:       c.namespace,
		CreateNamespace: true,
		Wait:            true,
		Timeout:         provisionTimeout,
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
		Timeout:         provisionTimeout,
		Atomic:          true,
		Force:           true,
		CleanupOnFail:   true,
	})
}

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
		Timeout:         provisionTimeout,
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

// Lookup reports the module image ref of whatever occupies a helm release name,
// or found=false when the release isn't installed.
//
// It satisfies repo.InstalledModules structurally — primitives only, so this
// package still does not import internal/repo, matching how BaseValues and
// UpgradeInstall are wired.
//
// The image is the authoritative identity of an installed release: it is
// literally what the pod runs, so it distinguishes two publishers shipping the
// same module name without needing any extra bookkeeping.
//
// A release that isn't installed is not an error — that is the normal case on a
// fresh cluster, and helm reports it as one.
func (c *Client) Lookup(_ context.Context, _, release string) (string, bool, error) {
	rel, err := c.helm.GetRelease(release)
	if err != nil || rel == nil {
		return "", false, nil
	}
	return moduleImageFromValues(rel.Config), true, nil
}

// moduleImageFromValues digs controllerManager.manager.image.{repository,tag}
// out of a release's values — where BaseValues puts it. Returns "" when the
// shape isn't what we wrote (a non-module release, or a chart that moved it),
// which callers treat as "unknown, assume ours" rather than blocking upgrades.
func moduleImageFromValues(vals map[string]any) string {
	dig := func(m map[string]any, key string) map[string]any {
		next, _ := m[key].(map[string]any)
		return next
	}
	img := dig(dig(dig(vals, "controllerManager"), "manager"), "image")
	if img == nil {
		return ""
	}
	repository, _ := img["repository"].(string)
	if repository == "" {
		return ""
	}
	if tag, _ := img["tag"].(string); tag != "" {
		return repository + ":" + tag
	}
	return repository
}
