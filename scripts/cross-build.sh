#!/usr/bin/env bash
# Cross-compile gcode for the supported release matrix with CGO disabled.
# Mirrors the CI `cross` job; used locally via `make cross`.
set -euo pipefail

cd "$(dirname "$0")/.."

export CGO_ENABLED=0
mkdir -p dist

TARGETS=(
  "linux/amd64"
  "linux/arm64"
  "darwin/amd64"
  "darwin/arm64"
  "windows/amd64"
  "freebsd/amd64"
)

for target in "${TARGETS[@]}"; do
  goos="${target%%/*}"
  goarch="${target##*/}"
  ext=""
  [[ "$goos" == "windows" ]] && ext=".exe"
  out="dist/gcode_${goos}_${goarch}${ext}"
  echo "building $out"
  GOOS="$goos" GOARCH="$goarch" go build -trimpath -o "$out" ./cmd/gcode
done

echo "done:"
ls -lh dist
