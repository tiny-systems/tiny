/*
Package fleet gives programs outside this module what the tiny CLI's fleet
screen uses: read a namespace's sessions and their open questions, answer
questions, pause, resume, create and delete sessions, and attach to a
session's terminal over a stream. The Tiny Systems desktop app is built on
it.

	c, err := fleet.Connect("tinybox", "agents")
	snap, err := c.Load(ctx)
	for _, r := range snap.Rows { fmt.Println(r.Glyph(), r.Name, r.Title) }

It is a thin layer over the CLI's own session code, so it behaves exactly
like the CLI. The API may change before tiny v1.
*/
package fleet

import (
	"github.com/tiny-systems/tiny/internal/kube"
	"github.com/tiny-systems/tiny/internal/sessions"
	"github.com/tiny-systems/tiny/internal/workload"
)

// Types the fleet works with. They are the CLI's own.
type (
	// Snapshot is one consistent read of a namespace: rows and questions
	// not tied to a session.
	Snapshot = sessions.Snapshot
	// Row is one session joined with its open question, if any.
	Row = sessions.Row
	// CreateOpts describes a session to start.
	CreateOpts = sessions.CreateOpts
	// TerminalSize is a terminal's width and height in cells.
	TerminalSize = sessions.TerminalSize
)

// Client acts on one namespace of one cluster, with the caller's own
// kubeconfig credentials. Its methods are the CLI's: Load, Answer, Pause,
// Resume, Create, Delete, AttachStream and the rest of the session store.
type Client struct {
	*sessions.Store
}

// Connect builds a client for a kubeconfig context and namespace. An empty
// context means the kubeconfig's current one.
func Connect(context, namespace string) (*Client, error) {
	k, err := kube.NewClient(kube.Options{Context: context, Namespace: namespace})
	if err != nil {
		return nil, err
	}
	return &Client{Store: &sessions.Store{Kube: k}}, nil
}

// SetVersion pins the agent images new sessions use to a tiny release
// ("0.17.0" or "v0.17.0") and records the caller's version. Anything that
// isn't a release version leaves the images on "main". Process-wide; call
// once before creating sessions.
func SetVersion(version string) { workload.SetDefaultImageTag(version) }
