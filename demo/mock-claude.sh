#!/usr/bin/env bash
# Mock of a Claude Code session for the README demo GIF — the right pane. The
# left pane (tiny) prints the one-time `claude mcp add` hint on boot; this pane
# starts a session already connected, then prompts. It calls tiny (whose ⚡ tool
# log you watch stream on the left) and reports done. Timed with mock-tiny.sh.
set -u

IND=$'\e[38;2;99;102;241m'
GRN=$'\e[38;2;16;185;129m'
GRY=$'\e[38;2;107;114;128m'
B=$'\e[1m'
R=$'\e[0m'

p() { printf '%b\n' "$1"; sleep "$2"; }

# Wait for tiny to finish booting (left pane) before we prompt.
sleep 3.9
p "${GRY}\$ ${R}claude" 0.9
p "  ${GRY}· tiny (mcp) connected${R}" 0.8
p "  ${IND}>${R} build an HTTP endpoint that echoes the JSON" 0.4
p "    I POST, and starts on a Signal" 1.1
p "" 0.4
p "  ${IND}·${R} I'll wire ${B}Signal -> HTTP Server -> Modify${R}," 0.4
p "    then start it and verify the run." 0.6
p "  ${GRY}  ... calling tiny (6 tools)${R}" 5.6
p "  ${IND}·${R} Flow ${B}\"echo-json\"${R} built ${GRY}(all edges green, first try)${R}" 0.7
p "  ${IND}·${R} fired · trace clean · localhost:43157 is live.  ${GRN}done${R}" 12
