#!/usr/bin/env bash
# launch_opencode.sh — boot opencode against the local engine.
#
# Usage:
#   scripts/launch_opencode.sh                              # profile=qwen36 (default)
#   scripts/launch_opencode.sh gemma4                       # different profile
#   PROFILE=qwen36 scripts/launch_opencode.sh
#   scripts/launch_opencode.sh qwen36 -- run "do a task"    # args after `--` go to opencode
#
# Prerequisites:
#   - opencode installed (npm install -g opencode-ai)
#   - ~/.config/opencode/opencode.json has the vllm-local provider entry
#
# Behavior:
#   1. Ensures the engine is running with the requested profile (boots if down).
#   2. Verifies opencode is on PATH.
#   3. Exec opencode (any args after `--` are forwarded).

set -euo pipefail

ROOT="$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --show-toplevel)"

# Parse args: optional first positional = profile name; everything after `--` = opencode args.
PROFILE="${PROFILE:-qwen36}"
OPENCODE_ARGS=()
if [[ $# -gt 0 && "$1" != "--" ]]; then
  PROFILE="$1"
  shift
fi
if [[ "${1:-}" == "--" ]]; then
  shift
  OPENCODE_ARGS=("$@")
fi

if ! command -v opencode >/dev/null 2>&1; then
  echo "launch_opencode: opencode not found on PATH" >&2
  echo "install:  npm install -g opencode-ai" >&2
  exit 127
fi

"$ROOT/scripts/ensure_engine.sh" "$PROFILE"

echo "launch_opencode: starting opencode (profile=$PROFILE)" >&2
if [[ ${#OPENCODE_ARGS[@]} -eq 0 ]]; then
  exec opencode
else
  exec opencode "${OPENCODE_ARGS[@]}"
fi
