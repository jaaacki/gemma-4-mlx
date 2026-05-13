#!/usr/bin/env bash
set -euo pipefail

if docker compose version >/dev/null 2>&1; then
  COMPOSE=(docker compose)
elif command -v docker-compose >/dev/null 2>&1; then
  COMPOSE=(docker-compose)
else
  echo "Docker Compose is not available." >&2
  exit 1
fi

HOST_PORT="${HOST_PORT:-18080}" "${COMPOSE[@]}" up --build -d
