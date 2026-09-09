#!/bin/bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ACTION="${1:-list}"
shift || true

case "$ACTION" in
  upload)
    exec "$SCRIPT_DIR/offsite-compose.sh" run --rm --no-deps offsite once
    ;;
  list)
    exec "$SCRIPT_DIR/offsite-compose.sh" run --rm --no-deps offsite list
    ;;
  verify)
    exec "$SCRIPT_DIR/offsite-compose.sh" run --rm --no-deps offsite verify "${1:-latest}"
    ;;
  fetch)
    [ "$#" -ge 2 ] || { echo "用法: ./deploy/offsite.sh fetch <远端.tar.age文件名> <本地目录>" >&2; exit 1; }
    package_name="$1"
    destination="$2"
    case "$package_name" in *.tar.age) ;; *) echo "远端文件必须以.tar.age结尾" >&2; exit 1 ;; esac
    case "$package_name" in */*|*..*) echo "远端文件名非法" >&2; exit 1 ;; esac
    mkdir -p "$destination"
    package_path="$destination/$package_name"
    checksum_path="$package_path.sha256"
    [ ! -e "$package_path" ] && [ ! -e "$checksum_path" ] || { echo "目标文件已存在，拒绝覆盖" >&2; exit 1; }
    cleanup_download() {
      rm -f "$package_path.partial" "$checksum_path.partial" "$package_path" "$checksum_path"
    }
    trap cleanup_download EXIT INT TERM
    "$SCRIPT_DIR/offsite-compose.sh" run -T --rm --no-deps offsite fetch "$package_name" package > "$package_path.partial"
    mv "$package_path.partial" "$package_path"
    "$SCRIPT_DIR/offsite-compose.sh" run -T --rm --no-deps offsite fetch "$package_name" checksum > "$checksum_path.partial"
    mv "$checksum_path.partial" "$checksum_path"
    [ -s "$package_path" ] && [ -s "$checksum_path" ] || { echo "远端下载结果为空" >&2; exit 1; }
    checksum_hash="$(awk 'NR==1 {print $1}' "$checksum_path")"
    checksum_name="$(awk 'NR==1 {print $2}' "$checksum_path")"
    checksum_lines="$(wc -l < "$checksum_path" | tr -d ' ')"
    case "$checksum_hash" in ''|*[!0-9a-f]*) echo "远端校验文件SHA格式错误" >&2; exit 1 ;; esac
    [ "${#checksum_hash}" -eq 64 ] && [ "$checksum_name" = "$package_name" ] && [ "$checksum_lines" -eq 1 ] || { echo "远端校验文件内容不匹配" >&2; exit 1; }
    if command -v sha256sum >/dev/null 2>&1; then
      (cd "$destination" && sha256sum -c "$package_name.sha256" >/dev/null)
    else
      (cd "$destination" && shasum -a 256 -c "$package_name.sha256" >/dev/null)
    fi
    trap - EXIT INT TERM
    echo "远端加密包已下载并校验: $package_path"
    ;;
  *)
    echo "用法: ./deploy/offsite.sh {upload|list|verify [receipt]|fetch <远端.tar.age> <目录>}" >&2
    exit 1
    ;;
esac
