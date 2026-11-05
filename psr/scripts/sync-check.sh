#!/usr/bin/env bash
# Conflict / divergence probe against upstream (BRE-199).
# Exit 0 = clean merge-tree into current HEAD from UPSTREAM_REF
# Exit 2 = conflicts detected
# Exit 1 = usage / git error
set -euo pipefail
ROOT=$(git rev-parse --show-toplevel)
cd "$ROOT"
UPSTREAM_REF=${1:-upstream/main}
BASE_REF=${2:-HEAD}

if ! git rev-parse -q --verify "$UPSTREAM_REF" >/dev/null; then
  echo "missing ref: $UPSTREAM_REF (run: git fetch upstream --tags)" >&2
  exit 1
fi

BASE=$(git merge-base "$BASE_REF" "$UPSTREAM_REF")
echo "base_ref=$BASE_REF ($(git rev-parse "$BASE_REF"))"
echo "upstream_ref=$UPSTREAM_REF ($(git rev-parse "$UPSTREAM_REF"))"
echo "merge_base=$BASE"

# Prefer git merge-tree (no index mutation)
if git merge-tree --write-tree "$BASE_REF" "$UPSTREAM_REF" >/tmp/psr-merge-tree.out 2>/tmp/psr-merge-tree.err; then
  echo "merge-tree: CLEAN"
  echo "result_tree=$(cat /tmp/psr-merge-tree.out)"
  exit 0
fi

# Older git: merge-tree without --write-tree prints conflicts to stdout
if git merge-tree "$BASE" "$BASE_REF" "$UPSTREAM_REF" > /tmp/psr-merge-tree.legacy 2>&1; then
  if grep -q 'changed in both\|CONFLICT' /tmp/psr-merge-tree.legacy; then
    echo "merge-tree: CONFLICTS"
    grep -E 'changed in both|CONFLICT|merged' /tmp/psr-merge-tree.legacy | head -100
    exit 2
  fi
  echo "merge-tree: CLEAN (legacy)"
  exit 0
fi

echo "merge-tree failed; see /tmp/psr-merge-tree.err" >&2
cat /tmp/psr-merge-tree.err >&2 || true
exit 1
