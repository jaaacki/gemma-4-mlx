#!/usr/bin/env bash
# launch_aider.sh — boot aider against the local engine, anchored to a project dir.
#
# Usage:
#   scripts/launch_aider.sh                                    # profile=qwen36, project=$PWD
#   scripts/launch_aider.sh gemma4
#   scripts/launch_aider.sh -C /path/to/project
#   scripts/launch_aider.sh qwen36 -C ~/Dev/myapp src/main.py  # files after profile/flags = aider args
#   scripts/launch_aider.sh qwen36 -- src/main.py              # explicit `--` separator also works
#   PROFILE=gemma4 PROJECT=~/Dev/myapp scripts/launch_aider.sh
#
# What it does:
#   1. Ensures the engine is running with the requested profile.
#   2. Reads model ID from the profile TOML.
#   3. Sets OPENAI_API_BASE/OPENAI_API_KEY so aider talks to the engine.
#   4. CDs into the project directory, prints where, and exec's aider with
#      --edit-format whole (most reliable with smaller open models).

set -euo pipefail

ROOT="$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --show-toplevel)"

PROFILE="${PROFILE:-qwen36}"
PROJECT="${PROJECT:-$PWD}"
AIDER_ARGS=()

saw_positional=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    -C|--project)
      [[ -n "${2:-}" ]] || { echo "launch_aider: $1 requires a directory" >&2; exit 2; }
      PROJECT="$2"; shift 2 ;;
    --profile)
      [[ -n "${2:-}" ]] || { echo "launch_aider: --profile requires a name" >&2; exit 2; }
      PROFILE="$2"; shift 2 ;;
    -h|--help)
      sed -n '1,/^set -e/p' "$0" | grep '^#' | sed 's/^# \{0,1\}//'
      exit 0 ;;
    --)
      shift; AIDER_ARGS=("$@"); break ;;
    -*)
      # Treat as an aider passthrough flag; the rest of argv goes to aider.
      AIDER_ARGS=("$@"); break ;;
    *)
      if [[ $saw_positional -eq 0 ]]; then
        PROFILE="$1"; saw_positional=1; shift
      else
        # Remaining positionals are aider's (file arguments).
        AIDER_ARGS=("$@"); break
      fi ;;
  esac
done

if [[ ! -d "$PROJECT" ]]; then
  echo "launch_aider: project directory does not exist: $PROJECT" >&2
  exit 2
fi
PROJECT="$(cd "$PROJECT" && pwd)"

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

cd "$PROJECT"
echo "launch_aider: project=$PWD profile=$PROFILE model=openai/$MODEL_ID edit-format=whole" >&2
if [[ ${#AIDER_ARGS[@]} -eq 0 ]]; then
  exec aider --model "openai/$MODEL_ID" --edit-format whole
else
  exec aider --model "openai/$MODEL_ID" --edit-format whole "${AIDER_ARGS[@]}"
fi
