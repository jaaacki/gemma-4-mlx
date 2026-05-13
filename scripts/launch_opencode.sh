#!/usr/bin/env bash
# launch_opencode.sh — boot opencode against the local engine, anchored to a project dir.
#
# Usage:
#   scripts/launch_opencode.sh                                 # profile=qwen36, project=$PWD
#   scripts/launch_opencode.sh gemma4                          # different profile
#   scripts/launch_opencode.sh -C /path/to/project             # explicit project root
#   scripts/launch_opencode.sh qwen36 -C ~/Dev/myapp           # both
#   scripts/launch_opencode.sh qwen36 -- run "task..."         # args after `--` go to opencode
#   PROFILE=gemma4 PROJECT=~/Dev/myapp scripts/launch_opencode.sh
#
# What it does:
#   1. Ensures the engine is running with the requested profile (boots if down).
#   2. Syncs opencode's stored default model to the booted profile (atomic JSON write
#      against ~/.config/opencode/opencode.json) so requests don't 404.
#   3. CDs into the project directory, prints where, and exec's opencode.

set -euo pipefail

ROOT="$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --show-toplevel)"

PROFILE="${PROFILE:-qwen36}"
PROJECT="${PROJECT:-$PWD}"
# Per-request output cap for opencode. Big enough that thinking-enabled models
# (Qwen 3.6) can think AND answer in the same response. Override via env var.
OUTPUT_TOKENS="${OUTPUT_TOKENS:-32768}"
OPENCODE_ARGS=()

# Arg parsing: -C DIR / --profile NAME / -- pass-through. Bare first positional = profile.
saw_positional=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    -C|--project)
      [[ -n "${2:-}" ]] || { echo "launch_opencode: $1 requires a directory" >&2; exit 2; }
      PROJECT="$2"; shift 2 ;;
    --profile)
      [[ -n "${2:-}" ]] || { echo "launch_opencode: --profile requires a name" >&2; exit 2; }
      PROFILE="$2"; shift 2 ;;
    -h|--help)
      sed -n '1,/^set -e/p' "$0" | grep '^#' | sed 's/^# \{0,1\}//'
      exit 0 ;;
    --)
      shift; OPENCODE_ARGS=("$@"); break ;;
    -*)
      echo "launch_opencode: unknown flag $1 (use -- to pass flags through to opencode)" >&2
      exit 2 ;;
    *)
      if [[ $saw_positional -eq 0 ]]; then
        PROFILE="$1"; saw_positional=1; shift
      else
        echo "launch_opencode: unexpected positional $1 (put extra args after --)" >&2
        exit 2
      fi ;;
  esac
done

if [[ ! -d "$PROJECT" ]]; then
  echo "launch_opencode: project directory does not exist: $PROJECT" >&2
  exit 2
fi
PROJECT="$(cd "$PROJECT" && pwd)"

if ! command -v opencode >/dev/null 2>&1; then
  echo "launch_opencode: opencode not found on PATH" >&2
  echo "install:  npm install -g opencode-ai" >&2
  exit 127
fi

"$ROOT/scripts/ensure_engine.sh" "$PROFILE"

# Sync opencode's default model to the profile we just booted.
OPENCODE_CONFIG="${OPENCODE_CONFIG:-$HOME/.config/opencode/opencode.json}"
if [[ -f "$OPENCODE_CONFIG" ]]; then
  python3 - "$ROOT/deploy/profiles/${PROFILE}.toml" "$OPENCODE_CONFIG" "$OUTPUT_TOKENS" <<'PY'
import json, os, sys, tempfile, tomllib

profile_path, opencode_path, output_tokens_str = sys.argv[1], sys.argv[2], sys.argv[3]
with open(profile_path, "rb") as f:
    profile = tomllib.load(f)
model_id = profile["model"]["id"]
display_name = profile["model"].get("display_name", model_id)
context_len = int(profile["server"]["max_model_len"])
output_tokens = int(output_tokens_str)
target = f"vllm-local/{model_id}"

with open(opencode_path) as f:
    cfg = json.load(f)

provider = cfg.setdefault("provider", {}).setdefault("vllm-local", {})
provider.setdefault("npm", "@ai-sdk/openai-compatible")
provider.setdefault("name", "Local vLLM (Metal)")
provider.setdefault("options", {"baseURL": "http://127.0.0.1:8000/v1"})
models = provider.setdefault("models", {})

# Ensure model entry exists and carries a sensible limit. limit.context follows
# the profile's max_model_len; limit.output is the per-request cap that needs
# to be roomy enough for thinking-mode models (Qwen 3.6) to think AND answer.
entry = models.setdefault(model_id, {"name": display_name})
entry.setdefault("name", display_name)
limit = entry.setdefault("limit", {})
limit["context"] = context_len
limit["output"] = output_tokens

changed = cfg.get("model") != target  # at minimum the default-model field
cfg["model"] = target

d = os.path.dirname(opencode_path) or "."
with tempfile.NamedTemporaryFile("w", dir=d, delete=False, suffix=".tmp") as tf:
    json.dump(cfg, tf, indent=2)
    tf.write("\n")
    tmp = tf.name
os.replace(tmp, opencode_path)
print(
    f"launch_opencode: synced opencode config "
    f"(model={target}, limit.context={context_len}, limit.output={output_tokens})",
    file=sys.stderr,
)
PY
fi

cd "$PROJECT"
echo "launch_opencode: project=$PWD profile=$PROFILE" >&2
if [[ ${#OPENCODE_ARGS[@]} -eq 0 ]]; then
  exec opencode
else
  exec opencode "${OPENCODE_ARGS[@]}"
fi
