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

# Sync opencode's default model to the profile we just booted. Without this,
# opencode's stored default may point at a model that isn't currently served,
# and every request returns "model does not exist".
OPENCODE_CONFIG="${OPENCODE_CONFIG:-$HOME/.config/opencode/opencode.json}"
if [[ -f "$OPENCODE_CONFIG" ]]; then
  python3 - "$ROOT/deploy/profiles/${PROFILE}.toml" "$OPENCODE_CONFIG" <<'PY'
import json, os, sys, tempfile, tomllib

profile_path, opencode_path = sys.argv[1], sys.argv[2]
with open(profile_path, "rb") as f:
    profile = tomllib.load(f)
model_id = profile["model"]["id"]
display_name = profile["model"].get("display_name", model_id)
target = f"vllm-local/{model_id}"

with open(opencode_path) as f:
    cfg = json.load(f)

# Ensure provider entry exists; harmless if already set.
provider = cfg.setdefault("provider", {}).setdefault("vllm-local", {})
provider.setdefault("npm", "@ai-sdk/openai-compatible")
provider.setdefault("name", "Local vLLM (Metal)")
provider.setdefault("options", {"baseURL": "http://127.0.0.1:8000/v1"})
models = provider.setdefault("models", {})
if model_id not in models:
    models[model_id] = {"name": display_name}

if cfg.get("model") == target and model_id in models:
    sys.exit(0)

cfg["model"] = target

# Atomic write so a Ctrl-C never leaves a half-written config.
d = os.path.dirname(opencode_path) or "."
with tempfile.NamedTemporaryFile("w", dir=d, delete=False, suffix=".tmp") as tf:
    json.dump(cfg, tf, indent=2)
    tf.write("\n")
    tmp = tf.name
os.replace(tmp, opencode_path)
print(f"launch_opencode: opencode default model set to {target}", file=sys.stderr)
PY
fi

echo "launch_opencode: starting opencode (profile=$PROFILE)" >&2
if [[ ${#OPENCODE_ARGS[@]} -eq 0 ]]; then
  exec opencode
else
  exec opencode "${OPENCODE_ARGS[@]}"
fi
