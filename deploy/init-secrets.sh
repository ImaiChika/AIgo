#!/bin/bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SECRET_DIR="$SCRIPT_DIR/secrets"
umask 077
mkdir -p "$SECRET_DIR"

command -v openssl >/dev/null || { echo "缺少 openssl，无法生成安全随机 Secret" >&2; exit 1; }

create_secret() {
  local path="$1"
  local bytes="$2"
  if [ -e "$path" ]; then
    echo "保留已有 Secret: $path"
    return
  fi
  openssl rand -base64 "$bytes" | tr -d '\n' > "$path"
  chmod 600 "$path"
  echo "已生成 Secret: $path"
}

create_secret "$SECRET_DIR/postgres_password" 48
create_secret "$SECRET_DIR/jwt_secret" 64
create_secret "$SECRET_DIR/admin_password" 24

echo "Secret 已生成且不会被 Git 跟踪。请将其纳入客户侧安全备份。"
