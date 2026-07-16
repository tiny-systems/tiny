package cmd

import (
	"flag"
	"io"
	"os"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"k8s.io/klog/v2"
)

// Two noisy loggers ride in on our dependencies and don't belong in a polished
// CLI's output:
//
//   - zerolog: the MCP-server internals we lifted (kube backend, adapters) log
//     plumbing events (NATS connect, port-forward fallbacks) as raw JSON.
//   - klog: client-go's credential plugins print raw glog-style lines to
//     stderr — e.g. a gcloud token-refresh failure (F0716 ... cred.go).
//
// Silence both by default; TINY_DEBUG=1 brings zerolog back as readable
// console lines when something needs diagnosing.
func init() {
	// Route klog to discard regardless of debug mode — we surface cluster
	// auth/connectivity problems ourselves, cleanly, via the preflight.
	var kfs flag.FlagSet
	klog.InitFlags(&kfs)
	_ = kfs.Set("logtostderr", "false")
	_ = kfs.Set("alsologtostderr", "false")
	klog.SetOutput(io.Discard)

	if os.Getenv("TINY_DEBUG") != "" {
		log.Logger = zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.Kitchen}).
			With().Timestamp().Logger()
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
		return
	}
	zerolog.SetGlobalLevel(zerolog.Disabled)
}
