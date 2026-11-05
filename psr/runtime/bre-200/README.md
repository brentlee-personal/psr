# BRE-200 — Mac Studio Colima isolated RAGFlow runtime (ARM64 adaptation)

Linear: [BRE-200](https://linear.app/brentlee/issue/BRE-200) · Pin: **v0.27.3** / `8090ffd8ba6098813d7f7ec2c4b8774f36c8423a`  
Compose project: **`ragflow-psr-m05`** · Host: Mac Studio Colima (`aarch64`, Rosetta enabled)

## What ran (truth)

| Item | Result |
| --- | --- |
| Isolated Colima start | YES — project `ragflow-psr-m05` |
| Host `5432` / `8787` collision | NONE (G0 kept) |
| Plumb PID | **92856** unchanged across Colima restart + smoke |
| Ingest MD+PDF → parse DONE | YES (ES path) |
| Retrieve positive | YES — hit sample.md mentioning v0.27.3 |
| Retrieve negative (high threshold) | YES — `total: 0` |
| Auth negative | YES — HTTP 401 |
| Restart recovery | YES — login + dataset list after `docker restart ragflow-cpu` |

## DOC_ENGINE choice (important)

1. **Tried Infinity first** (`v0.7.4-x64-v3` + Rosetta; arm64 tags GLIBC-broken). Manual thrift open OK; RAGFlow executor/API repeatedly `Could not connect … 23817`. See `evidence/INFINITY-THRIFT-FLAKE.md`.
2. **Smoke completed on Elasticsearch 8.11.3 (arm64)** + TEI `BAAI/bge-small-en-v1.5` Builtin embedding — reproducible on this Colima.
3. **Commercial default is NOT changed by this card.** BRE-206 commercial stack default remains **Infinity / OpenSearch**. Elasticsearch here is an **ARM64 Colima smoke adaptation only**, not a commercial DOC_ENGINE default.

## ARM64 adaptation summary

| Component | Arch path |
| --- | --- |
| `infiniflow/ragflow:v0.27.3` | **amd64-only** → Colima `--vz-rosetta` + `platform: linux/amd64` |
| Infinity arm64 tags | **incompatible** (GLIBC_2.36/2.38) |
| Infinity x64 under Rosetta | Process starts; **Thrift client flaky** for RAGFlow workers |
| Elasticsearch 8.11.3 | **arm64 native** — used for smoke |
| TEI cpu-1.8 | amd64 under Rosetta; model `BAAI/bge-small-en-v1.5` |
| mysql / redis(valkey) / minio(silo) | arm64 |

Colima resize used for this card: **6 CPU / 16 GiB / 80 GiB + Rosetta** (was 4/8/60). G0 compose restored after Colima restart; Plumb is host process (untouched).

## Start / stop

```bash
# from ragflow-psr worktree or clone with this branch
./psr/runtime/bre-200/scripts/up.sh
./psr/runtime/bre-200/scripts/compose.sh ps
./psr/runtime/bre-200/scripts/down.sh   # optional; does not touch G0/Plumb
```

Requires Colima with Rosetta (`colima start --vz-rosetta …`).

## Isolation mechanics

- `ports: !override` → all publishes on `127.0.0.1` (see `evidence/PORT-MAP.md`).
- Infinity PSQL host map (when enabled) is **`25432`**, never `5432`.
- No Go syncer / `ragflow-go` profile.
- Resource caps: ragflow 8 GiB, ES ~1.5 GiB, TEI 2 GiB, mysql 1.5 GiB.

## Evidence index

Under `psr/runtime/bre-200/evidence/`:

- `retrieve.json`, `upload.json`, `documents-list.json`, `negative.json`
- `restart-recovery.txt`, `compose-ps-final.txt`, `docker-ps-ports.txt`, `stats-final.txt`
- `INFINITY-THRIFT-FLAKE.md`, `PORT-MAP.md`, `access.txt`, `g0-plumb-after-smoke.txt`
- Colima/G0 restore logs from bring-up

## Non-goals

- No self-merge; no Linear Done flip from coding delivery.
- Do not occupy L20 / company ACK.
- Do not jump BRE-201.
