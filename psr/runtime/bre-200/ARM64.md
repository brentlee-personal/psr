# ARM64 / dependency adaptation notes (BRE-200)

## Pin constraint
Upstream `infiniflow/ragflow:v0.27.3` publishes **linux/amd64 only** (Hub inspect). Native arm64 RAGFlow images appear on the 1.0.0-rc line (BRE-202) — **out of pin** for this card.

## Dependency matrix (MS Colima aarch64)

| Image | Result |
| --- | --- |
| ragflow:v0.27.3 | Run via Rosetta (`platform: linux/amd64`) |
| infinity:v0.7.4-arm64 / v0.7.5-arm64 | Fail: missing GLIBC_2.36/2.38 |
| infinity:v0.7.4-x64-v3 | Starts under Rosetta; Thrift unstable for RAGFlow workers |
| elasticsearch:8.11.3 | arm64 OK — smoke DOC_ENGINE |
| text-embeddings-inference:cpu-1.8 | amd64 via Rosetta |
| mysql:8.0.40, valkey:8, pgsty/silo | arm64 OK |

## Sidecar-first patches (this tree)
- `docker-compose.override.yml` — loopback ports, mem/cpu caps, platform pins, ES/TEI/Infinity image overrides
- `infinity_conf.psr.toml` — buffer 1GB / memindex 512MB (Colima-sized)
- `env.psr` — remapped ports, profiles, TEI model
- No core RAGFlow source patch required for bring-up

## Incompatible / deferred
- Infinity as stable DOC_ENGINE on this MS Colima+Rosetta path — deferred; commercial track BRE-206 (Infinity/OpenSearch).
- Native arm64 ragflow image — requires pin bump beyond v0.27.3.
