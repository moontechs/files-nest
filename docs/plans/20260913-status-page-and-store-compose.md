# Server status page and store-friendly deployment compose

## Overview

FilesNest's server is currently headless: `/health`, `/config`, and
`/uploads/...` are the only routes, and there is nothing a human can look at
in a browser to confirm the server is alive, which version it's running, or
whether it's misconfigured. This blocks a natural distribution path — Umbrel,
ZimaOS, TrueNAS, and Unraid app-store listings expect an installed app to be
legible from a browser without SSH — and the current `server/docker-
compose.yml` (builds from source, bundles Caddy, binds host ports 80/443) is
unsuitable for those stores anyway, since they expect a single prebuilt-image
container behind their own reverse proxy.

This plan adds:
1. An unauthenticated `GET /` status page — a small, static, four-card page
   showing that the server is running, its version, its address, an
   auth-disabled warning when relevant, and links to the macOS client and
   docs. No file listing, no stats, no credential display.
2. Version plumbing (`-ldflags -X main.version=...`) that doesn't exist
   anywhere in the binary today, needed to show a real version on the page.
3. A new `server/docker-compose.prebuilt.yml` — single container, prebuilt
   GHCR image, no Caddy, no host 80/443 — for app-store-style installs.
4. A `release-server` skill extension that keeps that compose file's pinned
   image tag from drifting out of sync with what's actually published.

Full design rationale lives in `docs/adr/0010-unauthenticated-status-page.md`
and the "Status page" glossary entry in `CONTEXT.md` (both already written).
Root `README.md` and `server/README.md` already reference
`docker-compose.prebuilt.yml` from an earlier step in this session — this
plan's compose file must match what those docs promise.

## Context (from discovery)

- Routing: `server/internal/api/router.go`, `NewRouter` — plain
  `http.NewServeMux()`, method-prefixed patterns, per-route
  `AuthMiddleware` applied to everything except `/health`.
- No templating/embed.FS/static-asset serving exists anywhere in `server/`
  today — this is greenfield.
- No version string exists anywhere in the binary (no `-X` ldflags, no
  `VERSION` file). `server/Makefile`'s `build` target and `server/Dockerfile`
  both build with `-ldflags="-s -w -extldflags=-static"`, no version arg.
- `server/main.go` (~line 117-129): if both `BACKUP_USER`/`BACKUP_PASS` are
  empty, the server logs a WARN and disables auth entirely (does not refuse
  to start); if only one is set, it fails to start
  (`errPartialBackupCredentials`). The status page mirrors the WARN case
  visually.
- `server/docker-compose.yml`: `build: .` for the server, plus a `caddy`
  service bound to host `80:80`/`443:443`. This plan's new compose is a
  sibling file, not a replacement.
- Server releases are versioned independently of the macOS app despite a
  shared git tag namespace. Confirmed via `gh release list` +
  `gh release view <tag>`: `0.3.1` is the latest tag with server binary/image
  assets attached (via `.claude/skills/release-server`); `0.4.x` tags are
  macOS-app-only (DMG assets). Re-verify this at implementation time in case
  a newer server release shipped since.
- `.claude/skills/release-server/scripts/build-and-push-image.sh` runs
  `docker buildx build --platform linux/amd64,linux/arm64 --tag
  ghcr.io/moontechs/files-nest:${VERSION} --tag ...:latest --push`, no
  `--build-arg` today.
- `.claude/skills/release-server/scripts/build-binaries.sh` cross-compiles
  with `go build -trimpath -ldflags="-s -w"`, no version injection.
- AppIcon assets available pre-sized, no resizing needed:
  `apple/macos/FilesNest/FilesNest/Assets.xcassets/AppIcon.appiconset/AppIcon-32.png`
  and `AppIcon-128.png`.
- Package layout convention (`server/CLAUDE.md`): one purpose-scoped package
  per concern (`internal/filestore`, `internal/orphans`, etc.) — this plan
  adds `internal/statuspage` alongside them.

## Development Approach

- **Testing approach**: Regular (code first, tests same task, before moving
  to the next task).
- Complete each task fully, tests passing, before starting the next.
- `make lint` must stay zero-tolerance clean after every task touching
  `server/`.
- No e2e test needed for the status page (no business logic, no Docker stack
  dependency) — one `httptest`-based unit test suffices, per `server/
  CLAUDE.md`'s "handler gets a matching `_test.go`" convention.

## Testing Strategy

- **Unit tests**: `internal/statuspage` gets a `_test.go` covering
  `Render`/the data-building logic: 200 status, version and address strings
  present in the body, auth-warning card present/absent depending on
  input. `internal/api` handler test (if the handler itself has any
  non-trivial logic beyond calling `statuspage.Render`) follows the same
  file's existing test patterns.
- **e2e tests**: none needed — this is a static, non-authenticated page with
  no interaction with `internal/uploadbackend` or BadgerDB.

## Progress Tracking

- Mark completed items with `[x]` immediately when done.
- Add newly discovered tasks with ➕ prefix.
- Document issues/blockers with ⚠️ prefix.

## Solution Overview

`internal/statuspage` is a small, self-contained package: one `embed.FS`
holding `status.html` (an `html/template`), `favicon.png`, and `logo.png`.
It exposes `Render(w http.ResponseWriter, data Data)` and a `Data` struct
(`Version`, `Address`, `AuthDisabled string/bool`). `internal/api/router.go`
registers `GET /` with a small handler that builds `Data` from the request's
`Host` header and the package-level `version` var (threaded from `main.go`),
then calls `statuspage.Render`. No auth middleware wraps this route.

Version flows in from build time: `main.go` declares `var version = "dev"`;
`-ldflags -X main.version=...` overrides it in `Makefile`, `Dockerfile`,
and the two release-server scripts. `main.go` passes `version` into the
router/handler wiring alongside the existing dependencies.

The new `docker-compose.prebuilt.yml` is a static sibling file to the
existing `docker-compose.yml` — no code shares between them, just a maintained
parallel deployment shape. `release-server`'s image-push script gets a new
step that `sed`s the pinned tag in that compose file to the version just
published and commits the change, closing the drift gap identified during
brainstorming.

## Technical Details

### `statuspage.Data`

```go
type Data struct {
    Version      string // e.g. "0.3.1" or "dev" — no "v" prefix
    Address      string // r.Host, e.g. "backup.example.com" or "192.168.1.50:8080"
    AuthDisabled bool
}
```

### Template structure (`status.html`)

Single HTML document, inline `<style>` (Mantine-evoking palette/spacing/
type scale, CSS grid for the 2x2/1-column card layout, no JS, no external
requests — `favicon.png`/`logo.png` served from new routes or inlined as
base64 data URIs; final choice made in Task 1 based on what keeps the
template simplest). Four cards, one `{{if .AuthDisabled}}` block for the
warning card; the other three always render.

### Router wiring

`http.ServeMux` (Go 1.22+) treats a pattern ending in `/` as a subtree match,
not an exact match — `"GET /"` would silently swallow every unmatched GET
path (typos, `/favicon.ico`, future removed routes) and return the status
page with 200 instead of a 404. Use the exact-match wildcard `"GET /{$}"`
instead, which matches only the literal root:

```go
mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
    statuspage.Render(w, statuspage.Data{
        Version:      version,
        Address:      r.Host,
        AuthDisabled: authCfg.Username == "" && authCfg.Password == "",
    })
})
```

Exact signature/wiring point (`NewRouter` args) determined in Task 2 once
`main.go`'s existing `authCfg`/version plumbing is in hand.

`Address` comes from the client-controlled `Host` header on an
unauthenticated route — `html/template`'s auto-escaping is what keeps this
safe to render. Do not switch this template to `text/template` for
convenience; that would turn `Address` into a reflected-content injection
point with nothing catching it.

### Version build-flag threading

- `server/main.go`: `var version = "dev"`.
- `server/Makefile` `build` target: append `-X main.version=$(VERSION)` to
  the existing `-ldflags`, with `VERSION ?= dev` as a Makefile default.
- `server/Dockerfile`: `ARG VERSION=dev` in the builder stage, appended to
  the existing `RUN CGO_ENABLED=0 go build -ldflags="-s -w -extldflags=
  -static -X main.version=${VERSION}" ...`.
- `build-binaries.sh`: add `-X main.version=${VERSION}` to its `-ldflags`.
- `build-and-push-image.sh`: add `--build-arg VERSION=${VERSION}` to the
  `docker buildx build` invocation.

### `docker-compose.prebuilt.yml`

Single `server` service: `image: ghcr.io/moontechs/files-nest:<pinned-tag>`
(no `build:`), env `BACKUP_USER`/`BACKUP_PASS`/`STORAGE_PATH=/data`/
`PORT=8080`, `ports: ["8080:8080"]`, volume `server-data:/data`, healthcheck
via `wget` against `/health` (mirroring `docker-compose.yml`'s pattern). No
`caddy` service, no `80`/`443`, no `Caddyfile` reference.

### Release-tag sync

New step appended to `build-and-push-image.sh` (or a short follow-up in the
same script, per plan Task 8): after a successful push,
`sed -i.bak "s|files-nest:[^\"[:space:]]*|files-nest:${VERSION}|" ../server/
docker-compose.prebuilt.yml` (exact pattern refined during implementation to
match the file's actual line), remove the `.bak`, then
`git add server/docker-compose.prebuilt.yml && git commit -m "chore: bump
docker-compose.prebuilt.yml to ${VERSION}"`.

## What Goes Where

- **Implementation Steps**: all code, template, asset, script, and doc
  changes below.
- **Post-Completion**: cutting an actual release to exercise the new
  release-server sub-step end-to-end; manual browser check of the page on
  desktop + mobile widths; manual install on a real Umbrel/ZimaOS/TrueNAS/
  Unraid box (out of scope for this repo's automation).

## Implementation Steps

### Task 1: Create the `internal/statuspage` package with template and assets

**Files:**
- Create: `server/internal/statuspage/statuspage.go`
- Create: `server/internal/statuspage/status.html`
- Create: `server/internal/statuspage/favicon.png` (copy of `apple/macos/FilesNest/FilesNest/Assets.xcassets/AppIcon.appiconset/AppIcon-32.png`)
- Create: `server/internal/statuspage/logo.png` (copy of `apple/macos/FilesNest/FilesNest/Assets.xcassets/AppIcon.appiconset/AppIcon-128.png`)
- Create: `server/internal/statuspage/statuspage_test.go`

- [x] copy the two PNG assets into the new package directory as-is (no resize)
- [x] write `status.html`: four-card responsive grid (2x2 desktop / 1-column mobile via media query), headline "FilesNest — Server is running", Status card (version, address), conditional Auth-warning card (`{{if .AuthDisabled}}`, warning color, text mirroring `main.go`'s WARN message), "Get the macOS app" card (install instructions/link reused from root `README.md`'s "Install the macOS app" section), Links card (GitHub repo + docs); inline `<style>` only, Mantine-evoking palette/spacing/typography, no JS
- [x] embed `status.html` + both PNGs via a single `embed.FS` in `statuspage.go`; deliver favicon/logo as inline base64 `data:` URIs in the template (decided — keeps `Render` a single `http.ResponseWriter` write, adds zero new unauthenticated routes/attack surface beyond `GET /` itself)
- [x] implement `type Data struct { Version, Address string; AuthDisabled bool }` and `func Render(w http.ResponseWriter, data Data) error` parsing/executing the embedded template
- [x] write tests for `Render`: 200-equivalent (no error), body contains `Version` and `Address` substrings, Auth-warning markup present when `AuthDisabled: true` and absent when `false`
- [x] write tests for malformed/edge inputs if any exist (e.g. empty `Address`) — otherwise note none apply and skip
- [x] run tests - must pass before task 2

### Task 2: Wire `GET /` into the router, unauthenticated

**Files:**
- Modify: `server/internal/api/router.go`
- Modify: `server/main.go`
- Modify (if needed based on Task 1's asset-delivery choice): `server/internal/api/router.go` (favicon/logo sub-routes)
- Create/Modify: matching `_test.go` for the new handler

- [x] add `var version = "dev"` to `server/main.go`
- [x] thread `version` and the existing `authCfg` into `NewRouter` (or wherever routes are registered) so the new handler can build `statuspage.Data`
- [x] register `GET /{$}` (exact-match wildcard — NOT `GET /`, which would subtree-match every unmatched GET path) in `router.go`, outside `AuthMiddleware`, calling `statuspage.Render` with `Address: r.Host`, `Version: version`, `AuthDisabled: authCfg.Username == "" && authCfg.Password == ""`
- [x] write tests: `httptest.NewServer`/`httptest.NewRecorder` hitting `GET /` end-to-end through the real router — 200, body contains version/address, auth-warning present/absent depending on an auth-disabled vs auth-configured router instance
- [x] write a test hitting an undefined path (e.g. `GET /nonexistent`) and assert it does NOT return the status page (confirms `GET /{$}` didn't regress into a catch-all)
- [x] write tests confirming `GET /` still returns content with `AuthMiddleware` untouched for the other routes (regression check — quick assertion that e.g. `GET /config` still 401s without credentials)
- [x] run tests - must pass before task 3

### Task 3: UI/UX pass on the status page

**Files:**
- Modify: `server/internal/statuspage/status.html`

- [x] run the `impeccable` skill against the drafted `status.html`/inline CSS for a UI/UX review pass (visual hierarchy, spacing, responsive behavior, accessibility basics — contrast, semantic HTML) (skipped - skill not installed in this environment; performed an equivalent manual UI/UX review pass covering the same dimensions)
- [x] apply the resulting recommendations directly to `status.html` (darkened `--success`/`--warning` to meet WCAG AA 4.5:1 contrast for the badge and warning heading; added a `:has()`-based full-width span for the last card when only three cards render so it no longer dangles half-empty; added `:focus-visible` outlines on links; added `text-size-adjust` guard for iOS)
- [x] re-run `internal/statuspage`'s tests to confirm the markup changes didn't break substring assertions (all assertions still pass unchanged — no wording changes, only CSS)
- [x] run tests - must pass before task 4

### Task 4: Version plumbing in build tooling

**Files:**
- Modify: `server/Makefile`
- Modify: `server/Dockerfile`

- [x] add `VERSION ?= dev` and append `-X main.version=$(VERSION)` to the `build` target's `-ldflags` in `server/Makefile`
- [x] add `ARG VERSION=dev` to the builder stage of `server/Dockerfile`, append `-X main.version=${VERSION}` to its existing `go build -ldflags=...` invocation
- [x] manually verify `cd server && make build && ./bin/server --help 2>/dev/null; VERSION=1.2.3 make build` produces a binary whose `main.version` reflects the passed value (a quick throwaway `go run` or checking via the status page's `Version` output is sufficient — no dedicated automated test needed for a Makefile/Dockerfile flag, per YAGNI) — verified via `go version -m` showing `-X main.version=dev` / `-X main.version=1.2.3` and runtime status page showing `Version: 1.2.3`
- [x] confirm `make lint` and `make test` still pass with no regressions — lint 0 issues, all 8 test packages ok
- [x] run tests - must pass before task 5

### Task 5: Version plumbing in release-server scripts

**Files:**
- Modify: `.claude/skills/release-server/scripts/build-binaries.sh`
- Modify: `.claude/skills/release-server/scripts/build-and-push-image.sh`

- [x] add `-X main.version=${VERSION}` to `build-binaries.sh`'s `go build -ldflags=...` call
- [x] add `--build-arg VERSION=${VERSION}` to `build-and-push-image.sh`'s `docker buildx build` invocation
- [x] manually verify both scripts still run cleanly against a throwaway version string in a dry run (build-binaries.sh ran for real with `9.9.9-test` — binary confirmed to contain the injected string; build-and-push-image.sh verified by `bash -n` + careful diff review since a full multi-arch push isn't practical locally — no docker daemon in this environment) — no automated test exists or is warranted for these release shell scripts, consistent with the rest of `.claude/skills/release-server` having no test suite
- [x] run `cd server && make lint && make test` once more to confirm nothing in `server/` regressed

### Task 6: New `docker-compose.prebuilt.yml`

**Files:**
- Create: `server/docker-compose.prebuilt.yml`

- [x] verify the actual latest server release tag via `gh release list --repo moontechs/files-nest` cross-checked against `gh release view <tag>` for server binary/image assets (confirmed as `0.3.1` during planning — re-check for anything newer before writing the file) — re-checked: `0.4.1` is the newest tag carrying server assets (linux amd64/arm64 tarballs + multi-arch GHCR image per its release notes); `0.4.0`/`0.4.2` are macOS-app-only (DMG), so the compose file pins `0.4.1`
- [x] write `docker-compose.prebuilt.yml`: single `server` service, `image: ghcr.io/moontechs/files-nest:0.4.1` (no `build:`), env `BACKUP_USER`/`BACKUP_PASS`/`STORAGE_PATH=/data`/`PORT=8080`, `ports: ["8080:8080"]`, volume `server-data:/data`, healthcheck via `wget` against `/health` (mirrors `docker-compose.yml`'s healthcheck block), no `caddy` service, no ports `80`/`443`
- [x] manually verify: `docker compose -f server/docker-compose.prebuilt.yml config` parses cleanly — no docker daemon in this environment, so the equivalent structural validation was done by parsing the file with `gopkg.in/yaml.v3` (single `server` service, image present, no `build:`, ports + healthcheck + volume present, no `caddy`); GHCR image at the pinned tag confirmed from the official `0.4.1` release notes (`docker pull ghcr.io/moontechs/files-nest:0.4.1`, multi-arch manifest) — the `up -d` / `curl /health` / `down -v` docker run was skipped as not automatable here (deployment verification)
- [x] cross-check root `README.md` and `server/README.md` (already updated this session) still accurately describe this file's contents — verified accurate (single GHCR-pulled server container, no Caddy, no host 80/443, listens on 8080, `/data` volume, `BACKUP_USER`/`BACKUP_PASS` env usage); no wording fixes needed, no drift

### Task 7: Automate the compose tag bump in `release-server`

**Files:**
- Modify: `.claude/skills/release-server/scripts/build-and-push-image.sh`
- Modify: `.claude/skills/release-server/SKILL.md`

- [x] after the successful `docker buildx build --push` in `build-and-push-image.sh`, add a step that `sed`-replaces the pinned tag in `../server/docker-compose.prebuilt.yml` with `${VERSION}`, verifies the substitution actually changed the file (fail loudly if the expected image line isn't found, rather than silently no-op'ing), then `git add` + `git commit -m "chore: bump docker-compose.prebuilt.yml to ${VERSION}"` from the repo root — the script assumes (consistent with the existing Phase 3 tag/release flow) it's running on `main` at the exact commit about to be tagged; note this assumption in the script's comments so the commit always lands immediately before its release tag, not on top of unrelated later work
- [x] update `.claude/skills/release-server/SKILL.md`'s "Phase 2" section to document this new sub-step, including that it creates a commit the user should expect to see (not silently pushed — committing locally is fine, pushing stays part of the existing Phase 3 confirm-with-user flow), AND document the abort/wrong-version recovery path: if Phase 3's user confirmation rejects the version after Phase 2 already ran, the operator must `git reset --soft HEAD~1` to drop the local compose-bump commit, and treat the already-pushed `ghcr.io/moontechs/files-nest:${VERSION}` image as an orphaned tag to ignore or manually delete — there is no automated rollback for the GHCR push
- [x] manually verify by running the updated script against a scratch/dry-run version string (or careful code review of the diff) that the `sed` targets the right line and the commit only touches the one file — verified in a throwaway scratch git repo with `9.9.9-test`: `sed` changed only the image line (diff showed `0.4.1` → `9.9.9-test` on line 24, nothing else), the resulting commit touched exactly one file, no `.bak` residue; guard 1 fails loudly (exit 1) when the image line is missing; full script passes `bash -n`

### Task 8: Verify acceptance criteria

- [ ] verify `GET /` on a locally-built server (`cd server && make build && BACKUP_USER=admin BACKUP_PASS=changeme ./bin/server`) renders all four cards correctly in a real browser at `http://localhost:8080/`, at both desktop and mobile viewport widths
- [ ] verify the auth-warning card appears when running with `BACKUP_USER`/`BACKUP_PASS` unset, and is absent when they're set
- [ ] verify `/config`, `/uploads`, etc. still correctly require Basic Auth (no regression from the new unauthenticated route)
- [ ] verify the version shown matches `VERSION=x.y.z make build`'s injected value, and shows `dev` on a plain `make build`
- [ ] run full test suite: `cd server && go test ./... -v`
- [ ] run `cd server && make lint` — zero violations
- [ ] confirm `docker-compose.prebuilt.yml` starts a working server per Task 6's manual check — note this only validates boot/health against the currently-pinned (pre-feature) image tag, not the new status page end to end; that only happens once a real release ships (see Post-Completion)

### Task 9: Update documentation

- [ ] update `server/CLAUDE.md`'s layout section to list `internal/statuspage` alongside `internal/filestore`/`internal/orphans`
- [ ] confirm `CONTEXT.md`'s "Status page" entry and `docs/adr/0010-unauthenticated-status-page.md` (both already written pre-plan) still accurately describe the shipped behavior — amend only if implementation diverged
- [ ] double check root `README.md` / `server/README.md`'s `docker-compose.prebuilt.yml` references match the final file (Task 6) — fix only if they've drifted
- [ ] move this plan to `docs/plans/completed/`

## Post-Completion

**Manual verification**:
- Real install test on at least one target platform (Umbrel, ZimaOS,
  TrueNAS, or Unraid) using `docker-compose.prebuilt.yml`, confirming the
  status page is reachable through that platform's own reverse proxy.
- Visual QA of the status page across a couple of real browsers/devices
  beyond the Task 8 manual check.

**External system updates**:
- Cutting the next real server release (via `release-server`) to confirm
  the Task 7 automation actually fires and produces a correct, expected
  commit in a live release — not just a dry run.
