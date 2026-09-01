---
name: release-server
description: Cuts a new FilesNest server (Go backend) release — cross-compiles static linux/amd64 and linux/arm64 binaries, builds and pushes a multi-arch (amd64 + arm64) Docker image to GHCR from the local machine, and attaches the binaries plus image links to the GitHub Release for the version tag. Use this whenever the user wants to release, ship, or publish a new version of the FilesNest server, cut server binaries or Docker images for a homeserver install (Intel or ARM/Raspberry Pi), or attach downloadable server artifacts to a GitHub release — even if they don't say "release" explicitly (e.g. "build the server for arm and intel" or "make a linux binary people can just download").
---

# Release the FilesNest server

There is no CI workflow for this anymore — releases build and push
everything locally. (An earlier `.github/workflows/publish-image.yml` did
this on every tag push, but it only ever built `linux/amd64` since it never
set `platforms:` on `docker/build-push-action`; it was removed once the
local multi-arch flow below replaced it. Don't recreate it without the user
asking — that's a deliberate choice, not an oversight.)

Two artifact families come out of a release:

1. **Static binaries** for `linux/amd64` and `linux/arm64`, attached directly
   to the GitHub Release — for anyone who wants `./server` on a homeserver
   without Docker.
2. **A multi-arch Docker image** (`linux/amd64` + `linux/arm64` in one
   manifest) pushed to `ghcr.io/moontechs/files-nest`, built locally with
   `docker buildx`.

Server tags are bare `X.Y.Z` — **no `v` prefix** (see `git tag -l`:
`0.1.0` … `0.3.0`).

## Before releasing

Run the project's own quality gate — `server/CLAUDE.md` says tests and lint
must pass before a change is considered done, and a release is the least
forgiving place to skip that:

```bash
cd server && make lint && make test
```

## Phase 1 — build the binaries

```bash
.claude/skills/release-server/scripts/build-binaries.sh <version>
```

This cross-compiles two static (`CGO_ENABLED=0`) binaries — matching how the
project already builds the server (`server/Dockerfile`, `server/CLAUDE.md`:
"No CGO... pure Go" — this isn't a new constraint, just applied to two
`GOOS`/`GOARCH` pairs instead of one):

- `linux/amd64` — typical NAS / home server / cloud VM.
- `linux/arm64` — Raspberry Pi 4/5 and other ARM homeservers.

Each is packaged as `server/dist/files-nest-server_<version>_<os>_<arch>.tar.gz`,
plus a `checksums.txt` with SHA-256 sums of every tarball.

## Phase 2 — build and push the Docker image

```bash
.claude/skills/release-server/scripts/build-and-push-image.sh <version>
```

This runs `docker buildx build --platform linux/amd64,linux/arm64 --push`
against `server/Dockerfile` and pushes `ghcr.io/moontechs/files-nest:<version>`
and `:latest` as a single multi-arch manifest — `docker pull` on either
architecture picks the right layer automatically. Requires a buildx builder
with multi-platform support (Docker Desktop's default builder has this) and
being logged in to `ghcr.io` (`docker login ghcr.io`).

## Phase 3 — tag and release (confirm with the user first)

Check whether a release already exists for this tag — the `new`/release-notes
skill may have created one from merged PRs before this skill ever ran:

```bash
git tag <version>
git push origin main --tags
gh release view <version> --repo moontechs/files-nest \
  || gh release create <version> --repo moontechs/files-nest --generate-notes
gh release upload <version> server/dist/*.tar.gz server/dist/checksums.txt \
  --repo moontechs/files-nest
```

Then append the Docker image links to the release body (`gh release edit
<version> --notes-file -` or edit via `--notes`) so testers/users see both
distribution paths from the release page, not just the binary attachments.

Pushing the tag and creating/editing a public GitHub Release are the two
irreversible-ish, visible-to-others actions here — don't run them without
the user confirming the version number is right.

## After publishing

Tell the user both download paths now exist for this version:

- Docker (amd64 or arm64, same tag): `docker pull ghcr.io/moontechs/files-nest:<version>`
- Direct binary: the tarball matching their server's OS/arch from the GitHub
  Release, e.g.:
  ```bash
  curl -LO https://github.com/moontechs/files-nest/releases/download/<version>/files-nest-server_<version>_linux_arm64.tar.gz
  tar xzf files-nest-server_<version>_linux_arm64.tar.gz
  ./files-nest-server_<version>_linux_arm64/server
  ```
