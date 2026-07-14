# Delete organized files on DELETE /uploads/:id request

## Overview
Modify `DELETE /uploads/:id` to also remove the organized file from disk when the upload was previously completed (has `organized_path`). Currently the handler only terminates the tusd backend and marks the DB record as `deleted` — the organized file stays on disk forever.

## Context
- Current DELETE handler at `server/internal/api/handlers.go` (line ~834)
- Mover interface at `server/internal/filestore/mover.go` — no existing `Remove` method
- Existing delete tests at `server/internal/api/handlers_test.go` (line ~1879)
- Current README says "Does **not** remove the organized file" in DELETE docs
- **No multi-account**: single-user self-hosted server, no user/account/tenant field in Upload struct
- Storage layout: `$STORAGE_PATH/organized/YYYY/MM/DD/filename` — no user prefix
- `organized_path` is relative to `$STORAGE_PATH`, file removal is `os.Remove(filepath.Join(storagePath, upload.OrganizedPath))`
- File-level idempotency: file already gone on second DELETE should be silently tolerated
- Path safety: `organized_path` is set by our own `UpdateComplete`, itself fed only by `Mover.PlanDestination`/`SafePathSegment` output — never external input. `filepath.Join` alone does **not** prevent traversal (it cleans but does not clamp `..` segments), so `RemoveOrganizedFile` must explicitly verify the resolved absolute path is still contained within `storagePath` before removing it (see Task 1)

## Decisions

These were explicitly confirmed before implementation (see critique in conversation history):

- **Hard delete, no trash/retention window.** `DELETE /uploads/:id` permanently removes the organized backup file with no recovery path. This is a deliberate, accepted tradeoff for this self-hosted single-user server — not an oversight. The README (Task 4) must state this plainly ("permanently deletes the backed-up file — this cannot be undone") so callers aren't surprised.
- **File-removal failures are logged only; the DB record is still marked `deleted` and the response is still 204.** No reconciliation job or failure-surfacing mechanism is being built in this task. Accepted risk: a file removal that fails (e.g. permission error) leaves an orphaned file on disk with no automated way to detect it short of a manual disk-vs-DB scan. If this becomes a real problem in practice, a follow-up task can add reconciliation.
- **Empty parent directories under `organized/YYYY/MM/DD/` are not cleaned up after file removal.** Out of scope — minor disk bloat (empty dirs, not data) is an accepted tradeoff to keep this change small.

## Development Approach
- **Testing approach**: Code first, then tests
- Complete each task fully before moving to the next
- Every task that modifies code must include new/updated tests
- All tests must pass before starting the next task

## Implementation Steps

### Task 1: Add organized file removal to filestore.Mover

**Files:**
- Modify: `server/internal/filestore/mover.go`
- Modify: `server/internal/filestore/mover_test.go`

- [x] Add `RemoveOrganizedFile(organizedPath string) error` method to `Mover`
  - Empty path: return nil immediately (clean no-op for uploads that were never completed)
  - Join `m.storagePath` with `organizedPath` to get the absolute path, then `filepath.Clean` both the result and `m.storagePath` and verify the joined path is still contained within the storage root (e.g. `strings.HasPrefix(cleanAbs, cleanRoot+string(filepath.Separator))`). If not contained, return an error without touching the filesystem — defense-in-depth in case `organized_path` is ever fed from a source other than `Mover`'s own output
  - Acquire `m.moveMu` for the duration of the removal (same mutex used by `PlanAndMove`/`MoveToPlaned`/`MoveFile`). This closes the same TOCTOU class the mutex already exists to prevent: without it, a delete could run its `os.Remove` concurrently with another upload's `PlanDestination` collision-`os.Stat` + rename to the same computed path (possible via completion-intent retry landing on an identical destination). Removal is rare and the mutex hold time is a single `os.Remove` call, so this has no meaningful throughput impact
  - Call `os.Remove` on the absolute path
  - If `os.IsNotExist(err)`, return nil (idempotent — file may already be deleted or never existed)
  - Any other error (including permission errors) is returned to the caller — per the logged-only decision above, the handler will log it and continue, it is not fatal to the DELETE request
- [x] Write tests for `RemoveOrganizedFile`:
  - Success: creates a file, calls RemoveOrganizedFile, verifies it's gone
  - Idempotent: calling RemoveOrganizedFile twice returns nil both times
  - Non-existent file: returns nil (not an error)
  - Empty path: returns nil (clean no-op)
  - Path traversal: calling with a path like `../../etc/passwd` (or containing `..` segments) returns an error and does not remove anything outside the storage root
  - Permission denied: create a file with a read-only parent directory (or use a mock/chmod), verify RemoveOrganizedFile returns the underlying error rather than swallowing it
- [x] Run `go test ./internal/filestore/...` — must pass before Task 2

### Task 2: Integrate file deletion into DELETE handler

**Files:**
- Modify: `server/internal/api/handlers.go`

- [x] After the tusd `TerminateOrCleanup` call (and before the DB status update), check if `upload.OrganizedPath != ""`
- [x] If non-empty, call `h.mover.RemoveOrganizedFile(upload.OrganizedPath)`
- [x] Log the error if removal fails but do NOT abort the overall DELETE operation — the record should still be marked `deleted` in DB even if file cleanup fails
- [x] Continue with existing `UpdateStatus` to `deleted`

Key ordering:
- TUS terminate first (may clean up sidecar files)
- File deletion from organized/ second
- DB update to `deleted` last
- All errors from file ops are logged but non-fatal

### Task 3: Update DELETE handler tests

**Files:**
- Modify: `server/internal/api/handlers_test.go`

- [x] Add test `TestHandleDeleteUpload_RemovesOrganizedFile`: create a completed upload (upload data + PATCH status complete to get an organized_path), then DELETE it, verify the organized file is removed from disk
- [x] Add test `TestHandleDeleteUpload_NoOrganizedPath`: create an uploading-only upload (never completed, organized_path is empty), DELETE it, verify no file deletion attempted
- [x] Add test `TestHandleDeleteUpload_OrganizedFileAlreadyGone`: create completed upload, manually remove the file, then DELETE — should succeed (idempotent)
- [x] Update `TestHandleDeleteUpload_Success` to also verify the organized file is removed
- [x] Run `go test ./internal/api/... -run Delete` — must pass

### Task 4: Update README documentation

**Files:**
- Modify: `server/README.md`

- [x] Update the `DELETE /uploads/:id` section: change "Does **not** remove the organized file" to state plainly that the organized file IS now removed, that this is **permanent and unrecoverable** (no trash/retention window), and that file-removal failures are logged but do not block the record from being marked `deleted` (i.e. a 204 response does not guarantee the file was actually removed from disk)
- [x] Verify no other references to the old behavior need updating

### Task 5: Final verification

- [ ] `go test ./...` — all tests pass
- [ ] `go vet ./...` — no issues
- [ ] Verify README accurately describes the new DELETE behavior
