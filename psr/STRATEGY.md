# PSR RAGFlow adaptation strategy (BRE-199)

## Principles

1. **upstream-first** — Prefer consuming upstream releases/tags as-is. Bump the pin (`psr/UPSTREAM.lock.json`) rather than carrying long-lived core forks of behavior.
2. **sidecar-first** — Put PSR-specific behavior in sidecars, wrappers, compose overlays, or out-of-tree services that call RAGFlow APIs. Do not modify RAGFlow core for integration glue.
3. **patch core only when extension points are insufficient** — If a change must touch upstream code, drop a quilt/git patch under `psr/patches/` with a README entry explaining why sidecar/extension failed. Keep patches minimal and rebase them on every pin bump.

## Branch layout

| Ref | Role |
| --- | --- |
| `upstream` remote → `infiniflow/ragflow` | Official source of truth |
| tag `vX.Y.Z` (pinned) | Immutable baseline commit |
| `psr/baseline-vX.Y.Z` | Working baseline branch at the pin + PSR metadata (`psr/`) |
| `main` | Mirror of upstream `main` at last sync (not the PSR runtime pin) |

## Conflict / update policy

- Run `psr/scripts/sync-check.sh` before any pin bump or merge from `upstream/*`.
- Non-zero exit = conflicts or divergence report required; do not force-merge.
- When bumping: update lock file tag+full SHA, re-run sync drill, refresh diff report, re-apply `psr/patches/` if any.

## Out of scope for BRE-199

ARM64/Colima full stack bring-up is **BRE-200**. This card only locks source baseline + sync strategy.
