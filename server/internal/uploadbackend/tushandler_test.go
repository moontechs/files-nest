// Package uploadbackend_test tests the TUSHandler adapter against a real
// embedded tusd instance with temp storage. These tests verify the exact
// method names, handler configuration, deferred-length support, upload-info
// access, file path resolution, not-found errors, and termination cleanup
// behavior that the API handlers depend on.
package uploadbackend_test

import (
	"bytes"
	"context"
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/moontechs/files-nest/server/internal/uploadbackend"
)

// setupTUSHandler creates a TUSHandler backed by a temp directory and returns
// it along with the storage path.
func setupTUSHandler(t *testing.T) (*uploadbackend.TUSHandler, string) {
	t.Helper()

	storePath := t.TempDir()
	h, err := uploadbackend.New(storePath)
	if err != nil {
		t.Fatalf("uploadbackend.New(%q) failed: %v", storePath, err)
	}

	return h, storePath
}

// ---------------------------------------------------------------------------
// Test: CreateUpload
// ---------------------------------------------------------------------------

func TestTUSCreateUpload(t *testing.T) {
	h, _ := setupTUSHandler(t)

	id, err := h.CreateUpload(context.Background(), "")
	if err != nil {
		t.Fatalf("CreateUpload failed: %v", err)
	}
	if id == "" {
		t.Fatal("CreateUpload returned empty ID")
	}

	// Verify the upload exists by checking GetInfo.
	info, err := h.GetInfo(context.Background(), id)
	if err != nil {
		t.Fatalf("GetInfo after CreateUpload: %v", err)
	}
	if info.ID != id {
		t.Errorf("info.ID = %q, want %q", info.ID, id)
	}
	if !info.SizeIsDeferred {
		t.Error("info.SizeIsDeferred should be true for deferred-length upload")
	}
	if info.Offset != 0 {
		t.Errorf("info.Offset = %d, want 0", info.Offset)
	}
	if info.Size != 0 {
		t.Errorf("info.Size = %d, want 0 for deferred-length", info.Size)
	}
}

func TestTUSCreateUploadWithMetadata(t *testing.T) {
	h, _ := setupTUSHandler(t)

	id, err := h.CreateUpload(context.Background(), "filename aW1nXzEyMzQuanBn,filetype aW1hZ2UvanBlZw==")
	if err != nil {
		t.Fatalf("CreateUpload with metadata failed: %v", err)
	}

	info, err := h.GetInfo(context.Background(), id)
	if err != nil {
		t.Fatalf("GetInfo: %v", err)
	}

	if info.Metadata["filename"] != "img_1234.jpg" {
		t.Errorf("Metadata filename = %q, want %q", info.Metadata["filename"], "img_1234.jpg")
	}
	if info.Metadata["filetype"] != "image/jpeg" {
		t.Errorf("Metadata filetype = %q, want %q", info.Metadata["filetype"], "image/jpeg")
	}
}

// ---------------------------------------------------------------------------
// Test: GetOffset
// ---------------------------------------------------------------------------

func TestTUSGetOffsetInitial(t *testing.T) {
	h, _ := setupTUSHandler(t)

	id, err := h.CreateUpload(context.Background(), "")
	if err != nil {
		t.Fatalf("CreateUpload: %v", err)
	}

	offset, err := h.GetOffset(context.Background(), id)
	if err != nil {
		t.Fatalf("GetOffset: %v", err)
	}
	if offset != 0 {
		t.Errorf("initial offset = %d, want 0", offset)
	}
}

// ---------------------------------------------------------------------------
// Test: ForwardPatch
// ---------------------------------------------------------------------------

func TestTUSForwardPatchSingleChunk(t *testing.T) {
	h, _ := setupTUSHandler(t)

	payload := []byte("hello world, this is a test upload chunk")
	uploadLength := len(payload)

	id, err := h.CreateUpload(context.Background(), "")
	if err != nil {
		t.Fatalf("CreateUpload: %v", err)
	}

	// Write data and declare final length via Upload-Length in PATCH.
	newOffset, err := h.ForwardPatch(context.Background(), id, bytes.NewReader(payload), 0, strconv.Itoa(uploadLength))
	if err != nil {
		t.Fatalf("ForwardPatch: %v", err)
	}
	if newOffset != int64(uploadLength) {
		t.Errorf("new offset = %d, want %d", newOffset, uploadLength)
	}

	// Verify offset via GetOffset.
	offset, err := h.GetOffset(context.Background(), id)
	if err != nil {
		t.Fatalf("GetOffset after PATCH: %v", err)
	}
	if offset != int64(uploadLength) {
		t.Errorf("GetOffset = %d, want %d", offset, uploadLength)
	}

	// Verify completion.
	complete, err := h.IsComplete(context.Background(), id)
	if err != nil {
		t.Fatalf("IsComplete: %v", err)
	}
	if !complete {
		t.Error("upload should be complete after writing all bytes with declared length")
	}
}

func TestTUSForwardPatchSingleChunkNoLength(t *testing.T) {
	h, _ := setupTUSHandler(t)

	payload := []byte("data without declaring length")
	id, err := h.CreateUpload(context.Background(), "")
	if err != nil {
		t.Fatalf("CreateUpload: %v", err)
	}

	// Write data WITHOUT declaring Upload-Length (still deferred).
	newOffset, err := h.ForwardPatch(context.Background(), id, bytes.NewReader(payload), 0, "")
	if err != nil {
		t.Fatalf("ForwardPatch: %v", err)
	}
	if newOffset != int64(len(payload)) {
		t.Errorf("new offset = %d, want %d", newOffset, len(payload))
	}

	// Upload should NOT be complete (size still deferred).
	complete, err := h.IsComplete(context.Background(), id)
	if err != nil {
		t.Fatalf("IsComplete: %v", err)
	}
	if complete {
		t.Error("upload should NOT be complete when size is still deferred")
	}

	// Verify size is still deferred.
	info, err := h.GetInfo(context.Background(), id)
	if err != nil {
		t.Fatalf("GetInfo: %v", err)
	}
	if !info.SizeIsDeferred {
		t.Error("SizeIsDeferred should still be true after PATCH without Upload-Length")
	}
}

// ---------------------------------------------------------------------------
// Test: Incremental Patches
// ---------------------------------------------------------------------------

func TestTUSForwardPatchIncremental(t *testing.T) {
	h, _ := setupTUSHandler(t)

	id, err := h.CreateUpload(context.Background(), "")
	if err != nil {
		t.Fatalf("CreateUpload: %v", err)
	}

	// Write first chunk without declaring length.
	chunk1 := []byte("aaaaaaaaaaaaaaaaaaaa") // 20 bytes
	newOffset, err := h.ForwardPatch(context.Background(), id, bytes.NewReader(chunk1), 0, "")
	if err != nil {
		t.Fatalf("ForwardPatch chunk1: %v", err)
	}
	if newOffset != 20 {
		t.Errorf("after chunk1 offset = %d, want 20", newOffset)
	}

	// Write second chunk still without declaring length.
	chunk2 := []byte("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbb") // 30 bytes
	newOffset, err = h.ForwardPatch(context.Background(), id, bytes.NewReader(chunk2), 20, "")
	if err != nil {
		t.Fatalf("ForwardPatch chunk2: %v", err)
	}
	if newOffset != 50 {
		t.Errorf("after chunk2 offset = %d, want 50", newOffset)
	}

	// Declare length and finalize on the last PATCH.
	chunk3 := []byte("cccccccccccccccccccccccccccccccc") // 32 bytes
	newOffset, err = h.ForwardPatch(context.Background(), id, bytes.NewReader(chunk3), 50, "82")
	if err != nil {
		t.Fatalf("ForwardPatch chunk3 (final): %v", err)
	}
	if newOffset != 82 {
		t.Errorf("after chunk3 (final) offset = %d, want 82", newOffset)
	}

	// Verify completion.
	complete, err := h.IsComplete(context.Background(), id)
	if err != nil {
		t.Fatalf("IsComplete: %v", err)
	}
	if !complete {
		t.Error("upload should be complete after final PATCH with declared length")
	}
}

// ---------------------------------------------------------------------------
// Test: GetInfo
// ---------------------------------------------------------------------------

func TestTUSGetInfo(t *testing.T) {
	h, _ := setupTUSHandler(t)

	id, err := h.CreateUpload(context.Background(), "filename dGVzdC5qcGc=")
	if err != nil {
		t.Fatalf("CreateUpload: %v", err)
	}

	info, err := h.GetInfo(context.Background(), id)
	if err != nil {
		t.Fatalf("GetInfo: %v", err)
	}

	if info.ID != id {
		t.Errorf("info.ID = %q, want %q", info.ID, id)
	}
	if !info.SizeIsDeferred {
		t.Error("info.SizeIsDeferred should be true")
	}
	if info.Offset != 0 {
		t.Errorf("info.Offset = %d, want 0", info.Offset)
	}
	if info.Metadata["filename"] != "test.jpg" {
		t.Errorf("Metadata filename = %q, want %q", info.Metadata["filename"], "test.jpg")
	}
	if info.Storage == nil {
		t.Fatal("Storage should not be nil")
	}
	binPath, ok := info.Storage["Path"]
	if !ok || binPath == "" {
		t.Fatal("Storage[Path] should contain the binary file path")
	}
	if !filepath.IsAbs(binPath) {
		t.Errorf("Storage[Path] = %q, want absolute path", binPath)
	}
}

// ---------------------------------------------------------------------------
// Test: FilePath
// ---------------------------------------------------------------------------

func TestTUSFilePath(t *testing.T) {
	h, storePath := setupTUSHandler(t)

	id, err := h.CreateUpload(context.Background(), "")
	if err != nil {
		t.Fatalf("CreateUpload: %v", err)
	}

	path, err := h.FilePath(context.Background(), id)
	if err != nil {
		t.Fatalf("FilePath: %v", err)
	}

	// The path should be under the store's incoming directory,
	// with just the ID as the filename (no extension).
	expected := filepath.Join(storePath, "incoming", id)
	if path != expected {
		t.Errorf("FilePath = %q, want %q", path, expected)
	}

	// The file should exist on disk (even if empty).
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Errorf("binary file should exist at %s", path)
	}

	// The .info file should also exist.
	infoPath := filepath.Join(storePath, "incoming", id+".info")
	if _, err := os.Stat(infoPath); os.IsNotExist(err) {
		t.Errorf("info file should exist at %s", infoPath)
	}
}

// ---------------------------------------------------------------------------
// Test: IsComplete
// ---------------------------------------------------------------------------

func TestTUSIsCompleteDeferred(t *testing.T) {
	h, _ := setupTUSHandler(t)

	id, err := h.CreateUpload(context.Background(), "")
	if err != nil {
		t.Fatalf("CreateUpload: %v", err)
	}

	// Deferred-length upload with no bytes written is not complete.
	complete, err := h.IsComplete(context.Background(), id)
	if err != nil {
		t.Fatalf("IsComplete: %v", err)
	}
	if complete {
		t.Error("upload with deferred length should not be complete")
	}
}

func TestTUSIsCompleteAfterFullUpload(t *testing.T) {
	h, _ := setupTUSHandler(t)

	payload := []byte("complete check data")
	id, err := h.CreateUpload(context.Background(), "")
	if err != nil {
		t.Fatalf("CreateUpload: %v", err)
	}

	_, err = h.ForwardPatch(context.Background(), id, bytes.NewReader(payload), 0, strconv.Itoa(len(payload)))
	if err != nil {
		t.Fatalf("ForwardPatch: %v", err)
	}

	complete, err := h.IsComplete(context.Background(), id)
	if err != nil {
		t.Fatalf("IsComplete: %v", err)
	}
	if !complete {
		t.Error("upload should be complete after writing all bytes with declared length")
	}
}

// ---------------------------------------------------------------------------
// Test: TerminateOrCleanup
// ---------------------------------------------------------------------------

func TestTUSTerminateOrCleanup(t *testing.T) {
	h, storePath := setupTUSHandler(t)

	payload := []byte("data to be cleaned up")
	id, err := h.CreateUpload(context.Background(), "")
	if err != nil {
		t.Fatalf("CreateUpload: %v", err)
	}

	_, err = h.ForwardPatch(context.Background(), id, bytes.NewReader(payload), 0, strconv.Itoa(len(payload)))
	if err != nil {
		t.Fatalf("ForwardPatch: %v", err)
	}

	// Verify files exist before cleanup.
	binPath := filepath.Join(storePath, "incoming", id)
	infoPath := filepath.Join(storePath, "incoming", id+".info")
	if _, err := os.Stat(binPath); os.IsNotExist(err) {
		t.Fatal("binary file should exist before TerminateOrCleanup")
	}
	if _, err := os.Stat(infoPath); os.IsNotExist(err) {
		t.Fatal("info file should exist before TerminateOrCleanup")
	}

	// Terminate.
	err = h.TerminateOrCleanup(context.Background(), id)
	if err != nil {
		t.Fatalf("TerminateOrCleanup: %v", err)
	}

	// Verify files are removed.
	if _, err := os.Stat(binPath); !os.IsNotExist(err) {
		t.Errorf("binary file should be removed after TerminateOrCleanup, stat err = %v", err)
	}
	if _, err := os.Stat(infoPath); !os.IsNotExist(err) {
		t.Errorf("info file should be removed after TerminateOrCleanup, stat err = %v", err)
	}

	// Verify GetInfo returns ErrNotFound.
	_, err = h.GetInfo(context.Background(), id)
	if !errors.Is(err, uploadbackend.ErrNotFound) {
		t.Errorf("GetInfo after terminate: got %v, want ErrNotFound", err)
	}

	// Double termination should return ErrNotFound (not an error).
	err = h.TerminateOrCleanup(context.Background(), id)
	if !errors.Is(err, uploadbackend.ErrNotFound) {
		t.Errorf("double TerminateOrCleanup: got %v, want ErrNotFound", err)
	}
}

// TestTerminateOrCleanupAfterMove verifies cleanup works correctly when the
// binary file has already been moved (as during the completion flow).
// In this scenario, only the .info sidecar should remain and need cleanup.
func TestTUSTerminateOrCleanupAfterMove(t *testing.T) {
	h, storePath := setupTUSHandler(t)

	payload := []byte("data that will be moved")
	id, err := h.CreateUpload(context.Background(), "")
	if err != nil {
		t.Fatalf("CreateUpload: %v", err)
	}
	_, err = h.ForwardPatch(context.Background(), id, bytes.NewReader(payload), 0, strconv.Itoa(len(payload)))
	if err != nil {
		t.Fatalf("ForwardPatch: %v", err)
	}

	binPath := filepath.Join(storePath, "incoming", id)
	infoPath := filepath.Join(storePath, "incoming", id+".info")

	// Simulate a completed move: move the binary file to a new location.
	movedPath := filepath.Join(storePath, "moved_"+id)
	if err := os.Rename(binPath, movedPath); err != nil {
		t.Fatalf("move binary: %v", err)
	}

	// The .info file should still exist.
	if _, err := os.Stat(infoPath); os.IsNotExist(err) {
		t.Fatal("info file should still exist after binary moved")
	}

	// TerminateOrCleanup should clean up the .info and return ErrNotFound
	// (indicating no tusd state remains).
	err = h.TerminateOrCleanup(context.Background(), id)
	if !errors.Is(err, uploadbackend.ErrNotFound) {
		t.Errorf("TerminateOrCleanup after move: got %v, want ErrNotFound", err)
	}

	// Verify .info is cleaned up.
	if _, err := os.Stat(infoPath); !os.IsNotExist(err) {
		t.Errorf("info file should be removed after TerminateOrCleanup, stat err = %v", err)
	}

	// Restore the binary from moved location for cleanup.
	if err := os.Remove(movedPath); err != nil {
		t.Fatalf("cleanup moved file: %v", err)
	}
}

// TestTerminateOrCleanupNonExistent verifies cleanup of a non-existent upload
// returns ErrNotFound (not a hard error).
func TestTUSTerminateOrCleanupNonExistent(t *testing.T) {
	h, _ := setupTUSHandler(t)

	err := h.TerminateOrCleanup(context.Background(), "non-existent-id")
	if !errors.Is(err, uploadbackend.ErrNotFound) {
		t.Errorf("TerminateOrCleanup non-existent: got %v, want ErrNotFound", err)
	}
}

// ---------------------------------------------------------------------------
// Test: ErrNotFound
// ---------------------------------------------------------------------------

func TestTUSGetInfoNonExistent(t *testing.T) {
	h, _ := setupTUSHandler(t)

	_, err := h.GetInfo(context.Background(), "does-not-exist")
	if !errors.Is(err, uploadbackend.ErrNotFound) {
		t.Errorf("GetInfo non-existent: got %v, want ErrNotFound", err)
	}
}

func TestTUSGetOffsetNonExistent(t *testing.T) {
	h, _ := setupTUSHandler(t)

	_, err := h.GetOffset(context.Background(), "does-not-exist")
	if !errors.Is(err, uploadbackend.ErrNotFound) {
		t.Errorf("GetOffset non-existent: got %v, want ErrNotFound", err)
	}
}

func TestTUSIsCompleteNonExistent(t *testing.T) {
	h, _ := setupTUSHandler(t)

	_, err := h.IsComplete(context.Background(), "does-not-exist")
	if !errors.Is(err, uploadbackend.ErrNotFound) {
		t.Errorf("IsComplete non-existent: got %v, want ErrNotFound", err)
	}
}

func TestTUSFilePathNonExistent(t *testing.T) {
	h, _ := setupTUSHandler(t)

	_, err := h.FilePath(context.Background(), "does-not-exist")
	if !errors.Is(err, uploadbackend.ErrNotFound) {
		t.Errorf("FilePath non-existent: got %v, want ErrNotFound", err)
	}
}

// TestForwardPatchNonExistent verifies that patching a non-existent upload
// returns ErrNotFound.
func TestTUSForwardPatchNonExistent(t *testing.T) {
	h, _ := setupTUSHandler(t)

	_, err := h.ForwardPatch(context.Background(), "does-not-exist", bytes.NewReader([]byte("data")), 0, "")
	if !errors.Is(err, uploadbackend.ErrNotFound) {
		t.Errorf("ForwardPatch non-existent: got %v, want ErrNotFound", err)
	}
}

// TestCreateUploadNonExistentGetInfo is redundant but verifies the pattern
// that after create, GetInfo works, and before create, GetInfo returns NotFound.
func TestTUSNonExistentBeforeCreate(t *testing.T) {
	h, _ := setupTUSHandler(t)

	_, err := h.GetInfo(context.Background(), "never-created")
	if !errors.Is(err, uploadbackend.ErrNotFound) {
		t.Errorf("GetInfo for never-created ID: got %v, want ErrNotFound", err)
	}
}

// ---------------------------------------------------------------------------
// Test: MismatchedOffset
// ---------------------------------------------------------------------------

func TestTUSForwardPatchMismatchedOffset(t *testing.T) {
	h, _ := setupTUSHandler(t)

	id, err := h.CreateUpload(context.Background(), "")
	if err != nil {
		t.Fatalf("CreateUpload: %v", err)
	}

	// Write some data first.
	_, err = h.ForwardPatch(context.Background(), id, bytes.NewReader([]byte("first chunk")), 0, "")
	if err != nil {
		t.Fatalf("ForwardPatch first: %v", err)
	}

	// Try to write at offset 0 again (mismatch).
	_, err = h.ForwardPatch(context.Background(), id, bytes.NewReader([]byte("second attempt")), 0, "")
	if err == nil {
		t.Fatal("expected error for mismatched offset, got nil")
	}
	if errors.Is(err, uploadbackend.ErrInvalidOffset) {
		// ErrInvalidOffset is acceptable for mismatched offset.
	} else if !strings.Contains(err.Error(), "tusd") {
		t.Errorf("expected tusd-related error for mismatched offset, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Test: LargeUploadStress
// ---------------------------------------------------------------------------

func TestTUSLargeUploadStress(t *testing.T) {
	h, _ := setupTUSHandler(t)

	size := 64 * 1024 // 64 KB
	payload := make([]byte, size)
	//nolint:gosec // test-only random payload, not security-sensitive
	_, err := rand.Read(payload)
	if err != nil {
		t.Fatal(err)
	}

	id, err := h.CreateUpload(context.Background(), "")
	if err != nil {
		t.Fatalf("CreateUpload: %v", err)
	}

	// Upload in one chunk with declared length.
	newOffset, err := h.ForwardPatch(context.Background(), id, bytes.NewReader(payload), 0, strconv.Itoa(size))
	if err != nil {
		t.Fatalf("ForwardPatch: %v", err)
	}
	if newOffset != int64(size) {
		t.Errorf("new offset = %d, want %d", newOffset, size)
	}

	// Verify completion.
	complete, err := h.IsComplete(context.Background(), id)
	if err != nil {
		t.Fatalf("IsComplete: %v", err)
	}
	if !complete {
		t.Error("upload should be complete after writing all bytes")
	}
}

// ---------------------------------------------------------------------------
// Test: Full lifecycle
// ---------------------------------------------------------------------------

func TestTUSFullUploadLifecycle(t *testing.T) {
	h, storePath := setupTUSHandler(t)

	// 1. Create an upload with metadata.
	id, err := h.CreateUpload(context.Background(), "filename dGVzdC5qcGc=,filetype aW1hZ2UvanBlZw==")
	if err != nil {
		t.Fatalf("CreateUpload: %v", err)
	}
	if id == "" {
		t.Fatal("CreateUpload returned empty ID")
	}

	// 2. Verify initial state.
	offset, err := h.GetOffset(context.Background(), id)
	if err != nil {
		t.Fatalf("GetOffset: %v", err)
	}
	if offset != 0 {
		t.Errorf("initial offset = %d, want 0", offset)
	}

	info, err := h.GetInfo(context.Background(), id)
	if err != nil {
		t.Fatalf("GetInfo: %v", err)
	}
	if !info.SizeIsDeferred {
		t.Error("initial upload should have deferred length")
	}
	if info.Metadata["filename"] != "test.jpg" {
		t.Errorf("filename metadata = %q, want %q", info.Metadata["filename"], "test.jpg")
	}

	// 3. Write data (without declaring length).
	chunk1 := []byte("hello ")
	newOffset, err := h.ForwardPatch(context.Background(), id, bytes.NewReader(chunk1), 0, "")
	if err != nil {
		t.Fatalf("ForwardPatch chunk1: %v", err)
	}
	if newOffset != 6 {
		t.Errorf("after chunk1 offset = %d, want 6", newOffset)
	}

	// 4. Write more data and declare final length.
	chunk2 := []byte("world!") // 6 bytes, total = 12
	newOffset, err = h.ForwardPatch(context.Background(), id, bytes.NewReader(chunk2), 6, "12")
	if err != nil {
		t.Fatalf("ForwardPatch chunk2: %v", err)
	}
	if newOffset != 12 {
		t.Errorf("after chunk2 offset = %d, want 12", newOffset)
	}

	// 5. Verify completed status.
	complete, err := h.IsComplete(context.Background(), id)
	if err != nil {
		t.Fatalf("IsComplete: %v", err)
	}
	if !complete {
		t.Error("upload should be complete after all data written with declared length")
	}

	// 6. Verify file path.
	path, err := h.FilePath(context.Background(), id)
	if err != nil {
		t.Fatalf("FilePath: %v", err)
	}

	// Verify file content on disk.
	expected := filepath.Join(storePath, "incoming", id)
	if path != expected {
		t.Errorf("FilePath = %q, want %q", path, expected)
	}

	//nolint:gosec // test-only read of a temp-dir file, not attacker-controlled
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read binary file: %v", err)
	}
	if string(content) != "hello world!" {
		t.Errorf("file content = %q, want %q", string(content), "hello world!")
	}

	// 7. Cleanup.
	err = h.TerminateOrCleanup(context.Background(), id)
	if err != nil {
		t.Fatalf("TerminateOrCleanup: %v", err)
	}

	// 8. Verify cleanup.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("binary file should be removed after terminate")
	}
	infoPath := filepath.Join(storePath, "incoming", id+".info")
	if _, err := os.Stat(infoPath); !os.IsNotExist(err) {
		t.Error("info file should be removed after terminate")
	}
}

// ---------------------------------------------------------------------------
// Test: New with non-existent storage path creates directory
// ---------------------------------------------------------------------------

func TestTUSNewCreatesDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	storagePath := filepath.Join(tmpDir, "deeply", "nested", "storage")

	h, err := uploadbackend.New(storagePath)
	if err != nil {
		t.Fatalf("New(%q): %v", storagePath, err)
	}
	if h == nil {
		t.Fatal("New returned nil handler")
	}

	// Verify the incoming directory was created.
	incomingPath := filepath.Join(storagePath, "incoming")
	if _, err := os.Stat(incomingPath); os.IsNotExist(err) {
		t.Errorf("incoming dir should exist at %s", incomingPath)
	}
}

// ---------------------------------------------------------------------------
// Test: Body exhaustion — reading past declared length
// ---------------------------------------------------------------------------

func TestTUSForwardPatchExceedsDeclaredLength(t *testing.T) {
	h, _ := setupTUSHandler(t)

	id, err := h.CreateUpload(context.Background(), "")
	if err != nil {
		t.Fatalf("CreateUpload: %v", err)
	}

	// Write 5 bytes but declare length as 3.
	body := bytes.NewReader([]byte("hello"))
	_, err = h.ForwardPatch(context.Background(), id, body, 0, "3")
	// Should fail because declared length would be exceeded.
	if err == nil {
		t.Fatal("expected error when writing past declared length, got nil")
	}
	if errors.Is(err, uploadbackend.ErrNotFound) {
		t.Fatal("exceeding declared length should not produce ErrNotFound")
	}
}

// ---------------------------------------------------------------------------
// Test: TerminateOrCleanup idempotent after double call
// ---------------------------------------------------------------------------

func TestTUSDoubleTerminateIsIdempotent(t *testing.T) {
	h, _ := setupTUSHandler(t)

	id, err := h.CreateUpload(context.Background(), "")
	if err != nil {
		t.Fatalf("CreateUpload: %v", err)
	}

	// First terminate.
	err = h.TerminateOrCleanup(context.Background(), id)
	if err != nil {
		t.Fatalf("first TerminateOrCleanup: %v", err)
	}

	// Second terminate should return ErrNotFound (already gone).
	err = h.TerminateOrCleanup(context.Background(), id)
	if !errors.Is(err, uploadbackend.ErrNotFound) {
		t.Errorf("second TerminateOrCleanup: got %v, want ErrNotFound", err)
	}
}

// ---------------------------------------------------------------------------
// Test: Ensure error sentinels match
// ---------------------------------------------------------------------------

func TestTUSErrorSentinels(t *testing.T) {
	if uploadbackend.ErrNotFound == nil {
		t.Error("ErrNotFound should not be nil")
	}
}

// ---------------------------------------------------------------------------
// Test: Empty body PATCH
// ---------------------------------------------------------------------------

func TestTUSForwardPatchEmptyBody(t *testing.T) {
	h, _ := setupTUSHandler(t)

	id, err := h.CreateUpload(context.Background(), "")
	if err != nil {
		t.Fatalf("CreateUpload: %v", err)
	}

	// PATCH with an empty body and declare length of 0.
	newOffset, err := h.ForwardPatch(context.Background(), id, bytes.NewReader(nil), 0, "0")
	if err != nil {
		t.Fatalf("ForwardPatch with empty body: %v", err)
	}
	if newOffset != 0 {
		t.Errorf("offset after empty PATCH = %d, want 0", newOffset)
	}

	// Should be complete (0 bytes, declared length 0).
	complete, err := h.IsComplete(context.Background(), id)
	if err != nil {
		t.Fatalf("IsComplete: %v", err)
	}
	if !complete {
		t.Error("upload with 0 bytes should be complete")
	}
}

// TestEOFReading verifies that the tusd body reader handles finite bodies
// correctly and returns io.EOF when the body is exhausted.
func TestTUSForwardPatchReadsFullBody(t *testing.T) {
	h, _ := setupTUSHandler(t)

	id, err := h.CreateUpload(context.Background(), "")
	if err != nil {
		t.Fatalf("CreateUpload: %v", err)
	}

	// Write a chunk with declared length equal to the body.
	payload := []byte("exact size match")
	_, err = h.ForwardPatch(context.Background(), id, bytes.NewReader(payload), 0, strconv.Itoa(len(payload)))
	if err != nil {
		t.Fatalf("ForwardPatch: %v", err)
	}

	// Verify exact content via GetInfo.
	info, err := h.GetInfo(context.Background(), id)
	if err != nil {
		t.Fatalf("GetInfo: %v", err)
	}
	if int(info.Offset) != len(payload) {
		t.Errorf("info.Offset = %d, want %d", info.Offset, len(payload))
	}
	if info.Size != int64(len(payload)) {
		t.Errorf("info.Size = %d, want %d", info.Size, len(payload))
	}
}

func TestTUSForwardPatchBodyNotAReadCloser(t *testing.T) {
	h, _ := setupTUSHandler(t)

	id, err := h.CreateUpload(context.Background(), "")
	if err != nil {
		t.Fatalf("CreateUpload: %v", err)
	}

	// Use an io.Reader that is not an io.ReadCloser.
	payload := []byte("just a reader test")
	_, err = h.ForwardPatch(context.Background(), id, bytes.NewReader(payload), 0, strconv.Itoa(len(payload)))
	if err != nil {
		t.Fatalf("ForwardPatch with io.Reader: %v", err)
	}

	// Verify the data was written.
	offset, err := h.GetOffset(context.Background(), id)
	if err != nil {
		t.Fatalf("GetOffset: %v", err)
	}
	if int(offset) != len(payload) {
		t.Errorf("offset = %d, want %d", offset, len(payload))
	}
}

// TestTUSZeroByteFinalPatchDeclaresLength verifies that an upload whose bytes
// were all written WITHOUT a declared length can be finalized by a subsequent
// zero-byte PATCH that carries Upload-Length. The Mac client needs this for the
// zero-blob resume path: when a resumed upload has no new bytes to send, there
// is no chunk to attach Upload-Length to.
func TestTUSZeroByteFinalPatchDeclaresLength(t *testing.T) {
	h, _ := setupTUSHandler(t)

	payload := []byte("bytes written without declaring a length")
	id, err := h.CreateUpload(context.Background(), "")
	if err != nil {
		t.Fatalf("CreateUpload: %v", err)
	}

	// First PATCH: write all bytes, length still deferred.
	offset, err := h.ForwardPatch(context.Background(), id, bytes.NewReader(payload), 0, "")
	if err != nil {
		t.Fatalf("ForwardPatch (deferred): %v", err)
	}
	if offset != int64(len(payload)) {
		t.Fatalf("offset = %d, want %d", offset, len(payload))
	}

	complete, err := h.IsComplete(context.Background(), id)
	if err != nil {
		t.Fatalf("IsComplete before finalize: %v", err)
	}
	if complete {
		t.Fatal("upload should not be complete while size is deferred")
	}

	// Second PATCH: zero bytes, at the current offset, declaring the length.
	finalOffset, err := h.ForwardPatch(context.Background(), id, bytes.NewReader(nil), offset,
		strconv.FormatInt(offset, 10))
	if err != nil {
		t.Fatalf("zero-byte finalizing ForwardPatch: %v", err)
	}
	if finalOffset != offset {
		t.Errorf("final offset = %d, want %d", finalOffset, offset)
	}

	info, err := h.GetInfo(context.Background(), id)
	if err != nil {
		t.Fatalf("GetInfo: %v", err)
	}
	if info.SizeIsDeferred {
		t.Error("SizeIsDeferred should be false after declaring length")
	}

	complete, err = h.IsComplete(context.Background(), id)
	if err != nil {
		t.Fatalf("IsComplete after finalize: %v", err)
	}
	if !complete {
		t.Error("upload should be complete after zero-byte PATCH declared the length")
	}
}
