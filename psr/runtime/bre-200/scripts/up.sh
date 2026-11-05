#!/usr/bin/env bash
set -euo pipefail
DIR="$(cd "$(dirname "$0")" && pwd)"
"$DIR/compose.sh" up -d "$@"
"$DIR/compose.sh" ps
