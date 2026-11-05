# Baseline pin selection (BRE-199)

Explicit selection locked for PSR M0.5. Informed by BRE-202 upstream intel; this card only records the choice.

## Selected baseline

| Field | Value |
| --- | --- |
| Line | **0.27.x stable** |
| Tag | **`v0.27.3`** |
| Full commit | **`8090ffd8ba6098813d7f7ec2c4b8774f36c8423a`** |
| Not selected | `v1.0.0-rc1` / `rc2` / `rc3` (and any other `1.0.0-rc*`) |

Lock file: `psr/UPSTREAM.lock.json`. Reproduce: `psr/REPRODUCE.md`.

## Why 0.27.3 (not 1.0.0-rc*)

- **First PSR baseline prioritizes reproducible stability** over chasing Latest.
- BRE-202 signals on the RC line (summary):
  - **RC upgrades are effectively irreversible** for operators once taken.
  - **Sandbox removal** and related surface changes on the RC path.
  - **API deprecation risk** called out under intel items **S9 / S14**.
- Therefore M0.5 pins the latest non-RC stable release available at lock time: **v0.27.3**.

## Go syncer — not adopted on this card

- **Decision: do not adopt / deferred.** BRE-199 does **not** take a Go syncer path.
- If that path were chosen later, BRE-202 notes upstream PR **#21575** was still an open sync gap (not merged at intel time). Revisit only under a later card with explicit 主理派工 — not BRE-199/200 by default.

## MS ARM64 — deferred to BRE-200

- BRE-202 / M9: from **rc2** onward there are **arm64 images** worth referencing for Mac Studio / Colima.
- **This card does not start** Colima full bring-up, ARM64 dependency adaptation, or image pulls for runtime. That remains **BRE-200**.

## Non-goals (repeat)

- No self-merge of delivery PRs; no Linear Done flip from coding delivery.
- Do not touch existing PSR G0 (`127.0.0.1:5432` / `:8787`) or Plumb.
