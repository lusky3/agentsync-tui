#!/bin/sh
# Fake agentsync: argv to stdout, marker to stderr. FAKE_EXIT / FAKE_SLEEP tune behaviour.
if [ "$1" = "version" ] && [ -n "$FAKE_VERSION_FILE" ]; then cat "$FAKE_VERSION_FILE"; exit 0; fi
echo "$@"
echo "fake-stderr-marker" >&2
[ -n "$FAKE_SLEEP" ] && exec sleep "$FAKE_SLEEP"
exit "${FAKE_EXIT:-0}"
