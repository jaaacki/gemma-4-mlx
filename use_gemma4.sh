#!/usr/bin/env bash
# Switch vllm to Gemma 4 26B-A4B 4-bit MLX with full coding-agent config.
# Usage: ./use_gemma4.sh
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
"$ROOT/stop_vllm_metal.sh" 2>/dev/null || true
sleep 2
EXTRA_VLLM_ARGS='--enable-prefix-caching --enable-auto-tool-choice --tool-call-parser gemma4 --reasoning-parser gemma4' \
MAX_MODEL_LEN=131072 \
exec "$ROOT/start_vllm_metal.sh" mlx-community/gemma-4-26b-a4b-it-4bit
