#!/usr/bin/env bash
# Conflict / divergence probe against upstream (BRE-199).
# Exit 0 = clean (no "changed in both" / CONFLICT markers)
# Exit 2 = conflicts detected
# Exit 1 = usage / git error
set -euo pipefail
ROOT=$(git rev-parse --show-toplevel)
cd "$ROOT"
UPSTREAM_REF=${1:-upstream/main}
BASE_REF=${2:-HEAD}
OUT=${PSR_SYNC_OUT:-/tmp/psr-merge-tree.legacy}

if ! git rev-parse -q --verify "$UPSTREAM_REF" >/dev/null; then
  echo "missing ref: $UPSTREAM_REF (run: git fetch upstream --tags)" >&2
  exit 1
fi

BASE=$(git merge-base "$BASE_REF" "$UPSTREAM_REF")
echo "base_ref=$BASE_REF ($(git rev-parse "$BASE_REF"))"
echo "upstream_ref=$UPSTREAM_REF ($(git rev-parse "$UPSTREAM_REF"))"
echo "merge_base=$BASE"

# Classic three-arg merge-tree: prints conflict markers without touching index
git merge-tree "$BASE" "$BASE_REF" "$UPSTREAM_REF" > "$OUT" 2>&1 || true

if grep -qE 'changed in both|^CONFLICT' "$OUT"; then
  echo "merge-tree: CONFLICTS"
  # Count without SIGPIPE
  conflict_files=$(grep -c 'changed in both' "$OUT" || true)
  echo "changed_in_both_count=$conflict_files"
  grep -n 'changed in both' "$OUT" | sed -n '1,40p'
  echo "full_report=$OUT"
  exit 2
fi

echo "merge-tree: CLEAN"
echo "full_report=$OUT"
exit 0
