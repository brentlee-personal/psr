> **Desc**: Produce the complete PSR Go runtime as a source-bound Actions artifact.
> **Input**: Reviewed application SHA, producer SHA, frozen dependencies, isolated Linux amd64 runner.
> **Output**: Single-image OCI archive, source manifest, resource manifest, dependency artifact and logs.
> **Position**: Producer contract for the existing PSR Go artifact workflow.
> **Upstream**: AGENTS.md, official Dockerfile production target and build.sh.
> **Downstream**: Trusted broker OCI import and independent ACK/RDS acceptance.
> **Last-verified**: 2026-11-11; producer implementation and narrow tests, deployment not asserted.

# Complete Go runtime producer

The existing `.github/workflows/psr-go-core-artifact.yml` retains its binary
artifact job and adds an explicitly dispatched complete-runtime job. The latter
builds the application SHA in `inputs.lock.json` from a separate clean checkout.
The workflow's own `GITHUB_SHA` is the producer control SHA. These coordinates
are deliberately distinct.

Dispatch the reviewed producer branch with `complete_runtime=true`. It requires
the isolated, ephemeral repository runner labels
`[self-hosted, Linux, X64, psr-go-runtime-20261111]`. The job has only
`contents: read`, no cloud/database secrets, and does not publish to a registry
or deploy Kubernetes resources. Its containers use CPU limits and no GPU.

## Frozen inputs and rebuilding

`produce.sh APPLICATION_ROOT OUTPUT_ROOT REVIEWED_CACHE PRODUCER_SHA` requires
a new output directory and exactly the locked application SHA. It verifies the
official Dockerfile hash, every native dependency file and runtime resource,
the frozen base archive hash/image identity, and the installed base packages.

The frozen base was previously built from the official production base recipe
at source `c37f5b6f39cc177e283dd960d7150d787926143c`; its source, image ID,
Buildx descriptor digest, archive hash and package inventory are explicit
inputs. **It is not rebuilt from the network in this run.** It contains public
upstream runtime resources, system libraries and nginx. It is a dependency,
not a prebuilt application runtime archive.

Each complete-runtime run rebuilds native C++ and Go via `bash build.sh --all`,
and rebuilds the frontend via the checked-in npm lock with `npm ci`. It then
uses Buildx named contexts for the verified frozen base and freshly built
Go/frontend stages with the unmodified application Dockerfile's production
target. Provenance/SBOM indexes are disabled for the single-image OCI export;
producer identity and dependency hashes are provided by the explicit manifests.

The initial reviewed cache is
`/data/brent/workspace/go-runtime-build-evidence-20261111`. It is read only by
the producer: private copies receive build-tool cache changes. Optional Go
module/build caches are accelerators; `go mod download` and `go mod verify`
still run. No existing service/container, Docker data root or unrelated cache
is removed. All work/output paths are on the GPU's data volume.

## Runtime artifact

The Actions artifact is named
`psr-go-runtime-<application_source_sha>-linux-amd64`. Its ZIP has exactly
these three top-level files:

- `core-runtime.oci.tar`: OCI image layout 1.0.0 with exactly one image manifest.
- `source-manifest.json`: source, recipe, contracts, archive and OCI blob hashes.
- `runtime-resources.json`: actual image-file hashes and embedded dependency manifest.

`source-manifest.json` schema 1 contains:

```text
runtime_kind = complete-runtime
source_sha = exact application commit
producer_source_sha = workflow GITHUB_SHA
platform = linux/amd64
image_digest, config_digest, blobs[{mediaType,digest,size}]
archive_sha256, resource_manifest_sha256, binary_sha256
recipe_sha256, dependency_manifest_sha256
api_contract {path,sha256}, build_contract {path,sha256}
```

The OCI image revision label equals the application SHA; the producer label
is separate. The API/build contract hashes are read from the application
checkout, not the producer checkout. The recipe digest hashes the canonical
sorted sha256sum file list of this directory, excluding this README.

`runtime-resources.json.dependency_manifest` is also emitted as
`dependency-manifest.json` in the dependency artifact. Its canonical bytes
use sorted keys, compact separators, UTF-8 and exactly one trailing LF:
`jq -cS .`. With these ASCII input strings this equals
`json.dumps(obj, sort_keys=True, separators=(',', ':'), ensure_ascii=False) + '\\n'`.
`dependency_manifest_sha256` hashes those bytes.

The dependency artifact `psr-go-runtime-inputs-<producer_sha>` retains the
frozen base archive, native libraries, runtime resource files, input lock,
checksum files and package inventory. Reproduction can restore its contents
as REVIEWED_CACHE. The OCI runtime ZIP does not embed the dependency artifact
or its own Actions ZIP digest.

Actions run ID, actual workflow path/head SHA, artifact ID/name and uploaded
ZIP digest must be read independently from GitHub's API. The trusted consumer
locks these numeric identities and all three file hashes before import.
The producer never claims a registry receipt; the broker must push, read back
the registry manifest independently and create that receipt.

## Validation and limits

The producer validates every OCI blob digest/size, rejects extra images,
unreferenced blobs, symlinks and wrong platform/source/entrypoint. It compares
the actual packaged server, frontend, models, tokenizers, dictionaries,
configuration/templates and font with expected input hashes. Dynamic-library
closure, four mode help commands, nginx configuration and the real entrypoint
with all service modes disabled are checked in disconnected containers.

Narrow validator tests run through `bash build.sh --test -v ./psr/runtime`.
These tests do not constitute RDS integration or business acceptance.

No service is started with enabled runtime modes during packaging. In
particular, the API/syncer entrypoint may migrate a database: it must not run
against a shared database as a packaging smoke test. ACK readiness, real RDS
read/write, authorized business calls, registry scanning/publication and
rollback remain separate gates under the deployment coordinator.

Rollback of this producer consists of selecting an earlier reviewed control
commit and its compatible fixed input lock. Existing deployed services are
unaffected by producing artifacts.


## Reviewed Go compiler input

The complete-runtime candidate uses the exact official Go1.27.3 Linux amd64
archive and the frozen official download-metadata snapshot recorded in
`inputs.lock.json.go_toolchain`. The producer verifies archive size/SHA,
metadata SHA/selected file, safe archive members and the go/gofmt executable
hashes before mounting GOROOT read only. It asserts both the selected compiler
and the resulting server/CLI build versions. Clang/LLD20 and the existing
native74 inputs remain pinned independently.

The workflow's `PSR_APPROVED_TOOLCHAIN_INPUT` points to an owned, checksum
verified GPU input cache. For reconstruction from the normal frozen-input
artifact, leave that variable unset and restore the archive/metadata beside
the existing REVIEWED_CACHE inputs. Both public files are included in the
new frozen-input artifact; the final runtime image receives only application
outputs, not GOROOT or build credentials.

This compiler adoption applies to complete-runtime builds and their OCI
inspector. It does not claim the legacy binary or dependency-packaging tool
job runs with Go1.27.3; dependency image bytes are the separately pinned
upstream images.
