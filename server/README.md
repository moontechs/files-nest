# iCloud Backup Server

Self-hosted backup server for iCloud Photos. A single Go process that accepts
resumable uploads via the [TUS 1.0 protocol](https://tus.io/protocols/resumable-upload),
organizes completed files into a date-based directory tree, and persists all
metadata in an embedded BadgerDB database.

This is the **backend** component. A macOS client app (`files-nest-mac`)
streams photo/video data directly from iCloud to this server.

## Contents

- [Architecture](#architecture)
- [Quick Start](#quick-start)
- [Configuration](#configuration)
- [API Reference](#api-reference)
- [Data Model](#data-model)
- [Startup Recovery](#startup-recovery)
- [Docker Usage](#docker-usage)
- [TUS Protocol Details](#tus-protocol-details)
- [Development](#development)
- [Invariants](#invariants)

---

## Architecture

The macOS app talks TUS 1.0 over HTTP Basic Auth to a single Go server
binary. Inside the server, `internal/api` handlers sit in front of an
`internal/uploadbackend` adapter that wraps an embedded tusd (v2.10.0)
instance, and `internal/store` persists upload records, indexes, and
completion intents in an embedded BadgerDB. Completed files land on the
filesystem under `$STORAGE_PATH`, split into `db/`, `incoming/`, and
`organized/` (see [Storage Layout](#storage-layout)).

### Key Design Decisions

- **Server is the single source of truth.** The macOS app is stateless — no
  local database. All sync state lives on the server.
- **No CGO.** All dependencies (BadgerDB, tusd) are pure Go and routing uses
  the Go 1.22+ standard-library ServeMux, enabling fully static builds for
  Docker deployment.
- **Embedded tusd.** The TUS upload handler runs in-process (not as a separate
  service). A narrow adapter in `internal/uploadbackend` isolates the rest of
  the codebase from tusd API changes.
- **BadgerDB for state.** An embedded pure-Go KV store stores upload records
  with secondary indexes for date-range listing, status scans, and
  localIdentifier lookups.
- **Completion intents for crash recovery.** A crash between moving a file
  and updating the database is recovered on the next startup.

---

## Quick Start

### Prerequisites

- Go 1.26+ (for development builds)
- Docker + Docker Compose (for production deployment)

### Running Locally (Without Docker)

```bash
# Build the server
cd server
go build -o bin/server .

# Set up storage directory
mkdir -p data/db data/incoming data/organized

# Run the server
BACKUP_USER=admin BACKUP_PASS=secret STORAGE_PATH=./data PORT=8080 ./bin/server
```

The server starts and listens on `http://localhost:8080`. All API endpoints
(except `/health`) require HTTP Basic Auth with the configured credentials.

### Running Via Docker Compose

```bash
# Start with Caddy reverse proxy (auto-HTTPS for public domains)
DOMAIN=backup.example.com BACKUP_USER=admin BACKUP_PASS=changeme docker compose up -d

# For local development (no TLS):
DOMAIN=localhost BACKUP_USER=dev BACKUP_PASS=dev docker compose up
```

This starts two containers:
- **server** — The Go binary with BadgerDB + embedded tusd
- **caddy** — Caddy 2 reverse proxy with automatic TLS (Let's Encrypt)

See [Docker Usage](#docker-usage) for detailed configuration options.

---

## Configuration

The server is configured exclusively through environment variables.

| Variable        | Required | Default     | Description                                      |
|-----------------|----------|-------------|--------------------------------------------------|
| `BACKUP_USER`   | Yes*     | `""`        | HTTP Basic Auth username.                        |
| `BACKUP_PASS`   | Yes*     | `""`       | HTTP Basic Auth password.                        |
| `STORAGE_PATH`  | No       | `./data`    | Root directory for all storage (see below).      |
| `PORT`          | No       | `8080`      | HTTP listen port.                                |
| `MAX_CONCURRENT_UPLOADS` | No | `4` | Max concurrent in-flight `PATCH .../data` (see below). |
| `GC_ORPHANS_INTERVAL` | No | `48h` | Interval between background orphan-file cleanup cycles (see below). |
| `LOG_LEVEL` | No | `info` | Logging verbosity: `info`, `debug`, or `trace`; invalid values warn and use `info`. |

\* When both `BACKUP_USER` and `BACKUP_PASS` are empty, authentication is
disabled. This is useful for local development but **must not** be used in
production. If only one of the two is set, the server refuses to start
(partial credentials are a misconfiguration that would otherwise accept an
empty username or password as a valid match).

`LOG_LEVEL=debug` includes request and other happy-path diagnostics. Use
`trace` only when more verbose diagnostics are needed; `info` is the default.

### Concurrency Limit

`MAX_CONCURRENT_UPLOADS` caps how many `PATCH /uploads/:id/data` requests
can run **concurrently** on the server. This is a *concurrency* limit, not a
rate limit: any number of uploads may be in progress sequentially, but only
the configured number may be in-flight at a given moment. Requests over the
cap are rejected immediately (no server-side queuing) with `503 Service
Unavailable` and a `Retry-After: 1` header, so a client can back off and
retry. Only the data-upload route (`PATCH .../data`) is gated — cheap
metadata operations (create, status, list, delete) are never limited.

The value must be a positive integer. On a parse error or a value `<= 0` the
server logs a warning and falls back to the default of `4`. The production
(`docker-compose.yml`) and e2e (`docker-compose.e2e.yml`) Compose files
deliberately do not set this variable, so both pick up the default of `4`.
A client can discover the live limit at runtime via `GET /config` (see
below) rather than hard-coding it.

### Orphan Cleanup

`GC_ORPHANS_INTERVAL` controls the background orphan-file cleanup cycle.
Orphans are files in `organized/` with no live upload record — for example,
files whose delete failed to remove the on-disk copy, or files referenced
only by a `deleted`-status record. The server runs one cleanup cycle
immediately at startup (after crash recovery completes) and then one per
interval; the value must be a positive duration (`time.ParseDuration`
format, e.g. `48h`, `1h`, `30m`). On a parse error or a non-positive value
the server logs a warning and falls back to the default of `48h`.

Files are only removed once their inode status-change time (ctime) is older than 3 hours,
so an in-flight upload that has written its file to `organized/` moments
before its DB record commits as `complete` is never mistaken for an orphan.
Cleanup is fully automatic — there is no manual trigger and no dry-run mode.

### Storage Layout

Everything lives under `$STORAGE_PATH`:

- `db/` — BadgerDB database (upload records, indexes, completion intents).
- `incoming/` — tusd's working directory: in-progress uploads as a partial
  binary file plus a `.info` metadata sidecar. Once an upload completes, the
  file is moved to `organized/` and the `.info` sidecar is cleaned up.
- `organized/YYYY/MM/DD/` — completed files, named by their original
  filename with the upload's stable safe ID always appended before the
  extension (e.g. `IMG_1234_<id>.jpg`). The `_<id>` suffix is
  unconditional — never only on collision — so a completed file's path is
  fully deterministic given its creation date, filename, and record ID.

---

## API Reference

All endpoints (except `/health`) require HTTP Basic Auth.

### Base URL

```
http://<server>:8080
```

### Authentication

Include credentials in every request:

```
Authorization: Basic base64(username:password)
```

### Endpoints

#### `GET /health`

Server health/liveness check. Does **not** require authentication.

```
GET /health
```

**Response 200:**
```json
{"status":"ok"}
```

---

#### `GET /config`

Read-only server configuration, exposed so clients can discover the
concurrency limit before issuing parallel uploads. Requires authentication.

**Response 200:**
```json
{"maxConcurrentUploads": 4}
```

`maxConcurrentUploads` reflects the configured `MAX_CONCURRENT_UPLOADS`
value (default `4`). Clients should read this once at startup and use it to
drive how many concurrent `PATCH .../data` requests they issue.

**Status codes:**
| Code | Description |
|------|-------------|
| 200  | Configuration returned |
| 401  | Missing or invalid credentials |

---

#### `POST /uploads`

Create a new upload record and allocate a TUS upload on the backend.

Creates a TUS upload with `Upload-Defer-Length: 1` (file size not yet known).
Returns a deterministic server ID derived from `local_identifier`.

If a record already exists for the same `local_identifier`, the response
depends on the existing record's status:

- **uploading / completing / complete** — the existing record is returned
  with status 200 (idempotent). Any newly-created tusd upload is terminated.
- **backend_lost / deleted** — the server re-registers: it binds the
  newly-created tusd upload to the existing record, resets the status to
  `uploading`, and clears any stale `organized_path`. Returns 201 with the
  refreshed record. This is the recovery path for lost backends and for
  re-uploading a previously deleted `localIdentifier`.

**Request:**
```json
{
  "local_identifier": "ABC123-DEF456/ABC123-DEF456/L0/001",
  "filename": "IMG_1234.jpg",
  "creation_date": "2024-03-15T10:30:00Z",
  "bundle_id": "XYZ789-GHI012/XYZ789-GHI012/L0/001",
  "metadata": {
    "orientation": "6",
    "burstIdentifier": "BURST001"
  }
}
```

Both `camelCase` (`localIdentifier`, `creationDate`, `bundleId`) and
`snake_case` (`local_identifier`, `creation_date`, `bundle_id`) field names
are accepted. `snake_case` takes precedence when both are present.

**Response 201 (new record):**
```json
{
  "id": "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8g9h0a1b2c3d4e5f6a7b8c9d0e1",
  "local_identifier": "ABC123-DEF456/ABC123-DEF456/L0/001",
  "status": "uploading",
  "backend_id": "f47ac10b58cc4372a5670e02b2c3d479",
  "upload_url": "/uploads/a1b2c3d4.../data"
}
```

**Response 200 (existing record — idempotent):**
```json
{
  "id": "a1b2c3d4...",
  "local_identifier": "ABC123-DEF456/ABC123-DEF456/L0/001",
  "status": "uploading",
  "backend_id": "existing-backend-id",
  "upload_url": "/uploads/a1b2c3d4.../data"
}
```

**Status codes:**
| Code | Description |
|------|-------------|
| 201  | New upload record created |
| 200  | Upload already exists (idempotent) |
| 400  | Missing or invalid fields |
| 500  | Internal server error |

---

#### `GET /uploads`

List uploads with optional date range, status filter, cursor pagination, and
limit.

**Query parameters:**

| Parameter | Type     | Default         | Description                                    |
|-----------|----------|-----------------|------------------------------------------------|
| `from`    | RFC3339  | `1970-01-01`    | Start date (inclusive).                        |
| `to`      | RFC3339  | `2999-12-31`    | End date (inclusive).                          |
| `status`  | string   | (all)           | Filter by status: `uploading`, `complete`, etc.|
| `limit`   | int      | `500`           | Max results per page (clamped to 1–1000).      |
| `cursor`  | string   | (start)         | Pagination cursor from previous response.      |

**Response:**
```json
{
  "items": [
    {
      "id": "a1b2c3d4...",
      "local_identifier": "ABC123-DEF456/ABC123-DEF456/L0/001",
      "status": "complete",
      "backend_id": "f47ac10b...",
      "filename": "IMG_1234.jpg",
      "bundle_id": "XYZ789-GHI012/XYZ789-GHI012/L0/001",
      "creation_date": "2024-03-15T10:30:00Z",
      "created_at": "2024-03-15T10:30:00Z",
      "updated_at": "2024-03-15T10:32:00Z",
      "organized_path": "organized/2024/03/15/IMG_1234_<id>.jpg"
    }
  ],
  "next_cursor": "MjAyNC0wMy0xNS9hMWIyYzNkN...=="
}
```

`next_cursor` is empty when all results have been returned. Pass it as the
`cursor` parameter in the next request for the next page.

---

#### `GET /uploads/:id`

Get a single upload record by its safe server ID.

**Response 200:**
```json
{
  "id": "a1b2c3d4...",
  "local_identifier": "ABC123-DEF456/ABC123-DEF456/L0/001",
  "status": "uploading",
  "backend_id": "f47ac10b...",
  "filename": "IMG_1234.jpg",
  "creation_date": "2024-03-15T10:30:00Z",
  "created_at": "2024-03-15T10:30:00Z",
  "updated_at": "2024-03-15T10:30:00Z"
}
```

**Status codes:**
| Code | Description |
|------|-------------|
| 200  | Upload record returned |
| 400  | Invalid upload ID |
| 404  | Upload not found |

---

#### `HEAD /uploads/:id/data`

Get the current upload offset (TUS protocol). Returns TUS protocol headers.
Only valid while the upload is in the `uploading` state; HEAD on a completed,
deleted, or `backend_lost` upload does not contact the tusd backend (so it
cannot corrupt a completed record's status).

**Response 200:**
```
Upload-Offset: 1048576
Tus-Resumable: 1.0.0
```

**Status codes:**
| Code | Description |
|------|-------------|
| 200  | Offset returned in header |
| 404  | Upload not found or deleted |
| 409  | Backend lost, or upload already completed (`{"error":"..."}`) |

---

#### `PATCH /uploads/:id/data`

Upload a chunk of data (TUS protocol). Forwards the request body and standard
TUS headers to the embedded tusd handler.

**Request headers:**
```
Content-Type: application/offset+octet-stream
Upload-Offset: 1048576
Tus-Resumable: 1.0.0
Upload-Length: 4194304    ← optional: declares final size (deferred-length)
```

**Response 204:**
```
Upload-Offset: 2097152
Tus-Resumable: 1.0.0
```

**Status codes:**
| Code | Description |
|------|-------------|
| 204  | Chunk accepted, new offset in response header |
| 400  | Missing or invalid headers |
| 404  | Upload not found |
| 409  | Offset mismatch or backend lost |
| 503  | Over the concurrent-upload limit (`Retry-After: 1`); retry later |

The `Upload-Length` header is required to finalize a deferred-length upload.
The final chunk should include `Upload-Length` set to the total file size and
`Upload-Complete: 1`.

---

#### `PATCH /uploads/:id/status`

Transition an upload's status. Currently only accepts `{"status": "complete"}`.

Before marking complete, the server:
1. Verifies the tusd backend reports `offset == length` with known length
2. Saves a crash-recovery completion intent to BadgerDB
3. Moves the file from `incoming/` to `organized/`
4. Updates the DB record to `complete` with `organized_path`
5. Deletes the completion intent
6. Cleans up the tusd `.info` sidecar

**Request:**
```json
{"status": "complete"}
```

**Response 204** (no body).

**Status codes:**
| Code | Description |
|------|-------------|
| 204  | Upload marked complete, file organized |
| 400  | Invalid status value |
| 404  | Upload not found |
| 409  | Upload incomplete, already completed, deleted, or backend lost |

---

#### `DELETE /uploads/:id`

Delete an upload record. Terminates the tusd backend state (if it exists),
then removes the organized file from disk, and finally marks the record as
`deleted` in the database. The organized file is **permanently deleted and
cannot be recovered** — there is no trash or retention window.

If the organized file cannot be removed (e.g. permission error), the failure
is logged but does **not** prevent the record from being marked `deleted`.
A 204 response does not guarantee the file was actually removed from disk.
If the organized file is already gone (e.g. second DELETE on the same upload),
removal is silently skipped.

**Response 204** (no body).

**Status codes:**
| Code | Description |
|------|-------------|
| 204  | Upload deleted (or already deleted) |
| 404  | Upload not found |

---

## Data Model

### Safe Server IDs

The server uses deterministic, path-safe IDs derived from the original
PhotoKit `localIdentifier` by SHA-256 hashing it and base64url-encoding the
digest, producing a compact, fixed-length identifier safe for use in URL
paths, filesystem paths, and BadgerDB keys — e.g. `"ABC123-DEF456/ABC123-
DEF456/L0/001"` becomes `"a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8g9h0a1b2c3d4e5f6a7b8c9d0e1"`.

The original `localIdentifier` is preserved in the `local_identifier` field
of the upload record and is searchable via the `LocalIdentifierIndex`.

### Upload Record

```json
{
  "id": "<safe server id (SHA-256 + base64url)>",
  "local_identifier": "<original PhotoKit localIdentifier>",
  "status": "uploading | complete | deleted | backend_lost",
  "backend_id": "<tusd upload ID>",
  "filename": "IMG_1234.jpg",
  "bundle_id": "<Live Photo parent localIdentifier>",
  "creation_date": "2024-03-15T10:30:00Z",
  "created_at": "2024-03-15T10:30:00Z",
  "updated_at": "2024-03-15T10:30:00Z",
  "metadata": { "orientation": "6" },
  "organized_path": "organized/2024/03/15/IMG_1234_<id>.jpg"
}
```

### Status Lifecycle

A record starts `uploading`, and `PATCH /status {"status":"complete"}` moves
it to `complete`. If the tusd backend returns 409 (e.g. after a server
restart wiped its state), the record moves to `backend_lost`; a subsequent
`POST /uploads` re-registers it back to `uploading`. `DELETE /uploads/:id`
moves any of `uploading` / `complete` / `backend_lost` to `deleted`.

- **uploading**: Upload in progress. The client can send PATCH /data chunks.
- **complete**: File has been moved to the organized tree. No further data
  operations allowed.
- **deleted**: Record marked deleted by the client. The tusd backend is
  cleaned up. A subsequent `POST /uploads` with the same `localIdentifier`
  re-registers a fresh upload (resets the record to `uploading` with a new
  `backend_id`), so a deleted `localIdentifier` is never permanently stuck.
- **backend_lost**: Tusd backend no longer has the upload data (e.g. after
  server restart). The client re-registers by sending `POST /uploads` with
  the same `localIdentifier`; the server binds a fresh tusd upload to the
  existing record and resets it to `uploading`. No `DELETE` is required.

### Indexes

Four secondary indexes are maintained atomically with each record write:

| Index | Key Format | Value | Purpose |
|-------|-----------|-------|---------|
| Date | `idx/date/<YYYY-MM-DD>/<id>` | RFC3339 date | Date-range listing, pagination |
| Status | `idx/status/<status>/<id>` | Backend ID | Status-based scans, resume |
| Local ID | `idx/local/<base64(localIdentifier)>` | Safe ID | Lookup by PhotoKit identifier |
| Backend | `idx/backend/<backend_id>` | Safe ID | Lookup by tusd upload ID |

---

## Startup Recovery

On startup, the server runs two recovery phases before accepting requests:

### Phase 1: Completion Intent Recovery

Scans all pending `completion/<id>` records. For each intent:

- **Destination exists, source gone** → File was moved successfully but DB
  wasn't updated. Update the DB record to `complete`.
- **Source exists, destination gone** → Intent was saved but file wasn't
  moved. Move the file, update DB.
- **Both exist** → Remove the source (completed duplicate).
- **Neither exists** → Data loss. Keep the intent for manual repair.
- **Record already `complete`** → Delete stale intent, retry tusd cleanup.

### Phase 2: Backend Lost Detection

Scans all `uploading` and `completing` records and checks whether their tusd
backend still exists. If not, status is updated to `backend_lost` so the
client re-registers on the next sync.

---

## Docker Usage

### Building the Image

```bash
cd server
docker build -t files-nest-server .
```

### Running Standalone

```bash
docker run -p 8080:8080 \
  -e BACKUP_USER=admin \
  -e BACKUP_PASS=secret \
  -e STORAGE_PATH=/data \
  -v /path/to/data:/data \
  files-nest-server
```

### Docker Compose (With Caddy)

The `docker-compose.yml` file runs the server behind a Caddy reverse proxy
with automatic TLS via Let's Encrypt.

```bash
# Set domain, credentials, and start
DOMAIN=backup.example.com \
  BACKUP_USER=admin \
  BACKUP_PASS=changeme \
  docker compose up -d
```

#### Caddyfile

The Caddyfile configures:
- TLS termination with auto-HTTPS (Let's Encrypt) for public domains
- Plain HTTP for `localhost` (development mode)
- Unlimited request body size for TUS uploads
- Security headers (CSP, HSTS, X-Frame-Options, etc.)
- Health check passthrough (unauthenticated)
- Rate limiting (commented out; uncomment to enable)

To customize, edit `server/Caddyfile`:

```caddyfile
{$DOMAIN:localhost} {
    header {
        X-Content-Type-Options "nosniff"
        X-Frame-Options "DENY"
        Referrer-Policy "no-referrer"
        Content-Security-Policy "default-src 'self'; frame-ancestors 'none'"
        Strict-Transport-Security "max-age=31536000; includeSubDomains; preload"
    }

    handle /uploads* {
        request_body {
            max_size 0
        }
        reverse_proxy server:8080
    }

    handle /health {
        reverse_proxy server:8080
    }

    handle {
        respond 404
    }
}
```

---

## TUS Protocol Details

### Deferred-Length Uploads

The server uses `Upload-Defer-Length: 1` because iCloud streams file sizes
lazily — the total file size is not known when an upload is created.

**Upload flow:**
1. `POST /uploads` creates a TUS upload with `Upload-Defer-Length: 1`
2. Client sends chunks via `PATCH /uploads/:id/data` with only `Upload-Offset`
3. **Final chunk only**: Set `Upload-Length: <total_size>` and
   `Upload-Complete: 1` on the `PATCH` request
4. `PATCH /uploads/:id/status {"status": "complete"}` moves the file

### Required Headers

| Header | When | Value |
|--------|------|-------|
| `Tus-Resumable` | All requests | `1.0.0` |
| `Upload-Offset` | PATCH /data | Current byte offset |
| `Content-Type` | PATCH /data | `application/offset+octet-stream` |
| `Upload-Length` | Final PATCH /data | Total file size in bytes |

### Offset Verification

The server verifies that the `Upload-Offset` in a `PATCH /data` request
matches the current offset in the backend. If they differ, a 409 Conflict
is returned.

---

## Development

### Project Structure

- `main.go` — entry point, wiring, shutdown.
- `internal/api/` — HTTP handlers (`handlers.go`), routing/middleware
  (`router.go`), HTTP Basic Auth (`auth.go`), safe ID derivation and filename
  sanitization (`ids.go`), per-upload in-memory locking (`locks.go`), and
  startup crash recovery (`recovery.go`), each with a matching `_test.go`.
- `internal/store/` — the BadgerDB persistence layer: open/close
  (`store.go`), upload CRUD/status/pagination (`uploads.go`), secondary
  indexes (`index.go`), and completion-intent CRUD (`completion.go`).
- `internal/uploadbackend/` — the narrow tusd adapter (`tushandler.go`) and
  its sentinel errors (`errors.go`).
- `internal/filestore/` — path planning, file moves, and deterministic
  organized-filename suffixing
  (`mover.go`).

See `CLAUDE.md` in this directory for the same layout kept current alongside
`internal/orphans`.

### Running Tests

```bash
cd server
go test ./... -v
```

All tests use real BadgerDB instances in temp directories and real embedded
tusd storage. There are no mocks of BadgerDB, tusd, or the filesystem.

End-to-end tests are gated behind the `e2e` build tag and require a running
Docker Compose stack. See [End-to-End Tests](#end-to-end-tests) below.

### Linting

The server is linted with a maximum-strictness `golangci-lint` configuration
at `server/.golangci.yml` (`default: all`, disabling only the two deprecated
linters `wsl` and `gomodguard`, whose non-deprecated successors `wsl_v5` /
`gomodguard_v2` remain enabled). **golangci-lint v2+ is required** — the
config uses the v2 schema, and older 1.x releases will not parse it.

The single canonical manual command is:

```bash
cd server
make lint
```

`make lint` runs `golangci-lint run ./...` and is expected to report zero
violations at all times. To auto-format and apply auto-fixable fixes before a
manual check:

```bash
cd server
make lint-fix
```

`make lint-fix` runs `golangci-lint fmt ./...` followed by
`golangci-lint run --fix ./...`. Run `make lint` afterwards to confirm zero
violations remain (auto-fix does not address every linter, e.g. `gosec`
findings require manual review).

### Mutation Testing

Mutation testing covers the server's unit-test packages with `go-gremlins`.
Install `gremlins` once, then run:

```bash
cd server
make mutation-test
```

The target uses `server/.gremlins.yaml`, which pins the mutation scope and
stable concurrency settings and requires 100% efficacy and mutator coverage.

### Pre-commit Hook

The repo ships a plain shell pre-commit hook at `.githooks/pre-commit` (no new
dependency) that blocks a commit when staged changes touch `server/` and the
linter reports any violation. Install it once per clone, from the repo root:

```bash
git config core.hooksPath .githooks
```

What the hook does:

- **Fails closed if `golangci-lint` is missing** — it never silently skips
the gate. Install golangci-lint v2+ and put it on your `PATH` first.
- **Only triggers on staged `server/` changes** — it inspects
`git diff --cached --name-only --diff-filter=ACM` and exits 0 immediately
when no staged path starts with `server/`.
- **Lints the whole module, not a snapshot of staged hunks** — when `server/`
files are staged it runs `(cd server && golangci-lint run ./...)`. This is
deliberate: a partial `git add -p` stage can leave the working tree in a
state that only fails when the full module is analysed, so the hook catches
it before it lands. Fixing a violation therefore means editing the working
tree (not just the staged hunks) and re-staging.

On failure it prints the canonical fix commands (`make lint-fix`, then
`make lint`) and exits non-zero, so the commit is aborted.

### Adding a New Endpoint

1. Add the handler method to `internal/api/handlers.go`
2. Register the route in `internal/api/router.go`
3. Add tests to `internal/api/handlers_test.go`

The per-upload lock (`UploadLocker`) should be used for any handler that
mutates upload state — `PATCH /data`, `PATCH /status`, and `DELETE` all
acquire it.

### End-to-End Tests

The `server/e2e/` package provides black-box end-to-end tests that exercise
the full HTTP API through Caddy, verify real behavior (auth, upload lifecycle,
resume, listing, re-registration), and never import `internal/` packages.

All e2e test files are gated by `//go:build e2e` — normal `go test ./...`
runs are unaffected. The tests are invisible to the Go toolchain without the
`-tags=e2e` flag.

#### Prerequisites

- Docker + Docker Compose
- Go 1.26+

#### Quick Start (make e2e)

The fastest way to run the full e2e suite:

```bash
cd server
make e2e
```

This single target:
1. Starts the isolated e2e Docker Compose stack (builds images, binds to
   `127.0.0.1:18080` only — no production port conflicts)
2. Polls the `/health` endpoint until the server is ready
3. Runs the e2e test suite with `-tags=e2e -v -count=1`
4. Prints container diagnostics (logs, status) on failure
5. Tears down the stack (`down -v --remove-orphans`) on exit, even on
   failure

If the server does not become healthy within `E2E_WAIT` seconds, the target
fails immediately with container diagnostics printed before cleanup.

#### Compose-only Stack (manual)

Start the stack without running tests:

```bash
cd server
make e2e-up
```

Tail the logs, check status, or tear down:

```bash
make e2e-logs
make e2e-ps
make e2e-down
```

#### Running Tests Against an Existing Stack

If the stack is already running (started with `make e2e-up` or manually):

```bash
make e2e-test
```

Or directly with `go test`:

```bash
SERVER_URL=http://127.0.0.1:18080 \
  BACKUP_USER=testuser \
  BACKUP_PASS=testpass \
  go test -tags=e2e -v -count=1 -timeout=120s ./e2e/
```

#### Full make Target Reference

| Target         | Description                                                    |
|----------------|----------------------------------------------------------------|
| `make e2e`     | Full runner: `up → wait → test → down` (with failure diags).   |
| `make e2e-up`  | Start the e2e Docker Compose stack (detached).                 |
| `make e2e-down`| Stop the e2e stack, remove volumes and orphans.                |
| `make e2e-logs`| Tail logs from the e2e stack.                                  |
| `make e2e-ps`  | Show container status for the e2e stack.                       |
| `make e2e-test`| Run e2e tests against an already-running stack.                |
| `make mutation-test` | Run go-gremlins mutation tests for `internal/...`.       |

#### Configuration Variables

All variables have sensible defaults and can be overridden via environment
or `make VAR=value`.

| Variable                | Default | Description                                                  |
|-------------------------|---------|--------------------------------------------------------------|
| `E2E_HTTP_PORT`         | `18080` | Host port for the e2e Caddy container (binds `127.0.0.1`).   |
| `COMPOSE_PROJECT_NAME`  | `files-nest-e2e` | Docker Compose project name, isolating e2e from production. |
| `DOMAIN`                | `localhost` | Domain passed to Caddy (always plain HTTP for e2e).          |
| `BACKUP_USER`           | `testuser` | HTTP Basic Auth username for API requests.                   |
| `BACKUP_PASS`           | `testpass` | HTTP Basic Auth password. Must be set with `BACKUP_USER`.     |
| `E2E_WAIT`              | `30`      | Max seconds to wait for server `/health` before failing.     |
| `E2E_TIMEOUT`           | `120`     | Go test timeout in seconds (passed as `-timeout`).           |
| `SERVER_URL`            | *(derived)* | Base URL of the deployed server (`http://127.0.0.1:$E2E_HTTP_PORT`). Used directly for `make e2e-test` or manual `go test`. |

#### Behaviour: Skip vs Failure

The e2e test suite never hangs or requires user interaction:

- **`SERVER_URL` empty or unset** — All tests are skipped with a clean
  `os.Exit(0)` and a log message. This is the default when running
  `go test -tags=e2e ./e2e/` without setting `SERVER_URL`.
- **`SERVER_URL` set but unreachable** — `TestMain` polls `/health` for up
  to the `E2E_WAIT` timeout (default 30 seconds; override via the `E2E_WAIT`
  environment variable). If the server never responds with 200, the suite
  exits with a fatal error and a non-zero exit code.
- **Missing credentials** — If `SERVER_URL` is set but `BACKUP_USER` or
  `BACKUP_PASS` is unset/empty, `TestMain` prints a clear configuration
  error and exits with a non-zero status before any test runs. The e2e
  stack always enforces Basic Auth, so both credentials are required.

#### Isolated Port Usage

To run multiple e2e instances concurrently (e.g. CI matrix jobs), change the
port and project name:

```bash
make e2e E2E_HTTP_PORT=18081 COMPOSE_PROJECT_NAME=files-nest-e2e-alt
```

#### Test Suite Overview

| File                        | Focus                                                   |
|-----------------------------|---------------------------------------------------------|
| `main_test.go`              | `TestMain` — readiness polling, environment validation. |
| `client_test.go`            | Shared HTTP client helpers and request builders.        |
| `fixtures_test.go`          | Test data generation (unique IDs, dates, payloads).     |
| `auth_validation_test.go`   | Authentication: unauthenticated access, bad credentials, missing headers, disabled auth mode. |
| `lifecycle_test.go`         | Full upload lifecycle: create, chunk, complete, delete. |
| `resume_test.go`            | Resumable uploads: offset tracking, interrupted uploads, multi-chunk flows. |
| `reregister_test.go`        | Idempotent creation, re-registration after deleted/`backend_lost`. |
| `listing_test.go`           | List with date ranges, pagination, status filtering, empty results. |
| `storage_test.go`           | On-disk verification: organized file content matches uploaded bytes; DELETE removes the file from disk. |

#### On-Disk Storage Verification

Every other e2e test only checks HTTP responses (e.g. that `organized_path`
is non-empty). `storage_test.go` goes one level deeper: it reaches into the
running container's filesystem via `docker compose cp`/`exec` and confirms
the server actually wrote (and removed) the right bytes.

- **`TestStorage_CompletedFileContentMatchesUploadedBytes`** — uploads a
  known payload, then copies the organized file out of the container and
  asserts it is byte-for-byte identical to what was sent.
- **`TestStorage_DeleteRemovesFileFromDisk`** — confirms the organized file
  exists right after completion, then confirms it is actually gone after
  `DELETE /uploads/:id`.

These checks are **opt-in and auto-skip** when there's no local stack to
inspect (e.g. `SERVER_URL` points at a remote/staging deployment) — they
require `COMPOSE_PROJECT_NAME` (or `E2E_COMPOSE_PROJECT`) to be set and the
`docker` CLI to be on `PATH`. `make e2e` and `make e2e-test` export
`COMPOSE_PROJECT_NAME` automatically, so they run by default in the normal
local/CI flow. Override `E2E_COMPOSE_FILE` (default
`docker-compose.e2e.yml`) or `E2E_STORAGE_SERVICE` (default `server`) if
your stack uses different names.

#### Architecture

The e2e stack uses a separate Compose file and Caddyfile to avoid conflicts
with the production deployment: `go test -tags=e2e` talks plain HTTP to
Caddy on `127.0.0.1:${E2E_HTTP_PORT:-18080}`, which reverse-proxies to the Go
server on `:8080` — both containers built from the same production images.

- **Caddy** binds to `127.0.0.1:${E2E_HTTP_PORT:-18080}` (host) → `:80` (container).
- **Server** is built from the same `Dockerfile` as production.
- **Data** is ephemeral: `make e2e-down` or `docker compose down -v` removes
  volumes entirely.
- **Healthchecks** on the server container are diagnostic; the Go test suite
  does its own readiness polling in `TestMain`.

#### Key Constraints

- Every file in `e2e/` starts with `//go:build e2e` — without `-tags=e2e`,
  these files are invisible to the Go toolchain.
- E2E tests must **not** import `github.com/moontechs/files-nest/server/internal/...`.
- All response bodies must be closed after use.
- Listing assertions are scoped to the current test run's unique local
  identifier prefix and date window to avoid interference between runs.
- Final upload chunks must include `Upload-Length: <total_size>` and
  `Upload-Complete: 1`.
- Idempotent create expectations: existing active/completed records return
  `200`; re-registering deleted/`backend_lost` records returns `201`.
- The suite does not include destructive restart/backend-loss tests in this
  pass; those need a separate orchestrator.

---

## Invariants

- Never use raw `localIdentifier`, filename, bundle ID, or backend ID as an
  unescaped BadgerDB key segment or filesystem path segment.
- `POST /uploads` idempotency is based on `local_identifier`, not the
  derived safe ID.
- If `POST /uploads` creates a tusd upload but the DB insert fails,
  terminate the newly-created tusd upload before returning.
- API handlers depend on project-owned `uploadbackend` interfaces, not tusd
  types directly.
- `UpdateStatus` deletes the old status index and writes the new one in the
  same transaction (no ghost keys).
- Completion verifies tusd reports known length and `offset == length`.
- `PATCH /uploads/:id/status` only accepts `complete`.
- Completed files are moved before status becomes `complete`.
- File move and DB completion are recoverable from persisted completion
  intent.
- `backend_lost` is only set on normalized `uploadbackend.ErrNotFound`, not
  generic I/O errors.
- `DELETE` ignores `uploadbackend.ErrNotFound` but still updates BadgerDB to
  `deleted`.
