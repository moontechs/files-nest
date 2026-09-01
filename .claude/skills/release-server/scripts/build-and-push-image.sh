#!/usr/bin/env bash
# build-and-push-image.sh — build a multi-arch (linux/amd64 + linux/arm64)
# FilesNest server Docker image with buildx and push it to GHCR.
#
# This replaces the old .github/workflows/publish-image.yml CI job, which
# only ever built linux/amd64 (no `platforms:` on the build-push-action, so
# it built for the runner's native arch only). Images are now built and
# pushed from a developer machine as part of cutting a release, using
# whichever local buildx builder already has multi-platform support
# (Docker Desktop's default builder does out of the box).
#
# Usage: build-and-push-image.sh <version e.g. 0.3.1>
set -euo pipefail

VERSION="${1:?usage: build-and-push-image.sh <version e.g. 0.3.1>}"
IMAGE="ghcr.io/moontechs/files-nest"

REPO_ROOT="$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --show-toplevel)"
SERVER_DIR="$REPO_ROOT/server"

command -v docker >/dev/null || { echo "docker not found"; exit 1; }
docker buildx version >/dev/null 2>&1 || { echo "docker buildx not available"; exit 1; }

echo "==> building + pushing ${IMAGE}:${VERSION} and :latest (linux/amd64, linux/arm64)"
docker buildx build \
  --platform linux/amd64,linux/arm64 \
  --tag "${IMAGE}:${VERSION}" \
  --tag "${IMAGE}:latest" \
  --push \
  "$SERVER_DIR"

echo
echo "Pushed:"
echo "  ${IMAGE}:${VERSION}"
echo "  ${IMAGE}:latest"
echo
echo "Verify: docker buildx imagetools inspect ${IMAGE}:${VERSION}"
