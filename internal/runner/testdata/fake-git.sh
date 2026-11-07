#!/bin/sh
# Stand-in for git (and chezmoi) behind runner.Program. It prints one
# "arg:<x>" line per argument, then "env:<k>=<v>" for the variables the
# runner sets or scrubs (sorted), then "tty=yes" when /dev/tty can be opened
# (the child has a controlling terminal) and "tty=none" otherwise.
# A `config` invocation (the core.sshCommand probe) prints nothing: it runs
# $FAKE_REAL_GIT with the same argv when that is set, and otherwise exits
# ${FAKE_CONFIG_EXIT:-1} (1 = key unset).
for a in "$@"; do
	if [ "$a" = config ]; then
		[ -n "$FAKE_REAL_GIT" ] && exec "$FAKE_REAL_GIT" "$@"
		exit "${FAKE_CONFIG_EXIT:-1}"
	fi
done
for a in "$@"; do echo "arg:$a"; done
env | grep -E '^(GIT_|SSH_|LC_ALL=|NO_COLOR=)' | sort | sed 's/^/env:/'
if (exec 3</dev/tty) 2>/dev/null; then echo tty=yes; else echo tty=none; fi
