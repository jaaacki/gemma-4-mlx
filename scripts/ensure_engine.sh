#!/usr/bin/env bash
# ensure_engine.sh — make sure the named profile's engine is running and HTTP-ready.
#
# Usage:
#   scripts/ensure_engine.sh [PROFILE]              # default: qwen36
#   PROFILE=gemma4 scripts/ensure_engine.sh
#
# Behavior:
#   1. Builds bin/forge if missing.
#   2. If the engine is down: boots PROFILE.
#   3. If the engine is up with a different model than PROFILE expects: refuses
#      with a hint to run `forge swap`. (Auto-swap would surprise a running session.)
#   4. Polls /v1/models until it responds (max 3 minutes). Exits 0 on ready,
#      non-zero on timeout or error.
#
# Sourced helpers: none. Safe to call standalone or from another launch script.

set -euo pipefail

ROOT="$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --show-toplevel)"
PROFILE="${1:-${PROFILE:-qwen36}}"
PROFILE_TOML="$ROOT/deploy/profiles/${PROFILE}.toml"

if [[ ! -f "$PROFILE_TOML" ]]; then
  echo "ensure_engine: no such profile '$PROFILE'" >&2
  echo "available profiles:" >&2
  for p in "$ROOT"/deploy/profiles/*.toml; do
    [[ -f "$p" ]] || continue
    name=$(basename "$p" .toml)
    [[ "$name" == ".gitkeep" ]] && continue
    echo "  $name" >&2
  done
  exit 2
fi

# Build forge if missing.
if [[ ! -x "$ROOT/bin/forge" ]]; then
  echo "ensure_engine: bin/forge missing; running 'make build'..." >&2
  (cd "$ROOT" && make build >&2)
fi

# Extract expected model id from the profile TOML.
# Schema: top-level [model] section, key `id = "..."`. Stable since Phase 2.
EXPECTED_MODEL=$(python3 - "$PROFILE_TOML" <<'PY'
import sys, tomllib
with open(sys.argv[1], "rb") as f:
    data = tomllib.load(f)
print(data["model"]["id"])
PY
)

# What's currently loaded (if anything)?
STATUS_JSON=$("$ROOT/bin/forge" status --json 2>/dev/null || echo '{}')
RUNNING=$(printf '%s' "$STATUS_JSON" | python3 -c "import sys,json; print(json.load(sys.stdin).get('running', False))")
CURRENT_MODEL=$(printf '%s' "$STATUS_JSON" | python3 -c "import sys,json; print(json.load(sys.stdin).get('model', ''))")

if [[ "$RUNNING" == "True" ]]; then
  if [[ "$CURRENT_MODEL" != "$EXPECTED_MODEL" ]]; then
    echo "ensure_engine: engine is running with '$CURRENT_MODEL' but you asked for profile '$PROFILE' ($EXPECTED_MODEL)" >&2
    echo "to swap:  ./bin/forge swap $PROFILE" >&2
    echo "to stop:  ./bin/forge stop" >&2
    exit 1
  fi
  echo "ensure_engine: engine already running with $EXPECTED_MODEL" >&2
else
  echo "ensure_engine: booting profile $PROFILE ..." >&2
  "$ROOT/bin/forge" boot "$PROFILE" >&2
fi

# Poll /v1/models until HTTP-ready.
echo "ensure_engine: waiting for /v1/models (up to 3 min)..." >&2
START_TS=$(date +%s)
DEADLINE=$((START_TS + 180))
while true; do
  if curl -fsS --max-time 2 http://127.0.0.1:8000/v1/models >/dev/null 2>&1; then
    elapsed=$(( $(date +%s) - START_TS ))
    echo "ensure_engine: ready (${elapsed}s)" >&2
    exit 0
  fi
  PID=$(cat "$ROOT/state/vllm-metal.pid" 2>/dev/null || true)
  if [[ -z "$PID" ]] || ! kill -0 "$PID" 2>/dev/null; then
    echo "ensure_engine: engine process not alive; last log lines:" >&2
    tail -20 "$ROOT/state/vllm-metal.log" >&2 2>/dev/null || true
    exit 1
  fi
  now=$(date +%s)
  if [[ "$now" -ge "$DEADLINE" ]]; then
    echo "ensure_engine: timeout waiting for HTTP readiness" >&2
    exit 1
  fi
  sleep 5
done
