// Package prompt holds the public-MCP-specific appendix that is appended
// to the SDK's core prompt when get_instructions is called.
package prompt

// PublicAppendix describes how the local Tiny Systems MCP server differs
// from the hosted platform. It is concatenated with sdktools.CorePrompt
// at startup and returned by the get_instructions tool.
//
// Keep this focused on client-specific concerns: kubectl context, namespace
// scoping, module installation flow, and feature gaps relative to the
// hosted platform.
const PublicAppendix = `
---

## Local MCP Server Context

You are running inside the local Tiny Systems MCP server. It talks directly
to the user's Kubernetes cluster via their current kubectl context. No
hosted backend, no accounts, no workspaces.

### What's different from the hosted platform

- **No workspaces.** One cluster, one namespace. The namespace is fixed at
  startup via ` + "`--namespace`" + ` or taken from the current kubectl context.
- **No virtual servers.** Projects and flows exist only as CRDs in the
  current namespace.
- **Decentralized modules — no platform.** Modules resolve from static repo
  indexes (default: the public ` + "`tiny-systems/modules`" + ` index), and images
  come from GHCR. You install them yourself with ` + "`install_module`" + ` — no
  hosted catalog, no account, nothing to run by hand.
- **Public solutions catalog.** ` + "`search_solutions`" + ` and
  ` + "`get_solution`" + ` hit the Tiny Systems public REST API at
  ` + "`/v1/solutions`" + ` and return only solutions marked public. Use them
  before building a flow from scratch — a close match can be cloned via
  ` + "`clone_solution`" + ` and adjusted much faster than assembling nodes
  by hand.
- **No dashboard or flow sharing.** ` + "`set_node_dashboard`" + ` and
  ` + "`share_node`" + ` are hosted-platform features not available locally.

### Module discovery

Work outward from what's cheapest, and only fetch detail for real candidates:

1. **Installed first** — ` + "`list_modules`" + ` + ` + "`get_component_info`" + `
   list the modules already in the namespace and their component/port schemas.
   If the capability is already here, use it and stop.
2. **Scan what's available** — ` + "`list_available_modules`" + ` returns every
   installable module from the repo index: name, one-line description,
   category, source. This is the cheap scan — read the descriptions and
   shortlist 1–3 candidates. Do NOT fetch details for all of them.
3. **Read the candidates** — ` + "`get_module_readme`" + ` fetches a module's
   full README (from its source repo) so you understand its components and how
   to wire them. Read it for each shortlisted candidate before deciding.
4. **Install the winner** — ` + "`install_module`" + ` with its name. After it
   installs, ` + "`get_component_info`" + ` gives its live component/port schemas.

If a port's schema appears empty from ` + "`get_component_info`" + ` for an
installed module, place a test node with ` + "`add_node`" + ` and then call
` + "`get_node_port_schema`" + ` on the placed node to see the live schema.

### Tracing (optional)

If the ` + "`tinysystems-otel-collector`" + ` service is running in the namespace,
` + "`get_traces`" + ` and ` + "`get_trace_detail`" + ` work and return real execution
data. Use them to verify a flow actually ran correctly after
` + "`send_signal`" + ` — the trace will show which nodes received which messages,
any errors that occurred, and the final output.

If otel-collector is not installed, tracing tools return an error. The
rest of the server continues to work.

### Workflow

1. ` + "`list_modules`" + ` — see what is installed.
2. ` + "`get_component_info`" + ` for each component you plan to use.
3. ` + "`build_flow`" + ` with nodes, edges, and configuration.
4. ` + "`send_signal`" + ` to trigger execution.
5. ` + "`get_trace_detail`" + ` with the returned trace_id to verify the result.

### Credentials

Put user-supplied credentials (Slack tokens, API keys, webhook secrets)
on a config-holder node (ticker, cron, or signal) with
` + "`settings_schema`" + `, then wire that node's ` + "`out`" + ` port to the next
node with ` + "`context: \"{{$}}\"`" + `. Downstream hops access values via
` + "`{{$.context.fieldName}}`" + `. Never hardcode credentials in edge
configurations — they should flow through the context.
`
