#!/bin/sh

set -eu
umask 077

BACKUP_DIR="${AIGO_BACKUP_DIR:-/backups}"
PGHOST="${AIGO_DB_HOST:-postgres}"
PGPORT="${AIGO_DB_PORT:-5432}"
PGUSER="${AIGO_DB_USER:-aigo}"
PGDATABASE="${AIGO_DB_NAME:-aigo}"
PASSWORD_FILE="${AIGO_DB_PASSWORD_FILE:-/run/secrets/postgres_password}"
RETENTION_DAYS="${AIGO_BACKUP_RETENTION_DAYS:-14}"
MAX_BACKUPS="${AIGO_BACKUP_MAX_COUNT:-30}"
INTERVAL_SECONDS="${AIGO_BACKUP_INTERVAL_SECONDS:-86400}"
LOCK_TIMEOUT_SECONDS="${AIGO_BACKUP_LOCK_TIMEOUT_SECONDS:-30}"
APPLICATION_VERSION="${AIGO_APPLICATION_VERSION:-unknown}"
FINGERPRINT_TABLES="questions question_versions review_tasks review_records question_banks users roles knowledge_points audit_logs"

fail() {
  echo "backup error: $*" >&2
  exit 1
}

require_uint() {
  case "$2" in
    ''|*[!0-9]*) fail "$1 必须是非负整数" ;;
  esac
}

require_positive_uint() {
  require_uint "$1" "$2"
  [ "$2" -gt 0 ] || fail "$1 必须大于 0"
}

load_password() {
  [ -r "$PASSWORD_FILE" ] || fail "数据库 Secret 不可读: $PASSWORD_FILE"
  PGPASSWORD="$(tr -d '\r\n' < "$PASSWORD_FILE")"
  [ -n "$PGPASSWORD" ] || fail "数据库 Secret 为空"
  export PGPASSWORD PGHOST PGPORT PGUSER PGDATABASE
}

validate_settings() {
  require_uint AIGO_BACKUP_RETENTION_DAYS "$RETENTION_DAYS"
  require_positive_uint AIGO_BACKUP_MAX_COUNT "$MAX_BACKUPS"
  require_positive_uint AIGO_BACKUP_INTERVAL_SECONDS "$INTERVAL_SECONDS"
  require_positive_uint AIGO_BACKUP_LOCK_TIMEOUT_SECONDS "$LOCK_TIMEOUT_SECONDS"
  mkdir -p "$BACKUP_DIR"
  [ -w "$BACKUP_DIR" ] || fail "备份目录不可写: $BACKUP_DIR"
  load_password
}

acquire_operation_lock() {
  exec 9> "$BACKUP_DIR/.operation.lock"
  lock_started="$(date -u +%s)"
  while ! flock -n 9; do
    now="$(date -u +%s)"
    if [ $((now - lock_started)) -ge "$LOCK_TIMEOUT_SECONDS" ]; then
      fail "等待备份操作锁超时"
    fi
    sleep 1
  done
}

release_operation_lock() {
  flock -u 9
  exec 9>&-
}

database_fingerprint() {
  database="$1"
  table="$2"
  psql -X -v ON_ERROR_STOP=1 -At -d "$database" -c \
    "SELECT COUNT(*)::text || ':' || COALESCE(md5(string_agg(md5(row_to_json(t)::text), '' ORDER BY id)), md5('')) FROM $table t"
}

capture_fingerprints() {
  database="$1"
  output="$2"
  : > "$output"
  for table in $FINGERPRINT_TABLES; do
    value="$(database_fingerprint "$database" "$table")"
    echo "${table}_fingerprint=$value" >> "$output"
  done
}

metadata_value() {
  key="$1"
  file="$2"
  sed -n "s/^${key}=//p" "$file" | head -n 1
}

prune_backup_file() {
  dump_file="$1"
  rm -f "$dump_file" "$dump_file.sha256" "$dump_file.meta"
}

apply_retention() {
  if [ "$RETENTION_DAYS" -gt 0 ]; then
    find "$BACKUP_DIR" -maxdepth 1 -type f -name '*.dump' -mtime "+$RETENTION_DAYS" -print | while IFS= read -r old_dump; do
      prune_backup_file "$old_dump"
    done
  fi

  index=0
  find "$BACKUP_DIR" -maxdepth 1 -type f -name '*.dump' -print | sort -r | while IFS= read -r old_dump; do
    index=$((index + 1))
    if [ "$index" -gt "$MAX_BACKUPS" ]; then
      prune_backup_file "$old_dump"
    fi
  done
}

backup_once() {
  validate_settings
  acquire_operation_lock
  timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
  schema_version="$(psql -X -v ON_ERROR_STOP=1 -At -c 'SELECT COALESCE(MAX(version),0) FROM schema_migrations')"
  safe_database="$(printf '%s' "$PGDATABASE" | tr -c 'A-Za-z0-9_.-' '_')"
  base_name="${safe_database}_${timestamp}_schema${schema_version}_p$$.dump"
  final_file="$BACKUP_DIR/$base_name"
  partial_file="$final_file.partial"
  before_file="$BACKUP_DIR/.fingerprints-before-$$"
  after_file="$BACKUP_DIR/.fingerprints-after-$$"
  checksum_partial="$final_file.sha256.partial"
  metadata_partial="$final_file.meta.partial"

  [ ! -e "$final_file" ] || fail "同名备份已存在: $final_file"
  cleanup_partial() {
    rm -f "$partial_file" "$before_file" "$after_file" "$checksum_partial" "$metadata_partial"
  }
  trap cleanup_partial EXIT
  trap 'cleanup_partial; exit 130' INT
  trap 'cleanup_partial; exit 143' TERM

  capture_fingerprints "$PGDATABASE" "$before_file"
  pg_dump --format=custom --compress=9 --no-owner --no-acl --file="$partial_file" "$PGDATABASE"
  pg_restore --list "$partial_file" >/dev/null
  capture_fingerprints "$PGDATABASE" "$after_file"

  checksum="$(sha256sum "$partial_file" | awk '{print $1}')"
  printf '%s  %s\n' "$checksum" "$base_name" > "$checksum_partial"
  postgres_version="$(psql -X -v ON_ERROR_STOP=1 -At -c 'SHOW server_version')"
  created_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  {
    echo "format=aigo-postgres-custom-v1"
    echo "created_at=$created_at"
    echo "database=$PGDATABASE"
    echo "schema_version=$schema_version"
    echo "application_version=$APPLICATION_VERSION"
    echo "postgres_version=$postgres_version"
    echo "sha256=$checksum"
    for table in $FINGERPRINT_TABLES; do
      before="$(metadata_value "${table}_fingerprint" "$before_file")"
      after="$(metadata_value "${table}_fingerprint" "$after_file")"
      if [ "$before" = "$after" ]; then
        echo "${table}_fingerprint=$before"
      else
        echo "${table}_fingerprint=unstable"
      fi
    done
  } > "$metadata_partial"

  mv "$partial_file" "$final_file"
  mv "$checksum_partial" "$final_file.sha256"
  mv "$metadata_partial" "$final_file.meta"
  date -u +%s > "$BACKUP_DIR/.last_success.partial"
  mv "$BACKUP_DIR/.last_success.partial" "$BACKUP_DIR/.last_success"
  rm -f "$before_file" "$after_file"
  trap - EXIT INT TERM

  apply_retention
  release_operation_lock
  echo "backup complete: $base_name"
}

resolve_backup() {
  requested="${1:-latest}"
  case "$requested" in
    latest|'')
      file="$(find "$BACKUP_DIR" -maxdepth 1 -type f -name '*.dump' -print | sort -r | head -n 1)"
      ;;
    */*|*..*) fail "备份参数只允许文件名或 latest" ;;
    *) file="$BACKUP_DIR/$requested" ;;
  esac
  [ -n "${file:-}" ] && [ -f "$file" ] || fail "未找到备份: $requested"
  printf '%s\n' "$file"
}

verify_backup() {
  file="$(resolve_backup "${1:-latest}")"
  [ -f "$file.sha256" ] || fail "缺少校验文件: $(basename "$file").sha256"
  [ -f "$file.meta" ] || fail "缺少元数据文件: $(basename "$file").meta"
  (cd "$BACKUP_DIR" && sha256sum -c "$(basename "$file").sha256" >/dev/null)
  pg_restore --list "$file" >/dev/null
  [ "$(metadata_value format "$file.meta")" = "aigo-postgres-custom-v1" ] || fail "备份元数据格式不受支持"
  echo "backup verified: $(basename "$file")"
}

restore_backup() {
  mode="$1"
  requested="${2:-latest}"
  target="${3:-aigo_restore_drill}"
  case "$target" in
    ''|*[!a-z0-9_]*) fail "目标数据库名只允许小写字母、数字和下划线" ;;
  esac
  [ "$target" != "$PGDATABASE" ] || fail "禁止覆盖当前业务数据库 $PGDATABASE；请恢复到新数据库后受控切换"

  file="$(resolve_backup "$requested")"
  verify_backup "$(basename "$file")"
  exists="$(psql -X -v ON_ERROR_STOP=1 -At -d postgres -c "SELECT 1 FROM pg_database WHERE datname='$target'")"
  [ -z "$exists" ] || fail "目标数据库已存在: $target"

  created=0
  cleanup_failed_restore() {
    if [ "$created" -eq 1 ]; then
      dropdb --if-exists "$target" >/dev/null 2>&1 || true
    fi
  }
  trap cleanup_failed_restore EXIT
  trap 'cleanup_failed_restore; exit 130' INT
  trap 'cleanup_failed_restore; exit 143' TERM
  createdb --template=template0 "$target"
  created=1
  pg_restore --exit-on-error --single-transaction --no-owner --no-acl --dbname="$target" "$file"
  psql -X -v ON_ERROR_STOP=1 -d "$target" -c 'ANALYZE' >/dev/null

  metadata="$file.meta"
  restored_schema="$(psql -X -v ON_ERROR_STOP=1 -At -d "$target" -c 'SELECT COALESCE(MAX(version),0) FROM schema_migrations')"
  expected_schema="$(metadata_value schema_version "$metadata")"
  [ "$restored_schema" = "$expected_schema" ] || fail "恢复后 Schema 版本不一致: $restored_schema != $expected_schema"
  for table in $FINGERPRINT_TABLES; do
    expected="$(metadata_value "${table}_fingerprint" "$metadata")"
    if [ -n "$expected" ] && [ "$expected" != unstable ]; then
      actual="$(database_fingerprint "$target" "$table")"
      [ "$actual" = "$expected" ] || fail "恢复后 $table 指纹不一致"
    fi
  done

  if [ "$mode" = drill ]; then
    dropdb "$target"
    created=0
    echo "restore drill passed and temporary database removed: $target"
  else
    created=0
    echo "restore complete: $(basename "$file") -> $target"
  fi
  trap - EXIT INT TERM
}

health_check() {
  require_positive_uint AIGO_BACKUP_INTERVAL_SECONDS "$INTERVAL_SECONDS"
  [ -r "$BACKUP_DIR/.last_success" ] || exit 1
  last="$(cat "$BACKUP_DIR/.last_success")"
  require_uint last_success "$last"
  now="$(date -u +%s)"
  max_age=$((INTERVAL_SECONDS * 2 + 3600))
  age=$((now - last))
  [ "$age" -ge 0 ] && [ "$age" -le "$max_age" ]
}

list_backups() {
  find "$BACKUP_DIR" -maxdepth 1 -type f -name '*.dump' -print | sort -r
}

command="${1:-daemon}"
shift || true
case "$command" in
  daemon)
    while :; do
      backup_once
      sleep "$INTERVAL_SECONDS"
    done
    ;;
  backup) backup_once ;;
  verify) validate_settings; acquire_operation_lock; verify_backup "${1:-latest}"; release_operation_lock ;;
  restore) validate_settings; acquire_operation_lock; restore_backup restore "${1:-latest}" "${2:-aigo_restored}"; release_operation_lock ;;
  drill) validate_settings; acquire_operation_lock; restore_backup drill "${1:-latest}" "${2:-aigo_restore_drill}"; release_operation_lock ;;
  list) list_backups ;;
  health) health_check ;;
  *) fail "未知命令: $command（支持 daemon/backup/verify/restore/drill/list/health）" ;;
esac
