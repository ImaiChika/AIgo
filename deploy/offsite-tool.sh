#!/bin/sh

set -eu
set -o pipefail
umask 077

BACKUP_DIR="${AIGO_BACKUP_DIR:-/backups}"
STATE_DIR="${AIGO_OFFSITE_STATE_DIR:-/state}"
ACCESS_KEY_FILE="${AIGO_OFFSITE_ACCESS_KEY_FILE:-/run/secrets/offsite_access_key}"
SECRET_KEY_FILE="${AIGO_OFFSITE_SECRET_KEY_FILE:-/run/secrets/offsite_secret_key}"
RECIPIENT="${AIGO_OFFSITE_AGE_RECIPIENT:-}"
PROVIDER="${AIGO_OFFSITE_S3_PROVIDER:-Other}"
ENDPOINT="${AIGO_OFFSITE_S3_ENDPOINT:-}"
REGION="${AIGO_OFFSITE_S3_REGION:-us-east-1}"
BUCKET="${AIGO_OFFSITE_S3_BUCKET:-}"
PREFIX="${AIGO_OFFSITE_S3_PREFIX:-aigo}"
FORCE_PATH_STYLE="${AIGO_OFFSITE_S3_FORCE_PATH_STYLE:-true}"
INTERVAL_SECONDS="${AIGO_OFFSITE_INTERVAL_SECONDS:-300}"
LOCK_TIMEOUT_SECONDS="${AIGO_OFFSITE_LOCK_TIMEOUT_SECONDS:-30}"
CONNECT_TIMEOUT="${AIGO_OFFSITE_CONNECT_TIMEOUT:-5s}"
IO_TIMEOUT="${AIGO_OFFSITE_IO_TIMEOUT:-30s}"
HEALTH_GRACE_SECONDS="${AIGO_OFFSITE_HEALTH_GRACE_SECONDS:-900}"
ALLOW_INSECURE_ENDPOINT="${AIGO_OFFSITE_ALLOW_INSECURE_ENDPOINT:-0}"
REQUIRE_OBJECT_LOCK="${AIGO_OFFSITE_REQUIRE_OBJECT_LOCK:-1}"
OBJECT_LOCK_CONFIRMED="${AIGO_OFFSITE_BUCKET_OBJECT_LOCK_CONFIRMED:-0}"

fail() {
  echo "offsite error: $*" >&2
  exit 1
}

require_positive_uint() {
  case "$2" in
    ''|*[!0-9]*) fail "$1 必须是正整数" ;;
  esac
  [ "$2" -gt 0 ] || fail "$1 必须大于 0"
}

read_secret() {
  name="$1"
  file="$2"
  [ -r "$file" ] || fail "$name Secret 不可读: $file"
  value="$(tr -d '\r\n' < "$file")"
  [ -n "$value" ] || fail "$name Secret 为空"
  printf '%s' "$value"
}

validate_settings() {
  [ -d "$BACKUP_DIR" ] && [ -r "$BACKUP_DIR" ] || fail "本地备份目录不可读: $BACKUP_DIR"
  mkdir -p "$STATE_DIR/markers" "$STATE_DIR/spool"
  [ -w "$STATE_DIR" ] || fail "异机状态目录不可写: $STATE_DIR"
  require_positive_uint AIGO_OFFSITE_INTERVAL_SECONDS "$INTERVAL_SECONDS"
  require_positive_uint AIGO_OFFSITE_LOCK_TIMEOUT_SECONDS "$LOCK_TIMEOUT_SECONDS"
  require_positive_uint AIGO_OFFSITE_HEALTH_GRACE_SECONDS "$HEALTH_GRACE_SECONDS"
  [ -n "$RECIPIENT" ] || fail "必须配置 AIGO_OFFSITE_AGE_RECIPIENT 公钥"
  case "$RECIPIENT" in
    age1*|ssh-ed25519\ *|ssh-rsa\ *) ;;
    *) fail "AIGO_OFFSITE_AGE_RECIPIENT 不是受支持的 age/SSH 公钥" ;;
  esac
  [ -n "$BUCKET" ] || fail "必须配置 AIGO_OFFSITE_S3_BUCKET"
  case "$BUCKET" in */*|*..*) fail "S3 bucket 名称非法" ;; esac
  PREFIX="$(printf '%s' "$PREFIX" | sed 's#^/*##; s#/*$##')"
  case "$PREFIX" in *..*|*//* ) fail "S3 prefix 不能包含 .. 或连续斜杠" ;; esac
  if [ -n "$ENDPOINT" ]; then
    case "$ENDPOINT" in
      https://*) ;;
      http://*) [ "$ALLOW_INSECURE_ENDPOINT" = 1 ] || fail "非TLS S3端点仅允许隔离测试" ;;
      *) fail "S3 endpoint 必须使用 http:// 或 https://" ;;
    esac
  fi
  if [ "$REQUIRE_OBJECT_LOCK" = 1 ]; then
    [ "$OBJECT_LOCK_CONFIRMED" = 1 ] || fail "必须先在bucket启用默认Object Lock/版本控制，再设置 AIGO_OFFSITE_BUCKET_OBJECT_LOCK_CONFIRMED=1"
  fi

  access_key="$(read_secret access-key "$ACCESS_KEY_FILE")"
  secret_key="$(read_secret secret-key "$SECRET_KEY_FILE")"
  export RCLONE_CONFIG_AIGO_TYPE=s3
  export RCLONE_CONFIG_AIGO_PROVIDER="$PROVIDER"
  export RCLONE_CONFIG_AIGO_ENV_AUTH=false
  export RCLONE_CONFIG_AIGO_ACCESS_KEY_ID="$access_key"
  export RCLONE_CONFIG_AIGO_SECRET_ACCESS_KEY="$secret_key"
  export RCLONE_CONFIG_AIGO_REGION="$REGION"
  export RCLONE_CONFIG_AIGO_ENDPOINT="$ENDPOINT"
  export RCLONE_CONFIG_AIGO_ACL=private
  export RCLONE_CONFIG_AIGO_FORCE_PATH_STYLE="$FORCE_PATH_STYLE"
  export RCLONE_CONFIG_AIGO_NO_CHECK_BUCKET=true
  remote_bucket="aigo:${BUCKET}"
  remote_root="$remote_bucket"
  if [ -n "$PREFIX" ]; then
    remote_root="${remote_root}/${PREFIX}"
  fi
}

acquire_lock() {
  exec 9> "$STATE_DIR/.operation.lock"
  started="$(date -u +%s)"
  while ! flock -n 9; do
    now="$(date -u +%s)"
    if [ $((now - started)) -ge "$LOCK_TIMEOUT_SECONDS" ]; then
      fail "等待异机备份操作锁超时"
    fi
    sleep 1
  done
}

release_lock() {
  flock -u 9
  exec 9>&-
}

rclone_cmd() {
  rclone --config /dev/null --contimeout "$CONNECT_TIMEOUT" --timeout "$IO_TIMEOUT" --retries 2 --low-level-retries 2 "$@"
}

remote_file_exists() {
  object_name="$1"
  parent="$(dirname "$object_name")"
  base="$(basename "$object_name")"
  rclone_cmd lsf "$remote_root/$parent" --files-only 2>/dev/null | grep -Fqx "$base"
}

upload_immutable() {
  source_file="$1"
  object_name="$2"
  if remote_file_exists "$object_name"; then
    return 2
  fi
  rclone_cmd copyto "$source_file" "$remote_root/$object_name" --immutable
}

remote_sha256() {
  object_name="$1"
  rclone_cmd cat "$remote_root/$object_name" | sha256sum | awk '{print $1}'
}

metadata_value() {
  key="$1"
  file="$2"
  sed -n "s/^${key}=//p" "$file" | head -n 1
}

write_marker_from_receipt() {
  receipt_file="$1"
  dump_base="$2"
  marker="$STATE_DIR/markers/$dump_base.uploaded"
  cp "$receipt_file" "$marker.partial"
  mv "$marker.partial" "$marker"
}

verify_remote_receipt() {
  receipt_file="$1"
  expected_source_sha="$2"
  [ "$(metadata_value format "$receipt_file")" = "aigo-offsite-receipt-v1" ] || fail "远端receipt格式不受支持"
  [ "$(metadata_value source_sha256 "$receipt_file")" = "$expected_source_sha" ] || fail "远端同名备份对应的源SHA不一致"
  encrypted_object="$(metadata_value encrypted_object "$receipt_file")"
  expected_encrypted_sha="$(metadata_value encrypted_sha256 "$receipt_file")"
  [ -n "$encrypted_object" ] && [ -n "$expected_encrypted_sha" ] || fail "远端receipt不完整"
  actual_encrypted_sha="$(remote_sha256 "packages/$encrypted_object")"
  [ "$actual_encrypted_sha" = "$expected_encrypted_sha" ] || fail "远端加密包SHA-256不一致"
  remote_checksum="$(rclone_cmd cat "$remote_root/packages/$encrypted_object.sha256")"
  checksum_hash="$(printf '%s\n' "$remote_checksum" | awk '{print $1}')"
  checksum_name="$(printf '%s\n' "$remote_checksum" | awk '{print $2}')"
  [ "$checksum_hash" = "$expected_encrypted_sha" ] && [ "$checksum_name" = "$encrypted_object" ] || fail "远端加密包校验文件不一致"
}

upload_dump() {
  dump_file="$1"
  dump_base="$(basename "$dump_file")"
  [ -f "$dump_file.sha256" ] && [ -f "$dump_file.meta" ] || fail "本地备份三件套不完整: $dump_base"
  (cd "$BACKUP_DIR" && sha256sum -c "$dump_base.sha256" >/dev/null)
  source_sha="$(awk '{print $1}' "$dump_file.sha256")"
  marker="$STATE_DIR/markers/$dump_base.uploaded"
  if [ -f "$marker" ] && [ "$(metadata_value source_sha256 "$marker")" = "$source_sha" ]; then
    rm -f "$STATE_DIR/spool/${dump_base}.receipt" "$STATE_DIR/spool/${dump_base}.tar.age" "$STATE_DIR/spool/${dump_base}.tar.age.sha256"
    return
  fi

  receipt_name="${dump_base}.receipt"
  if remote_file_exists "receipts/$receipt_name"; then
    remote_receipt="$STATE_DIR/spool/$receipt_name.remote"
    rclone_cmd copyto "$remote_root/receipts/$receipt_name" "$remote_receipt"
    verify_remote_receipt "$remote_receipt" "$source_sha"
    write_marker_from_receipt "$remote_receipt" "$dump_base"
    rm -f "$remote_receipt"
    echo "offsite already verified: $dump_base"
    return
  fi

  package_name="${dump_base}.tar.age"
  package_file="$STATE_DIR/spool/$package_name"
  checksum_file="$package_file.sha256"
  receipt_file="$STATE_DIR/spool/$receipt_name"
  partial="$package_file.partial"
  cleanup_partial() {
    rm -f "$partial" "$checksum_file.partial" "$receipt_file.partial"
  }
  trap cleanup_partial EXIT
  trap 'cleanup_partial; exit 130' INT
  trap 'cleanup_partial; exit 143' TERM

  tar -C "$BACKUP_DIR" -cf - "$dump_base" "$dump_base.sha256" "$dump_base.meta" | age -r "$RECIPIENT" -o "$partial"
  mv "$partial" "$package_file"
  encrypted_sha="$(sha256sum "$package_file" | awk '{print $1}')"
  printf '%s  %s\n' "$encrypted_sha" "$package_name" > "$checksum_file.partial"
  mv "$checksum_file.partial" "$checksum_file"
  {
    echo "format=aigo-offsite-receipt-v1"
    echo "source_dump=$dump_base"
    echo "source_sha256=$source_sha"
    echo "encrypted_object=$package_name"
    echo "encrypted_sha256=$encrypted_sha"
    echo "recipient_sha256=$(printf '%s' "$RECIPIENT" | sha256sum | awk '{print $1}')"
    echo "bucket_object_lock_confirmed=$OBJECT_LOCK_CONFIRMED"
    echo "uploaded_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  } > "$receipt_file.partial"
  mv "$receipt_file.partial" "$receipt_file"

  upload_status=0
  upload_immutable "$package_file" "packages/$package_name" || upload_status=$?
  if [ "$upload_status" -eq 2 ]; then
    [ "$(remote_sha256 "packages/$package_name")" = "$encrypted_sha" ] || fail "远端已有同名加密包但内容不同"
  elif [ "$upload_status" -ne 0 ]; then
    fail "上传加密包失败: $package_name"
  fi
  upload_status=0
  upload_immutable "$checksum_file" "packages/$package_name.sha256" || upload_status=$?
  if [ "$upload_status" -ne 0 ] && [ "$upload_status" -ne 2 ]; then
    fail "上传校验文件失败: $package_name"
  fi
  [ "$(remote_sha256 "packages/$package_name")" = "$encrypted_sha" ] || fail "上传后回读SHA-256不一致"
  upload_status=0
  upload_immutable "$receipt_file" "receipts/$receipt_name" || upload_status=$?
  if [ "$upload_status" -ne 0 ] && [ "$upload_status" -ne 2 ]; then
    fail "上传receipt失败: $receipt_name"
  fi
  remote_receipt="$STATE_DIR/spool/$receipt_name.confirm"
  rclone_cmd copyto "$remote_root/receipts/$receipt_name" "$remote_receipt"
  verify_remote_receipt "$remote_receipt" "$source_sha"
  write_marker_from_receipt "$remote_receipt" "$dump_base"
  rm -f "$remote_receipt"
  rm -f "$package_file" "$checksum_file" "$receipt_file"
  trap - EXIT INT TERM
  echo "offsite upload complete: $package_name"
}

scan_once() {
  validate_settings
  acquire_lock
  # 先验证远端可访问；bucket必须由运维预先创建并配置Object Lock/版本控制。
  rclone_cmd lsf "$remote_bucket" --max-depth 1 >/dev/null
  find "$BACKUP_DIR" -maxdepth 1 -type f -name '*.dump' -print | sort | while IFS= read -r dump_file; do
    upload_dump "$dump_file"
  done
  date -u +%s > "$STATE_DIR/.last_success.partial"
  mv "$STATE_DIR/.last_success.partial" "$STATE_DIR/.last_success"
  release_lock
}

verify_remote() {
  validate_settings
  acquire_lock
  requested="${1:-latest}"
  if [ "$requested" = latest ]; then
    receipt_name="$(rclone_cmd lsf "$remote_root/receipts" --files-only | grep '\.receipt$' | sort -r | head -n 1)"
  else
    case "$requested" in */*|*..*) fail "远端备份参数只允许文件名或latest" ;; esac
    receipt_name="$requested"
    case "$receipt_name" in *.receipt) ;; *) receipt_name="${receipt_name}.receipt" ;; esac
  fi
  [ -n "${receipt_name:-}" ] || fail "远端没有可校验的备份"
  receipt_file="$STATE_DIR/spool/$receipt_name.verify"
  cleanup_verify() {
    rm -f "$receipt_file"
  }
  trap cleanup_verify EXIT
  trap 'cleanup_verify; exit 130' INT
  trap 'cleanup_verify; exit 143' TERM
  rclone_cmd copyto "$remote_root/receipts/$receipt_name" "$receipt_file"
  source_sha="$(metadata_value source_sha256 "$receipt_file")"
  verify_remote_receipt "$receipt_file" "$source_sha"
  rm "$receipt_file"
  trap - EXIT INT TERM
  release_lock
  echo "offsite verified: $receipt_name"
}

list_remote() {
  validate_settings
  rclone_cmd lsf "$remote_root/packages" --files-only | grep '\.tar\.age$' | sort -r
}

fetch_remote() {
  validate_settings
  package_name="${1:-}"
  case "$package_name" in *.tar.age) ;; *) fail "fetch必须指定.tar.age文件名" ;; esac
  case "$package_name" in */*|*..*) fail "远端文件名非法" ;; esac
  part="${2:-package}"
  case "$part" in
    package)
      remote_file_exists "packages/$package_name" || fail "远端加密包不存在: $package_name"
      rclone_cmd cat "$remote_root/packages/$package_name"
      ;;
    checksum)
      remote_file_exists "packages/$package_name.sha256" || fail "远端校验文件不存在: $package_name.sha256"
      rclone_cmd cat "$remote_root/packages/$package_name.sha256"
      ;;
    *) fail "fetch类型仅支持package/checksum" ;;
  esac
}

health_check() {
  require_positive_uint AIGO_OFFSITE_INTERVAL_SECONDS "$INTERVAL_SECONDS"
  [ -r "$STATE_DIR/.last_success" ] || exit 1
  last="$(cat "$STATE_DIR/.last_success")"
  case "$last" in ''|*[!0-9]*) exit 1 ;; esac
  now="$(date -u +%s)"
  max_age=$((INTERVAL_SECONDS * 2 + HEALTH_GRACE_SECONDS))
  age=$((now - last))
  [ "$age" -ge 0 ] && [ "$age" -le "$max_age" ]
}

command="${1:-daemon}"
shift || true
case "$command" in
  daemon)
    while :; do
      scan_once
      sleep "$INTERVAL_SECONDS"
    done
    ;;
  once) scan_once ;;
  verify) verify_remote "${1:-latest}" ;;
  list) list_remote ;;
  fetch) fetch_remote "${1:-}" "${2:-package}" ;;
  health) health_check ;;
  *) fail "未知命令: $command（支持daemon/once/verify/list/fetch/health）" ;;
esac
