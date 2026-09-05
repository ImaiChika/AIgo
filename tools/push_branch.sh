#!/usr/bin/env bash
# Safely push a Git branch while making large history uploads explicit.
# This script never stages, commits, force-pushes, or changes the working tree.

set -euo pipefail

branch="$(git branch --show-current)"
base="origin/main"
threshold_mb=50
dry_run=0
allow_large=0
assume_yes=0
transport="https"
http1=0
retries=1
retry_delay=15

usage() {
  cat <<'USAGE'
Usage: tools/push_branch.sh [options]

Push the current branch to origin with a history-size preflight.

Options:
  --branch NAME       Branch to push. Default: current branch.
  --base REF          Base ref used for the size estimate. Default: origin/main.
  --threshold-mb N    Stop above this estimated size unless --allow-large is set.
                     Default: 50.
  --allow-large       Permit an estimated upload above the threshold.
  --transport NAME    Transport to use: https or ssh. Default: https.
  --http1             Use HTTP/1.1 for HTTPS pushes. Useful after HTTP/2 errors.
  --retries N         Total upload attempts after failures. Default: 1.
  --retry-delay N     Seconds to wait between retry attempts. Default: 15.
  --yes               Skip the final typed confirmation.
  --dry-run           Only show the estimate and push target.
  --help              Show this help.

Examples:
  tools/push_branch.sh --dry-run
  tools/push_branch.sh --transport ssh --allow-large
  tools/push_branch.sh --http1 --retries 2 --allow-large
  tools/push_branch.sh --branch codex/navigation-and-batch-workspace --transport ssh --allow-large

The estimate is the uncompressed size of objects reachable from BRANCH but not
from BASE. Git compresses objects while uploading, so the network transfer is
usually smaller. The script still treats the estimate as a safety boundary.
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --branch) branch="${2:?--branch requires a name}"; shift 2 ;;
    --base) base="${2:?--base requires a ref}"; shift 2 ;;
    --threshold-mb) threshold_mb="${2:?--threshold-mb requires a number}"; shift 2 ;;
    --allow-large) allow_large=1; shift ;;
    --transport) transport="${2:?--transport requires https or ssh}"; shift 2 ;;
    --http1) http1=1; shift ;;
    --retries) retries="${2:?--retries requires a positive number}"; shift 2 ;;
    --retry-delay) retry_delay="${2:?--retry-delay requires a positive number}"; shift 2 ;;
    --yes) assume_yes=1; shift ;;
    --dry-run) dry_run=1; shift ;;
    --help|-h) usage; exit 0 ;;
    *) echo "Unknown option: $1" >&2; usage >&2; exit 64 ;;
  esac
done

if ! [[ "$threshold_mb" =~ ^[1-9][0-9]*$ && "$retries" =~ ^[1-9][0-9]*$ && "$retry_delay" =~ ^[1-9][0-9]*$ ]]; then
  echo "--threshold-mb, --retries, and --retry-delay must be positive integers." >&2
  exit 64
fi
if [[ "$transport" != "https" && "$transport" != "ssh" ]]; then
  echo "--transport must be https or ssh." >&2
  exit 64
fi
if (( http1 )) && [[ "$transport" != "https" ]]; then
  echo "--http1 only applies to HTTPS pushes." >&2
  exit 64
fi

if ! git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  echo "Run this script inside a Git worktree." >&2
  exit 2
fi
if ! git rev-parse --verify --quiet "$branch^{commit}" >/dev/null; then
  echo "Branch or commit '$branch' was not found." >&2
  exit 2
fi
if ! git rev-parse --verify --quiet "$base^{commit}" >/dev/null; then
  echo "Base ref '$base' was not found. Run 'git fetch origin' first or pass --base." >&2
  exit 2
fi
if ! git remote get-url origin >/dev/null 2>&1; then
  echo "Remote 'origin' is not configured." >&2
  exit 2
fi

estimate_bytes="$(git rev-list --objects --no-object-names "$base..$branch" | git cat-file --batch-check='%(objecttype) %(objectsize)' | awk '$1 == "blob" { total += $2 } END { printf "%.0f", total + 0 }')"
threshold_bytes=$((threshold_mb * 1024 * 1024))
estimate_mib="$(awk -v bytes="$estimate_bytes" 'BEGIN { printf "%.1f", bytes / 1048576 }')"
commit_count="$(git rev-list --count "$base..$branch")"
dirty_count="$(git status --porcelain | wc -l | tr -d ' ')"

echo "Push target: origin $branch"
echo "Compared with: $base"
echo "Commits to push: $commit_count"
echo "Estimated uncompressed blobs: ${estimate_mib} MiB"
echo "Transport: $transport"
if [[ "$dirty_count" != "0" ]]; then
  echo "Working tree: $dirty_count local changes are not part of this push."
fi

if (( estimate_bytes > threshold_bytes )) && (( ! allow_large )); then
  echo "Stopped: estimate exceeds ${threshold_mb} MiB." >&2
  echo "Review with --dry-run, then rerun with --allow-large when you are ready." >&2
  exit 3
fi

if (( dry_run )); then
  echo "Dry run complete; no network upload was started."
  exit 0
fi

if (( ! assume_yes )); then
  read -r -p "Type PUSH to upload this branch: " confirmation
  if [[ "$confirmation" != "PUSH" ]]; then
    echo "Cancelled; no upload was started."
    exit 0
  fi
fi

mkdir -p output
log_file="output/git-push-$(date +%Y%m%d-%H%M%S).log"
echo "Uploading with Git progress. Log: $log_file"

if [[ "$transport" == "ssh" ]]; then
  push_url="$(git remote get-url --push origin)"
  case "$push_url" in
    https://github.com/*) push_url="git@github.com:${push_url#https://github.com/}" ;;
    git@github.com:*) ;;
    *)
      echo "SSH conversion is supported only for GitHub origin URLs. Current URL: $push_url" >&2
      exit 2
      ;;
  esac
  push_command=(git -c "remote.origin.pushurl=$push_url" push --progress -u origin "$branch")
else
  push_command=(git)
  if (( http1 )); then
    push_command+=(-c http.version=HTTP/1.1)
  fi
  push_command+=(push --progress -u origin "$branch")
fi

for (( attempt = 1; attempt <= retries; attempt++ )); do
  echo "Push attempt $attempt/$retries"
  if "${push_command[@]}" 2>&1 | tee -a "$log_file"; then
    echo "Push completed: origin/$branch"
    exit 0
  fi
  if (( attempt < retries )); then
    echo "Push attempt failed; retrying in ${retry_delay}s." >&2
    sleep "$retry_delay"
  fi
done

echo "Push failed after $retries attempt(s). See $log_file" >&2
exit 1
