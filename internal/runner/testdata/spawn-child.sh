#!/bin/sh
# Stand-in for an agentsync write that spawns a helper (git, npm): the child
# ignores SIGINT, so only a signal to the whole process group can stop it.
# Appends "child <pid>" and "ready" to $TRAP_LOG; the parent exits on SIGINT.
trap 'exit 130' INT
sh -c 'trap "" INT; while :; do sleep 0.05; done' &
echo "child $!" >> "$TRAP_LOG"
echo "self $$" >> "$TRAP_LOG"
echo ready >> "$TRAP_LOG"
wait
