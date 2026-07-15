# Roadmap

Where `tiny` is, and what it takes to get to a local agent runtime you drive from your editor.

## Where we are

`v0.1.x` shipped the **delivery pipeline**, not the features. `brew install tiny-systems/tap/tiny` works, `tiny upgrade` self-updates, and a tagged release fans out cross-platform binaries + a Homebrew formula automatically.

The commands are mostly honest scaffolding. Real today:

- `tiny status` — reads the TinyModule CRs on the target cluster.
- `tiny mcp` — prints the client config snippet.
- `tiny upgrade` — checks GitHub releases and swaps the binary (defers to `brew upgrade` for Homebrew installs).

Everything else (`up`, `install`, the bare `tiny` dev server, `edit`) prints its intent and points here.

## The one decision that shapes everything

**`tiny` embeds the Go SDK in-process** — it imports `module/pkg/tools` (build_flow, read_project, get_component_info, …) and `resource.Manager` directly, and talks to your cluster through your kubeconfig. It does not shell out to a separate binary, and it does not require the hosted platform.

This is the same thing the (now-retired) `mcp-server` binary did, and it's what makes Phase B and C tractable: the tool logic and the local cluster reader already exist as importable Go. Everything below assumes this.

## Phase A — provision a cluster (`up` / `install` / `status`)

Turn an empty cluster into a running runtime from the terminal.

- `tiny up` — install the NATS/JetStream broker, the operator + CRDs, and a core set of modules (common, http, llm, kubernetes) onto the confirmed context/namespace.
- `tiny install <module>` — resolve one module's chart and install it. Also the path an agent uses to install capabilities on the fly through the MCP endpoint.
- `tiny status` — grow from "list modules" to node health, module versions, and broker reachability.

Two pieces to build:

1. **Catalog resolution.** Each module carries its own chart coordinates (`ChartRepo` / `ChartName` / `ChartVersion`) in the published index. `tiny` needs a public source for that index — a static published catalog or a public read endpoint — so it can resolve a name to a chart without the platform. *Open decision.*
2. **Helm integration.** Embed the Helm Go SDK rather than shelling out to `helm`.

Effort: medium. Independently shippable. This is the promise the README already makes ("empty cluster → running"), made real.

## Phase B — local MCP server (`tiny mcp`)

The reason to use `tiny` at all: **prompt-build agents on your own cluster from Claude Code, with no hosted account.**

Serve an MCP endpoint over HTTP/SSE (not stdio — so one process can also serve the editor) backed by `module/pkg/tools` against a local `resource.Manager` built from your kubeconfig. The tools an agent needs — build_flow, read_project, get_component_info, install_module (install-on-the-fly), send_signal, get_traces — all already exist in the SDK.

What exists to lift:

- Local cluster access via `resource.NewManagerFromConfig` (proven in the desktop client).
- The MCP server skeleton (the retired `mcp-server` binary), modernized to the current SDK.

Effort: large. This is the feature that earns stars — the point where `tiny` stops being a wrapper and becomes the product.

## Phase C — the local editor (`tiny` dev + `tiny edit`)

The two-URL magic: one process serving the MCP endpoint and the browser editor, over the same live cluster state. Prompt on the left, watch it materialize on the right.

This has two halves, and the second is the real work:

- **Frontend (in progress).** The editor is being extracted into `@tinysystems/editor` so the platform and `tiny` share one canvas. The package + the JSON-schema editor are done; the graph components, the store factory, and the inspector follow. The seam is a typed injected `EditorClient` — the host supplies a gRPC-web client, the components never reach for a specific backend.
- **Backend (the fork).** The canvas talks to a `FlowService` over gRPC-web. Today that service lives only in the platform (`manager/services/grpc-api/flow`). For `tiny edit` to serve the same editor, that backend has to be shareable too — either extracted into a shared package (mirroring the frontend) or imported by `tiny`. This is the second half of "stop maintaining platform and tiny separately," and it's a real architectural decision, not a file move.

Effort: large, gated on the backend-sharing decision. Do not start until A and B are solid — a half-built local backend is worse than none.

## Housekeeping (alongside the phases)

- Config file (`~/.config/tiny`) for default context / namespace / ports.
- `tiny doctor` preflight: kubectl + helm present, cluster reachable, versions sane.
- `tiny login` for hosted/team mode (attach a workspace) — later.
- Windows `.zip` self-update (`tiny upgrade` currently handles unix `.tar.gz` only).
- Bump the release workflow off Node 20 actions (deprecation warning).
- Tests + CI for the CLI itself.

## Order

**A → B → C.**

A is the fastest thing that makes the repo feel alive and is independently shippable. B is the feature people adopt for. C is the largest and forces the backend-sharing decision that touches the platform — leave it until the first two are solid.
