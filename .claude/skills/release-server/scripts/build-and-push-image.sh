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
  --build-arg VERSION="${VERSION}" \
  --push \
  "$SERVER_DIR"

echo
echo "Pushed:"
echo "  ${IMAGE}:${VERSION}"
echo "  ${IMAGE}:latest"
echo
echo "Verify: docker buildx imagetools inspect ${IMAGE}:${VERSION}"

# ---------------------------------------------------------------------------
# Bump the pinned image tag in server/docker-compose.prebuilt.yml so app-store
# installs (Umbrel/ZimaOS/TrueNAS/Unraid pulling the prebuilt image) get this
# release on their next pull, instead of letting that file drift arbitrarily
# far behind what's actually published.
#
# Assumption: this script runs on `main` at the exact commit that is about to
# be tagged (see Phase 2/3 in SKILL.md). Committing the bump here means the
# compose-bump commit always lands immediately before its release tag, never
# on top of unrelated later work.
# ---------------------------------------------------------------------------
echo
echo "==> bumping pinned image tag in server/docker-compose.prebuilt.yml to ${VERSION}"
COMPOSE_FILE="$SERVER_DIR/docker-compose.prebuilt.yml"

# Guard 1: the pinned image line must exist — fail loudly instead of silently
# no-op'ing (a drift bug would otherwise go unnoticed).
if ! grep -q "image: ${IMAGE}:" "$COMPOSE_FILE"; then
  echo "ERROR: no 'image: ${IMAGE}:...' line found in ${COMPOSE_FILE}" >&2
  exit 1
fi

sed -i.bak "s|image: ${IMAGE}:[^[:space:]]*|image: ${IMAGE}:${VERSION}|" "$COMPOSE_FILE"
rm -f "${COMPOSE_FILE}.bak"

# Guard 2: the substitution must have actually landed.
if ! grep -q "image: ${IMAGE}:${VERSION}" "$COMPOSE_FILE"; then
  echo "ERROR: failed to set image tag to ${IMAGE}:${VERSION} in ${COMPOSE_FILE}" >&2
  exit 1
fi

echo "  ${COMPOSE_FILE} now pins ${IMAGE}:${VERSION}"

# Commit locally only — pushing stays part of Phase 3's confirm-with-the-user
# flow, so the operator reviews the bump commit before anything goes remote.
git -C "$REPO_ROOT" add server/docker-compose.prebuilt.yml
git -C "$REPO_ROOT" commit -m "chore: bump docker-compose.prebuilt.yml to ${VERSION}"
echo "  committed: $(git -C "$REPO_ROOT" log -1 --oneline)"
