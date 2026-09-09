#!/bin/bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
ENV_FILE="${AIGO_DEPLOY_ENV_FILE:-$SCRIPT_DIR/production.env}"

if [ ! -r "$ENV_FILE" ]; then
  echo "部署配置不存在或不可读: $ENV_FILE" >&2
  echo "请先复制 deploy/production.env.example 并完成配置。" >&2
  exit 1
fi

exec docker compose \
  --project-directory "$PROJECT_DIR" \
  --env-file "$ENV_FILE" \
  -f "$PROJECT_DIR/compose.production.yaml" \
  "$@"
