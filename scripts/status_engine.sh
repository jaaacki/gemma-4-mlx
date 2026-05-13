#!/usr/bin/env bash
set -euo pipefail

ROOT="$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --show-toplevel)"
PID_FILE="$ROOT/state/vllm-metal.pid"
LOG_FILE="$ROOT/state/vllm-metal.log"
HOST="${HOST:-127.0.0.1}"
PORT="${PORT:-8000}"

if [[ -f "$PID_FILE" ]] && kill -0 "$(cat "$PID_FILE")" 2>/dev/null; then
  echo "running pid=$(cat "$PID_FILE")"
else
  echo "not running"
fi

if curl -fsS "http://$HOST:$PORT/v1/models" >/tmp/vllm-metal-models.json 2>/dev/null; then
  python3 -m json.tool /tmp/vllm-metal-models.json
else
  echo "API not responding at http://$HOST:$PORT"
fi

if [[ -f "$LOG_FILE" ]]; then
  echo "log=$LOG_FILE"
fi
