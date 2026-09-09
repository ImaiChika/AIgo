#!/bin/bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ACTION="${1:-list}"
shift || true

case "$ACTION" in
  create)
    exec "$SCRIPT_DIR/compose.sh" run --rm --no-deps backup backup
    ;;
  list)
    exec "$SCRIPT_DIR/compose.sh" run --rm --no-deps backup list
    ;;
  verify)
    exec "$SCRIPT_DIR/compose.sh" run --rm --no-deps backup verify "${1:-latest}"
    ;;
  drill)
    exec "$SCRIPT_DIR/compose.sh" run --rm --no-deps backup drill "${1:-latest}" "${2:-aigo_restore_drill}"
    ;;
  restore)
    [ "$#" -ge 2 ] || { echo "用法: ./deploy/backup.sh restore <备份文件名> <新数据库名>" >&2; exit 1; }
    exec "$SCRIPT_DIR/compose.sh" run --rm --no-deps backup restore "$1" "$2"
    ;;
  export)
    [ "$#" -ge 1 ] || { echo "用法: ./deploy/backup.sh export <宿主机目录>" >&2; exit 1; }
    destination="$1"
    mkdir -p "$destination"
    "$SCRIPT_DIR/compose.sh" cp backup:/backups/. "$destination"
    echo "备份已导出到: $destination"
    ;;
  import)
    [ "$#" -ge 2 ] || { echo "用法: ./deploy/backup.sh import <解密目录> <备份.dump文件名>" >&2; exit 1; }
    source_dir="$(cd "$1" && pwd)"
    backup_name="$2"
    case "$backup_name" in *.dump) ;; *) echo "备份文件名必须以.dump结尾" >&2; exit 1 ;; esac
    case "$backup_name" in */*|*..*) echo "备份文件名非法" >&2; exit 1 ;; esac
    for suffix in "" .sha256 .meta; do
      [ -f "$source_dir/$backup_name$suffix" ] || { echo "缺少恢复文件: $backup_name$suffix" >&2; exit 1; }
    done
    "$SCRIPT_DIR/compose.sh" run --rm --no-deps \
      -e IMPORT_NAME="$backup_name" -v "$source_dir:/import:ro" \
      --entrypoint /bin/sh backup -c '
        set -eu
        umask 077
        cd /import
        sha256sum -c "$IMPORT_NAME.sha256" >/dev/null
        for suffix in "" .sha256 .meta; do
          [ ! -e "/backups/$IMPORT_NAME$suffix" ] || { echo "备份卷已存在同名文件" >&2; exit 1; }
        done
        cleanup() { rm -f /backups/.import-dump.partial /backups/.import-sha.partial /backups/.import-meta.partial; }
        trap cleanup EXIT INT TERM
        cp "$IMPORT_NAME" /backups/.import-dump.partial
        cp "$IMPORT_NAME.sha256" /backups/.import-sha.partial
        cp "$IMPORT_NAME.meta" /backups/.import-meta.partial
        chmod 600 /backups/.import-dump.partial /backups/.import-sha.partial /backups/.import-meta.partial
        mv /backups/.import-dump.partial "/backups/$IMPORT_NAME"
        mv /backups/.import-sha.partial "/backups/$IMPORT_NAME.sha256"
        mv /backups/.import-meta.partial "/backups/$IMPORT_NAME.meta"
        trap - EXIT INT TERM
      '
    echo "解密备份已导入本地备份卷: $backup_name"
    ;;
  *)
    echo "用法: ./deploy/backup.sh {create|list|verify [文件]|drill [文件] [临时库]|restore <文件> <新库>|export <目录>|import <解密目录> <文件.dump>}" >&2
    exit 1
    ;;
esac
