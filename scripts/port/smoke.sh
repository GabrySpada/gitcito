#!/usr/bin/env bash
# Native smoke run: launch the built app with a throwaway data dir, wait for "window ready", keep it up for a
# few seconds, check the log for errors, then kill the whole process group (app, WebKit helpers, Xvfb).
# `timeout` alone is not enough: it kills xvfb-run, and WebKit's helper processes outlive it.
# Usage: scripts/port/smoke.sh <binary> [seconds]
set -u
bin=${1:?binary}; hold=${2:-10}
data=$(mktemp -d); log=$(mktemp)
runner=()
if [ "$(uname)" = Linux ] && [ -z "${DISPLAY:-}${WAYLAND_DISPLAY:-}" ]; then runner=(xvfb-run -a); fi
GITCITO_DATA_DIR=$data GITCITO_KEYCHAIN_SUFFIX=smoke setsid "${runner[@]}" "$bin" >"$log" 2>&1 &
pid=$!
pgid=$(ps -o pgid= "$pid" | tr -d ' ')
status=1
for _ in $(seq 1 60); do
  if grep -q "window ready" "$log"; then status=0; break; fi
  kill -0 "$pid" 2>/dev/null || break
  sleep 0.5
done
if [ $status -eq 0 ]; then
  sleep "$hold"
  kill -0 "$pid" 2>/dev/null || { echo "smoke: app exited during the hold" >&2; status=1; }
fi
kill -TERM -- "-$pgid" 2>/dev/null; sleep 1; kill -KILL -- "-$pgid" 2>/dev/null
if grep -E "panic|FATAL| ERR " "$log" >&2; then status=1; fi
[ $status -eq 0 ] && echo "smoke: ok (window ready, alive for ${hold}s, no errors)" || { echo "smoke: FAILED" >&2; tail -20 "$log" >&2; }
rm -rf "$data" "$log"
exit $status
