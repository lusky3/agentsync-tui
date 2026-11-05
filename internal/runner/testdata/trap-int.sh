#!/bin/sh
# Stand-in for an agentsync write that handles SIGINT. It appends "ready" to
# $TRAP_LOG once its trap is armed and "INT" for every SIGINT it receives.
# TRAP_MODE=exit: exit 130 on SIGINT (finish or roll back, then stop).
# TRAP_MODE=ignore: note SIGINT and keep running, so only SIGKILL stops it.
trap 'echo INT >> "$TRAP_LOG"; [ "$TRAP_MODE" = exit ] && exit 130' INT
echo ready >> "$TRAP_LOG"
while :; do sleep 0.05; done
