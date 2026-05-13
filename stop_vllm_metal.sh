#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PID_FILE="$ROOT/logs/vllm-metal.pid"

if [[ ! -f "$PID_FILE" ]]; then
  echo "vLLM Metal is not tracked as running."
  exit 0
fi

PID="$(cat "$PID_FILE")"
if kill -0 "$PID" 2>/dev/null; then
  kill "$PID"
  echo "Stopped vLLM Metal pid=$PID"
else
  echo "Tracked pid=$PID is not running."
fi

rm -f "$PID_FILE"
