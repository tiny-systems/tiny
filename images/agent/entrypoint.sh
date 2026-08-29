#!/bin/sh
# tiny agent entrypoint — the coding agent in tmux, resumable by design.
# TINY_AGENT picks which one: claude (default) or codex.
#
# POSIX sh on purpose: this script is INJECTED into arbitrary session images
# (spec.image: golang, maven, your dev image) which may not carry bash.
# Image contract: glibc, /bin/sh, git. Everything else travels with us.
#
# Contract with the Session controller:
#   TINY_TASK          the task (first prompt)
#   TINY_REPO          optional git URL cloned into the workspace on first run
#   TINY_SESSION_NAME  identity, for logs only (the sidecar carries the real one)
#   /workspace         the persistent volume; everything that matters lives here
#
# Resume rule: the FIRST pod for a session starts claude with the task; any
# later pod finds the transcript in the workspace and continues it instead —
# a rescheduled pod is a continuation, never a restart from zero.
set -u

# Everything we ship lives next to this script — /opt/tiny in our own image,
# /tiny when injected into a foreign one. The tree is relocatable.
TINY_HOME=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
export PATH="$TINY_HOME/bin:$PATH"

WORKSPACE=/workspace
MARKER="$WORKSPACE/.tiny/started"
AGENT="${TINY_AGENT:-claude}"
export CLAUDE_CONFIG_DIR="$WORKSPACE/.claude"
# uid 61000 has no passwd entry in a foreign image; HOME must still point
# somewhere writable or git/go/claude all sulk.
export HOME="$WORKSPACE"
# Claude Code draws with Unicode; without a UTF-8 locale every glyph
# degrades to '_' and the whole TUI looks broken.
export LANG=C.UTF-8 LC_ALL=C.UTF-8
# The IMAGE owns claude's version — deterministic pods, no over-the-air
# self-updates half-applied into the workspace, no restart nagging. New
# claude arrives the honest way: a new agent image.
export DISABLE_AUTOUPDATER=1
# ask_human blocks for hours BY DESIGN; claude's MCP client must not give
# up first. 24h in milliseconds.
export MCP_TOOL_TIMEOUT=86400000
export MCP_TIMEOUT=120000
mkdir -p "$WORKSPACE/.tiny" "$CLAUDE_CONFIG_DIR"

# Fail loud and early where the contract is broken — a wedged pod with no
# message is the one outcome worse than failure.
die() {
  echo "tiny: $1" >&2
  # The termination log is how the reason travels to Session status and the
  # fleet screen — best effort, some runtimes mount it read-only.
  echo "tiny: $1" > /dev/termination-log 2>/dev/null || true
  exit 1
}
if [ "$AGENT" = "codex" ]; then
  # codex is a static musl binary — it runs in any linux image.
  if ! "$TINY_HOME/bin/codex" --version >/dev/null 2>&1; then
    die "codex cannot run in this image — the payload may be damaged."
  fi
elif ! "$TINY_HOME/bin/claude" --version >/dev/null 2>&1; then
  die "claude cannot run in this image — it needs glibc (alpine/musl images will not work). Use a glibc-based image (debian, ubuntu, fedora slim variants)."
fi
if ! command -v git >/dev/null 2>&1; then
  die "this image has no git — the agent needs it. Add git to the image or use the default one."
fi

# Seed claude's user config: onboarding done (no human at the theme picker),
# the workspace pre-trusted (no human at the trust dialog), and the tiny MCP
# server registered USER-SCOPE — this is the file MCP servers live in;
# settings.json below carries only hooks. Written only when absent so later
# runs keep whatever state claude accumulated.
if [ ! -f "$CLAUDE_CONFIG_DIR/.claude.json" ]; then
  cat > "$CLAUDE_CONFIG_DIR/.claude.json" <<'JSON'
{
  "hasCompletedOnboarding": true,
  "theme": "dark",
  "mcpServers": {
    "tiny": { "type": "http", "url": "http://127.0.0.1:8080/mcp" }
  },
  "projects": {
    "/workspace":      { "hasTrustDialogAccepted": true },
    "/workspace/repo": { "hasTrustDialogAccepted": true }
  }
}
JSON
fi

# The attention hooks — the safety net when the model asks in the shell
# instead of calling ask_human. Written every start so an image upgrade can
# evolve the wiring; user settings merge over.
cat > "$CLAUDE_CONFIG_DIR/settings.json" <<'JSON'
{
  "skipDangerousModePermissionPrompt": true,
  "autoUpdates": false,
  "permissions": {
    "allow": ["mcp__tiny__ask_human", "mcp__tiny__await_answer", "mcp__tiny__set_title", "mcp__tiny__session_list", "mcp__tiny__session_create", "mcp__tiny__expose_port", "mcp__tiny__enable_store"]
  },
  "hooks": {
    "Notification": [
      { "hooks": [ { "type": "command",
          "command": "TINY_NOTIFY_PLACEHOLDER 'The agent is waiting for your input.'" } ] }
    ],
    "Stop": [
      { "hooks": [ { "type": "command",
          "command": "TINY_NOTIFY_PLACEHOLDER --stop" } ] }
    ]
  }
}
JSON
# The hook path is only known at runtime (payload may live at /tiny or
# /opt/tiny) — patch it in.
sed -i "s|TINY_NOTIFY_PLACEHOLDER|$TINY_HOME/bin/tiny-notify|g" "$CLAUDE_CONFIG_DIR/settings.json"

# Codex keeps its world under CODEX_HOME on the workspace: config, auth,
# and the session rollouts that make resume-after-restart work.
if [ "$AGENT" = "codex" ]; then
  export CODEX_HOME="$WORKSPACE/.codex"
  mkdir -p "$CODEX_HOME"
  # Auth rides the agent-env secret: OPENAI_API_KEY is already in the
  # environment; a ChatGPT login arrives as the full auth.json. Written
  # only when absent — codex refreshes tokens in place and the workspace
  # copy is the live one.
  if [ -n "${TINY_CODEX_AUTH_JSON:-}" ] && [ ! -f "$CODEX_HOME/auth.json" ]; then
    printf '%s' "$TINY_CODEX_AUTH_JSON" > "$CODEX_HOME/auth.json"
    chmod 600 "$CODEX_HOME/auth.json"
  fi
  # A pod that died mid-thought leaves its rollout's writer lock behind on
  # the volume, and resume then fails with "already has an active writer".
  # One pod per session by design (Recreate strategy): any lock found at
  # start is stale by definition.
  rm -f "$CODEX_HOME"/thread-writer-locks/*.lock 2>/dev/null || true
  if [ ! -f "$CODEX_HOME/config.toml" ]; then
    cat > "$CODEX_HOME/config.toml" <<'TOML'
# The pod is the sandbox: kubernetes provides isolation, the tiny gate
# provides human approval. Codex's own sandbox (landlock) does not work
# in most container runtimes anyway.
approval_policy = "never"
sandbox_mode = "danger-full-access"

# ask_human blocks for hours BY DESIGN; codex's MCP client must not give
# up first.
[mcp_servers.tiny]
url = "http://127.0.0.1:8080/mcp"
tool_timeout_sec = 86400
startup_timeout_sec = 120

[projects."/workspace"]
trust_level = "trusted"

[projects."/workspace/repo"]
trust_level = "trusted"
TOML
  fi
fi

# Repo deploy key, when the cluster has one: kubernetes mounts secrets
# group-readable and ssh refuses keys like that, so copy with owner-only
# permissions into the HOME ssh looks at.
if [ -s /tiny-keys/id_ed25519 ]; then
  mkdir -p "$HOME/.ssh"
  cp /tiny-keys/id_ed25519 "$HOME/.ssh/id_ed25519"
  chmod 600 "$HOME/.ssh/id_ed25519"
  [ -s /tiny-keys/known_hosts ] && cp /tiny-keys/known_hosts "$HOME/.ssh/known_hosts"
  export GIT_SSH_COMMAND="ssh -i $HOME/.ssh/id_ed25519 -o UserKnownHostsFile=$HOME/.ssh/known_hosts -o IdentitiesOnly=yes"
  echo "tiny: repo deploy key installed"
fi

# The artifact store, when the namespace runs one: preconfigure mc so
# `mc cp file store/bucket/` just works.
if [ -n "${MINIO_ROOT_USER:-}" ] && [ -n "${TINY_STORE_ENDPOINT:-}" ]; then
  export MC_CONFIG_DIR="$WORKSPACE/.tiny/mc"
  "$TINY_HOME/bin/mc" --config-dir "$MC_CONFIG_DIR" alias set store \
    "$TINY_STORE_ENDPOINT" "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" >/dev/null 2>&1 \
    && echo "tiny: artifact store wired (mc alias: store)"
fi

# House rules travel as CLAUDE.md — written once so the user can evolve it.
if [ ! -f "$WORKSPACE/CLAUDE.md" ]; then
  cat > "$WORKSPACE/CLAUDE.md" <<'MD'
# You are a tiny session

You run as a Kubernetes pod with a persistent workspace; your operator
watches a fleet screen, not this terminal.

- Call `set_title` now, and again whenever the nature of your work changes —
  a short present-tense line ("migrating auth tests"). It is how the
  operator tells sessions apart.
- Before anything hard to undo, or when genuinely stuck, call `ask_human`
  and wait. The operator answers from their screen — it may take a while;
  that is normal, do not give up or invent an answer.
- Your pod may be replaced at any time; the workspace and transcript
  survive. Keep state in files, commit early.
- Need to BUILD a container image? Spawn a builder with `session_create`
  using image `quay.io/buildah/stable` AND `user: "1000"` — buildah's
  rootless machinery is wired to its build user; without the uid the
  build fails on missing subuid entries. When the `TINY_REGISTRY` env var is set, the namespace runs
  its own registry: push there (`buildah push --tls-verify=false <img>
  ${TINY_REGISTRY}/team/<name>:<tag>`) and spawn sessions from that
  reference — build, push, run, all inside the namespace.
- No `store` alias in `mc`? Call the `enable_store` tool — it provisions
  the namespace artifact store (through the approval gate) and returns
  the command that wires you up.
- If `mc` reports an alias named `store`, the namespace has a shared
  artifact store: hand files to other sessions with
  `mc cp <file> store/artifacts/` and `mc cp store/artifacts/<file> .`
  (create buckets with `mc mb store/<bucket>` as needed).
- When a task needs a toolchain this image lacks, do not improvise an
  install — spawn a specialist with `session_create`, passing the right
  `image` (and `cpu`/`memory` for heavy builds). Images must be
  glibc-based with git and /bin/sh — debian/ubuntu-family tags work,
  alpine/musl ones do not. The operator approves each spawn. Watch your
  children with `session_list`; they report through their titles.
MD
fi

# Codex reads AGENTS.md where claude reads CLAUDE.md — same rules, both
# names, so either agent (and a human editing one file) stays covered.
if [ ! -f "$WORKSPACE/AGENTS.md" ]; then
  cp "$WORKSPACE/CLAUDE.md" "$WORKSPACE/AGENTS.md" 2>/dev/null || true
fi

# First run: bring the repo in, if there is one.
if [ ! -f "$MARKER" ] && [ -n "${TINY_REPO:-}" ]; then
  echo "tiny: cloning $TINY_REPO"
  git clone --depth 1 "$TINY_REPO" "$WORKSPACE/repo" || echo "tiny: clone failed — starting on an empty workspace"
fi
cd "$WORKSPACE/repo" 2>/dev/null || cd "$WORKSPACE"

# The agent runs inside tmux so a human can attach mid-thought and detach
# without stopping anything. tmux execs its argument directly (no shell), so
# the command lives in a script — one argument, shell semantics preserved.
RUN="$WORKSPACE/.tiny/run.sh"
if [ -f "$MARKER" ]; then
  echo "tiny: workspace has history — resuming"
  RESUMING=1
  if [ "$AGENT" = "codex" ]; then
    cat > "$RUN" <<'RUNEOF'
#!/bin/sh
# resume --last reopens the newest rollout and then WAITS for input; the
# entrypoint nudges through tmux once the TUI is up. Approvals and sandbox
# are settled in config.toml, model rides TINY_MODEL.
codex resume --last ${TINY_MODEL:+-c model="$TINY_MODEL"}
echo
echo "tiny: agent exited — session stays for inspection"
while :; do sleep 3600; done
RUNEOF
  else
    cat > "$RUN" <<'RUNEOF'
#!/bin/sh
# --continue alone reopens the transcript and then WAITS for input — a
# restarted pod would sit idle forever. The nudge turns resumption back into
# motion. But a nudge whose turn never RAN (usage-limit park, rapid pod
# cycles) must not stack another on top: the pending marker survives on the
# workspace and is cleared by the Stop hook when a turn truly completes.
if [ -f /workspace/.tiny/nudge-pending ]; then
  claude ${TINY_MODEL:+--model "$TINY_MODEL"} --continue --permission-mode bypassPermissions
else
  touch /workspace/.tiny/nudge-pending
  claude ${TINY_MODEL:+--model "$TINY_MODEL"} --continue --permission-mode bypassPermissions \
    "The infrastructure restarted your session. Pick up your task where the transcript leaves off; if it was already complete, say so and stop."
fi
echo
echo "tiny: agent exited — session stays for inspection"
while :; do sleep 3600; done
RUNEOF
  fi
else
  date -u +%FT%TZ > "$MARKER"
  echo "tiny: fresh session — starting task"
  if [ "$AGENT" = "codex" ]; then
    cat > "$RUN" <<'RUNEOF'
#!/bin/sh
codex ${TINY_MODEL:+-m "$TINY_MODEL"} "${TINY_TASK:-No task was given. Call set_title with 'waiting for instructions' now, then wait for the human here. Output no greeting - just wait.}"
echo
echo "tiny: agent exited — session stays for inspection"
while :; do sleep 3600; done
RUNEOF
  else
    cat > "$RUN" <<'RUNEOF'
#!/bin/sh
claude ${TINY_MODEL:+--model "$TINY_MODEL"} --permission-mode bypassPermissions "${TINY_TASK:-No task was given. Call set_title with 'waiting for instructions' now, then wait for the human here. Output no greeting - just wait.}"
echo
echo "tiny: agent exited — session stays for inspection"
while :; do sleep 3600; done
RUNEOF
  fi
fi
chmod +x "$RUN"

# tmux tuned for a modern terminal: 256-color base everyone's terminfo has,
# truecolor override, mouse scroll, no status bar (claude has its own), and
# room to scroll back through a long night.
cat > "$WORKSPACE/.tiny/tmux.conf" <<'TMUXEOF'
set -g default-terminal "screen-256color"
set -ga terminal-overrides ",*:Tc"
# Mouse mode is OFF on purpose: claude v2 handles mouse selection itself,
# and a second interceptor in the middle produced fighting modes and
# accidental copy-mode freezes ("the UI broke"). With tmux hands-off,
# claude talks to the terminal like it does locally. set-clipboard still
# lets claude's copies reach the system clipboard (OSC52), and ctrl-q [
# remains for deliberate scrollback.
set -g set-clipboard on
set -g history-limit 50000
set -g focus-events on
setw -g aggressive-resize on
# ctrl-q is tiny's prefix: one-handed on every keyboard, no readline
# conflicts (ctrl-a is line-start inside claude's input). ctrl-b stays as
# a second prefix for tmux muscle memory.
set -g prefix C-q
set -g prefix2 C-b
# Hold ctrl, tap the prefix twice: toggle claude <-> shell.
bind C-q last-window
bind C-b last-window
# One quiet line: the keys nobody should have to memorize, and a PREFIX
# lamp so pressing ctrl-b visibly arms the chord.
set -g status on
set -g status-position bottom
set -g status-style "bg=#20281f,fg=colour108"
set -g status-left "#[bg=colour22,fg=colour84,bold] ◇ tiny #[default]  "
set -g status-left-length 12
set -g status-interval 15
set -g status-right "#{?client_prefix,#[bg=colour179#,fg=colour232] PREFIX #[default] ,}#[fg=colour84]#(cat /workspace/.tiny/usage.txt 2>/dev/null)#[fg=colour108] · TINY_BUILD_PLACEHOLDER · #{?#{e|>:#{session_windows},1},win #I/#{session_windows} · ctrl-q ctrl-q switch · ,}ctrl-q d detach · ctrl-q c shell  "
set -g status-right-length 110
set -g status-right-length 80
set -g window-status-format ""
set -g window-status-current-format ""
TMUXEOF
sed -i "s|TINY_BUILD_PLACEHOLDER|${TINY_AGENT_BUILD:-dev}|" "$WORKSPACE/.tiny/tmux.conf"

# Detached: a pod has no TTY. A human attaches later with
#   kubectl exec -it <pod> -c agent -- tmux -u attach -t main
tmux -f "$WORKSPACE/.tiny/tmux.conf" new-session -d -s main "$RUN" || { echo "tiny: tmux failed to start"; exit 1; }

# Codex's resume waits silently for input; type the nudge once the TUI is
# up. (Claude's branch nudges itself via --continue's prompt argument.)
if [ "$AGENT" = "codex" ] && [ "${RESUMING:-}" = "1" ]; then
  ( sleep 12
    tmux send-keys -t main -l -- "The infrastructure restarted your session. Pick up your task where the transcript leaves off; if it was already complete, say so and stop."
    tmux send-keys -t main Enter
    # The TUI may still be replaying the transcript when the first Enter
    # lands and the text stays in the composer; a second Enter after it
    # settles submits it. Harmless when the first one already did.
    sleep 6
    tmux send-keys -t main Enter ) &
fi

# Self-reported usage + the one state only the pane knows: the usage-limit
# banner ("continuing automatically at ..."). Reported on transitions so
# the fleet screen can show a paused session as paused.
( LIMIT_LAST=""
  while :; do
    "$TINY_HOME/bin/tiny-notify" --usage 2>/dev/null
    # Claude and codex word their limit banners differently; match both.
    LIMIT_NOW=$(tmux capture-pane -t main -p 2>/dev/null | grep -io "usage limit reached[^]*\|hit your usage limit[^]*" | tail -1)
    if [ "$LIMIT_NOW" != "$LIMIT_LAST" ]; then
      "$TINY_HOME/bin/tiny-notify" --limit "$LIMIT_NOW" 2>/dev/null && LIMIT_LAST="$LIMIT_NOW"
    fi
    sleep 30
  done ) &

# The mailbox: undelivered spec.inbox messages land in the prompt within
# seconds; a replacement pod replays whatever never landed.
( while :; do "$TINY_HOME/bin/tiny-notify" --inbox >>/workspace/.tiny/notify.log 2>&1; sleep 5; done ) &

# PID 1 lives as long as the tmux session does.
while tmux has-session -t main 2>/dev/null; do sleep 5; done
