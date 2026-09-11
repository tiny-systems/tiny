/*
tiny-web serves the read-only fleet page inside a namespace: what every
session is doing, what each has changed, and which files two sessions are
both editing. It is an add-on — nothing runs it unless someone switches it
on — and it can only read.
*/
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/tiny-systems/tiny/internal/kube"
	"github.com/tiny-systems/tiny/internal/sessions"
	"github.com/tiny-systems/tiny/internal/webui"
)

func main() {
	var addr, namespace string
	flag.StringVar(&addr, "addr", ":8080", "listen address")
	flag.StringVar(&namespace, "namespace", os.Getenv("POD_NAMESPACE"),
		"namespace to show (default: POD_NAMESPACE)")
	flag.Parse()
	if namespace == "" {
		namespace = "default"
	}

	cfg, err := ctrl.GetConfig()
	if err != nil {
		log.Fatalf("kubeconfig: %v", err)
	}
	k, err := kube.NewClientFromConfig(cfg, namespace)
	if err != nil {
		log.Fatalf("cluster client: %v", err)
	}

	srv := &webui.Server{Store: &sessions.Store{Kube: k}}
	handler, err := srv.Handler()
	if err != nil {
		log.Fatalf("page: %v", err)
	}
	log.Printf("tiny-web serving %s on %s", namespace, addr)
	s := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Fatal(s.ListenAndServe())
}
