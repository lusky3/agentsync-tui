#!/bin/sh
# Stand-in for an agentsync write that spawns a helper (git, npm): the child
# ignores SIGINT, SIGTERM and SIGHUP, so only SIGKILL to the whole process
# group stops it; the parent exits on SIGINT (trap) and on SIGTERM/SIGHUP.
# Appends "child <pid>" and "ready" to $TRAP_LOG; the parent exits on SIGINT.
trap 'exit 130' INT
sh -c 'trap "" INT TERM HUP; while :; do sleep 0.05; done' &
echo "child $!" >> "$TRAP_LOG"
echo "self $$" >> "$TRAP_LOG"
echo ready >> "$TRAP_LOG"
wait
