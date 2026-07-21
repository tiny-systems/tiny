#!/usr/bin/env bash
# Orchestrates the two-pane demo for VHS: left pane = tiny serving + its tool
# log, right pane = the agent (Claude Code) prompting. Both panes launch at once
# and run on the same timeline, so the GIF shows them moving together — one
# window prompting, the other streaming the tools it drives.
#
# Run standalone to preview:  bash demo/run.sh   (Ctrl-b then x, or wait, to end)
set -e
cd "$(dirname "$0")"

tmux kill-session -t tinydemo 2>/dev/null || true
tmux new-session  -d -s tinydemo -x 210 -y 30 "bash mock-tiny.sh"
tmux set-option   -t tinydemo status off           # no green status bar in the GIF
tmux split-window -h -t tinydemo                "bash mock-claude.sh"
tmux select-pane  -t tinydemo:0.0
tmux attach       -t tinydemo
