#!/usr/bin/env bash
# Builds eirl-engine (the Go engine, cgo against the board's GStreamer 1.14) for linux/arm64 in the
# eirl-engine-build image (engine.Dockerfile). Output: build/out/eirl-engine.
#   build/engine.sh            → tests of the engine (Mac and image) + the binary
set -euo pipefail
cd "$(dirname "$0")/.."
# EIRL_VERSION: the release version (tools/release.sh); otherwise git describe
ver="${EIRL_VERSION:-$(git describe --always --dirty 2>/dev/null || echo dev)}"
docker image inspect eirl-engine-build >/dev/null 2>&1 ||
  docker build --platform linux/arm64 -f build/engine.Dockerfile -t eirl-engine-build build
mkdir -p build/out
(cd manager && go test ./internal/engine/... ./internal/service/ >/dev/null)
docker run --rm --platform linux/arm64 -v "$PWD/manager":/src -v "$PWD/build/out":/out -v eirl-engine-gocache:/go \
  eirl-engine-build sh -c "go test ./internal/gst/ ./internal/engine/displayhold/ >/dev/null && \
    go build -trimpath -ldflags '-s -w -X main.version=$ver' -o /out/eirl-engine ./cmd/eirl-engine"
echo "build/out/eirl-engine $ver ($(du -h build/out/eirl-engine | cut -f1))"
