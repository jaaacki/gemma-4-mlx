#!/usr/bin/env bash
# launch_bench.sh — run the bench harness against the local engine.
#
# Usage:
#   scripts/launch_bench.sh                                 # profile=qwen36, default bench args
#   scripts/launch_bench.sh gemma4
#   scripts/launch_bench.sh qwen36 -- --requests 50 --concurrency 8
#
# Prerequisites:
#   - uv installed (brew install uv).
#
# Behavior:
#   1. Ensures the engine is running with the requested profile.
#   2. Reads model ID from the profile TOML.
#   3. Runs `uv sync` if .venv is missing.
#   4. Exec `uv run python -m bench.harness` with sensible defaults; any args
#      after `--` are forwarded and override the defaults.

set -euo pipefail

ROOT="$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --show-toplevel)"

PROFILE="${PROFILE:-qwen36}"
EXTRA_ARGS=()
if [[ $# -gt 0 && "$1" != "--" ]]; then
  PROFILE="$1"
  shift
fi
if [[ "${1:-}" == "--" ]]; then
  shift
  EXTRA_ARGS=("$@")
fi

if ! command -v uv >/dev/null 2>&1; then
  echo "launch_bench: uv not found on PATH" >&2
  echo "install:  brew install uv" >&2
  exit 127
fi

"$ROOT/scripts/ensure_engine.sh" "$PROFILE"

PROFILE_TOML="$ROOT/deploy/profiles/${PROFILE}.toml"
MODEL_ID=$(python3 - "$PROFILE_TOML" <<'PY'
import sys, tomllib
with open(sys.argv[1], "rb") as f:
    data = tomllib.load(f)
print(data["model"]["id"])
PY
)

# Ensure deps are synced.
cd "$ROOT"
if [[ ! -d ".venv" ]]; then
  echo "launch_bench: running 'uv sync' (one-time)..." >&2
  uv sync >&2
fi

# Default arg set; user can override anything via `-- ...`.
DEFAULT_ARGS=(
  --model "$MODEL_ID"
  --stream
  --requests 10
  --warmup 2
  --concurrency 4
  --max-tokens 128
)

echo "launch_bench: model=$MODEL_ID" >&2
exec uv run python -m bench.harness "${DEFAULT_ARGS[@]}" "${EXTRA_ARGS[@]}"
