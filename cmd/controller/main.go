/*
tiny-controller has ONE role left: serve — the powerless MCP toolbox
(ask_human, set_title, session_list, session_create, expose_port,
enable_store) running as a localhost sidecar in every session pod.

There is no manager anymore. Sessions are Deployments Kubernetes itself
keeps alive; the gate's actions are executed by the ANSWERING tiny client
with the answerer's own credentials; add-ons are applied by the CLI when
their toggles flip. The voice/hands split survives — the hands are now
the humans' own.
*/
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentsv1 "github.com/tiny-systems/tiny/api/v1alpha1"
	"github.com/tiny-systems/tiny/internal/tools"
)

func main() {
	args := os.Args[1:]
	// One role remains; "serve" as a first arg is accepted for
	// compatibility with old pod specs.
	if len(args) > 0 && args[0] == "serve" {
		args = args[1:]
	}
	serve(args)
}

func scheme() *runtime.Scheme {
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	utilruntime.Must(agentsv1.AddToScheme(s))
	return s
}

// serve runs the MCP toolbox: localhost sidecar in a session pod, or a
// standalone endpoint for anything else that wants the gate.
func serve(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	var addr, namespace string
	fs.StringVar(&addr, "addr", ":8080", "listen address for /mcp, /attention and /healthz")
	fs.StringVar(&namespace, "namespace", os.Getenv("POD_NAMESPACE"),
		"namespace Questions live in (default: POD_NAMESPACE)")
	_ = fs.Parse(args)
	if namespace == "" {
		namespace = "default"
	}

	cfg, err := ctrl.GetConfig()
	if err != nil {
		log.Fatalf("kubeconfig: %v", err)
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme()})
	if err != nil {
		log.Fatalf("cluster client: %v", err)
	}

	srv := &tools.Server{Client: c, Namespace: namespace}
	// The manager used to write Session phases; now the session announces
	// itself. Best effort — a status write must never block serving.
	srv.AnnounceRunning(context.Background())
	log.Printf("tiny-mcp serving on %s (namespace %s): /mcp, /attention, /healthz", addr, namespace)
	s := &http.Server{
		Addr:    addr,
		Handler: srv.Handler(),
		// ask_human blocks by design; only reads are bounded.
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Fatal(s.ListenAndServe())
}
