package api_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moontechs/files-nest/server/internal/api"
	"github.com/moontechs/files-nest/server/internal/store"
	"github.com/moontechs/files-nest/server/internal/uploadbackend"
)

// ---------------------------------------------------------------------------
// Test setup helpers
// ---------------------------------------------------------------------------

// setupRecovery creates a store, tusd backend, and storage temp directory for
// recovery testing. Returns the Recoverer, store, storage path, and backend.
func setupRecovery(t *testing.T) (*api.Recoverer, *store.Store, string, *uploadbackend.TUSHandler) {
	t.Helper()

	storageDir := t.TempDir()

	// Create store
	st, err := store.Open(filepath.Join(storageDir, "db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	// Create tusd backend (incoming/ is created inside storageDir)
	bh, err := uploadbackend.New(storageDir)
	if err != nil {
		t.Fatalf("uploadbackend.New: %v", err)
	}

	rec := api.NewRecoverer(st, bh, storageDir)
	return rec, st, storageDir, bh
}

// saveIntent is a test helper that creates and persists a CompletionIntent.
func saveIntent(t *testing.T, st *store.Store, intent *store.CompletionIntent) {
	t.Helper()
	if err := st.SaveCompletionIntent(intent); err != nil {
		t.Fatalf("SaveCompletionIntent: %v", err)
	}
}

// createIntent returns a populated CompletionIntent for testing.
func createIntent(id, src, dst, dstRel, backendID string) *store.CompletionIntent {
	return &store.CompletionIntent{
		ID:        id,
		BackendID: backendID,
		Src:       src,
		Dst:       dst,
		DstRel:    dstRel,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
}

// writeTestFile creates a file at the given path with known content.
func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("MkdirAll %s: %v", dir, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}

// fileContent reads the content of a file, failing the test on error.
func fileContent(t *testing.T, path string) string {
	t.Helper()
	//nolint:gosec // test-only read of a temp-dir file, not attacker-controlled
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile %s: %v", path, err)
	}
	return string(data)
}

// fileExists returns true if a non-directory exists at path.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// ---------------------------------------------------------------------------
// No intents to recover
// ---------------------------------------------------------------------------

func TestRecover_NoIntents(t *testing.T) {
	//nolint:dogsled // test only needs the Recoverer; other fixtures are intentionally ignored
	rec, _, _, _ := setupRecovery(t)

	// Should not error when there are no intents.
	if err := rec.Recover(); err != nil {
		t.Fatalf("Recover with no intents: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Recover intent: file already moved (dst exists, src gone)
// ---------------------------------------------------------------------------

func TestRecover_Intent_AlreadyMoved(t *testing.T) {
	rec, st, storageDir, _ := setupRecovery(t)

	id := "test-already-moved"
	backendID := "tusd-already-moved-001"
	dstRel := "organized/2024/03/15/IMG_0001.jpg"

	// Create the destination file (simulating a completed move).
	dst := filepath.Join(storageDir, dstRel)
	writeTestFile(t, dst, "file content after move")

	// The source file should NOT exist — it was already moved.
	src := filepath.Join(storageDir, "incoming", backendID)

	// Save a completion intent.
	intent := createIntent(id, src, dst, dstRel, backendID)
	saveIntent(t, st, intent)

	// Create an upload record for the intent.
	upload := &store.Upload{
		ID:              id,
		LocalIdentifier: "ALREADY-MOVED-001/L0/000",
		Status:          store.StatusUploading,
		BackendID:       backendID,
		Filename:        "IMG_0001.jpg",
		CreationDate:    creationDate,
		CreatedAt:       time.Now().UTC().Format(time.RFC3339),
		UpdatedAt:       time.Now().UTC().Format(time.RFC3339),
	}
	if _, _, err := st.PutUploadIfAbsent(upload); err != nil {
		t.Fatalf("PutUploadIfAbsent: %v", err)
	}

	// Run recovery.
	if err := rec.Recover(); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	// Verify the DB record was updated to complete.
	got, err := st.GetUpload(id)
	if err != nil {
		t.Fatalf("GetUpload after recovery: %v", err)
	}
	if got.Status != store.StatusComplete {
		t.Errorf("expected status complete, got %q", got.Status)
	}
	if got.OrganizedPath != dstRel {
		t.Errorf("expected organized_path %q, got %q", dstRel, got.OrganizedPath)
	}

	// Verify the completion intent was deleted.
	intentCheck, err := st.GetCompletionIntent(id)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetCompletionIntent after recovery: %v", err)
	}
	if intentCheck != nil {
		t.Error("completion intent should have been deleted after recovery")
	}

	// Verify the destination file still exists with correct content.
	if !fileExists(dst) {
		t.Error("destination file should still exist after recovery")
	}
	if got := fileContent(t, dst); got != "file content after move" {
		t.Errorf("destination file content: got %q, want %q", got, "file content after move")
	}
}

// ---------------------------------------------------------------------------
// Recover intent: file still at source (src exists, dst gone)
// ---------------------------------------------------------------------------

func TestRecover_Intent_NotYetMoved(t *testing.T) {
	rec, st, storageDir, _ := setupRecovery(t)

	id := "test-not-yet-moved"
	backendID := "tusd-not-yet-moved-001"
	dstRel := "organized/2024/03/15/IMG_0002.jpg"
	dst := filepath.Join(storageDir, dstRel)
	src := filepath.Join(storageDir, "incoming", backendID)

	// Create the source file (simulating a pending move).
	writeTestFile(t, src, "source content not yet moved")

	// Save a completion intent.
	intent := createIntent(id, src, dst, dstRel, backendID)
	saveIntent(t, st, intent)

	// Create an upload record.
	upload := &store.Upload{
		ID:              id,
		LocalIdentifier: "NOT-YET-MOVED-001/L0/000",
		Status:          store.StatusUploading,
		BackendID:       backendID,
		Filename:        "IMG_0002.jpg",
		CreationDate:    creationDate,
		CreatedAt:       time.Now().UTC().Format(time.RFC3339),
		UpdatedAt:       time.Now().UTC().Format(time.RFC3339),
	}
	if _, _, err := st.PutUploadIfAbsent(upload); err != nil {
		t.Fatalf("PutUploadIfAbsent: %v", err)
	}

	// Run recovery.
	if err := rec.Recover(); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	// Verify the DB record was updated to complete.
	got, err := st.GetUpload(id)
	if err != nil {
		t.Fatalf("GetUpload after recovery: %v", err)
	}
	if got.Status != store.StatusComplete {
		t.Errorf("expected status complete, got %q", got.Status)
	}
	if got.OrganizedPath != dstRel {
		t.Errorf("expected organized_path %q, got %q", dstRel, got.OrganizedPath)
	}

	// Verify the file was moved to the destination.
	if !fileExists(dst) {
		t.Error("destination file should exist after recovery")
	}
	if got := fileContent(t, dst); got != "source content not yet moved" {
		t.Errorf("destination content: got %q, want %q", got, "source content not yet moved")
	}

	// Verify the source file was removed.
	if fileExists(src) {
		t.Error("source file should have been removed after move")
	}

	// Verify the intent was deleted.
	intentCheck, err := st.GetCompletionIntent(id)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetCompletionIntent: %v", err)
	}
	if intentCheck != nil {
		t.Error("completion intent should have been deleted after recovery")
	}
}

// ---------------------------------------------------------------------------
// Recover intent: both src and dst exist (recoverable edge case)
// ---------------------------------------------------------------------------

func TestRecover_Intent_BothExist(t *testing.T) {
	rec, st, storageDir, _ := setupRecovery(t)

	id := "test-both-exist"
	backendID := "tusd-both-exist-001"
	dstRel := "organized/2024/03/15/IMG_0003.jpg"
	dst := filepath.Join(storageDir, dstRel)
	src := filepath.Join(storageDir, "incoming", backendID)

	// Create both source and destination files.
	writeTestFile(t, src, "source copy")
	writeTestFile(t, dst, "destination copy")

	// Save a completion intent.
	intent := createIntent(id, src, dst, dstRel, backendID)
	saveIntent(t, st, intent)

	// Create an upload record.
	upload := &store.Upload{
		ID:              id,
		LocalIdentifier: "BOTH-EXIST-001/L0/000",
		Status:          store.StatusUploading,
		BackendID:       backendID,
		Filename:        "IMG_0003.jpg",
		CreationDate:    creationDate,
		CreatedAt:       time.Now().UTC().Format(time.RFC3339),
		UpdatedAt:       time.Now().UTC().Format(time.RFC3339),
	}
	if _, _, err := st.PutUploadIfAbsent(upload); err != nil {
		t.Fatalf("PutUploadIfAbsent: %v", err)
	}

	// Run recovery.
	if err := rec.Recover(); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	// Verify the DB record was updated to complete.
	got, err := st.GetUpload(id)
	if err != nil {
		t.Fatalf("GetUpload after recovery: %v", err)
	}
	if got.Status != store.StatusComplete {
		t.Errorf("expected status complete, got %q", got.Status)
	}

	// Verify the source file was removed (recovery removes src when both exist).
	if fileExists(src) {
		t.Error("source file should have been removed when both src and dst existed")
	}

	// Verify the destination file still exists.
	if !fileExists(dst) {
		t.Error("destination file should still exist")
	}

	// Verify the intent was deleted.
	intentCheck, err := st.GetCompletionIntent(id)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetCompletionIntent: %v", err)
	}
	if intentCheck != nil {
		t.Error("completion intent should have been deleted")
	}
}

// ---------------------------------------------------------------------------
// Recover intent: record already complete (stale intent)
// ---------------------------------------------------------------------------

func TestRecover_Intent_AlreadyComplete(t *testing.T) {
	rec, st, storageDir, _ := setupRecovery(t)

	id := "test-already-complete"
	backendID := "tusd-already-complete-001"
	dstRel := "organized/2024/03/15/IMG_0011.jpg"
	dst := filepath.Join(storageDir, dstRel)
	src := filepath.Join(storageDir, "incoming", backendID)

	// Create the destination file (simulating a completed move).
	writeTestFile(t, dst, "file content")

	// Save a completion intent (stale — the record is already complete).
	intent := createIntent(id, src, dst, dstRel, backendID)
	saveIntent(t, st, intent)

	// Create an upload record with status already complete.
	upload := &store.Upload{
		ID:              id,
		LocalIdentifier: "ALREADY-COMPLETE-001/L0/000",
		Status:          store.StatusComplete,
		BackendID:       backendID,
		Filename:        "IMG_0011.jpg",
		CreationDate:    creationDate,
		CreatedAt:       time.Now().UTC().Format(time.RFC3339),
		UpdatedAt:       time.Now().UTC().Format(time.RFC3339),
		OrganizedPath:   dstRel,
	}
	if _, _, err := st.PutUploadIfAbsent(upload); err != nil {
		t.Fatalf("PutUploadIfAbsent: %v", err)
	}

	// Run recovery.
	if err := rec.Recover(); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	// Verify the record is still complete (unchanged).
	got, err := st.GetUpload(id)
	if err != nil {
		t.Fatalf("GetUpload after recovery: %v", err)
	}
	if got.Status != store.StatusComplete {
		t.Errorf("expected status complete, got %q", got.Status)
	}
	if got.OrganizedPath != dstRel {
		t.Errorf("expected organized_path %q, got %q", dstRel, got.OrganizedPath)
	}

	// Verify the stale intent was deleted.
	intentCheck, err := st.GetCompletionIntent(id)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetCompletionIntent: %v", err)
	}
	if intentCheck != nil {
		t.Error("stale completion intent should have been deleted")
	}

	// Verify the destination file still exists.
	if !fileExists(dst) {
		t.Error("destination file should still exist")
	}
}

// ---------------------------------------------------------------------------
// Recover intent: neither src nor dst exists (data lost)
// ---------------------------------------------------------------------------

func TestRecover_Intent_DataLost(t *testing.T) {
	rec, st, storageDir, _ := setupRecovery(t)

	id := "test-data-lost"
	backendID := "tusd-data-lost-001"
	dstRel := "organized/2024/03/15/IMG_0004.jpg"
	dst := filepath.Join(storageDir, dstRel)
	src := filepath.Join(storageDir, "incoming", backendID)

	// Do NOT create any files — simulate data loss.

	// Save a completion intent.
	intent := createIntent(id, src, dst, dstRel, backendID)
	saveIntent(t, st, intent)

	// Create an upload record.
	originalStatus := store.StatusUploading
	upload := &store.Upload{
		ID:              id,
		LocalIdentifier: "DATA-LOST-001/L0/000",
		Status:          originalStatus,
		BackendID:       backendID,
		Filename:        "IMG_0004.jpg",
		CreationDate:    creationDate,
		CreatedAt:       time.Now().UTC().Format(time.RFC3339),
		UpdatedAt:       time.Now().UTC().Format(time.RFC3339),
	}
	if _, _, err := st.PutUploadIfAbsent(upload); err != nil {
		t.Fatalf("PutUploadIfAbsent: %v", err)
	}

	// Run recovery.
	if err := rec.Recover(); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	// Verify the DB record was NOT changed — it should still have the
	// original status because recovery leaves data-lost records untouched
	// for manual inspection.
	got, err := st.GetUpload(id)
	if err != nil {
		t.Fatalf("GetUpload after recovery: %v", err)
	}
	if got.Status != originalStatus {
		t.Errorf("expected status to remain %q, got %q", originalStatus, got.Status)
	}

	// Verify the intent was NOT deleted — it is kept for manual repair.
	intentCheck, err := st.GetCompletionIntent(id)
	if err != nil {
		t.Fatalf("GetCompletionIntent: %v", err)
	}
	if intentCheck == nil {
		t.Error("completion intent should have been kept after data loss for manual repair")
	}
}

// ---------------------------------------------------------------------------
// Recover intent: upload record already deleted
// ---------------------------------------------------------------------------

func TestRecover_Intent_UploadRecordDeleted(t *testing.T) {
	rec, st, storageDir, _ := setupRecovery(t)

	id := "test-record-deleted"
	backendID := "tusd-record-deleted-001"
	dstRel := "organized/2024/03/15/IMG_0005.jpg"
	dst := filepath.Join(storageDir, dstRel)
	src := filepath.Join(storageDir, "incoming", backendID)

	// Create the destination file (already moved).
	writeTestFile(t, dst, "file content")

	// Save a completion intent but do NOT create a corresponding upload record.
	intent := createIntent(id, src, dst, dstRel, backendID)
	saveIntent(t, st, intent)

	// Run recovery.
	if err := rec.Recover(); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	// Verify the intent was deleted (cleaned up orphan).
	intentCheck, err := st.GetCompletionIntent(id)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetCompletionIntent: %v", err)
	}
	if intentCheck != nil {
		t.Error("orphaned completion intent should have been deleted")
	}

	// Verify the destination file still exists.
	if !fileExists(dst) {
		t.Error("destination file should still exist")
	}
}

// ---------------------------------------------------------------------------
// Recover intent: with tusd sidecar cleanup
// ---------------------------------------------------------------------------

func TestRecover_Intent_WithTusdCleanup(t *testing.T) {
	rec, st, storageDir, bh := setupRecovery(t)

	// Create a real tusd upload to generate an .info sidecar.
	backendID, err := bh.CreateUpload(context.Background(), "filename "+"aW1nXzAwMDEuanBn")
	if err != nil {
		t.Fatalf("CreateUpload: %v", err)
	}

	// Write some data to the upload so there's a binary file.
	data := []byte("test content for tusd cleanup recovery")
	_, err = bh.ForwardPatch(context.Background(), backendID, strings.NewReader(string(data)), 0, "")
	if err != nil {
		t.Fatalf("ForwardPatch: %v", err)
	}

	// Get the file path to use as src in the intent.
	srcPath, err := bh.FilePath(context.Background(), backendID)
	if err != nil {
		t.Fatalf("FilePath: %v", err)
	}

	id := "test-tusd-cleanup"
	dstRel := "organized/2024/03/15/IMG_0006.jpg"
	dst := filepath.Join(storageDir, dstRel)

	// Write content to dst to simulate a completed move.
	writeTestFile(t, dst, string(data))

	// Remove the source file (simulating move).
	if err := os.Remove(srcPath); err != nil {
		t.Fatalf("Remove source: %v", err)
	}

	// Save a completion intent.
	intent := createIntent(id, srcPath, dst, dstRel, backendID)
	saveIntent(t, st, intent)

	// Create an upload record.
	upload := &store.Upload{
		ID:              id,
		LocalIdentifier: "TUSD-CLEANUP-001/L0/000",
		Status:          store.StatusUploading,
		BackendID:       backendID,
		Filename:        "IMG_0006.jpg",
		CreationDate:    creationDate,
		CreatedAt:       time.Now().UTC().Format(time.RFC3339),
		UpdatedAt:       time.Now().UTC().Format(time.RFC3339),
	}
	if _, _, err := st.PutUploadIfAbsent(upload); err != nil {
		t.Fatalf("PutUploadIfAbsent: %v", err)
	}

	// Run recovery.
	if err := rec.Recover(); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	// Verify the DB record was updated to complete.
	got, err := st.GetUpload(id)
	if err != nil {
		t.Fatalf("GetUpload after recovery: %v", err)
	}
	if got.Status != store.StatusComplete {
		t.Errorf("expected status complete, got %q", got.Status)
	}

	// Verify the tusd backend was cleaned up (.info sidecar removed).
	_, err = bh.GetInfo(context.Background(), backendID)
	if !errors.Is(err, uploadbackend.ErrNotFound) {
		t.Errorf("expected backend to be cleaned up, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Multiple intents
// ---------------------------------------------------------------------------

func TestRecover_MultipleIntents(t *testing.T) {
	rec, st, storageDir, _ := setupRecovery(t)

	// Create 3 intents in different states. Use unique filenames so dst
	// paths do not overlap (the recovery code only checks the intent's own
	// src and dst, but overlapping dst paths would cause cross-contamination
	// when one intent's dst file is created by another intent).
	intents := []struct {
		id        string
		backendID string
		state     string // "moved", "not-moved", "lost"
	}{
		{"multi-001", "tusd-multi-001", statusMoved},
		{"multi-002", "tusd-multi-002", statusNotMoved},
		{"multi-003", "tusd-multi-003", statusLost},
	}

	for _, it := range intents {
		dstRel := "organized/2024/03/15/" + it.id + ".jpg"
		dst := filepath.Join(storageDir, dstRel)
		src := filepath.Join(storageDir, "incoming", it.backendID)

		switch it.state {
		case statusMoved:
			writeTestFile(t, dst, "content "+it.id)
		case statusNotMoved:
			writeTestFile(t, src, "content "+it.id)
		case statusLost:
			// No files created.
		}

		intent := createIntent(it.id, src, dst, dstRel, it.backendID)
		saveIntent(t, st, intent)

		upload := &store.Upload{
			ID:              it.id,
			LocalIdentifier: "MULTI-" + it.id + "/L0/000",
			Status:          store.StatusUploading,
			BackendID:       it.backendID,
			Filename:        it.id + ".jpg",
			CreationDate:    creationDate,
			CreatedAt:       time.Now().UTC().Format(time.RFC3339),
			UpdatedAt:       time.Now().UTC().Format(time.RFC3339),
		}
		if _, _, err := st.PutUploadIfAbsent(upload); err != nil {
			t.Fatalf("PutUploadIfAbsent for %s: %v", it.id, err)
		}
	}

	// Run recovery.
	if err := rec.Recover(); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	// Verify all three intents were resolved.
	for _, it := range intents {
		got, err := st.GetUpload(it.id)
		if err != nil {
			t.Fatalf("GetUpload %s: %v", it.id, err)
		}

		switch it.state {
		case statusMoved, statusNotMoved:
			if got.Status != store.StatusComplete {
				t.Errorf("intent %s: expected status complete, got %q", it.id, got.Status)
			}
		case statusLost:
			// Data-lost records are left unchanged for manual repair.
			if got.Status != store.StatusUploading {
				t.Errorf("intent %s: expected status to remain uploading, got %q", it.id, got.Status)
			}
		}

		// Verify intent status.
		intentCheck, err := st.GetCompletionIntent(it.id)
		switch it.state {
		case statusMoved, statusNotMoved:
			if !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("GetCompletionIntent %s: %v", it.id, err)
			}
			if intentCheck != nil {
				t.Errorf("intent %s should have been deleted", it.id)
			}
		case statusLost:
			if err != nil {
				t.Fatalf("GetCompletionIntent %s: %v", it.id, err)
			}
			if intentCheck == nil {
				t.Errorf("intent %s should have been kept for manual repair", it.id)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Backend lost recovery
// ---------------------------------------------------------------------------

func TestRecover_BackendLost(t *testing.T) {
	rec, st, _, _ := setupRecovery(t)

	id := "test-backend-lost-recovery"
	backendID := "tusd-backend-lost-rec-001"

	// Create an upload record that references a backend ID that does not exist.
	upload := &store.Upload{
		ID:              id,
		LocalIdentifier: "BACKEND-LOST-REC-001/L0/000",
		Status:          store.StatusUploading,
		BackendID:       backendID,
		Filename:        "IMG_0007.jpg",
		CreationDate:    creationDate,
		CreatedAt:       time.Now().UTC().Format(time.RFC3339),
		UpdatedAt:       time.Now().UTC().Format(time.RFC3339),
	}
	if _, _, err := st.PutUploadIfAbsent(upload); err != nil {
		t.Fatalf("PutUploadIfAbsent: %v", err)
	}

	// Run recovery.
	if err := rec.Recover(); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	// Verify the record was updated to backend_lost.
	got, err := st.GetUpload(id)
	if err != nil {
		t.Fatalf("GetUpload: %v", err)
	}
	if got.Status != store.StatusBackendLost {
		t.Errorf("expected status backend_lost, got %q", got.Status)
	}
}

func TestRecover_BackendLost_OnlyNonExistent(t *testing.T) {
	rec, st, _, bh := setupRecovery(t)

	// Create a real tusd upload so it should NOT be marked as lost.
	realBackendID, err := bh.CreateUpload(context.Background(), "filename "+"aW1nXzAwMDguanBn")
	if err != nil {
		t.Fatalf("CreateUpload: %v", err)
	}

	realID := "test-backend-still-exists"
	upload := &store.Upload{
		ID:              realID,
		LocalIdentifier: "BACKEND-EXISTS-001/L0/000",
		Status:          store.StatusUploading,
		BackendID:       realBackendID,
		Filename:        "IMG_0008.jpg",
		CreationDate:    creationDate,
		CreatedAt:       time.Now().UTC().Format(time.RFC3339),
		UpdatedAt:       time.Now().UTC().Format(time.RFC3339),
	}
	if _, _, err := st.PutUploadIfAbsent(upload); err != nil {
		t.Fatalf("PutUploadIfAbsent: %v", err)
	}

	// Also create one with a non-existent backend.
	lostID := "test-backend-lost-002"
	lostBackendID := "tusd-nonexistent-002"
	lostUpload := &store.Upload{
		ID:              lostID,
		LocalIdentifier: "BACKEND-LOST-002/L0/000",
		Status:          store.StatusUploading,
		BackendID:       lostBackendID,
		Filename:        "IMG_0009.jpg",
		CreationDate:    creationDate,
		CreatedAt:       time.Now().UTC().Format(time.RFC3339),
		UpdatedAt:       time.Now().UTC().Format(time.RFC3339),
	}
	if _, _, err := st.PutUploadIfAbsent(lostUpload); err != nil {
		t.Fatalf("PutUploadIfAbsent: %v", err)
	}

	// Run recovery.
	if err := rec.Recover(); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	// Verify only the lost backend was updated.
	stillExists, err := st.GetUpload(realID)
	if err != nil {
		t.Fatalf("GetUpload: %v", err)
	}
	if stillExists.Status != store.StatusUploading {
		t.Errorf("upload with live backend should still be uploading, got %q", stillExists.Status)
	}

	wasLost, err := st.GetUpload(lostID)
	if err != nil {
		t.Fatalf("GetUpload: %v", err)
	}
	if wasLost.Status != store.StatusBackendLost {
		t.Errorf("upload with missing backend should be backend_lost, got %q", wasLost.Status)
	}
}

// ---------------------------------------------------------------------------
// Recover is idempotent
// ---------------------------------------------------------------------------

func TestRecover_Idempotent(t *testing.T) {
	rec, st, storageDir, _ := setupRecovery(t)

	id := "test-idempotent"
	backendID := "tusd-idempotent-001"
	dstRel := "organized/2024/03/15/IMG_0010.jpg"
	dst := filepath.Join(storageDir, dstRel)
	src := filepath.Join(storageDir, "incoming", backendID)

	// Destination exists (already moved).
	writeTestFile(t, dst, "idempotent content")

	// Save intent.
	intent := createIntent(id, src, dst, dstRel, backendID)
	saveIntent(t, st, intent)

	// Create upload record.
	upload := &store.Upload{
		ID:              id,
		LocalIdentifier: "IDEMPOTENT-001/L0/000",
		Status:          store.StatusUploading,
		BackendID:       backendID,
		Filename:        "IMG_0010.jpg",
		CreationDate:    creationDate,
		CreatedAt:       time.Now().UTC().Format(time.RFC3339),
		UpdatedAt:       time.Now().UTC().Format(time.RFC3339),
	}
	if _, _, err := st.PutUploadIfAbsent(upload); err != nil {
		t.Fatalf("PutUploadIfAbsent: %v", err)
	}

	// Run recovery twice.
	if err := rec.Recover(); err != nil {
		t.Fatalf("first Recover: %v", err)
	}
	if err := rec.Recover(); err != nil {
		t.Fatalf("second Recover: %v", err)
	}

	// Verify the record is complete.
	got, err := st.GetUpload(id)
	if err != nil {
		t.Fatalf("GetUpload: %v", err)
	}
	if got.Status != store.StatusComplete {
		t.Errorf("expected status complete, got %q", got.Status)
	}

	// Verify intent was deleted after first recovery.
	intentCheck, err := st.GetCompletionIntent(id)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetCompletionIntent: %v", err)
	}
	if intentCheck != nil {
		t.Error("completion intent should have been deleted after first recovery")
	}
}

// ---------------------------------------------------------------------------
// No upload records at all (empty store)
// ---------------------------------------------------------------------------

func TestRecover_EmptyStore(t *testing.T) {
	//nolint:dogsled // test only needs the Recoverer; other fixtures are intentionally ignored
	rec, _, _, _ := setupRecovery(t)

	if err := rec.Recover(); err != nil {
		t.Fatalf("Recover on empty store: %v", err)
	}
}
