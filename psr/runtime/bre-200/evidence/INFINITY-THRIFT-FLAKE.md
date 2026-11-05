# Infinity Thrift flake on MS ARM64 / Colima Rosetta (BRE-200)

## Observed
- Image: `infiniflow/infinity:v0.7.4-x64-v3` under `platform: linux/amd64` (Rosetta).
- Host/container TCP to `infinity:23817` succeeded; manual `TSocket`/`TBufferedTransport.open()` from a fresh `docker exec` process returned `thrift_open_ok`.
- Concurrently, RAGFlow task executor + `/api/v1/retrieval` repeatedly failed with:
  `TTransportException("Could not connect to any of [('172.20.0.6', 23817)]")`
- `infiniflow/infinity:v0.7.4-arm64` / `v0.7.5-arm64` failed at process start:
  `GLIBC_2.36` / `GLIBC_2.38` not found.

## Decision for this card
- Stopped burning time on Infinity Thrift under Rosetta after repeated fails with TCP/manual thrift OK.
- Completed reproducible ingest+retrieve smoke on **Elasticsearch 8.11.3 (arm64)** + TEI Builtin embedding.
- **Commercial default remains Infinity / OpenSearch (BRE-206).** This card does **not** establish Elasticsearch as the commercial DOC_ENGINE default — ES is an ARM64 Colima smoke adaptation only.

Evidence files: `infinity-thrift-probe.txt`, `thrift-client-probe.txt`, `documents-list.json` (Infinity FAIL rows), `retrieve.json` (ES success).
