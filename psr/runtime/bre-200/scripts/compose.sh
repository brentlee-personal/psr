#!/usr/bin/env bash
set -euo pipefail
ROOT="/Users/libin/repos/worktrees/ragflow-psr-bre-200"
ENV_PSR="$ROOT/psr/runtime/bre-200/env.psr"
OVR="$ROOT/psr/runtime/bre-200/docker-compose.override.yml"
export COMPOSE_PROJECT_NAME=ragflow-psr-m05
cd "$ROOT/docker"
exec docker compose --env-file .env --env-file "$ENV_PSR" -f docker-compose.yml -f "$OVR" "$@"
