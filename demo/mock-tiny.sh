#!/usr/bin/env bash
# Mock of the `tiny` CLI for the README demo GIF — no cluster required. Prints
# the same shapes the real CLI does (banner, connect hint, · tool stream), on a
# timeline that lines up with mock-claude.sh so the two tmux panes tell one
# story: the agent (right) prompts while tiny (left) streams the tool calls it
# makes. Keep this in sync with the real serve banner in cmd/ when it changes.
set -u

IND=$'\e[38;2;99;102;241m'  # indigo — keys
GRN=$'\e[38;2;16;185;129m'  # green  — ok
GRY=$'\e[38;2;107;114;128m' # grey   — subtle
YLW=$'\e[38;2;245;158;11m'  # amber  — lightning
B=$'\e[1m'
R=$'\e[0m'

# p LINE SECONDS — print a line, then pause (drives the streaming feel).
p() { printf '%b\n' "$1"; sleep "$2"; }

p "${GRY}\$ ${R}tiny up" 0.6
p "  ${GRN}·${R} CRDs · broker · operator · core modules  ${GRY}ready${R}" 0.7
p "" 0.2
p "${GRY}\$ ${R}tiny" 0.4
p "" 0.1
p "  ${IND}· ${B}tiny${R}  ${GRY}self-hosted AI agents on your own Kubernetes${R}" 0.3
p "" 0.1
p "  ${IND}context${R} minikube   ${IND}namespace${R} tinysystems" 0.12
p "  ${IND}project${R} playground" 0.12
p "  ${IND}serving${R} http://localhost:7776/mcp" 0.12
p "  ${IND}editor${R} http://localhost:7775   ${GRY}-> open in your browser${R}" 0.4
p "" 0.1
p "  ${GRY}Connect Claude Code (one-time):${R}" 0.15
p "    ${GRY}claude mcp add --transport http tiny http://localhost:7776/mcp${R}" 0.5
p "" 0.1
p "  ${GRY}Ctrl-C to stop · tool calls stream below.${R}" 4.4
# The tool log streams as the agent works in the other pane.
p "  ${YLW}·${R} list_modules ${GRY}11ms${R}" 0.9
p "  ${YLW}·${R} get_component_info ${GRY}88ms${R}" 1.0
p "  ${YLW}·${R} create_flow ${GRY}120ms${R}" 1.2
p "  ${YLW}·${R} build_flow ${GRY}1.2s${R}" 1.4
p "  ${YLW}·${R} send_signal ${GRY}64ms${R}" 1.0
p "  ${YLW}·${R} get_traces ${GRY}9ms${R}" 0.9
p "  ${IND}tunnel${R} localhost:43157 -> ${GRY}tinysystems-http-module-v0${R}" 12
