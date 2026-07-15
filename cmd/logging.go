package cmd

import (
	"os"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// The MCP-server internals we lifted (the kube backend and adapters) log
// plumbing events — NATS connect, port-forward fallbacks, trace reads —
// through zerolog's global logger. As raw JSON on stderr they're noise on
// top of tiny's styled output (the "nats: connected via port-forward" line
// users see before the banner). Silence them by default; TINY_DEBUG=1 brings
// them back as readable console lines when something needs diagnosing.
func init() {
	if os.Getenv("TINY_DEBUG") != "" {
		log.Logger = zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.Kitchen}).
			With().Timestamp().Logger()
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
		return
	}
	zerolog.SetGlobalLevel(zerolog.Disabled)
}
