#!/usr/bin/env bash
# launch_aider.sh — boot aider against the local engine.
#
# Usage:
#   scripts/launch_aider.sh                                 # profile=qwen36 (default)
#   scripts/launch_aider.sh gemma4
#   scripts/launch_aider.sh qwen36 -- src/main.py           # args after `--` go to aider
#
# Prerequisites:
#   - aider installed (`python -m pip install aider-install && aider-install`)
#
# Behavior:
#   1. Ensures the engine is running with the requested profile.
#   2. Reads the active model ID from the profile TOML.
#   3. Sets OPENAI_API_BASE/OPENAI_API_KEY so aider talks to the engine.
#   4. Exec aider with the right model and a stable edit format (`whole` is
#      most reliable with smaller open models; they emit malformed diffs
#      under the default udiff format).

set -euo pipefail

ROOT="$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --show-toplevel)"

PROFILE="${PROFILE:-qwen36}"
AIDER_ARGS=()
if [[ $# -gt 0 && "$1" != "--" ]]; then
  PROFILE="$1"
  shift
fi
if [[ "${1:-}" == "--" ]]; then
  shift
  AIDER_ARGS=("$@")
fi

if ! command -v aider >/dev/null 2>&1; then
  echo "launch_aider: aider not found on PATH" >&2
  echo "install:  python -m pip install aider-install && aider-install" >&2
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

export OPENAI_API_BASE="http://127.0.0.1:8000/v1"
export OPENAI_API_KEY="local"

echo "launch_aider: model=openai/$MODEL_ID  edit-format=whole" >&2
exec aider \
  --model "openai/$MODEL_ID" \
  --edit-format whole \
  "${AIDER_ARGS[@]}"
