// Package backend selects between the two ways mcp-server can talk to
// a Tiny Systems cluster: directly via kubeconfig, or indirectly through
// the hosted platform-api with a developer token.
//
// The bundle returned by both builders is the same shape — an
// sdktools.ExecutionContext populated with concrete implementations of
// the per-operation interfaces (ModuleCatalog, SignalSender,
// TraceReader, ...). MCP tool handlers consume the ExecutionContext
// without knowing or caring which mode is active.
//
// Why two builders rather than one Backend interface wrapping it all:
// ExecutionContext is already 17 segregated interfaces. The SDK's tool
// handlers depend on those interfaces directly. Introducing a parent
// Backend wrapper would either duplicate the interface surface or
// force handlers to depend on a richer type than they need. Two
// builders that produce the same struct gives platform-mode parity
// with zero churn on the tool side.
package backend

import (
	sdktools "github.com/tiny-systems/module/pkg/tools"
)

// Bundle aliases sdktools.ExecutionContext for clarity at the
// construction site. Builders return this; serve.go assigns it
// straight into ExecutionContext when registering tools.
type Bundle = sdktools.ExecutionContext

// Cleanup is the teardown function returned by each builder. It
// closes long-lived resources such as the trace reader's
// port-forwarder or the platform gRPC connection. Always defer it.
type Cleanup func()
