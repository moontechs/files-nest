#!/usr/bin/env bash
# build-binaries.sh — cross-compile static FilesNest server binaries for
# linux/amd64 and linux/arm64, package as tarballs, and checksum them.
#
# Usage: build-binaries.sh <version e.g. 0.4.0>
set -euo pipefail

VERSION="${1:?usage: build-binaries.sh <version e.g. 0.4.0>}"

REPO_ROOT="$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --show-toplevel)"
SERVER_DIR="$REPO_ROOT/server"
DIST_DIR="$SERVER_DIR/dist"

command -v go >/dev/null || { echo "go not found"; exit 1; }

rm -rf "$DIST_DIR"
mkdir -p "$DIST_DIR"

build() {
  local os="$1" arch="$2"
  local name="files-nest-server_${VERSION}_${os}_${arch}"
  local outdir="$DIST_DIR/$name"

  echo "==> building ${os}/${arch}"
  mkdir -p "$outdir"
  (
    cd "$SERVER_DIR"
    CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
      go build -trimpath -ldflags="-s -w" -o "$outdir/server" .
  )
  cp "$SERVER_DIR/README.md" "$outdir/" 2>/dev/null || true

  (cd "$DIST_DIR" && tar -czf "${name}.tar.gz" "$name")
  rm -rf "$outdir"
  echo "    -> dist/${name}.tar.gz"
}

build linux amd64
build linux arm64

echo "==> checksums"
(cd "$DIST_DIR" && shasum -a 256 ./*.tar.gz | tee checksums.txt)

echo
echo "Built: $(ls "$DIST_DIR"/*.tar.gz | wc -l | tr -d ' ') tarballs in $DIST_DIR"
