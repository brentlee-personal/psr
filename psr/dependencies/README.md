> **Desc**: Produce the four approved isolated-test dependency images through normal Actions.
> **Input**: Exact DockerHub repositories, full tags and Linux amd64 platform-manifest digests.
> **Output**: Four source-bound Actions OCI artifacts and persistent GPU evidence.
> **Position**: Dependency producer for the existing PSR Go artifact workflow.
> **Upstream**: Reviewed inputs.lock.json; docker/docker-compose-base.yml is the original baseline.
> **Downstream**: Trusted broker dependency import; isolated ACK readiness tests.
> **Last-verified**: 2026-11-11; upstream manifest audit and narrow producer tests only.

# Test dependency OCI artifacts

Dispatch the existing artifact workflow with `dependency_only=true` and
`complete_runtime=false`. One ephemeral GPU runner produces all four payloads
sequentially, then uploads one artifact per dependency. No registry-push or
cloud/database credential is available to this job.

The only approved repositories and full source tags are:

| Slot | DockerHub repository | Tag |
|---|---|---|
| kvrocks | apache/kvrocks | 2.16.0 |
| nats | library/nats | 2.14.2 |
| infinity | infiniflow/infinity | v0.7.5-x64-v3 |
| minio | pgsty/silo | RELEASE.2026-11-09T00-00-00Z |

The `minio` slot names the application's S3 test dependency; its actual image is
pgsty/silo. Infinity's x64-v3 CPU requirements must be checked against ACK nodes
before deployment. These images are test dependencies, not new production
services.

The November Silo tag and its locked digest are reviewed security candidate
inputs. The original Compose source remains the baseline and does not establish
this candidate identity. Normal artifact production, compatibility validation,
a new security scan and consumer admission remain separate requirements.

`inputs.lock.json` records the exact platform manifests, independently resolved
from the above tags. The producer accepts no arbitrary repository, tag, URL or
local image archive. The `audit` command performs a separate current-upstream
read and prints candidate pins; it is never used to silently update a normal
production run's lock.

The Go registry client obtains anonymous, repository-scoped DockerHub pull
tokens in memory and downloads the approved manifest/config/layer bytes. All
bytes are verified against their descriptors. The original schema2 or OCI
manifest is retained without conversion, so `image_digest` equals the approved
`upstream_manifest_digest`. The source manifest lists every unique blob and
the exact OCI archive hash. Image config OS/architecture and rootfs layer-count
shape are checked. The trusted importer independently verifies compressed and
uncompressed layer digests, tar structure and the deployed resource profile.

Each artifact is `psr-test-dependency-<slot>-linux-amd64`, with exactly:

- `image.oci.tar`: single-image OCI layout; original upstream manifest and blobs.
- `source-manifest.json`: schema1, runtime_kind=test-dependency, source identity and hashes.
- `upstream-manifest.json`: exact upstream platform-manifest bytes, not a multi-platform index.

The normal Actions run/head/workflow/artifact ID/ZIP digest must be read from
GitHub independently. Local preflight output is not an Actions artifact and
does not establish registry delivery. The broker receives only approved
Actions artifacts, imports to the existing ACR test repository and reads back
registry digests independently.

This GPU has a resident `mihomo.service` proxy listening on 127.0.0.1:7897
(PID14092, PPID1 at audit). The job alone reuses this existing transport through
HTTP(S)_PROXY; it does not edit global DNS, proxy config or host credentials.
The proxy was verified as a GPU systemd service rather than an MBP SSH reverse
listener. Upstream routing and artifact upload still require actual network
verification per run; a listener is not proof of completed delivery. Failures
are retained as normal Actions logs and under
`/data/brent/workspace/go-core-oci-producer-evidence-20261111/dependency-runs/<run>-<attempt>`.

Run narrow tests through
`bash build.sh --test -v ./psr/runtime ./psr/dependencies`.
They enforce closed inputs, platform selection, blob tamper rejection and
byte-preserving deterministic OCI packaging. Actual upstream pulls, artifact
upload, broker publication and ACK service readiness are separate evidence.

