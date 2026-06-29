# iCloud Backup Server

Self-hosted backup server for iCloud Photos. A single Go process that accepts
resumable uploads via the [TUS 1.0 protocol](https://tus.io/protocols/resumable-upload),
organizes completed files into a date-based directory tree, and persists all
metadata in an embedded BadgerDB database.

This is the **backend** component. A macOS client app (`files-nest-mac`)
streams photo/video data directly from iCloud to this server.

---

## Architecture

```
┌─────────────┐   TUS 1.0 protocol    ┌─────────────────────────────┐
│ macOS app   │ ◄───────────────────► │ Go server (single binary)   │
│ (stateless) │   HTTP Basic Auth     │                             │
└─────────────┘                       │  ┌───────────────────────┐  │
                                      │  │ API handlers          │  │
                                      │  │ (internal/api)        │  │
                                      │  └──────┬────────────────┘  │
                                      │         │                   │
                                      │  ┌──────▼────────────────┐  │
                                      │  │ uploadbackend adapter │  │
                                      │  │ (internal/upload-     │  │
                                      │  │  backend)             │  │
                                      │  │   ┌───────────────┐   │  │
                                      │  │   │ embedded tusd  │   │  │
                                      │  │   │ (v2.10.0)      │   │  │
                                      │  │   └───────┬───────┘   │  │
                                      │  └───────────┼────────────┘  │
                                      │              │              │
                                      │  ┌──────────▼─────────────┐ │
                                      │  │ BadgerDB (internal/    │ │
                                      │  │ store)                 │ │
                                      │  │  - upload records      │ │
                                      │  │  - indexes             │ │
                                      │  │  - completion intents  │ │
                                      │  └────────────────────────┘ │
                                      └─────────────────────────────┘
                                                  │
                                          ┌───────▼────────┐
                                          │   Filesystem    │
                                          │  $STORAGE_PATH │
                                          │  ├── db/       │
                                          │  ├── incoming/ │
                                          │  └── organized/│
                                          └────────────────┘
```

### Key Design Decisions

- **Server is the single source of truth.** The macOS app is stateless — no
  local database. All sync state lives on the server.
- **No CGO.** All dependencies (BadgerDB, tusd, chi) are pure Go, enabling
  fully static builds for Docker deployment.
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

\* When both `BACKUP_USER` and `BACKUP_PASS` are empty, authentication is
disabled. This is useful for local development but **must not** be used in
production.

### Storage Layout

```
$STORAGE_PATH/
├── db/                      ← BadgerDB database (upload records, indexes, intents)
├── incoming/                ← tusd upload directory (partial + .info sidecars)
└── organized/               ← Completed files organized by date
    └── YYYY/
        └── MM/
            └── DD/
                ├── IMG_1234.jpg
                └── IMG_0001_<backend_id>.jpg  ← collision dedup suffix
```

The `incoming/` directory holds in-progress TUS uploads. The `.info` sidecar
files carry metadata; the binary files contain the uploaded data. Once an
upload is complete, the file is moved from `incoming/` to `organized/` and
the `.info` sidecar is cleaned up.

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

#### `POST /uploads`

Create a new upload record and allocate a TUS upload on the backend.

Creates a TUS upload with `Upload-Defer-Length: 1` (file size not yet known).
Returns a deterministic server ID derived from `local_identifier`.

If a record already exists for the same `local_identifier`, the existing
record is returned with status 200 (idempotent). Any newly-created tusd
upload is terminated.

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
      "organized_path": "organized/2024/03/15/IMG_1234.jpg"
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

**Response 204:**
```
Upload-Offset: 1048576
Tus-Resumable: 1.0.0
```

**Status codes:**
| Code | Description |
|------|-------------|
| 204  | Offset returned in header |
| 404  | Upload not found or deleted |
| 409  | Backend lost (`{"error":"backend_lost"}`) |

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
then marks the record as `deleted` in the database. Does **not** remove the
organized file.

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
PhotoKit `localIdentifier`. The derivation uses SHA-256 + base64url encoding
to produce a compact, fixed-length identifier safe for use in URL paths,
filesystem paths, and BadgerDB keys.

```
localIdentifier: "ABC123-DEF456/ABC123-DEF456/L0/001"
          ↓ SHA-256
          ↓ base64url encoding
safe ID:        "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8g9h0a1b2c3d4e5f6a7b8c9d0e1"
```

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
  "organized_path": "organized/2024/03/15/IMG_1234.jpg"
}
```

### Status Lifecycle

```
uploading ──── PATCH /status complete ──→ complete
    │
    └── 409 from tusd backend ──→ backend_lost
                                      │
                              POST /uploads (re-register)
                                      │
                                      ↓
                                  uploading

uploading / complete / backend_lost ── DELETE /uploads/:id ──→ deleted
```

- **uploading**: Upload in progress. The client can send PATCH /data chunks.
- **complete**: File has been moved to the organized tree. No further data
  operations allowed.
- **deleted**: Record deleted by client. Tusd backend cleaned up.
- **backend_lost**: Tusd backend no longer has the upload data (e.g. after
  server restart). The client must delete and re-register.

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

    handle_path /uploads* {
        request_body {
            max_size 0
        }
        reverse_proxy server:8080
    }

    handle_path /health {
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

```
server/
├── main.go                           ← Entry point, wiring, shutdown
├── go.mod / go.sum                   ← Dependencies
├── Dockerfile                        ← Multi-stage Docker build
├── docker-compose.yml                ← Server + Caddy
├── Caddyfile                          ← Caddy reverse proxy config
└── internal/
    ├── api/                          ← HTTP handlers, middleware, routing
    │   ├── handlers.go               ← All endpoint implementations
    │   ├── handlers_test.go           ← Integration tests
    │   ├── router.go                 ← Route registration, middleware
    │   ├── auth.go                   ← HTTP Basic Auth middleware
    │   ├── auth_test.go
    │   ├── ids.go                    ← Safe ID derivation, filename sanitization
    │   ├── ids_test.go
    │   ├── locks.go                  ← Per-upload in-memory locking
    │   ├── locks_test.go
    │   ├── recovery.go               ← Startup crash recovery
    │   └── recovery_test.go
    ├── store/                        ← BadgerDB persistence layer
    │   ├── store.go                  ← BadgerDB open/close
    │   ├── store_test.go
    │   ├── uploads.go                ← Upload CRUD, status updates, pagination
    │   ├── uploads_test.go
    │   ├── index.go                  ← Index registry, date/status/local/backend indexes
    │   ├── completion.go             ← Completion intent CRUD
    │   └── completion_test.go
    ├── uploadbackend/                ← tusd adapter (narrow interface)
    │   ├── tushandler.go             ← Wraps embedded tusd v2
    │   ├── tushandler_test.go
    │   ├── tusd_api_test.go          ← tusd API verification spike tests
    │   └── errors.go                 ← Sentinel errors (ErrNotFound, etc.)
    └── filestore/                    ← File organization and moving
        ├── mover.go                  ← Path planning, file moves, collision handling
        └── mover_test.go
```

### Running Tests

```bash
cd server
go test ./... -v
```

All tests use real BadgerDB instances in temp directories and real embedded
tusd storage. There are no mocks of BadgerDB, tusd, or the filesystem.

### Adding a New Endpoint

1. Add the handler method to `internal/api/handlers.go`
2. Register the route in `internal/api/router.go`
3. Add tests to `internal/api/handlers_test.go`

The per-upload lock (`UploadLocker`) should be used for any handler that
mutates upload state — `PATCH /data`, `PATCH /status`, and `DELETE` all
acquire it.

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
