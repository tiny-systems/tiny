# ◇ tiny

<p align="center">
  <img src="demo/demo.gif" alt="tiny — prompt on the right, watch it build on the left" width="100%">
</p>

<p align="center">
  <b>Self-hosted AI agents on your own Kubernetes.</b><br>
  Prompt one in your editor and it runs as real workloads. Your cluster, your keys, your data.
</p>

<p align="center">
  <a href="https://github.com/tiny-systems/tiny/releases"><img src="https://img.shields.io/github/v/release/tiny-systems/tiny?style=flat-square&color=6366f1" alt="release"></a>
  <img src="https://img.shields.io/badge/go-1.25-00ADD8?style=flat-square" alt="go">
  <img src="https://img.shields.io/badge/license-MIT-10b981?style=flat-square" alt="MIT">
  <img src="https://img.shields.io/badge/status-building%20in%20the%20open-f59e0b?style=flat-square" alt="building in the open">
</p>

---

`tiny` is the local front door to the [Tiny Systems](https://tinysystems.io) agent runtime. Point it at any cluster you can `kubectl` into, describe an agent from your editor, and watch it build itself and run as real pods. Nothing leaves your cluster. 🏠

> 🌱 **Early days.** The runtime, SDK, operator, and modules are production-tested; this CLI is the new front door that ties them together. The [roadmap](#-roadmap) says what works today and what's next.

## 💡 The idea

Most agent frameworks are a Python library, a hosted API, or a Docker-Compose box. `tiny` is a different shape: an agent is a set of **real Kubernetes workloads** the operator reconciles, and you build it by prompting from Claude Code, Cursor, or any MCP client — instead of writing glue.

```
$ tiny

  ◇ tiny  self-hosted AI agents on your own Kubernetes

  context minikube   namespace tinysystems
  project playground
  serving http://localhost:7776/mcp
  editor http://localhost:7775   → open in your browser

  Connect Claude Code (one-time):

    claude mcp add -s user --transport http tiny http://localhost:7776/mcp

  Ctrl-C to stop · tool calls stream below.
```

One process serves both surfaces: the **MCP endpoint** your editor drives and the **browser editor** that mirrors what the agent builds — live, over the same cluster state. You prompt on the right and watch it materialize on the left. ✨

## 🔒 Why self-hosted

- 🏠 **Your cluster.** Agents run where your data already lives — no round-trip to someone else's cloud.
- 🔑 **Your keys.** LLM calls use the key you set on the agent. `tiny` never holds it, and pinned sample data is secret-redacted before it ever touches etcd.
- 📦 **Real workloads.** Every capability is a Helm-installable module reconciled by an operator, not a function in a hosted sandbox. `kubectl get pods` shows you your agent.
- ⚡ **Empty to running.** Start from a bare `kind`, `k3s`, or cloud cluster. `tiny up` provisions the broker, the operator, and core modules. Anything else installs on demand — including automatically when a prompt-built agent reaches for a capability it doesn't have yet.
- ✅ **Built for agents that build.** Flows validate green on the first `build_flow` call (schemas are derived, sample data auto-scaffolds), signals wait for the flow to wake before firing, and every warning reaches the model — so your editor's agent ships working automations instead of chasing ghosts.
- 🛑 **Safe to point at production.** An agent that can restart a Deployment is only useful if it can't do it behind your back. Put an `ask` node in front of a destructive step and the flow presents you a form — the proposed action, the evidence, Approve/Deny — and waits. Bound the loop with `budget_guard` so a model that won't converge can't bill forever.

## 🤖 What an agent can actually do here

Not "call an API and hope" — these run as workloads on your cluster:

- 👀 **Watch and diagnose.** React to real pod events, read the logs, query metrics and logs from any Prometheus- or Loki-compatible backend (in-cluster or Grafana Cloud/AMP/Mimir — it's a URL), and inspect its own execution traces to debug the automations it built.
- ✋ **Ask before it acts.** `ask` renders a form from a schema the flow supplies and blocks the branch until a human answers. Approval is just a form with two buttons.
- 🧪 **Run code it wrote.** `sandbox_run` executes a script in a throwaway Job — non-root, read-only filesystem, no service-account token, dropped capabilities, CPU/memory limits, killed on a deadline. Real Python or Node, next to your data, in a box.
- 🔌 **Use any MCP server's tools.** `mcp_tools` discovers them, `mcp_call` invokes them — so an agent reaches GitHub, Sentry, or your own MCP server without a connector being written for each.

## 📦 Install

```sh
# Homebrew
brew install tiny-systems/tap/tiny

# or grab a binary from Releases, or:
go install github.com/tiny-systems/tiny@latest
```

Update later with `tiny upgrade` (or `brew upgrade tiny`).

## 🚀 Quick start

```sh
tiny up            # provision the runtime onto your current cluster (asks first)
tiny               # serve the MCP endpoint + browser editor
```

Connect your editor once — `tiny` prints the exact command on start:

```sh
claude mcp add -s user --transport http tiny http://localhost:7776/mcp
```

Then, in your editor: *"an HTTP endpoint that summarizes the JSON I POST and alerts Slack if the sentiment is negative"* — and watch it build on the canvas. 🎨

Or point one at the cluster it's running on:

> *"watch pods in `prod`; when one crashloops, pull its logs, work out what's wrong, and **ask me before restarting anything**"*

which builds `pod_watch → pod_logs_get → llm → ask → restart`. When a pod actually breaks, the form arrives with the diagnosis and waits for you. In our own test the model read the logs and argued *against* its proposed restart — a port conflict a restart wouldn't have fixed. That's the point of the gate. 🛑

## 🧰 Commands

| command | what it does |
|---|---|
| `tiny` | serve the local MCP endpoint + browser editor, stream tool calls |
| `tiny up` | provision the runtime (NATS/JetStream broker + operator + core modules) |
| `tiny install <module>` | add a capability module from the configured repos |
| `tiny repo …` | manage module repos — Helm-style static indexes, add your own |
| `tiny status` | show the runtime + installed modules on the target cluster |
| `tiny edit [flow]` | open the web canvas against your cluster |
| `tiny upgrade` | update tiny to the latest release |
| `tiny --print` | print the MCP client config and exit (don't serve) |

Every mutating command shows the exact context and namespace it will touch and asks before it acts. Pass `--yes` to skip in CI, or `--context` / `--namespace` / `--project` to target explicitly. 🎯

## 🧩 How it fits together

- ⚙️ **The operator** reconciles agents into workloads and installs capability modules as Helm releases.
- 🧱 **Modules** are the capabilities: LLM and MCP, HTTP and metrics/logs, Kubernetes (including sandboxed execution), databases, Slack, and more — each a small Go service the agent composes.
- 🔌 **MCP** is the prompt surface. `tiny` serves it locally against your kubeconfig; the hosted platform serves the same tools at `mcp.tinysystems.io` for teams.
- 👀 **The editor** is the trust layer. You watch and inspect what you didn't hand-write.
- 📸 **Scenarios** are the memory. Pin a real run as sample data (secrets redacted) and every edge validates against real shapes.

The runtime and SDK are open source. The [hosted platform](https://tinysystems.io) adds a team layer (shared workspaces, observability across clusters, managed clusters) for those who want it. `tiny` needs none of it.

## 🗺 Roadmap

- ✅ **v0.1** — `up` / `install` / `status` against your cluster, with the target-confirmation guard. Empty cluster → working runtime from your terminal.
- ✅ **v0.2** — `tiny` (dev): the live MCP endpoint and browser editor in one process, streaming agent activity into the terminal as your editor drives. Prompt-built agents on your own cluster, no hosted account.
- ✅ **v0.3** — decentralized module repos (`tiny repo` — Helm-style indexes you can host anywhere), scenario pinning with secret redaction, first-build-green validation.
- ✅ **v0.4** — agents that act on infrastructure safely: human-in-the-loop approval (`ask`), sandboxed code execution, MCP client so any MCP server's tools are reachable, observability (own traces, PromQL/LogQL), and loop budgets. Plus `module tools rbac-check`, which compares a module's declared RBAC against the Kubernetes calls its code actually makes.
- 🔭 **next** — richer canvas, more modules, smoother day-2 (upgrades, multi-cluster).

Follow along or open an issue — this is being built in the open. 🛠

## 📄 License

MIT. Depends on the [Tiny Systems Module SDK](https://github.com/tiny-systems/module).
