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
- **No background job tracking.** Module installation is done by the user
  running ` + "`helm install`" + ` themselves — there is no ` + "`install_module`" + ` tool.
- **Public solutions catalog.** ` + "`search_solutions`" + ` and
  ` + "`get_solution`" + ` hit the Tiny Systems public REST API at
  ` + "`/v1/solutions`" + ` and return only solutions marked public. Use them
  before building a flow from scratch — a close match can be cloned via
  ` + "`clone_solution`" + ` and adjusted much faster than assembling nodes
  by hand.
- **No dashboard or flow sharing.** ` + "`set_node_dashboard`" + ` and
  ` + "`share_node`" + ` are hosted-platform features not available locally.

### Module discovery

Two discovery paths, and you will often need both:

- **` + "`list_modules`" + ` / ` + "`get_component_info`" + `** — cluster-scoped.
  Lists modules already installed in the caller's namespace. Use this
  first: if the module you need is here, skip the catalog.
- **` + "`search_modules`" + ` / ` + "`get_module_info`" + `** — catalog-scoped.
  Queries the public Tiny Systems catalog (same slice the website
  https://tinysystems.io/modules shows). Use this when ` + "`list_modules`" + `
  is empty or missing something a solution needs. The catalog returns
  components, port schemas, RBAC requirements, and the helm install
  command the user can run to add the module to their cluster.

When ` + "`list_modules`" + ` comes up short, the right move is to
` + "`search_modules`" + ` for the capability, then ` + "`get_module_info`" + `
for the winning module and quote its ` + "`helm_install.command`" + ` (with
prerequisites and warnings) to the user so they can install it. Do NOT
attempt to install modules yourself — module install is a user action
in local mode.

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
