#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

usage() {
    cat <<'EOF'
Build and package the PSR shared Go core for Linux.

Usage:
  bash psr/package-go-core.sh

The script requires a clean git worktree, builds with `bash build.sh --all`,
smoke-checks both binaries, and writes a source-pinned archive under dist/.
The archive contains binaries only; runtime configuration, models, tokenizer
assets, dictionaries, and service dependencies must be provided separately.
EOF
}

if [[ "${1:-}" == "--help" || "${1:-}" == "-h" ]]; then
    usage
    exit 0
fi

if [[ "$#" -ne 0 ]]; then
    usage >&2
    exit 2
fi

if [[ "$(go env GOOS)" != "linux" ]]; then
    echo "error: PSR Go core artifacts must be built on a Linux host" >&2
    exit 1
fi

if [[ -n "$(git -C "$ROOT" status --porcelain --untracked-files=no)" ]]; then
    echo "error: tracked source changes prevent mapping the artifact to one commit" >&2
    exit 1
fi

for command in sha256sum tar; do
    if ! command -v "$command" >/dev/null 2>&1; then
        echo "error: required command not found: $command" >&2
        exit 1
    fi
done

if ! tar --version | head -n 1 | grep -q 'GNU tar'; then
    echo "error: GNU tar is required to normalize archive metadata" >&2
    exit 1
fi

SOURCE_SHA="$(git -C "$ROOT" rev-parse HEAD)"
SOURCE_DATE_EPOCH="$(git -C "$ROOT" show -s --format=%ct "$SOURCE_SHA")"
GO_VERSION="$(go version)"
GOARCH="$(go env GOARCH)"
ARTIFACT_DIR="${PSR_ARTIFACT_DIR:-$ROOT/dist/psr-go-core}"
ARTIFACT_NAME="psr-go-core-${SOURCE_SHA}-linux-${GOARCH}.tar.gz"

bash "$ROOT/build.sh" --all

SERVER="$ROOT/bin/ragflow_server"
CLI="$ROOT/bin/ragflow-cli"
for binary in "$SERVER" "$CLI"; do
    if [[ ! -x "$binary" ]]; then
        echo "error: expected executable was not built: $binary" >&2
        exit 1
    fi
done

expect_server_output() {
    local expected="$1"
    shift
    local output
    local status
    if output="$("$@" 2>&1)"; then
        status=0
    else
        status=$?
    fi
    if [[ "$status" -ne 1 || "$output" != *"$expected"* ]]; then
        echo "error: $* did not produce the expected server output (exit $status)" >&2
        printf '%s\n' "$output" >&2
        exit 1
    fi
}

expect_server_output "RAGFlow version:" "$SERVER" --api --version
expect_server_output "RAGFlow API Server" "$SERVER" --api --help
if ! "$CLI" --version | grep -q "RAGFlow CLI version"; then
    echo "error: CLI version command did not produce its version output" >&2
    exit 1
fi

STAGING_DIR="$(mktemp -d)"
trap 'rm -rf "$STAGING_DIR"' EXIT
mkdir -p "$STAGING_DIR/bin"
cp "$SERVER" "$CLI" "$STAGING_DIR/bin/"
cp "$ROOT/LICENSE" \
    "$ROOT/psr/GO_AUTHORIZATION_CONTRACT.md" \
    "$ROOT/psr/GO_CORE_BUILD_AND_RUN.md" \
    "$STAGING_DIR/"

cat >"$STAGING_DIR/BUILD-MANIFEST.txt" <<EOF
Product: PSR shared Go core
Source commit: $SOURCE_SHA
Target: linux/$GOARCH
Go toolchain: $GO_VERSION
Build command: bash build.sh --all
Runtime contract: psr/GO_AUTHORIZATION_CONTRACT.md
EOF

(cd "$STAGING_DIR" && sha256sum \
    LICENSE \
    GO_AUTHORIZATION_CONTRACT.md \
    GO_CORE_BUILD_AND_RUN.md \
    bin/ragflow_server \
    bin/ragflow-cli \
    BUILD-MANIFEST.txt > SHA256SUMS)

mkdir -p "$ARTIFACT_DIR"
tar --sort=name \
    --mtime="@$SOURCE_DATE_EPOCH" \
    --owner=0 \
    --group=0 \
    --numeric-owner \
    -C "$STAGING_DIR" \
    -czf "$ARTIFACT_DIR/$ARTIFACT_NAME" \
    BUILD-MANIFEST.txt SHA256SUMS LICENSE GO_AUTHORIZATION_CONTRACT.md \
    GO_CORE_BUILD_AND_RUN.md bin/ragflow_server bin/ragflow-cli
(cd "$ARTIFACT_DIR" && sha256sum "$ARTIFACT_NAME" > "$ARTIFACT_NAME.sha256")

printf 'Created %s\n' "$ARTIFACT_DIR/$ARTIFACT_NAME"
