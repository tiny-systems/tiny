// Package kube builds the mcp-server execution bundle using direct
// Kubernetes access via kubeconfig. This is the mode the MCP server
// has shipped with since v0.1.x — talks to the cluster's TinyModule,
// TinyProject, TinyFlow, TinyNode, TinySignal, TinyScenario CRDs
// directly, and reaches the in-cluster otel-collector by port-forward.
//
// All construction logic that used to live inline in cmd/serve.go is
// now here so platform mode can be added without disturbing the
// existing path.
package kube

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog/log"
	"github.com/tiny-systems/module/pkg/resource"
	sdktools "github.com/tiny-systems/module/pkg/tools"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/tiny-systems/tiny/internal/adapters"
	"github.com/tiny-systems/tiny/internal/backend"
	"github.com/tiny-systems/tiny/internal/kube"
)

// Options carries everything the kube backend needs at construction.
// SolutionSearcher and PublicModuleCatalog are NOT here — they hit
// the public REST API and are mode-agnostic, so serve.go constructs
// them once and assigns them onto the returned bundle regardless of
// mode.
type Options struct {
	KubeconfigPath string
	Context        string
	Namespace      string
	OtelService    string
	OtelPort       int

	// NATS service location for SignalSender. Defaults applied below
	// when zero — namespace "nats", service "nats-nats", port 4222
	// match the platform's standard install. Override only if your
	// cluster runs NATS elsewhere.
	NatsNamespace string
	NatsService   string
	NatsPort      int
}

// New constructs the kube-backed execution bundle. The cleanup
// function closes the trace reader's port-forwarder; caller must
// defer it.
func New(opts Options) (backend.Bundle, backend.Cleanup, error) {
	kubeClient, err := kube.NewClient(kube.Options{
		KubeconfigPath: opts.KubeconfigPath,
		Context:        opts.Context,
		Namespace:      opts.Namespace,
	})
	if err != nil {
		return backend.Bundle{}, nil, fmt.Errorf("init kube client: %w", err)
	}

	moduleCatalog := adapters.NewModuleCatalog(kubeClient)
	projectReader := adapters.NewProjectReader(kubeClient)
	projectLister := adapters.NewProjectLister(kubeClient)
	flowLifecycle := adapters.NewFlowLifecycle(kubeClient)
	nodeEditor := adapters.NewNodeEditor(kubeClient)

	// Best-effort NATS port-forward for SignalSender. We hold one
	// long-lived forward to nats-nats:4222 for the lifetime of the
	// session — signals reuse the same TCP connection, no
	// per-signal kubelet round-trip. Failure here is non-fatal:
	// signal_sender falls back to TinySignal CRD when nc is nil.
	natsNs := opts.NatsNamespace
	if natsNs == "" {
		natsNs = "nats"
	}
	natsSvc := opts.NatsService
	if natsSvc == "" {
		natsSvc = "nats-nats"
	}
	natsPort := opts.NatsPort
	if natsPort == 0 {
		natsPort = 4222
	}
	// Dial lazily. tiny is routinely started against a cluster whose NATS
	// isn't up yet (a fresh `tiny up` provisions it moments later), and
	// binding once at boot left send_signal dead until the process was
	// restarted. connectNATS re-dials on demand and caches the live
	// connection, so a boot-time miss heals itself on the next signal.
	//
	// The tinysystems-nats chart runs with token auth; connect with the
	// generated token or the broker rejects us (auth violation). The token is
	// read per dial so a Secret created after boot is picked up too.
	var (
		natsMu   sync.Mutex
		natsPF   *resource.PortForwarder
		natsConn *nats.Conn
	)
	connectNATS := func() *nats.Conn {
		natsMu.Lock()
		defer natsMu.Unlock()

		if natsConn != nil && !natsConn.IsClosed() {
			return natsConn
		}
		if natsPF != nil { // drop the forwarder behind a dead connection
			natsPF.StopAll()
			natsPF = nil
		}
		natsPF, natsConn = dialNATS(kubeClient.RESTConfig, natsNs, natsSvc, natsPort, natsToken(kubeClient, natsNs))
		return natsConn
	}

	// Warm the happy path at boot; a failure here is no longer terminal.
	signalSender := adapters.NewSignalSender(kubeClient, connectNATS(), connectNATS)
	scenarioManager := adapters.NewScenarioManager(kubeClient)
	portInspector := adapters.NewPortInspector(kubeClient)
	dashboardReader := adapters.NewDashboardReader(kubeClient)
	dashboardWriter := adapters.NewDashboardWriter(kubeClient)

	traceReader, err := adapters.NewTraceReader(adapters.TraceReaderOptions{
		KubeClient:  kubeClient,
		ServiceName: opts.OtelService,
		ServicePort: opts.OtelPort,
	})
	if err != nil {
		return backend.Bundle{}, nil, fmt.Errorf("init trace reader: %w", err)
	}

	bundle := backend.Bundle{
		ProjectReader:          projectReader,
		ProjectLister:          projectLister,
		FlowModifier:           nodeEditor,
		ModuleCatalog:          moduleCatalog,
		PortInspector:          portInspector,
		NodeAdder:              nodeEditor,
		EdgeAdder:              nodeEditor,
		EdgeConfigurer:         nodeEditor,
		NodeSettingsConfigurer: nodeEditor,
		FlowCreator:            flowLifecycle,
		FlowDeleter:            flowLifecycle,
		SignalSender:           signalSender,
		TraceReader:            traceReader,
		ScenarioManager:        scenarioManager,
		DashboardReader:        dashboardReader,
		DashboardWriter:        dashboardWriter,
		PositionTracker:        sdktools.NewPositionTracker(),
	}

	cleanup := func() {
		traceReader.Close()
		natsMu.Lock()
		defer natsMu.Unlock()
		if natsConn != nil {
			natsConn.Close()
		}
		if natsPF != nil {
			natsPF.StopAll()
		}
	}

	return bundle, cleanup, nil
}

// natsToken reads the broker auth token the tinysystems-nats chart generated
// into the tinysystems-nats-auth secret. Returns "" (tokenless connect) when
// the secret is absent — a pre-auth broker or read error still connects.
func natsToken(kubeClient *kube.Client, namespace string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sec := &corev1.Secret{}
	if err := kubeClient.Client.Get(ctx, crclient.ObjectKey{Namespace: namespace, Name: "tinysystems-nats-auth"}, sec); err != nil {
		return ""
	}
	return string(sec.Data["token"])
}

// dialNATS sets up a long-lived port-forward to <namespace>/<service>:<port>
// and connects a NATS client to the forwarded local address. Returns
// (nil, nil) on failure — the caller logs and falls back to the
// TinySignal CRD path. token is passed to the broker when non-empty.
func dialNATS(restConfig *rest.Config, namespace, service string, port int, token string) (*resource.PortForwarder, *nats.Conn) {
	pf, err := resource.CreatePortForwarderFromConfig(restConfig, namespace)
	if err != nil {
		log.Warn().Err(err).Str("namespace", namespace).Msg("nats: port-forwarder unavailable, signals will use CRD fallback")
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	addr, err := pf.ForwardService(ctx, service, port)
	if err != nil {
		pf.StopAll()
		log.Warn().Err(err).Str("service", service).Msg("nats: port-forward failed, signals will use CRD fallback")
		return nil, nil
	}
	connOpts := []nats.Option{nats.Name("tiny"), nats.Timeout(5 * time.Second)}
	if token != "" {
		connOpts = append(connOpts, nats.Token(token))
	}
	nc, err := nats.Connect(addr, connOpts...)
	if err != nil {
		pf.StopAll()
		log.Warn().Err(err).Str("addr", addr).Msg("nats: connect failed, signals will use CRD fallback")
		return nil, nil
	}
	log.Info().Str("addr", addr).Msg("nats: connected via port-forward, signals will use NATS")
	return pf, nc
}
