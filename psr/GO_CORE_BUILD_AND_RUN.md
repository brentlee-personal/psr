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

The ToB semantic state store remains separate from the RAGFlow metadata
store even when both use the same PostgreSQL instance. Native PostgreSQL
metadata is selected with `DB_TYPE=postgres`; it does not enable Vastbase's
MySQL compatibility mode. Use a dedicated database and role, never the
existing semantic database.

The `postgres:` configuration section accepts `host`, `port`, `name`, `user`,
`password`, `max_connections`, `stale_timeout`, `ssl_mode`, and `ssl_root_cert`.
The normal Viper environment mapping accepts the corresponding
`RAGFLOW_POSTGRES_HOST`, `RAGFLOW_POSTGRES_PORT`, `RAGFLOW_POSTGRES_NAME`,
`RAGFLOW_POSTGRES_USER`, `RAGFLOW_POSTGRES_PASSWORD`,
`RAGFLOW_POSTGRES_MAX_CONNECTIONS`, `RAGFLOW_POSTGRES_STALE_TIMEOUT`,
`RAGFLOW_POSTGRES_SSL_MODE`, and `RAGFLOW_POSTGRES_SSL_ROOT_CERT` variables.
Keep credentials in the existing protected deployment configuration or Secret.
The default pool is six connections. Remote connections default to
`ssl_mode=verify-full`; a mounted root certificate can be supplied through
`ssl_root_cert`.

Before deployment, verify the fixed candidate against a dedicated PostgreSQL
16 test database with the normal native toolchain:

```sh
bash build.sh --test ./internal/server/config ./internal/server ./internal/dao
bash build.sh --test-integration-go -run TestPostgreSQLMetadataIntegration -v ./internal/dao
```

The integration test requires `PSR_POSTGRES_TEST_DSN`, the exact
`PSR_POSTGRES_TEST_DATABASE` (beginning with `psr_go_`), and
`PSR_POSTGRES_TEST_ALLOW_WRITE=1`. It verifies the database identity before
creating its uniquely named schemas, and preserves those schemas for
independent inspection. Credentials are not printed. A missing test DSN
skips the integration test and is not database acceptance.

Native PostgreSQL migrations validate existing generated-column and unique
index definitions instead of accepting a same-named incompatible object.
Dataset names and canvas titles retain their explicit case-folded uniqueness
contracts. An unknown legacy `tenant_llm` primary-key layout is rejected;
this path does not automatically convert an arbitrary historical schema.
Source/unit checks, database integration, complete service startup and business
acceptance remain separate evidence.
