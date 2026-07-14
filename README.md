# tiny

**Self-hosted AI agents on your own Kubernetes. Prompt one in your editor and it runs as real workloads. Your cluster, your keys, your data.**

`tiny` is the local front door to the [Tiny Systems](https://tinysystems.io) agent runtime. Point it at any cluster you can `kubectl` into, describe an agent from your editor, and watch it build itself and run as real pods. Nothing leaves your cluster.

> **Early days.** The runtime, SDK, operator, and modules are production-tested; this CLI is the new front door that ties them together. The [roadmap](#roadmap) says what works today and what's next.

---

## The idea

Most agent frameworks are a Python library, a hosted API, or a Docker-Compose box. `tiny` is a different shape: an agent is a set of **real Kubernetes workloads** the operator reconciles, and you build it by prompting from Claude Code, Cursor, or any MCP client, instead of writing glue.

```
$ tiny

  ◇ tiny  self-hosted AI agents on your own Kubernetes

  ╭──────────────────────────────────────────────────────────────╮
  │  runtime   ✓ context my-cluster                              │
  │  mcp       http://localhost:7776/mcp   → point Claude Code    │
  │  editor    http://localhost:7775       → opens in your browser│
  ╰──────────────────────────────────────────────────────────────╯

  prompt in your editor; the canvas mirrors it live.
```

One process serves both surfaces: the MCP endpoint your editor drives, and the browser editor that mirrors what the agent builds, live, over the same cluster state. You prompt on the left and watch it materialize on the right.

## Why self-hosted

- **Your cluster.** Agents run where your data already lives, with no round-trip to someone else's cloud.
- **Your keys.** LLM calls use the key you set on the agent. `tiny` never holds it.
- **Real workloads.** Every capability is a Helm-installable module reconciled by an operator, not a function in a hosted sandbox. `kubectl get pods` shows you your agent.
- **Empty to running.** Start from a bare `kind`, `k3s`, or cloud cluster. `tiny up` provisions the broker, the operator, and a core set of modules. Anything else installs on demand, including automatically when a prompt-built agent reaches for a capability it doesn't have yet.

## Install

```sh
# Homebrew (coming with the first release)
brew install tiny-systems/tap/tiny

# or grab a binary from Releases, or:
go install github.com/tiny-systems/tiny@latest
```

## Quick start

```sh
tiny up            # provision the runtime onto your current cluster (asks first)
tiny mcp           # print the line to add to Claude Code / Cursor
tiny               # start the dev server: MCP endpoint + editor
```

Then, in your editor: *"an HTTP endpoint that summarizes the JSON I POST and alerts Slack if the sentiment is negative"* — and watch it build on the canvas.

## Commands

| command | what it does |
|---|---|
| `tiny` | start the dev server — MCP endpoint + browser editor, one process |
| `tiny up` | provision the runtime (NATS/JetStream broker + operator + core modules) |
| `tiny install <module>` | add a capability module from the public catalog |
| `tiny status` | show the runtime + installed modules on the target cluster |
| `tiny mcp` | run the local MCP server, or print the client config |
| `tiny edit [flow]` | open the web canvas against your cluster |

Every mutating command shows the exact context and namespace it will touch and asks before it acts. Pass `--yes` to skip in CI, or `--context` / `--namespace` to target explicitly.

## How it fits together

- **The operator** reconciles agents into workloads and installs capability modules as Helm releases.
- **Modules** are the capabilities: LLM, HTTP, Kubernetes, databases, Slack, and more, each a small Go service the agent composes.
- **MCP** is the prompt surface. `tiny` serves it locally against your kubeconfig; the hosted platform serves the same tools at `mcp.tinysystems.io` for teams.
- **The editor** is the trust layer. You watch and inspect what you didn't hand-write.

The runtime and SDK are open source. The [hosted platform](https://tinysystems.io) adds a team layer (shared workspaces, observability across clusters, managed clusters) for those who want it. `tiny` needs none of it.

## Roadmap

- **v0.1** — `up` / `install` / `status` against your cluster, with the target-confirmation guard. Turn an empty cluster into a working runtime from your terminal.
- **v0.2** — `tiny` (dev): the live MCP endpoint and editor in one process, streaming agent activity into the terminal as your editor drives. This is the point where prompt-built agents run on your own cluster with no hosted account.
- **v0.3** — the local canvas (`tiny edit`), which retires the separate desktop app.

Follow along or open an issue. This is being built in the open.

## License

MIT. Depends on the [Tiny Systems Module SDK](https://github.com/tiny-systems/module).
