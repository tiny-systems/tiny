package mcp

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"sync"

	sdktools "github.com/tiny-systems/module/pkg/tools"
)

// StdioTransport reads MCP JSON-RPC frames from stdin (newline-delimited)
// and writes responses to stdout. Each frame is dispatched to Server.HandleFrame.
//
// This is the transport Claude Desktop uses today via the
// `claude_desktop_config.json` `command` field — the host spawns the
// binary and pipes JSON-RPC over its stdio.
type StdioTransport struct {
	server *Server

	in  io.Reader
	out io.Writer

	mu sync.Mutex // guards writes to out
}

// NewStdioTransport returns a transport that reads from stdin and
// writes to stdout. The Server it wraps owns the tool registry and
// execution context.
func NewStdioTransport(server *Server) *StdioTransport {
	return &StdioTransport{
		server: server,
		in:     os.Stdin,
		out:    os.Stdout,
	}
}

// NewStdioServer is kept as a thin shim so existing callers in
// cmd/serve.go compile unchanged. New code should call
// NewServer + NewStdioTransport directly.
//
// Deprecated: use NewServer + NewStdioTransport.
func NewStdioServer(registry *sdktools.Registry, execCtx sdktools.ExecutionContext) *StdioTransport {
	return NewStdioTransport(NewServer(registry, execCtx))
}

// Run starts the read loop. It returns nil on clean EOF and error on
// anything else (I/O failure, context cancellation).
func (t *StdioTransport) Run(ctx context.Context) error {
	reader := bufio.NewReader(t.in)

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		line, err := reader.ReadBytes('\n')
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read stdin: %w", err)
		}
		if len(line) == 0 {
			continue
		}

		response, herr := t.server.HandleFrame(ctx, line)
		if herr != nil {
			fmt.Fprintf(os.Stderr, "handle frame: %v\n", herr)
			continue
		}
		if response == nil {
			// Notification — no reply expected.
			continue
		}
		t.writeFrame(response)
	}
}

func (t *StdioTransport) writeFrame(data []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if _, err := t.out.Write(data); err != nil {
		fmt.Fprintf(os.Stderr, "write response: %v\n", err)
		return
	}
	if _, err := t.out.Write([]byte{'\n'}); err != nil {
		fmt.Fprintf(os.Stderr, "write newline: %v\n", err)
	}
}
