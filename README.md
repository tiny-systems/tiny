# ◇ tiny

**Claude Code & Codex sessions on your own Kubernetes.**

![tiny demo: a session is created, its pod is killed mid-task, and the fleet screen shows it still working](https://tinysystems.io/static/demo.gif?v=clean1)

Start a session with a task. It runs as a pod with a persistent
workspace and keeps working after you close your laptop, through rate
limits and pod restarts. When the agent needs a decision, its row lights
up; answer from the fleet screen, or from anywhere with `kubectl`.

```
$ tiny new "fix the flaky checkout test, open a PR"
  ◌ session s-x7k2f created on prod/team-a
  ◌ creating workspace and pod
  ◌ pulling images / creating containers
  ● agent up (s-x7k2f-agent)

$ tiny
    NAME        STATE      AGE   CPU    MEM  WHAT
  ✳ s-x7k2f     needs you  12m   84m  512Mi  May I force-push the rebased branch?
  ● api-fix     running     3h  212m  1.1Gi  migrating auth tests to vitest
  └ ● api-db    running    41m  907m  2.9Gi  rewriting migrations in golang:1.26
  ● night-run   running     8h    1m  301Mi  ⏸ Usage limit reached · continuing at 5:20pm
  · ＋ new session
  · ⚙ new session with options…
  · ☰ namespace settings
  · ✕ quit

  [enter] attach  [a] answer  [m] message  [d] delete  [q] quit
```

CPU and memory are self-reported by each session from its own cgroup — no
metrics-server, no extra RBAC. WHAT is live: the agent's declared title,
refreshed by its own turns; a session paused on a usage limit says so and
resumes itself.

## What makes it different

- **It's the real CLI, not a wrapper.** Attach and you're in genuine
  Claude Code (or Codex) over a TTY: hotkeys, slash commands, plan mode,
  subagents, skills from your repo, your `.mcp.json` servers. tiny does
  not parse or proxy the agent, so new agent features work without us
  shipping anything.
- **Two agents, one flag.** `tiny new --agent codex` runs OpenAI's Codex
  instead of Claude; `--model` picks the model for either. Sign in with
  the plan you already pay for — Claude Pro/Max or ChatGPT Plus.
- **Sessions survive pod loss.** The workspace is a persistent volume and
  the pod is disposable: a rescheduled pod resumes the transcript and
  keeps going. We test this by force-killing pods mid-task.
- **Any image becomes an agent environment.** `--image golang:1.26`,
  `--image maven:3-eclipse-temurin-21`, or your own dev image: an init
  container injects the agent (claude, codex, a static tmux, the
  entrypoint) into whatever you name. The contract is glibc, git and
  /bin/sh; you don't maintain a special image.
- **Sessions spawn sessions — through a human gate.** A light root session
  plans, then asks to start specialists in the right toolchain with the
  right cpu/memory. You approve each spawn from the fleet screen; children
  render under their parent.
- **Namespace add-ons, one checkbox each.** A namespace is a group of
  agents — a team, a project, one person. Its settings screen can switch
  on a **zot registry cache** (one Docker Hub pull per image per
  namespace: 191s cold, 9s warm in our tests) and a **minio artifact
  store** (sessions hand each other files with `mc cp build.tar
  store/artifacts/`). Agents can request the store themselves through the
  gate — your y both approves and provisions it. The
  cache is a push target too: a buildah session builds an image, pushes
  it to `$TINY_REGISTRY`, and the next session runs what the last one
  built — build, push, spawn, all inside the namespace.
- **Every decision is an auditable object.** Blocked tool calls park as
  Question CRs until a person answers:

```sh
kubectl get questions
NAME      SESSION   QUESTION                                        ANSWER
q-pr5qp   s-x7k2f   May I force-push the rebased branch?            yes
q-8w6lw   root      …start a session in golang:1.26 (cpu 1) — allow?  allow
```

## How it works

**There is no server and no operator pod.** The namespace runs the
sessions you started and the add-ons you switched on, and nothing else.

- A **Session** is a Kubernetes object whose workload is a plain
  Deployment: the agent in a detachable tmux plus a small **tiny-mcp**
  sidecar on localhost — the agent's toolbox (`ask_human`, `set_title`,
  `session_list`, `session_create`, `expose_port`, `enable_store`).
  Kubernetes itself resurrects dead pods — kill one mid-task and the
  replacement resumes the transcript; that is stock ReplicaSet behavior,
  not tiny code.
- Whoever **creates** a session materialises its workload with their own
  credentials — your CLI on `tiny new`, a runner job on `tiny deliver`.
  Deleting the session garbage-collects everything via owner references.
- The sidecar is powerless by design: it can create Questions and update
  its own session's status, nothing else. When the agent reaches a
  decision it must not make alone, the tool call **blocks** — minutes or
  hours — until a person answers. And **answering is acting**: pressing y
  performs the approved action with *your* credentials, so the cluster's
  audit log names the human, not a service account.
- Agents keep a living **title** and turn-by-turn **activity**, so the
  fleet screen says what each session is doing *now*.

## Install

```sh
brew install tiny-systems/tap/tiny     # or a binary from Releases
tiny setup                             # one wizard: cluster, runtime, token, repo key
tiny new "your task"
```

`tiny setup` pins your cluster, installs the runtime (2 CRDs and one
ServiceAccount — no pods), stores your agent credential (`claude setup-token` or an API
key), and mints an ed25519 **deploy key** for private repos — the private
half lives in your cluster, the public half is printed for GitHub. It never
reads your `~/.ssh`.

Re-run `tiny setup` any time: it only offers what's missing, and asks
before replacing an existing token. When a session's credential goes bad,
its fleet row says so in the agent's own words (`Invalid API key`, `OAuth
token has expired`) — replace the token, cycle the session's pod, the
transcript resumes.

## Everyday commands

| command | what |
|---|---|
| `tiny` | the fleet screen — who runs, who needs you |
| `tiny new [task]` | start a session; with no task, attaches you straight to the agent's terminal |
| `tiny new --image golang:1.26 --cpu 2 --memory 4Gi "…"` | session in your toolchain, sized |
| `tiny new --image quay.io/buildah/stable --user 1000 "…"` | a builder — agents build images and push them to the namespace registry |
| `tiny new --agent codex --model gpt-5.2-codex "…"` | the same session, OpenAI's Codex inside |
| `tiny broadcast "demo at 10 — wrap up"` | one message into every unfinished session's inbox |
| `tiny shell <session>` | shell on a session's workspace — finished sessions too |
| `tiny answer <question> <text>` | answer a ✳ card — and perform its action, as you |
| `echo "…" \| tiny deliver <session> --ensure` | pipe a message into a session's inbox (what event sources call) |
| `tiny setup` | interactive setup — and rotation: token, repo key |
| `tiny init` | scriptable runtime install for CI (`--context X -n Y --yes`) |
| `tiny upgrade` | update the binary |

On the fleet screen: `m` types a message straight into a session's prompt
(delivered through a durable inbox — it survives pod restarts and
usage-limit pauses), and **dropping a file onto the terminal** — fleet screen
or attached session — streams it to `/workspace/uploads/` with live
progress and hands the agent the path.
Every start opens with an arrow-key picker for the cluster and namespace
(enter-enter repeats yesterday's; `--context` and `-n` skip it).

Attached-session tricks (it's tmux, prefix `ctrl-q` — `ctrl-b` works
too): `ctrl-q d` detach, `ctrl-q c` a plain shell beside the agent,
`ctrl-q ctrl-q` toggle between them, `ctrl-q [` scrollback.

## Label an issue, get a PR

The repo carries a [workflow](.github/workflows/tiny.yml): label an issue
`tiny` and a five-second job on the in-cluster runner (a namespace-settings
add-on) pipes it into the root session's inbox. The session works — spawns
specialists if it needs toolchains — and ships **through the outbox**:

**Agents hold no credentials at all.** To send work out, a session writes
a git bundle to `/workspace/outbox/` — `git bundle create
/workspace/outbox/tiny-issue-7.bundle tiny/issue-7` — and a scheduled
seconds-long courier job (`tiny export`, every ~5 minutes) lifts pending
bundles out over the exec API, rebases them onto `main`, and pushes with
the job's own short-lived token: `tiny/issue-N` becomes a pull request,
a `REPLY.md` on `tiny/reply-N` becomes an issue comment. A bundle is
retired only after its push succeeds. Nothing to paste, nothing stored,
and a compromised agent can neither push nor call the GitHub API.

One-time GitHub setting for the PR half: org **Settings → Actions →
General → Workflow permissions → allow GitHub Actions to create and
approve pull requests**.

## Layout

One repository, one version:

| path | what |
|---|---|
| `cmd/tiny` | the CLI and the fleet screen |
| `cmd/controller` | the tiny-mcp sidecar (its only remaining role) |
| `internal/workload`, `internal/actions`, `internal/addons` | what used to be the manager — run by whoever creates, answers, or toggles |
| `api/`, `config/` | the Session + Question CRDs and the embedded install manifests |
| `images/agent` | the agent image and the injectable payload |

---

> 🌱 Early and building in the open. We're small; stars are the main way
> people find projects like this, so if you want it to keep going,
> **a star up top genuinely helps.**

Website & docs: **[tinysystems.io](https://tinysystems.io)** · field
notes: [tinysystems.io/blog](https://tinysystems.io/blog/) · demo garden:
[seedling](https://github.com/tiny-systems/seedling)

MIT.
