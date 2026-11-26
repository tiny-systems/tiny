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
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

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
func Connect(kubeContext, namespace string) (*Client, error) {
	k, err := kube.NewClient(kube.Options{Context: kubeContext, Namespace: namespace})
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

// CopyAgentCredentials copies the agent credentials Secret (the Claude and
// Codex sign-ins `tiny setup` stores) from one fleet to another, creating
// or replacing it there. The values go cluster to cluster through this
// process and are never returned.
func CopyAgentCredentials(ctx context.Context, from, to *Client) error {
	src := &corev1.Secret{}
	srcKey := client.ObjectKey{Namespace: from.Kube.Namespace, Name: workload.AgentEnvSecret}
	if err := from.Kube.Client.Get(ctx, srcKey, src); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("%s has no agent credentials to copy", from.Kube.Namespace)
		}
		return err
	}
	dst := &corev1.Secret{}
	err := to.Kube.Client.Get(ctx, client.ObjectKey{Namespace: to.Kube.Namespace, Name: workload.AgentEnvSecret}, dst)
	if apierrors.IsNotFound(err) {
		return to.Kube.Client.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: to.Kube.Namespace, Name: workload.AgentEnvSecret},
			Data:       src.Data,
		})
	}
	if err != nil {
		return err
	}
	dst.Data = src.Data
	return to.Kube.Client.Update(ctx, dst)
}
