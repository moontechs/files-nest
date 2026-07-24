# FilesNestCore — ServerClient Design

Date: 2026-07-23
Status: Draft (for review)
Scope: The first unit of `FilesNestCore`. Companion to `docs/architecture.md`.

## 1. Context & scope

`FilesNestCore` is the shared Swift package that both the macOS and (future) iOS
clients build on. This spec covers **only its first unit: `ServerClient`** — the
single HTTP client that speaks to the Go server's TUS + Basic-Auth API.

Everything else in the core (`AssetUploader`, `SyncCoordinator`, `KeychainStore`,
the PhotoKit `AssetReader`) is out of scope here and gets its own spec. They are
mentioned only where they define a seam `ServerClient` must expose.

`ServerClient` is deliberately thin: it constructs requests, injects auth, sends
them via `URLSession`, and maps responses to typed models or typed errors. It
holds **no streaming, buffering, or backpressure logic** — those live in
`AssetUploader` (the memory-critical unit). `ServerClient`'s chunk method takes a
already-bounded `Data` and an offset; it never sees a whole asset.

The server contract was extracted from `server/internal/api/router.go` and
`handlers.go`, not the doc prose.

## 2. Package layout

A new SwiftPM package at `apple/FilesNestCore/` (replacing the `.gitkeep`):

```
apple/FilesNestCore/
  Package.swift
  Sources/FilesNestCore/
    ServerClient.swift
    ServerClientError.swift
    Models/            (UploadRecord, UploadStatus, CreateUploadRequest, UploadPage, UploadOffset, BasicCredentials)
    CredentialStore.swift   (the auth seam protocol)
  Tests/FilesNestCoreTests/
    MockURLProtocol.swift
    ServerClientTests.swift
```

- One library target `FilesNestCore`, one test target `FilesNestCoreTests`.
- Platforms: `.macOS(.v15)`, `.iOS(.v17)` — the package is cross-platform from
  day one; `ServerClient` is pure Foundation (`URLSession`), so it compiles on both.
- **Swift 6 language mode with complete concurrency checking**, from the start.
  (This is the clean-slate answer to the audit's Swift-6 isolation findings.)

## 3. Public API surface

`ServerClient` is a `Sendable` struct (immutable config; no mutable state, so no
actor is needed). All methods are `async throws`.

```
struct ServerClient: Sendable {
    init(baseURL: URL, credentials: any CredentialStore, session: URLSession = ...)

    // POST /uploads
    func createUpload(_ request: CreateUploadRequest) async throws -> UploadRecord

    // GET /uploads?cursor=...
    func listUploads(cursor: String?) async throws -> UploadPage

    // GET /uploads/{id}
    func getUpload(id: String) async throws -> UploadRecord

    // HEAD /uploads/{id}/data  → parses Upload-Offset / Upload-Length
    func offset(forUploadID id: String) async throws -> UploadOffset

    // PATCH /uploads/{id}/data  → returns the new server Upload-Offset
    func patchData(uploadID id: String, offset: Int64, data: Data, isFinal: Bool) async throws -> Int64

    // PATCH /uploads/{id}/status {status:"complete"}
    func markComplete(uploadID id: String) async throws

    // DELETE /uploads/{id}  (server performs TUS termination)
    func deleteUpload(id: String) async throws
}
```

Notes:
- **URL construction is client-side.** The data endpoint is built as
  `baseURL / "uploads" / id / "data"` using `URL` path APIs (not string concat and
  not the server's `upload_url`, which is only the relative `/uploads/{id}/data`).
  A single private `dataURL(for:)` helper owns this join so trailing-slash behavior
  is defined in one place and unit-tested.
- `createUpload` returns the decoded record for both the `201 Created` and
  `200 OK` (already-exists) cases; the caller branches on `record.status`
  (`uploading`→resume, `complete`→skip, `backendLost`→re-register). The
  created-vs-existing HTTP distinction is not surfaced now (YAGNI); if a caller
  ever needs it we add it without breaking the model.
- `patchData` sets TUS headers: `Content-Type: application/offset+octet-stream`,
  `Upload-Offset`, `Tus-Resumable: 1.0.0`, and signals completion on the last
  chunk (per `architecture.md`; exact final header confirmed in §10). It returns
  the response's `Upload-Offset` so the caller can assert progress.

## 4. Data models

All `Codable & Sendable`. JSON is snake_case; request encoding uses explicit
`CodingKeys` (the server accepts both cases but we send the documented snake_case).

- **`CreateUploadRequest`**: `localIdentifier`, `filename`, `creationDate: Date`
  (encoded RFC3339/ISO8601), `bundleID: String?`, `metadata: [String: String]?`.
- **`UploadRecord`**: mirrors the server's stored record —
  `id`, `localIdentifier`, `status: UploadStatus`, `backendID`, `filename?`,
  `bundleID?`, `creationDate?`, `createdAt?`, `updatedAt?`. Fields absent from the
  lean create-response decode cleanly as `nil`. The server's `upload_url` is
  ignored (we build the data URL ourselves, §3) and is not a field on the model.
- **`UploadStatus`**: enum `uploading | complete | deleted | backendLost`
  (raw values `uploading`, `complete`, `deleted`, `backend_lost`). Decoding an
  unrecognized value throws a decoding error (strict; we control the server).
- **`UploadPage`**: `items: [UploadRecord]`, `nextCursor: String?` (empty
  `next_cursor` decodes to `nil`; pagination stops when `nil`).
- **`UploadOffset`**: `offset: Int64`, `length: Int64?` (`nil` when the server
  reports `Upload-Defer-Length: 1`).
- **`BasicCredentials`**: `username: String`, `password: String`.

## 5. Error model & 409 branching

```
enum ServerClientError: Error, Sendable, Equatable {
    case unauthorized                 // 401
    case notFound                     // 404
    case backendLost                  // 409 {"error":"backend_lost"}
    case alreadyCompleted             // 409 {"error":"upload already completed"}
    case notUploading                 // 409 {"error":"upload not in uploading state"}
    case offsetConflict               // 409 on PATCH: wrong Upload-Offset  (see §10)
    case badRequest(message: String)  // 400 {"error":"..."}
    case requestTooLarge              // 413
    case unexpectedStatus(code: Int, message: String?)   // other/5xx
    case decoding(String)             // response body didn't match the model
    case transport(String)            // URLSession-level failure
}
```

Central mapping: a private helper decodes the `{ "error": "..." }` body and maps
`(statusCode, errorString)` → the typed case. **409 is overloaded** on the server,
so the error string is the discriminator — `backend_lost` is the one
`SyncCoordinator` will branch on, so it must map exactly. `backendLost` is not a
failure to log-and-drop; it's a signal ("delete record, re-register") the caller
acts on.

## 6. Auth / credentials seam

`ServerClient` must not depend on Keychain (a later unit). It depends on a small
protocol:

```
protocol CredentialStore: Sendable {
    func basicCredentials() async throws -> BasicCredentials?
}
```

- `ServerClient` calls it per request and, if non-nil, adds
  `Authorization: Basic base64(user:pass)`. If `nil`, no header is sent (matches
  the server's unauthenticated dev mode).
- Tests inject a trivial fake returning fixed creds (or `nil`).
- Later, `KeychainStore` conforms to `CredentialStore`; `ServerClient` is unchanged.
- `async throws` so a Keychain-backed implementation can be actor-isolated and
  fail loudly rather than returning a bogus credential.

## 7. Concurrency & Swift 6

- Package compiles under Swift 6 language mode, complete concurrency checking, no
  warnings.
- `ServerClient` is a `Sendable` struct; all models and errors are `Sendable`.
- No `@MainActor` anywhere in this unit — it is isolation-free, callable from any
  context. (UI/main-actor concerns live in the app shells, not the core.)

## 8. Testing strategy

- `URLSession` + a custom `MockURLProtocol` registered on an injected
  `URLSessionConfiguration`. Tests stub status/headers/body and assert on the
  **actual request** the client produced. Fully headless (`swift test`), no server.
- Coverage:
  - **Per endpoint, happy path:** correct method, URL, headers, request body;
    correct decode of the response.
  - **Auth:** `Authorization: Basic ...` present and correctly encoded when creds
    exist; absent when the store returns `nil`.
  - **URL join:** `dataURL(for:)` is correct for `baseURL` with and without a
    trailing slash, and with a base subpath.
  - **Errors:** 401→`unauthorized`, 404→`notFound`, each 409 string→its typed
    case, 400→`badRequest`, 413→`requestTooLarge`, malformed body→`decoding`.
  - **TUS:** HEAD `Upload-Offset`/`Upload-Defer-Length` parsing; PATCH returns the
    response offset; final-chunk completion header is set.

## 9. Definition of done

- All 7 endpoints implemented as `async throws` methods returning the §4 models.
- Typed error mapping incl. exact 409 branching.
- Basic Auth via the injected `CredentialStore` seam.
- Builds in Swift 6 language mode with zero concurrency warnings.
- `swift test` green, covering §8.
- Zero dependency on Keychain, PhotoKit, SwiftUI, or any app target — pure Foundation.

## 10. Open items — all resolved

1. ~~`UploadRecord` exact fields~~ — confirmed against `server/internal/store`; modelled in
   `Models/UploadRecord.swift` and covered by `ModelCodingTests`.
2. ~~PATCH finalization header~~ — resolved: a resolved **`Upload-Length` on the last chunk**, not
   `Upload-Complete: 1`. `handlers.go:616-619` forwards the client's `Upload-Length` into
   `ForwardPatch`, and `tushandler_test.go:154-189` confirms deferred length survives PATCHes that
   omit it. Implemented as `patchData(finalLength:)`.
3. ~~409-on-PATCH disambiguation~~ — resolved: the server overloads 409 and the **error body string**
   is the discriminator. Mapped in `ServerClientError.map(status:body:)`, where match order matters
   because the PATCH-data handler emits a combined "already completed or deleted" message.
4. ~~`metadata` schema~~ — omitted. Live Photo pairing uses `bundleID`; the client has no use for
   `metadata` yet, so it is not sent.

## 11. Out of scope (future slices, each its own spec)

- `AssetUploader` — the PhotoKit → TUS streaming engine with capacity-1
  backpressure and the `XCTMemoryMetric` size-independence gate (the memory
  concern lives here, not in `ServerClient`).
- `SyncCoordinator` — listing/diffing/queueing, resume, `backendLost` recovery.
- `KeychainStore` — `CredentialStore` conformance backed by the Keychain.
- `AssetReader` — the `PHAssetResourceManager` seam + fakes.
- App shells (macOS menu-bar, iOS) — UI and background-execution strategy.
