# PSR shared Go core: build and run contract

## Fixed source

The shared core is the PSR repository branch `psr/go-core`. Consumers should
pin a full commit SHA, not a moving branch name. The authorization endpoint and
its input/output contract are documented in
[`GO_AUTHORIZATION_CONTRACT.md`](GO_AUTHORIZATION_CONTRACT.md).

## Build a Linux binary artifact

Build from a clean checkout of the exact source SHA on Linux:

```sh
git checkout --detach <full-commit-sha>
uv run tools/scripts/download_dev_deps.py
bash psr/package-go-core.sh
```

Prepare the documented CMake, Go, Clang/C++20, linker, and PCRE2 prerequisites
first; `internal/development.md` lists the supported toolchain setup. The
dependency downloader fetches the pinned native libraries and runtime resources.
The package script runs `bash build.sh --all`, which builds the native C++
library and Go server/CLI with the repository-pinned Go modules and native
library versions.
It checks the server and CLI version commands and the API help path, then writes
`dist/psr-go-core/psr-go-core-<source-sha>-linux-<arch>.tar.gz` and a SHA-256
sidecar. `BUILD-MANIFEST.txt` records the source commit, target architecture,
Go toolchain, and build command; `SHA256SUMS` verifies the archive contents.
The archive also carries the repository license and API/build contracts.
`PSR_ARTIFACT_DIR` can select another output directory.
The `PSR Go core artifact` Actions workflow runs this process for pushes to
`psr/go-core` and uploads a Linux/amd64 artifact named with the full source SHA
for 30 days. Its upload digest is recorded in the workflow summary. The CI
runner image currently uses a mutable upstream tag, so this pins source identity
and records the build toolchain but does not promise byte-identical rebuilds
across runner-image updates.

The archive is a **binary payload, not a complete runtime image**. It contains
only `bin/ragflow_server` and `bin/ragflow-cli`. In particular, it does not
include configuration or credentials, native dictionaries, DeepDoc models,
tokenizer assets, fonts, or external services. Do not copy a developer's
`conf/service_conf.yaml` or customer-specific settings into this artifact.

The binary must run in the matching Linux architecture and with the runtime
resources prepared by the supported production image build. For the complete
RAGFlow service runtime, build the root `Dockerfile` from the same source
checkout and use its production target; the entrypoint coordinates migrations
and the API/admin/ingestor/syncer processes. The Docker build currently consumes
some mutable upstream dependency references, so pinning the PSR source SHA does
not by itself make the whole image byte-reproducible.

## Run and integrate the permission API

The service needs its normal RAGFlow configuration and dependencies. For a
local API process, after preparing those runtime resources and setting the
configuration path:

```sh
bin/ragflow_server --api --config /path/to/service_conf.yaml
```

An integration must authenticate as a RAGFlow user through the normal API
middleware and call:

```http
POST /api/v1/permissions/check
Authorization: Bearer <RAGFlow API token>
Content-Type: application/json

{"resource":{"kind":"dataset","id":"<resource-id>"},"operation":"read"}
```

`allowed` and `denied` are HTTP 200 decisions; a permission-source failure is
HTTP 503 `unverified` and must be treated as no grant. The caller cannot supply
an identity. A prior `allowed` result is not a durable grant: the protected
resource operation must perform its own authorization immediately before use.

This endpoint checks only RAGFlow's permission model. A ToB service must map an
already authenticated customer subject to an authorized RAGFlow identity and
enforce customer-owned grants in its private application layer. Keep those
mappings, customer content, credentials, and grants out of PSR.

The ToB semantic PostgreSQL store remains separate from RAGFlow metadata. The
Go metadata configuration does not accept generic `DB_TYPE=postgres`; its
PostgreSQL-compatible metadata option is the separately supported `vastbase`
configuration. Do not point the semantic store at the metadata database or
infer a migration path from this artifact contract.
