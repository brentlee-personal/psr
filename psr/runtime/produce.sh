#!/usr/bin/env bash
# Full runtime producer. The only accepted application is the reviewed input lock.
set -euo pipefail
umask 022
CONTROL=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
RECIPE=$CONTROL/psr/runtime
[[ $# == 4 ]] || { echo 'usage: produce.sh APPLICATION_ROOT OUTPUT_ROOT REVIEWED_CACHE PRODUCER_SHA' >&2; exit 2; }
APP=$(realpath "$1"); OUT=$(realpath -m "$2"); INPUT=$(realpath "$3"); PRODUCER=$4
LOCK=$RECIPE/inputs.lock.json
SOURCE=$(jq -er '.application_source_sha' "$LOCK")
[[ $SOURCE =~ ^[a-f0-9]{40}$ && $PRODUCER =~ ^[a-f0-9]{40}$ ]]
[[ $(git -C "$CONTROL" rev-parse HEAD) == "$PRODUCER" ]]
[[ -z $(git -C "$CONTROL" status --porcelain --untracked-files=no) ]]
[[ $(git -C "$APP" rev-parse HEAD) == "$SOURCE" ]]
[[ -z $(git -C "$APP" status --porcelain --untracked-files=no) ]]
[[ $(uname -s)/$(uname -m) == Linux/x86_64 ]]
[[ ! -e "$OUT" ]] || { echo 'output must be a new directory' >&2; exit 1; }
[[ $(df -PB1 /var/lib/docker | awk 'NR==2 {print $4}') -gt 17179869184 ]] || { echo 'Docker free space below 16GiB floor' >&2; exit 1; }
[[ $(df -PB1 "$(dirname "$OUT")" | awk 'NR==2 {print $4}') -gt 21474836480 ]] || { echo 'output volume free space below 20GiB' >&2; exit 1; }
mkdir -p "$OUT"/{payload,work,logs,dependencies}
exec > >(tee "$OUT/logs/producer.log") 2>&1
trap 'rc=$?; if ((rc)); then printf "state=FAILED exit=%s producer_source=%s application_source=%s\n" "$rc" "$PRODUCER" "$SOURCE" > "$OUT/RESULT"; fi' EXIT
printf 'producer_source=%s application_source=%s started=%s\n' "$PRODUCER" "$SOURCE" "$(date -Is)"
RUNNER=$(jq -er '.runner' "$LOCK")
[[ $(sha256sum "$APP/Dockerfile" | cut -d' ' -f1) == $(jq -er '.dockerfile_sha256' "$LOCK") ]]
# Inputs are copied from reviewed caches, never trusted just because present.
copy_locked_files() {
 local input_root=$1 manifest=$2 destination=$3 hash relative resolved
 mkdir -p "$destination"
 while read -r hash relative; do
  relative=${relative#./}
  [[ $hash =~ ^[a-f0-9]{64}$ && $relative != /* && "/$relative/" != */../* && "/$relative/" != */./* ]]
  resolved=$(realpath "$input_root/$relative")
  [[ $resolved == "$input_root/"* && -f $resolved ]]
  mkdir -p "$(dirname "$destination/$relative")"
  cp --reflink=auto --preserve=mode,timestamps -- "$resolved" "$destination/$relative"
 done < "$manifest"
}
# Copy only allowlisted dependency files; never publish arbitrary cache contents.
copy_locked_files "$INPUT/runtime-resources" "$RECIPE/resources.sha256" "$OUT/work/resources"
copy_locked_files "$INPUT/native-libs" "$RECIPE/native.sha256" "$OUT/work/native"
(cd "$OUT/work/resources" && sha256sum -c "$RECIPE/resources.sha256")
(cd "$OUT/work/native" && sha256sum -c "$RECIPE/native.sha256")
BASE_ARCHIVE=$INPUT/$(jq -er '.frozen_base.archive' "$LOCK")
[[ $(sha256sum "$BASE_ARCHIVE" | cut -d' ' -f1) == $(jq -er '.frozen_base.archive_sha256' "$LOCK") ]]
cp "$RECIPE"/{dictionary.sha256,base-packages.tsv} "$OUT/logs/"
BASE_ID=$(jq -er '.frozen_base.image_id' "$LOCK")
DOCKER=(sudo -n docker)
"${DOCKER[@]}" load --input "$BASE_ARCHIVE"
BASE_TAG=psr-runtime-producer-base:${PRODUCER:0:12}
"${DOCKER[@]}" tag "$BASE_ID" "$BASE_TAG"
[[ $("${DOCKER[@]}" image inspect "$BASE_TAG" --format '{{.Id}}') == "$BASE_ID" ]]
"${DOCKER[@]}" run --rm --runtime=runc --network=none --read-only --cpus=1 --memory=1g --pids-limit=128 \
 --entrypoint /usr/bin/dpkg-query "$BASE_TAG" -W '-f=${binary:Package}\t${Version}\n' > "$OUT/logs/base-packages.actual.tsv"
cmp "$RECIPE/base-packages.tsv" "$OUT/logs/base-packages.actual.tsv"
RUN_TAG=psr-runtime-producer:${SOURCE:0:12}-${PRODUCER:0:12}
# Build in isolated containers with no cloud/database credentials. CPU only.
mkdir -p "$OUT/work"/{gomod,gocache,tmp,npm,tmp-web}
if [[ -d "$INPUT/gomodcache" ]]; then cp --reflink=auto -a "$INPUT/gomodcache/." "$OUT/work/gomod/"; fi
if [[ -d "$INPUT/gocache" ]]; then cp --reflink=auto -a "$INPUT/gocache/." "$OUT/work/gocache/"; fi
"${DOCKER[@]}" run --rm --runtime=runc --user 0:0 --env HOME=/root --read-only \
 --cpus=2 --cpuset-cpus=6,7 --memory=10g --pids-limit=512 --network=host \
 --env "HTTPS_PROXY=${HTTPS_PROXY:-}" --env "HTTP_PROXY=${HTTP_PROXY:-}" --env "NO_PROXY=${NO_PROXY:-}" \
 --env NVIDIA_VISIBLE_DEVICES=void --env CUDA_VISIBLE_DEVICES= --env GOTOOLCHAIN=local \
 --env GOMAXPROCS=2 --env 'GOFLAGS=-p=2 -buildvcs=false' --env GIT_TERMINAL_PROMPT=0 \
 --env GOPROXY=https://proxy.golang.org,direct --env CC=clang --env CXX=clang++ \
 --env GOMODCACHE=/root/.cache/gomod --env GOCACHE=/root/.cache/gobuild \
 --mount "type=bind,src=$APP,dst=/workspace" \
 --mount "type=bind,src=$OUT/work/native,dst=/root/ragflow-native-libs" \
 --mount "type=bind,src=$OUT/work/gomod,dst=/root/.cache/gomod" \
 --mount "type=bind,src=$OUT/work/gocache,dst=/root/.cache/gobuild" \
 --mount "type=bind,src=$OUT/work/tmp,dst=/tmp" \
 --workdir /workspace --entrypoint /bin/bash "$RUNNER" -ec \
 'go version; clang --version | head -1; ld.lld --version; go mod download; go mod verify; bash build.sh --all' | tee "$OUT/logs/go-build.log"
"${DOCKER[@]}" run --rm --runtime=runc --user 0:0 --env HOME=/root --read-only \
 --cpus=2 --cpuset-cpus=6,7 --memory=12g --pids-limit=512 --network=host \
 --env "HTTPS_PROXY=${HTTPS_PROXY:-}" --env "HTTP_PROXY=${HTTP_PROXY:-}" --env "NO_PROXY=${NO_PROXY:-}" \
 --env NVIDIA_VISIBLE_DEVICES=void --env CUDA_VISIBLE_DEVICES= \
 --env NODE_OPTIONS=--max-old-space-size=8192 --env VITE_BUILD_SOURCEMAP=false --env VITE_MINIFY=esbuild \
 --mount "type=bind,src=$APP,dst=/workspace" \
 --mount "type=bind,src=$OUT/work/npm,dst=/root/.npm" \
 --mount "type=bind,src=$OUT/work/tmp-web,dst=/tmp" \
 --workdir /workspace/web --entrypoint /bin/bash "$RUNNER" -ec \
 'node --version; npm --version; npm ci; npm run build' | tee "$OUT/logs/frontend-build.log"
[[ -z $(git -C "$APP" status --porcelain --untracked-files=no) ]]
mkdir -p "$OUT/work"/{go-stage/ragflow/bin,web-stage/ragflow/web}
cp "$APP/bin/ragflow_server" "$OUT/work/go-stage/ragflow/bin/"
cp -a "$APP/web/dist" "$OUT/work/web-stage/ragflow/web/"
git -C "$APP" describe --tags --match='v*' --first-parent --always > "$OUT/work/web-stage/ragflow/VERSION"
export BUILDX_METADATA_PROVENANCE=disabled
"${DOCKER[@]}" buildx build --platform linux/amd64 --target production --network none \
 --provenance=false --sbom=false --build-context "base=docker-image://$BASE_TAG" \
 --build-context "infiniflow/ragflow_deps:latest=$OUT/work/resources" \
 --build-context "ubuntu:24.04=docker-image://ubuntu@$(jq -er ' .ubuntu.index' "$LOCK")" \
 --build-context "runner-amd64=docker-image://$RUNNER" \
 --build-context "go-builder=$OUT/work/go-stage" --build-context "web-builder=$OUT/work/web-stage" \
 --label "org.opencontainers.image.revision=$SOURCE" \
 --label "io.psr.producer.revision=$PRODUCER" \
 --output "type=oci,dest=$OUT/payload/core-runtime.oci.tar" \
 --metadata-file "$OUT/logs/buildx-metadata.json" --tag "$RUN_TAG" --file "$APP/Dockerfile" "$APP" 2>&1 \
 | tee "$OUT/logs/assemble.log"
mkdir "$OUT/work/oci"
tar --no-same-owner -xf "$OUT/payload/core-runtime.oci.tar" -C "$OUT/work/oci"
# Standalone stdlib inspector is a packaging tool, not a backend build.
"${DOCKER[@]}" run --rm --runtime=runc --user 0:0 --env HOME=/root --read-only \
 --cpus=1 --memory=1g --pids-limit=128 --network=none --env GOTOOLCHAIN=local \
 --env GOCACHE=/tmp/gocache --tmpfs /tmp:rw,exec,nosuid,nodev,size=512m \
 --mount "type=bind,src=$CONTROL,dst=/control,readonly" \
 --mount "type=bind,src=$OUT/work/oci,dst=/oci,readonly" \
 --workdir /control --entrypoint /bin/bash "$RUNNER" -ec \
 'go run ./psr/runtime /oci "$1"' -- "$SOURCE" > "$OUT/logs/oci-inspection.json"
"${DOCKER[@]}" load --input "$OUT/payload/core-runtime.oci.tar"
# Verify packaged runtime content before emitting a manifest. Full image resource
# inventory includes configuration templates from public source, never customer config.
"${DOCKER[@]}" run --rm --runtime=runc --network=none --read-only --cpus=1 --memory=2g --pids-limit=128 \
 --entrypoint /bin/bash "$RUN_TAG" -ec \
 'cd /; find ragflow/bin ragflow/web/dist ragflow/internal/rag/res/deepdoc ragflow/deps ragflow/conf ragflow/agent/templates ragflow/internal/ingestion/knowledge_compile/templates usr/share/infinity/resource -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum; sha256sum ragflow/VERSION ragflow/entrypoint.sh usr/local/share/fonts/truetype/noto/NotoSansCJKsc-VF.ttf etc/nginx/nginx.conf etc/nginx/proxy.conf etc/nginx/conf.d/ragflow.conf' \
 > "$OUT/logs/image-resources.sha256"
# Compare resources to their actual producer inputs, rather than merely listing
# whatever happened to be in the image.
expected=$OUT/logs/expected-image-files.sha256
: > "$expected"
expect_file() { printf '%s  %s\n' "$(sha256sum "$1" | cut -d' ' -f1)" "$2" >> "$expected"; }
expect_file "$APP/bin/ragflow_server" ragflow/bin/ragflow_server
expect_file "$OUT/work/web-stage/ragflow/VERSION" ragflow/VERSION
expect_file "$OUT/work/resources/cl100k_base.tiktoken" ragflow/deps/cl100k_base.tiktoken
expect_file "$OUT/work/resources/NotoSansCJKsc-VF.ttf" usr/local/share/fonts/truetype/noto/NotoSansCJKsc-VF.ttf
for f in det.ort layout.ort tsr.ort rec.ort ocr.res; do expect_file "$OUT/work/resources/huggingface.co/InfiniFlow/deepdoc/$f" "ragflow/internal/rag/res/deepdoc/$f"; done
for asset in BAAI/bge-m3/sentencepiece.bpe.model BAAI/bge-large-en-v1.5/vocab.txt Qwen/Qwen3-Embedding-0.6B/tokenizer.json intfloat/e5-mistral-7b-instruct/tokenizer.json; do
 expect_file "$OUT/work/resources/huggingface.co/$asset" "ragflow/deps/huggingface.co/$asset"
done
while IFS= read -r -d '' f; do expect_file "$f" "ragflow/web/dist/${f#"$APP/web/dist/"}"; done < <(find "$APP/web/dist" -type f -print0 | LC_ALL=C sort -z)
while read -r hash rel; do printf '%s  usr/share/infinity/resource/%s\n' "$hash" "$rel" >> "$expected"; done < "$RECIPE/dictionary.sha256"
while IFS= read -r -d '' f; do expect_file "$f" "ragflow/conf/${f#"$APP/conf/"}"; done < <(find "$APP/conf" -type f -print0 | LC_ALL=C sort -z)
expect_file "$APP/docker/service_conf.yaml.template" ragflow/conf/service_conf.yaml.template
expect_file "$APP/docker/entrypoint.sh" ragflow/entrypoint.sh
for f in nginx.conf proxy.conf; do expect_file "$APP/docker/nginx/$f" "etc/nginx/$f"; done
expect_file "$APP/docker/nginx/ragflow.conf" etc/nginx/conf.d/ragflow.conf
for dir in internal/agent/templates internal/ingestion/knowledge_compile/templates; do
 while IFS= read -r -d '' f; do
  if [[ $dir == internal/agent/templates ]]; then target="ragflow/agent/templates/${f#"$APP/$dir/"}"; else target="ragflow/$dir/${f#"$APP/$dir/"}"; fi
  expect_file "$f" "$target"
 done < <(find "$APP/$dir" -type f -print0 | LC_ALL=C sort -z)
done
while IFS= read -r expected_file; do grep -Fx "$expected_file" "$OUT/logs/image-resources.sha256" >/dev/null; done < "$expected"
BINARY_SHA=$(sha256sum "$APP/bin/ragflow_server" | cut -d' ' -f1)
grep -Fx "$BINARY_SHA  ragflow/bin/ragflow_server" "$OUT/logs/image-resources.sha256"
"${DOCKER[@]}" run --rm --runtime=runc --network=none --read-only --cpus=1 --memory=1g --pids-limit=128 \
 --entrypoint /bin/bash "$RUN_TAG" -ec 'ldd /ragflow/bin/ragflow_server' > "$OUT/logs/ldd.log"
! grep -F 'not found' "$OUT/logs/ldd.log"
for mode in api admin ingestor syncer; do
 set +e
 "${DOCKER[@]}" run --rm --runtime=runc --network=none --read-only --cpus=1 --memory=2g --pids-limit=128 \
  --entrypoint /usr/bin/timeout "$RUN_TAG" 20 /ragflow/bin/ragflow_server "--$mode" --help > "$OUT/logs/help-$mode.log" 2>&1
 rc=$?; set -e
 [[ $rc == 1 ]]; grep -F -- "--$mode" "$OUT/logs/help-$mode.log" >/dev/null
done
"${DOCKER[@]}" run --rm --runtime=runc --network=none --cpus=1 --memory=2g --pids-limit=128 \
 --cap-drop ALL --security-opt no-new-privileges "$RUN_TAG" \
 --disable-api-server --disable-ingestor --disable-syncer > "$OUT/logs/entrypoint-disabled.log" 2>&1
"${DOCKER[@]}" run --rm --runtime=runc --network=none --read-only --cpus=1 --memory=1g --pids-limit=128 \
 --tmpfs /var/log/nginx --tmpfs /var/cache/nginx --tmpfs /run \
 --entrypoint /usr/sbin/nginx "$RUN_TAG" -t > "$OUT/logs/nginx.log" 2>&1
jq -Rn '[inputs|capture("^(?<sha256>[0-9a-f]{64})  (?<path>.+)$")]' < "$RECIPE/native.sha256" > "$OUT/logs/native-inputs.json"
jq -ncS --slurpfile lock "$LOCK" --slurpfile native "$OUT/logs/native-inputs.json" \
 --arg resources "$(sha256sum "$RECIPE/resources.sha256" | cut -d' ' -f1)" \
 --arg dictionary "$(sha256sum "$RECIPE/dictionary.sha256" | cut -d' ' -f1)" \
 --arg packages "$(sha256sum "$RECIPE/base-packages.tsv" | cut -d' ' -f1)" \
 '{schema:1,inputs:$lock[0],native_files:$native[0],resource_checksums_sha256:$resources,dictionary_checksums_sha256:$dictionary,base_packages_sha256:$packages}' \
 > "$OUT/dependencies/dependency-manifest.json"
DEPENDENCY_SHA=$(sha256sum "$OUT/dependencies/dependency-manifest.json" | cut -d' ' -f1)
jq -Rn --arg source "$SOURCE" --slurpfile inputs "$OUT/dependencies/dependency-manifest.json" \
 '{schema:1,source_sha:$source,platform:"linux/amd64",dependency_manifest:$inputs[0],files:[inputs|capture("^(?<sha256>[0-9a-f]{64})  (?<path>.+)$")]}' \
 < "$OUT/logs/image-resources.sha256" > "$OUT/payload/runtime-resources.json"
(cd "$RECIPE"; find . -maxdepth 1 -type f ! -name README.md -print0 | LC_ALL=C sort -z | xargs -0 sha256sum) > "$OUT/logs/recipe-files.sha256"
RECIPE_SHA=$(sha256sum "$OUT/logs/recipe-files.sha256" | cut -d' ' -f1)
jq -n --arg source "$SOURCE" --arg producer "$PRODUCER" --arg binary "$BINARY_SHA" \
 --arg resource "$(sha256sum "$OUT/payload/runtime-resources.json" | cut -d' ' -f1)" \
 --arg dependency "$DEPENDENCY_SHA" --arg recipe "$RECIPE_SHA" --arg archive "$(sha256sum "$OUT/payload/core-runtime.oci.tar" | cut -d' ' -f1)" \
 --arg api "$(sha256sum "$APP/psr/GO_AUTHORIZATION_CONTRACT.md" | cut -d' ' -f1)" \
 --arg build "$(sha256sum "$APP/psr/GO_CORE_BUILD_AND_RUN.md" | cut -d' ' -f1)" \
 --slurpfile oci "$OUT/logs/oci-inspection.json" \
 '{schema:1,runtime_kind:"complete-runtime",source_sha:$source,producer_source_sha:$producer,platform:"linux/amd64",image_digest:$oci[0].image_digest,config_digest:$oci[0].config_digest,blobs:$oci[0].blobs,archive_sha256:$archive,resource_manifest_sha256:$resource,binary_sha256:$binary,recipe_sha256:$recipe,dependency_manifest_sha256:$dependency,api_contract:{path:"psr/GO_AUTHORIZATION_CONTRACT.md",sha256:$api},build_contract:{path:"psr/GO_CORE_BUILD_AND_RUN.md",sha256:$build}}' \
 > "$OUT/payload/source-manifest.json"
# Keep frozen dependencies available independently of the runtime ZIP contract.
cp --reflink=auto "$BASE_ARCHIVE" "$OUT/dependencies/production-base.image.tar"
cp "$LOCK" "$RECIPE"/{resources.sha256,native.sha256,dictionary.sha256,base-packages.tsv} "$OUT/dependencies/"
cp --reflink=auto -a "$OUT/work/resources" "$OUT/dependencies/runtime-resources"
copy_locked_files "$OUT/work/native" "$RECIPE/native.sha256" "$OUT/dependencies/native-libs"
printf 'state=PRODUCED application_source=%s producer_source=%s\n' "$SOURCE" "$PRODUCER" > "$OUT/RESULT"
cat "$OUT/payload/source-manifest.json"
