# Third-party notices (PSR center)

> **Nature**: engineering SPDX inventory bound to soil pin **RAGFlow v0.27.3** /
> `8090ffd8ba6098813d7f7ec2c4b8774f36c8423a`. **Not legal advice.**
> Cross-ref: Linear BRE-206 inventory comment `7da45154…`.

## Application

| Component | SPDX / license | Pin / version | Role | PSR commercial default |
| --- | --- | --- | --- | --- |
| RAGFlow (soil → center) | Apache-2.0 | v0.27.3 / `8090ffd8…` | Application | **Yes** (center tracks this pin) |
| InfiniFlow DeepDoc / related HF cards (where marked Apache-2.0) | Apache-2.0 | as vendored at pin | Parsing helpers | Yes (verify card license on upgrade) |
| ONNX Runtime | MIT | as in pin lockfiles | Runtime | Yes |
| Apache Tika | Apache-2.0 | 3.3.0 (pin) | Parse | Yes |

## Approved engineering defaults (BRE-206)

| Component | SPDX / license | Version (compose) | Role | Why chosen |
| --- | --- | --- | --- | --- |
| **Infinity** | Apache-2.0 | `infiniflow/infinity:v0.7.4` (compose tag) | DOC_ENGINE | Aligns with MS BRE-200 runtime; Apache-2.0; avoids ES SSPL/EL2 |
| **OpenSearch** (alternate) | Apache-2.0 | 2.19.1 | DOC_ENGINE alt | Approved alternate if Infinity unsuitable; still rejects ES |
| **SeaweedFS** | Apache-2.0 | `chrislusf/seaweedfs:3.86` | Object storage (S3 API) | Non-AGPL S3-compatible; replaces pgsty/silo |
| **PostgreSQL** | PostgreSQL License | `postgres:16-alpine` | Metadata DB | Permissive; replaces MySQL Community GPL-2.0 |
| Valkey | BSD-3-Clause | valkey:8 | Cache/queue | Unchanged |
| NATS | Apache-2.0 | as in pin compose | MQ | Unchanged |

### Infinity vs OpenSearch (selection note)

- **Default = Infinity**: already proven on MS Colima for BRE-200 (ARM64 via amd64 emulation), same pin family as soil, Apache-2.0.
- **OpenSearch**: documented alternate (`DOC_ENGINE=opensearch`) when operators need Lucene/ES-API familiarity without Elastic SSPL/EL2.
- **Elasticsearch**: **not** a PSR commercial default (SSPL-1.0 + Elastic-2.0).

## Rejected as PSR commercial defaults (optional legacy profiles only)

| Component | SPDX / license | Why rejected as default | Still in tree? |
| --- | --- | --- | --- |
| Elasticsearch | SSPL-1.0 + Elastic-2.0 | Hosted/SaaS redistribution friction | Optional DOC_ENGINE profile only |
| pgsty/silo (MinIO lineage) | AGPL-3.0 | Strong copyleft on object-store server | Optional `object-silo-agpl` profile (BRE-200 continuity) |
| MySQL Community Server | GPL-2.0 | Server GPL obligations for redistribution | Optional `metadata-mysql` profile |
| Garage | AGPL-3.0 | Same AGPL class as silo; not a replacement | Not added |
| Official MinIO server (post-relicense) | AGPL-3.0 | Same class; do not treat as “non-AGPL MinIO” | Not added |

## PSR policy

1. ToB / customer surfaces track **center pin only** (BRE-205); no soil direct merge.
2. Sync must not silently drop LICENSE / NOTICE / this file (BRE-199/204).
3. Do **not** claim “legally fully green”; say **“approved engineering default (BRE-206)”**.
4. BRE-204 (public upstream contribution) remains closed unless 主理 approves.
