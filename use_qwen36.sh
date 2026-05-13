#!/usr/bin/env bash
# Switch vllm to Qwen 3.6 35B-A3B 4-bit MLX with full coding-agent config.
# Usage: ./use_qwen36.sh
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
"$ROOT/stop_vllm_metal.sh" 2>/dev/null || true
sleep 2
EXTRA_VLLM_ARGS='--enable-prefix-caching --enable-auto-tool-choice --tool-call-parser qwen3_xml --reasoning-parser qwen3' \
MAX_MODEL_LEN=131072 \
exec "$ROOT/start_vllm_metal.sh" mlx-community/Qwen3.6-35B-A3B-4bit
