# Roadmap

Where `tiny` is, and what it takes to get from a local agent runtime that works to one an agent can finish on its own.

## Where we are

The runtime works end to end. `tiny up` turns an empty cluster into a running one, bare `tiny` serves an MCP endpoint and a browser editor over that cluster, and an agent driving the endpoint from your editor builds flows that run as real pods. The three phases the previous roadmap laid out — provisioning, the local MCP server, the local editor — all shipped. What follows is what shipping them exposed.

Real today:

- `tiny up` — installs the CRDs, the NATS/JetStream broker, the OpenTelemetry collector and four core modules (common, http, llm, kubernetes) as Helm releases, then prints how to serve them. Shows and confirms the target context + namespace first.
- `tiny install <module>[@version]` — resolves a module from the configured repos and helm-installs it. The agent's `install_module` takes the same path mid-build, so a prompt that reaches for a missing capability installs it rather than failing.
- `tiny repo list | add | remove | update | index` — Helm-style module repos. `repo index <dir>` builds an `index.yaml` from `module.yaml` manifests, so a repo is a static file anyone can host. The baked-in default is `tiny-systems/modules` served raw off `main`.
- `tiny` (no command) — the headline. One process: MCP over HTTP on `:7776`, and the editor SPA plus a gRPC-web `FlowService` on `:7775`, both against the same cluster. It picks or creates the session's project up front, streams every tool call into the terminal and into the editor's Activity feed, and port-forwards cluster-side server ports to localhost so the URLs the editor shows are actually reachable.
- `tiny publish` — packages a project's flows, nodes, dashboard pages and scenarios into the platform's solution-export envelope and uploads it. It validates first: stale scenario references, samples that contradict their port schema, edges that can't be verified, widgets the installer would choke on.
- `tiny login` / `logout` / `whoami` — OIDC device flow against the platform's Keycloak realm, tokens refreshed automatically. Only `publish` needs them; everything else is kubeconfig-only.
- `tiny upgrade` — checks GitHub releases and swaps the binary in place, including over a Homebrew install (it resolves the symlink, so brew's `bin/` link stays valid and only brew's recorded version goes stale).
- `tiny --print` — prints the MCP client config and exits without serving.

Thin, and worth naming as such:

- `tiny status` — still what it was on day one: it shells out to `kubectl get tinymodules -o name` and counts the lines. It is the only command that reports on a cluster, and it reports almost nothing.
- `tiny edit [flow]` — a stub. It prints "the editor is served by the dev server — run `tiny` with no command" and exits. The editor is real; this command is a signpost.

## The one decision that shapes everything

**`tiny` embeds the Go SDK in-process** — it imports `module/pkg/tools` (get_instructions, read_project, build_flow, edit_flow, get_traces, …) and builds a `resource.Manager` from your kubeconfig, and talks to the cluster through that. It does not shell out to a separate MCP binary, and it does not require the hosted platform.

Still true, and now load-bearing in both directions: the same in-process SDK backs the MCP endpoint *and* the editor's `FlowService`, so an agent's `build_flow` and a human's canvas save go through the same graph helpers against the same CRDs. That is why the editor shows what the agent just did without a sync step.

Two honest caveats on "in-process":

- `tiny status` and target-context resolution shell out to `kubectl`; `tiny upgrade` shells out to `brew --prefix`. Everything that matters uses client-go, but the CLI is not binary-free.
- The editor is only half-shared. The frontend is the shared `@tinysystems/editor` package, consumed straight from git. The backend is not shared: `internal/flow` is `tiny`'s own implementation of the platform's `FlowService`, roughly 3,000 lines against the `platform-go` protos, with the platform-only RPCs (prompt, revision history, registry browse) falling through to `Unimplemented`. The wire contract is shared; the implementation is a fork, and both sides now have to be kept honest about the same CRDs.

## Project lifecycle — the commands that don't exist

You can build a project and publish it. You cannot manage it.

`export`, `import`, `list`, `recover` and `delete` are all absent from the CLI. `publish` is the only way out of a project, and it is a one-way door to the public solutions catalog — there is no local round trip. The pieces are already sitting in `cmd/publish.go`: it builds the export envelope (flows, nodes, dashboard pages, scenarios) and the SDK's `clone_solution` is the import side. What's missing is the plumbing that doesn't go through the platform, plus the flat operations (`list`, `delete`) that are a few CRD calls each.

Effort: medium. This is the gap a second user hits first — a project that exists only as CRDs in one namespace, with no way to move it, copy it, or throw it away.

## Let the agent finish what it starts

The canvas can do two things the MCP surface can't, and the agent notices.

- **Share a node across flows.** A canvas save writes the `shared-with-flows` annotation (`internal/flow/save.go:150`) and the stream renders the node on the other layer. `edit_flow`'s action enum (`module/pkg/tools/edit_flow.go:69`) is six actions — `add_node`, `delete_node`, `add_edge`, `delete_edge`, `configure_edge`, `configure_node` — and every handler is hard-scoped to the ambient flow. The only writer in the SDK is the graph importer behind `clone_solution`, which copies the annotation verbatim out of a solution export — there is no way to set it deliberately. So an agent building across layers has to hand the wiring back to a human.
- **Lay out a dashboard.** `set_node_dashboard` is registered and wired, so an agent *can* flag a node as a widget — but in kubeconfig mode that adapter only toggles the node's `DashboardLabel`, ignores the port argument, and returns the project name as the "page". Pages are `TinyWidgetPage` resources, and only the canvas creates, deletes or positions them (`CreateDashboardPage` / `DeleteDashboardPage` / `SaveWidgets`). An agent can therefore pin widgets but cannot build the dashboard it just pinned them to.

Same shape of problem in `publish`: a scenario whose nodes were removed by `delete_node` blocks the publish with an error that names the orphaned **ports** and tells the agent to "delete the stale ones" — but `scenarios(action=delete)` takes a `resource_name`, `scenarios(action=list)` returns only `resource_name`, `name` and `port_count`, and the orphans collect in the machine-written auto-scaffold scenario. There is no way to get from a port name back to the scenario holding it, and no way to drop one sample without destroying the rest. The error is correct and unactionable. (A sweep that prunes orphaned samples after a node delete is in the working tree, uncommitted — `internal/adapters/scenario_prune.go`.)

Effort: medium. The write paths exist on the `tiny` side; this is tool surface in the SDK plus the adapters to back it.

## Session cost

A session spends about **23.8k tokens before any work happens**: `get_instructions` 10.1k, `list_modules` 7.3k, `tools/list` 6.3k, plus ~150 tokens of server-level instructions on `initialize`. Only `tools/list` and the `initialize` instructions are strictly protocol-automatic; the other 17.4k is model-initiated, but the prompts steer hard enough ("call get_instructions first") that in practice it always fires.

Two of the three scale badly. `list_modules` costs roughly 110–150 tokens per installed component, so the more capable the cluster, the more expensive its first turn — exactly backwards. And `edit_flow`, `build_flow` and `scenarios` are ~9 KB between them, 36% of the whole `tools/list` payload, paid on every session whether or not the agent builds anything.

Nothing here is free to cut. `get_instructions` is the SDK's `CorePrompt` (41 KB) plus `tiny`'s public appendix (3.5 KB), and both earn their length — first-build-green depends on the model knowing the rules before it writes a flow. The work is making the cost proportional: paginate or summarise `list_modules` so it doesn't grow linearly with the catalog, and move the parts of the prompt and the fat schemas that only matter mid-build behind the tool that needs them.

Effort: medium, and it lands mostly in the SDK, so it changes the hosted platform's cost too.

## The agent page setup zone

`/app/:project` exists and renders — chat, widgets, dashboard page tabs — and it is the surface a non-builder is meant to use. It is missing its front half.

- There is no setup zone. An agent that needs an API key before it can start has no place to ask for one, so the handoff from "agent built" to "agent running" still goes through the flow canvas.
- The status chip reports liveness only — Live / Connecting / Offline off the stream. "Needs setup" is written into the design but has nothing to compute it from.
- Nothing links to the page. `tiny` prints the editor root, and no MCP response carries a `setupUrl`, so the finish-setup line the design assumes has no source.

The design is written up in the editor repo (`docs/superpowers/specs/2026-08-15-agent-page-design.md`). Effort: medium, but it crosses three repos — the editor package for the zone and the chip, `FlowService` for aggregating unfilled required settings, and `tiny` for printing the link.

## Housekeeping (alongside the above)

- `tiny status` — grow it from a line count to node health, module versions and broker reachability, and stop shelling out to `kubectl` for it.
- `tiny edit` — implement it (open the browser at the right flow against a running server, or start one) or delete it. A command that tells you to run a different command is worse than no command.
- Local state is split in two: repos and their cache live in `~/.tiny/`, the auth token lives under `os.UserConfigDir()/tiny/auth.json`. One of those is wrong. There is still no config for a default context, namespace or ports — ports are env-only (`TINY_MCP_PORT`, `TINY_EDITOR_PORT`).
- `tiny doctor` preflight: kubectl reachable, cluster reachable, versions sane. Bare `tiny` already does a version of this inline (ping, then a CRD check that says "run `tiny up`"); a standalone command would make it usable before something fails.
- Windows `.zip` self-update — `tiny upgrade` still extracts `.tar.gz` only, and fails with "windows .zip self-update not supported yet" when handed a zip.
- CI fires on tags only and does nothing but rebuild the embedded SPA and run goreleaser. There are 24 test files in the tree and nothing runs them — the only automated check anywhere is the Homebrew formula's `tiny --version` smoke test. A push-triggered `go test ./...` is the cheapest thing on this list.
- `internal/catalog` is dead. Nothing calls `catalog.Resolve`, and `provision.InstallModule` — its only consumer — has no callers either. The repo model replaced it. Delete it before someone maintains it.

## Order

**Project lifecycle → agent parity → session cost → setup zone.**

Lifecycle first: it is the smallest, it is self-contained in `tiny`, and it is the gap that makes a working project feel trapped. Agent parity next, because every one of those gaps ends with "and then a human has to open the canvas", which is the thing this is supposed to avoid. Session cost is real but nobody is blocked on it. The setup zone is last not because it matters least — it is the difference between an agent you built and an agent you can hand to someone — but because it is the only item that needs all three repos moving together.
