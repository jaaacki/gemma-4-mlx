#!/usr/bin/env bash
set -euo pipefail

ROOT="$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --show-toplevel)"
VENV="$ROOT/.venv-vllm-metal"
STATE_DIR="$ROOT/state"
PID_FILE="$STATE_DIR/vllm-metal.pid"
LOG_FILE="$STATE_DIR/vllm-metal.log"
MODEL="${1:-${MODEL:-Qwen/Qwen3-0.6B}}"
HOST="${HOST:-127.0.0.1}"
PORT="${PORT:-8000}"
MAX_MODEL_LEN="${MAX_MODEL_LEN:-8192}"

if [[ ! -d "$VENV" ]]; then
  echo "Missing $VENV. Install vLLM Metal first." >&2
  exit 1
fi

mkdir -p "$STATE_DIR"

if [[ -f "$PID_FILE" ]]; then
  PID="$(cat "$PID_FILE")"
  if kill -0 "$PID" 2>/dev/null; then
    echo "vLLM Metal already running: pid=$PID log=$LOG_FILE" >&2
    exit 1
  fi
  rm -f "$PID_FILE"
fi

source "$VENV/bin/activate"

ARGS=(
  serve "$MODEL"
  --host "$HOST"
  --port "$PORT"
  --max-model-len "$MAX_MODEL_LEN"
)

if [[ -n "${DTYPE:-}" ]]; then
  ARGS+=(--dtype "$DTYPE")
fi

if [[ -n "${SPECULATIVE_CONFIG:-}" ]]; then
  ARGS+=(--speculative-config "$SPECULATIVE_CONFIG")
fi

if [[ -n "${EXTRA_VLLM_ARGS:-}" ]]; then
  read -r -a EXTRA_ARGS <<< "$EXTRA_VLLM_ARGS"
  ARGS+=("${EXTRA_ARGS[@]}")
fi

{
  NOW="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
  echo "[$NOW] model=$MODEL host=$HOST port=$PORT max_model_len=$MAX_MODEL_LEN"
  echo "[$NOW] VLLM_METAL_USE_PAGED_ATTENTION=${VLLM_METAL_USE_PAGED_ATTENTION:-unset}"
  echo "[$NOW] command: vllm ${ARGS[*]}"
} >> "$LOG_FILE"

nohup vllm "${ARGS[@]}" >> "$LOG_FILE" 2>&1 &
echo $! > "$PID_FILE"

echo "Started vLLM Metal pid=$(cat "$PID_FILE")"
echo "Log: $LOG_FILE"
echo "Models: http://$HOST:$PORT/v1/models"
