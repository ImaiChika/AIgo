#!/bin/bash
# 前台运行正式构建，适合由 systemd、launchd、Docker 等进程管理器托管。

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
CURRENT_LINK="$SCRIPT_DIR/output/production"

if [ ! -d "$CURRENT_LINK" ]; then
  echo "正式构建不存在或不完整，请先执行 ./build-production.sh" >&2
  exit 1
fi
RELEASE_DIR="$(cd "$CURRENT_LINK" && pwd -P)"
BINARY="$RELEASE_DIR/aigo"

if [ ! -x "$BINARY" ] || [ ! -f "$RELEASE_DIR/web/index.html" ]; then
  echo "正式构建不存在或不完整，请先执行 ./build-production.sh" >&2
  exit 1
fi

# 正式运行默认不读取项目目录 .env，凭证应由进程环境/Secrets 注入；
# 如需 dotenv，显式设置 AIGO_ENV_FILE=/secure/path/production.env。
export AIGO_ENV_FILE="${AIGO_ENV_FILE:--}"
export AIGO_HTTP_ADDR="${AIGO_HTTP_ADDR:-127.0.0.1:8080}"
export AIGO_WEB_DIST_DIR="${AIGO_WEB_DIST_DIR:-$RELEASE_DIR/web}"
export AIGO_REGISTER_ENABLED="${AIGO_REGISTER_ENABLED:-1}"

if [ "$AIGO_ENV_FILE" != "-" ] && [ ! -r "$AIGO_ENV_FILE" ]; then
  echo "AIGO_ENV_FILE 不存在或不可读: $AIGO_ENV_FILE" >&2
  exit 1
fi

cd "$SCRIPT_DIR"
"$BINARY" migrate
exec "$BINARY" serve
