#!/usr/bin/env bash
# Mock of a Claude Code session for the README demo GIF — the right pane. tiny
# auto-connects Claude Code when it starts (left pane), so this pane just waits
# for tiny to be up, then prompts. It calls tiny (whose tool log you watch stream
# on the left) and reports done. Timed to move with mock-tiny.sh.
set -u

IND=$'\e[38;2;99;102;241m'
GRN=$'\e[38;2;16;185;129m'
GRY=$'\e[38;2;107;114;128m'
B=$'\e[1m'
R=$'\e[0m'

p() { printf '%b\n' "$1"; sleep "$2"; }

# Wait for tiny to finish booting + auto-connect (left pane) before we prompt.
sleep 3.6
p "${GRY}\$ ${R}claude" 0.9
p "  ${GRY}· connected to tiny (mcp)${R}" 0.8
p "  ${IND}>${R} build an HTTP endpoint that echoes the JSON" 0.4
p "    I POST, and starts on a Signal" 1.1
p "" 0.4
p "  ${IND}·${R} I'll wire ${B}Signal -> HTTP Server -> Modify${R}," 0.4
p "    then start it." 0.6
p "  ${GRY}  ... calling tiny (5 tools)${R}" 5.4
p "  ${IND}·${R} Flow ${B}\"echo-json\"${R} built and running." 0.8
p "  ${IND}·${R} localhost:43157 is live.  ${GRN}done${R}" 3
