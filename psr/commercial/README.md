# PSR commercial engineering defaults (BRE-206)

**Engineering default only — not a legal opinion / not “fully green”.**

Pin: RAGFlow **v0.27.3** / `8090ffd8ba6098813d7f7ec2c4b8774f36c8423a`.

## Approved defaults

| Layer | Default | SPDX | Rejected default |
| --- | --- | --- | --- |
| DOC_ENGINE | **Infinity** (alt: OpenSearch) | Apache-2.0 | Elasticsearch (SSPL/EL2) |
| Object storage | **SeaweedFS** (S3 API) | Apache-2.0 | pgsty/silo AGPL-3.0 |
| Metadata DB | **PostgreSQL 16** | PostgreSQL License | MySQL Community GPL-2.0 |

### Infinity vs OpenSearch

Prefer **Infinity** to stay aligned with the MS BRE-200 Colima runtime already on
Infinity. Use **OpenSearch** when operators need ES-API familiarity without Elastic
SSPL/EL2. Do not set `DOC_ENGINE=elasticsearch` for PSR commercial deliveries.

## Compose profiles

Default `COMPOSE_PROFILES` (from `docker/.env`) includes:

`infinity,cpu,metadata-postgres,object-seaweedfs`

Legacy (non-default) profiles kept for migration / BRE-200 continuity:

- `metadata-mysql` — MySQL 8 (GPL) sidecar
- `object-silo-agpl` — pgsty/silo AGPL object store

## Cutover (do not hard-stop BRE-200)

BRE-200 (`ragflow-psr-m05-*`) may still run Infinity + MySQL + silo for acceptance.
This BRE-206 change:

1. Does **not** recreate or stop `ragflow-psr-m05-*`.
2. Documents the new defaults for **new** stacks / post-acceptance cutover.
3. Keeps silo available only under `object-silo-agpl`.

See `BRE-200-CONTINUITY.md`.

## Guardrails

- Do not bind host `:5432` / `:8787` (G0 / Plumb).
- Do not ACK-apply ToB; do not open BRE-204.
- Do not force-push; do not self-merge.
- Wording: **approved engineering default (BRE-206)** — never “legal full green”.
