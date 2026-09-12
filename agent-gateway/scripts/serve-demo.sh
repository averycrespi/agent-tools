#!/usr/bin/env bash
set -euo pipefail
MODULE="$(dirname -- "${BASH_SOURCE[0]}")/.."
# Keep the bootstrap executable outside disposable credential/data roots.
go -C "$MODULE" build -mod=readonly -tags=e2e -o .demo-bin/serve-demo ./test/demo
exec "$MODULE/.demo-bin/serve-demo" "$@"
