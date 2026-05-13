#!/usr/bin/env bash
# launch_claude_code.sh — boot Claude Code against the local engine.
#
# Usage:
#   scripts/launch_claude_code.sh                              # profile=qwen36, project=$PWD
#   scripts/launch_claude_code.sh gemma4
#   scripts/launch_claude_code.sh -C /path/to/project
#   scripts/launch_claude_code.sh qwen36 -C ~/Dev/myapp
#   scripts/launch_claude_code.sh qwen36 -- "do a task"        # args after `--` go to claude
#   PROFILE=qwen36 PROJECT=~/Dev/myapp scripts/launch_claude_code.sh
#
# Prerequisites:
#   - Claude Code installed (`npm install -g @anthropic-ai/claude-code`)
#   - The active profile must declare `served_model_name` aliases that include
#     whatever Anthropic-style names you'll route through ANTHROPIC_DEFAULT_*_MODEL
#     (already set up for qwen36 in deploy/profiles/qwen36.toml).
#
# What it does:
#   1. Ensures the engine is running with the requested profile.
#   2. Reads the FIRST served_model_name alias from the profile TOML.
#      That becomes the canonical short name Claude Code routes to.
#   3. Exports ANTHROPIC_BASE_URL/ANTHROPIC_AUTH_TOKEN/ANTHROPIC_DEFAULT_*_MODEL
#      so Claude Code talks to the local engine instead of api.anthropic.com.
#   4. CDs into the project directory and exec's `claude`.
#
# This uses vLLM 0.20.x's NATIVE /v1/messages endpoint — no LiteLLM proxy.

set -euo pipefail

ROOT="$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --show-toplevel)"

PROFILE="${PROFILE:-qwen36}"
PROJECT="${PROJECT:-$PWD}"
CLAUDE_ARGS=()

saw_positional=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    -C|--project)
      [[ -n "${2:-}" ]] || { echo "launch_claude_code: $1 requires a directory" >&2; exit 2; }
      PROJECT="$2"; shift 2 ;;
    --profile)
      [[ -n "${2:-}" ]] || { echo "launch_claude_code: --profile requires a name" >&2; exit 2; }
      PROFILE="$2"; shift 2 ;;
    -h|--help)
      sed -n '1,/^set -e/p' "$0" | grep '^#' | sed 's/^# \{0,1\}//'
      exit 0 ;;
    --)
      shift; CLAUDE_ARGS=("$@"); break ;;
    -*)
      # forward unknown flags to claude
      CLAUDE_ARGS=("$@"); break ;;
    *)
      if [[ $saw_positional -eq 0 ]]; then
        PROFILE="$1"; saw_positional=1; shift
      else
        CLAUDE_ARGS=("$@"); break
      fi ;;
  esac
done

if [[ ! -d "$PROJECT" ]]; then
  echo "launch_claude_code: project directory does not exist: $PROJECT" >&2
  exit 2
fi
PROJECT="$(cd "$PROJECT" && pwd)"

if ! command -v claude >/dev/null 2>&1; then
  echo "launch_claude_code: claude (Claude Code) not found on PATH" >&2
  echo "install:  npm install -g @anthropic-ai/claude-code" >&2
  exit 127
fi

"$ROOT/scripts/ensure_engine.sh" "$PROFILE"

# The profile's first served_model_name alias is the canonical short name we
# route through ANTHROPIC_DEFAULT_*_MODEL. If the profile doesn't declare any
# aliases, fall back to the raw model id (likely to break Claude Code because
# of the slash, but better to surface that explicitly than guess).
PROFILE_TOML="$ROOT/deploy/profiles/${PROFILE}.toml"
MODEL_ALIAS=$(python3 - "$PROFILE_TOML" <<'PY'
import sys, tomllib
with open(sys.argv[1], "rb") as f:
    data = tomllib.load(f)
aliases = data.get("server", {}).get("served_model_name") or []
if aliases:
    print(aliases[0])
else:
    print(data["model"]["id"])
PY
)

export ANTHROPIC_BASE_URL="http://127.0.0.1:8000"
export ANTHROPIC_AUTH_TOKEN="${ANTHROPIC_AUTH_TOKEN:-local-no-auth}"
export ANTHROPIC_API_KEY="${ANTHROPIC_API_KEY:-$ANTHROPIC_AUTH_TOKEN}"
export ANTHROPIC_DEFAULT_OPUS_MODEL="${ANTHROPIC_DEFAULT_OPUS_MODEL:-$MODEL_ALIAS}"
export ANTHROPIC_DEFAULT_SONNET_MODEL="${ANTHROPIC_DEFAULT_SONNET_MODEL:-$MODEL_ALIAS}"
export ANTHROPIC_DEFAULT_HAIKU_MODEL="${ANTHROPIC_DEFAULT_HAIKU_MODEL:-$MODEL_ALIAS}"

cd "$PROJECT"
echo "launch_claude_code: project=$PWD profile=$PROFILE model=$MODEL_ALIAS endpoint=$ANTHROPIC_BASE_URL" >&2
if [[ ${#CLAUDE_ARGS[@]} -eq 0 ]]; then
  exec claude
else
  exec claude "${CLAUDE_ARGS[@]}"
fi
