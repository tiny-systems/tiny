#!/usr/bin/env bash
# Mock of the `tiny` CLI for the README demo GIF — no cluster required. Prints
# the same shapes the real CLI does, on a timeline that lines up with
# mock-claude.sh so the two tmux panes tell one story: the agent (right) prompts
# while tiny (left) streams the tool calls it makes.
set -u

IND=$'\e[38;2;99;102;241m'  # indigo — keys
GRN=$'\e[38;2;16;185;129m'  # green  — ok
GRY=$'\e[38;2;107;114;128m' # grey   — subtle
B=$'\e[1m'
R=$'\e[0m'

# p LINE SECONDS — print a line, then pause (drives the streaming feel).
p() { printf '%b\n' "$1"; sleep "$2"; }

p "${GRY}\$ ${R}tiny up" 0.6
p "  ${GRN}·${R} CRDs · broker · operator · core modules  ${GRY}ready${R}" 0.7
p "" 0.2
p "${GRY}\$ ${R}tiny" 0.5
p "" 0.1
p "  ${IND}· ${B}tiny${R}  ${GRY}self-hosted AI agents on your own Kubernetes${R}" 0.3
p "  ${IND}context${R} minikube   ${IND}namespace${R} tinysystems" 0.15
p "  ${IND}project${R} playground" 0.15
p "  ${IND}serving${R} http://localhost:7776/mcp" 0.15
p "  ${IND}editor${R}  http://localhost:7775  ${GRY}-> open in your browser${R}" 0.5
p "  ${GRY}Ctrl-C to stop · tool calls stream below.${R}" 4.8
# The tool log streams as the agent works in the other pane.
p "  ${IND}·${R} list_modules        ${GRY}11ms${R}" 0.9
p "  ${IND}·${R} get_component_info   ${GRY}88ms${R}" 1.1
p "  ${IND}·${R} create_flow         ${GRY}120ms${R}" 1.5
p "  ${IND}·${R} build_flow          ${GRY}1.2s${R}" 1.6
p "  ${IND}·${R} set_dashboard        ${GRY}40ms${R}" 0.9
p "  ${IND}tunnel${R} localhost:43157 -> ${GRY}tinysystems-http-module-v0${R}" 5
