#!/bin/bash
# 构建不依赖 Go/Node 源码即可运行的 AIgo 发布目录。

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
OUTPUT_ROOT="$SCRIPT_DIR/output"
RELEASES_DIR="$OUTPUT_ROOT/releases"
CURRENT_LINK="$OUTPUT_ROOT/production"
mkdir -p "$OUTPUT_ROOT"
mkdir -p "$RELEASES_DIR"
STAGING_DIR="$(mktemp -d "$RELEASES_DIR/.production-build.XXXXXX")"
BUILD_ID="$(date -u +%Y%m%dT%H%M%SZ)-$$"
RELEASE_DIR="$RELEASES_DIR/$BUILD_ID"
TEMP_LINK="$OUTPUT_ROOT/.production-link-$$"

cleanup() {
  rm -rf "$STAGING_DIR"
  rm -f "$TEMP_LINK"
}
trap cleanup EXIT

command -v go >/dev/null || { echo "缺少 Go，无法构建后端" >&2; exit 1; }
command -v npm >/dev/null || { echo "缺少 npm，无法构建前端" >&2; exit 1; }

echo "[1/3] 按 package-lock.json 安装前端依赖并构建..."
cd "$SCRIPT_DIR/web"
npm ci --no-audit --no-fund
npm run build
test -f "$SCRIPT_DIR/web/dist/index.html"

echo "[2/3] 构建 Go 后端..."
cd "$SCRIPT_DIR"
go build -trimpath -o "$STAGING_DIR/aigo" ./cmd/aigo
mkdir -p "$STAGING_DIR/web"
cp -R "$SCRIPT_DIR/web/dist/." "$STAGING_DIR/web/"

echo "[3/3] 替换已完整构建的发布目录..."
mv "$STAGING_DIR" "$RELEASE_DIR"
if [ -e "$CURRENT_LINK" ] && [ ! -L "$CURRENT_LINK" ]; then
  mv "$CURRENT_LINK" "$RELEASES_DIR/pre-versioned-$BUILD_ID"
fi
ln -s "releases/$BUILD_ID" "$TEMP_LINK"
if mv -Tf "$TEMP_LINK" "$CURRENT_LINK" 2>/dev/null; then
  : # GNU/Linux：不跟随目标目录链接
else
  mv -fh "$TEMP_LINK" "$CURRENT_LINK" # macOS/BSD
fi
trap - EXIT

echo "构建完成: $RELEASE_DIR"
echo "当前版本: $CURRENT_LINK -> releases/$BUILD_ID"
echo "运行方式: ./run-production.sh"
