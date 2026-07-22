package adapters

import (
	"context"
	"strings"
	"testing"

	"github.com/nats-io/nats.go"
)

// The bug this guards: the sender used to capture *nats.Conn once at boot, so
// starting tiny before the cluster's NATS was reachable left send_signal dead
// until the process was restarted. It must re-dial on every attempt while
// disconnected, so the first signal after NATS comes up succeeds.
func TestSignalSenderRedialsWhileDisconnected(t *testing.T) {
	calls := 0
	s := NewSignalSender(nil, nil, func() *nats.Conn {
		calls++
		return nil // NATS still unreachable
	})

	for i := 0; i < 3; i++ {
		err := s.SendSignal(context.Background(), "proj", "node-1", "_control", []byte(`{"send":true}`), "")
		if err == nil {
			t.Fatal("expected an error while NATS is unreachable")
		}
		if !strings.Contains(err.Error(), "NATS not reachable") {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	if calls != 3 {
		t.Fatalf("connect called %d times, want 3 — a disconnected sender must retry, not give up permanently", calls)
	}
}

// A nil connect func must not panic — it just reports NATS unavailable.
func TestSignalSenderNilConnect(t *testing.T) {
	s := NewSignalSender(nil, nil, nil)
	err := s.SendSignal(context.Background(), "proj", "node-1", "_control", []byte(`{}`), "")
	if err == nil || !strings.Contains(err.Error(), "NATS not reachable") {
		t.Fatalf("want NATS-unreachable error, got %v", err)
	}
}

// Argument validation happens before any dial attempt.
func TestSignalSenderValidatesArgs(t *testing.T) {
	calls := 0
	s := NewSignalSender(nil, nil, func() *nats.Conn { calls++; return nil })

	if err := s.SendSignal(context.Background(), "proj", "", "_control", nil, ""); err == nil {
		t.Fatal("expected error for empty node id")
	}
	if err := s.SendSignal(context.Background(), "proj", "node-1", "", nil, ""); err == nil {
		t.Fatal("expected error for empty port name")
	}
	if calls != 0 {
		t.Fatalf("dialed %d times on invalid args, want 0", calls)
	}
}
