package store_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/moontechs/files-nest/server/internal/api"
	"github.com/moontechs/files-nest/server/internal/store"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// openTestStore opens a Store backed by a temp directory.
func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "db"))
	if err != nil {
		t.Fatalf("Store.Open failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// testUpload creates a minimal Upload suitable for tests.
func testUpload(localIdentifier string, overrides map[string]string) *store.Upload {
	id := api.SafeID(localIdentifier)
	creationDate := "2024-03-15T10:30:00Z"
	if v, ok := overrides["creationDate"]; ok {
		creationDate = v
	}
	filename := "IMG_1234.jpg"
	if v, ok := overrides["filename"]; ok {
		filename = v
	}
	status := store.StatusUploading
	if v, ok := overrides["status"]; ok {
		status = store.Status(v)
	}
	now := time.Now().UTC().Format(time.RFC3339)

	return &store.Upload{
		ID:              id,
		LocalIdentifier: localIdentifier,
		Status:          status,
		BackendID:       "tusd-" + id[:8],
		Filename:        filename,
		CreationDate:    creationDate,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

// assertUploadEqual checks two uploads for equality on key fields.
func assertUploadEqual(t *testing.T, got, want *store.Upload) {
	t.Helper()
	if got.ID != want.ID {
		t.Errorf("ID: got %q, want %q", got.ID, want.ID)
	}
	if got.LocalIdentifier != want.LocalIdentifier {
		t.Errorf("LocalIdentifier: got %q, want %q", got.LocalIdentifier, want.LocalIdentifier)
	}
	if got.Status != want.Status {
		t.Errorf("Status: got %q, want %q", got.Status, want.Status)
	}
	if got.BackendID != want.BackendID {
		t.Errorf("BackendID: got %q, want %q", got.BackendID, want.BackendID)
	}
	if got.Filename != want.Filename {
		t.Errorf("Filename: got %q, want %q", got.Filename, want.Filename)
	}
	if got.CreationDate != want.CreationDate {
		t.Errorf("CreationDate: got %q, want %q", got.CreationDate, want.CreationDate)
	}
}

// ---------------------------------------------------------------------------
// CreateUpload
// ---------------------------------------------------------------------------

func TestCreateUpload_Success(t *testing.T) {
	s := openTestStore(t)
	u := testUpload("asset-001", nil)

	if err := s.CreateUpload(u); err != nil {
		t.Fatalf("CreateUpload failed: %v", err)
	}

	got, err := s.GetUpload(u.ID)
	if err != nil {
		t.Fatalf("GetUpload failed: %v", err)
	}
	assertUploadEqual(t, got, u)
}

func TestCreateUpload_IndexesAreWritten(t *testing.T) {
	s := openTestStore(t)
	u := testUpload("asset-localidx", nil)

	if err := s.CreateUpload(u); err != nil {
		t.Fatalf("CreateUpload failed: %v", err)
	}

	// Verify lookup by local identifier works (proves local index exists)
	got, err := s.UploadByLocalIdentifier(u.LocalIdentifier)
	if err != nil {
		t.Fatalf("UploadByLocalIdentifier failed: %v", err)
	}
	if got == nil {
		t.Fatal("UploadByLocalIdentifier returned nil, expected upload")
	}
	assertUploadEqual(t, got, u)

	// Verify lookup by backend ID works (proves backend index exists)
	got2, err := s.UploadByBackendID(u.BackendID)
	if err != nil {
		t.Fatalf("UploadByBackendID failed: %v", err)
	}
	if got2 == nil {
		t.Fatal("UploadByBackendID returned nil, expected upload")
	}
	assertUploadEqual(t, got2, u)

	// Verify list by status works (proves status index exists)
	uploads, err := s.ListByStatus(store.StatusUploading)
	if err != nil {
		t.Fatalf("ListByStatus failed: %v", err)
	}
	if len(uploads) == 0 {
		t.Fatal("ListByStatus returned no uploads, expected at least 1")
	}
}

func TestCreateUpload_Conflict(t *testing.T) {
	s := openTestStore(t)
	u := testUpload("asset-002", nil)

	if err := s.CreateUpload(u); err != nil {
		t.Fatalf("first CreateUpload failed: %v", err)
	}

	// Second attempt with same localIdentifier should conflict
	u2 := testUpload("asset-002", nil)
	u2.BackendID = "different-backend-id"

	err := s.CreateUpload(u2)
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("expected ErrConflict, got: %v", err)
	}

	// Verify the original record is unchanged (backendID should still be the first one)
	got, err := s.GetUpload(u.ID)
	if err != nil {
		t.Fatalf("GetUpload failed: %v", err)
	}
	if got.BackendID != u.BackendID {
		t.Errorf("BackendID changed after conflict: got %q, want %q", got.BackendID, u.BackendID)
	}
}

func TestCreateUpload_WithMetadata(t *testing.T) {
	s := openTestStore(t)
	u := testUpload("asset-meta", nil)

	meta := json.RawMessage(`{"latitude":37.7749,"longitude":-122.4194}`)
	u.Metadata = meta

	if err := s.CreateUpload(u); err != nil {
		t.Fatalf("CreateUpload failed: %v", err)
	}

	got, err := s.GetUpload(u.ID)
	if err != nil {
		t.Fatalf("GetUpload failed: %v", err)
	}
	if got.Metadata == nil {
		t.Fatal("expected metadata to be stored")
	}

	// Compare semantically by unmarshalling both
	var gotObj, wantObj map[string]float64
	if err := json.Unmarshal(got.Metadata, &gotObj); err != nil {
		t.Fatalf("unmarshal got metadata: %v", err)
	}
	if err := json.Unmarshal(meta, &wantObj); err != nil {
		t.Fatalf("unmarshal want metadata: %v", err)
	}
	if gotObj["latitude"] != wantObj["latitude"] {
		t.Errorf("latitude: got %v, want %v", gotObj["latitude"], wantObj["latitude"])
	}
	if gotObj["longitude"] != wantObj["longitude"] {
		t.Errorf("longitude: got %v, want %v", gotObj["longitude"], wantObj["longitude"])
	}
}

// ---------------------------------------------------------------------------
// PutUploadIfAbsent / Idempotency
// ---------------------------------------------------------------------------

func TestPutUploadIfAbsent_CreatesNew(t *testing.T) {
	s := openTestStore(t)
	u := testUpload("asset-putifabsent-new", nil)

	got, created, err := s.PutUploadIfAbsent(u)
	if err != nil {
		t.Fatalf("PutUploadIfAbsent failed: %v", err)
	}
	if !created {
		t.Fatal("expected created=true for new upload")
	}
	if got == nil {
		t.Fatal("expected non-nil upload")
	}
	assertUploadEqual(t, got, u)

	// Verify it was actually persisted
	fromDB, err := s.GetUpload(u.ID)
	if err != nil {
		t.Fatalf("GetUpload failed after PutUploadIfAbsent: %v", err)
	}
	assertUploadEqual(t, fromDB, u)
}

func TestPutUploadIfAbsent_ReturnsExistingOnDuplicate(t *testing.T) {
	s := openTestStore(t)
	u := testUpload("asset-putifabsent-dup", nil)

	// First create
	got1, created, err := s.PutUploadIfAbsent(u)
	if err != nil {
		t.Fatalf("first PutUploadIfAbsent failed: %v", err)
	}
	if !created {
		t.Fatal("expected created=true on first put")
	}
	if got1 == nil {
		t.Fatal("expected non-nil upload on first put")
	}
	assertUploadEqual(t, got1, u)

	// Second create with same localIdentifier should return existing
	u2 := testUpload("asset-putifabsent-dup", nil)
	u2.BackendID = "different-backend-id"
	u2.Filename = "different.jpg"

	got2, created, err := s.PutUploadIfAbsent(u2)
	if err != nil {
		t.Fatalf("second PutUploadIfAbsent failed: %v", err)
	}
	if created {
		t.Fatal("expected created=false for duplicate localIdentifier")
	}
	if got2 == nil {
		t.Fatal("expected non-nil upload on duplicate")
	}

	// Should return the original record, not the proposed new one
	if got2.BackendID != u.BackendID {
		t.Errorf("BackendID: got %q, want original %q", got2.BackendID, u.BackendID)
	}
	if got2.Filename != u.Filename {
		t.Errorf("Filename: got %q, want original %q", got2.Filename, u.Filename)
	}
	if got2.Status != u.Status {
		t.Errorf("Status: got %q, want original %q", got2.Status, u.Status)
	}
}

func TestPutUploadIfAbsent_ReturnsExistingRecordWithAllFields(t *testing.T) {
	s := openTestStore(t)
	u := testUpload("asset-putifabsent-full", map[string]string{
		"creationDate": "2024-06-15T08:30:00Z",
		"filename":     "IMG_9999.jpg",
		"status":       string(store.StatusCompleting),
	})
	u.BundleID = "BUNDLE-123/L0/000"
	u.OrganizedPath = "organized/2024/06/15/IMG_9999.jpg"

	// Create
	_, created, err := s.PutUploadIfAbsent(u)
	if err != nil {
		t.Fatalf("first PutUploadIfAbsent failed: %v", err)
	}
	if !created {
		t.Fatal("expected created=true")
	}

	// Update the record in the DB
	updated, err := s.UpdateStatus(u.ID, store.StatusComplete)
	if err != nil {
		t.Fatalf("UpdateStatus failed: %v", err)
	}

	// Fetch via PutUploadIfAbsent again (simulates POST /uploads on existing record)
	u2 := testUpload("asset-putifabsent-full", nil)
	got2, created, err := s.PutUploadIfAbsent(u2)
	if err != nil {
		t.Fatalf("second PutUploadIfAbsent failed: %v", err)
	}
	if created {
		t.Fatal("expected created=false for existing record")
	}
	if got2 == nil {
		t.Fatal("expected non-nil upload")
	}

	// Must return the updated record (status=complete), not the original
	if got2.Status != store.StatusComplete {
		t.Errorf("Status: got %q, want %q", got2.Status, store.StatusComplete)
	}
	if got2.UpdatedAt != updated.UpdatedAt {
		t.Errorf("UpdatedAt mismatch: got %q, want %q", got2.UpdatedAt, updated.UpdatedAt)
	}
}

func TestPutUploadIfAbsent_IdempotentAcrossMultipleCalls(t *testing.T) {
	s := openTestStore(t)
	u := testUpload("asset-putifabsent-multi", nil)

	// Call three times — only first should create
	for i := range 3 {
		got, created, err := s.PutUploadIfAbsent(u)
		if err != nil {
			t.Fatalf("PutUploadIfAbsent call %d failed: %v", i, err)
		}
		if i == 0 && !created {
			t.Fatal("first call should create")
		}
		if i > 0 && created {
			t.Fatal("subsequent calls should not create")
		}
		if got == nil {
			t.Fatal("expected non-nil upload")
		}
		if got.ID != u.ID {
			t.Errorf("call %d: ID = %q, want %q", i, got.ID, u.ID)
		}
	}
}

func TestPutUploadIfAbsent_IndexesWrittenOnCreate(t *testing.T) {
	s := openTestStore(t)
	u := testUpload("asset-putifabsent-idx", nil)

	_, created, err := s.PutUploadIfAbsent(u)
	if err != nil {
		t.Fatalf("PutUploadIfAbsent failed: %v", err)
	}
	if !created {
		t.Fatal("expected created=true")
	}

	// Verify all indexes exist
	got, err := s.UploadByLocalIdentifier(u.LocalIdentifier)
	if err != nil {
		t.Fatalf("UploadByLocalIdentifier: %v", err)
	}
	if got == nil {
		t.Fatal("upload not found by local identifier after PutUploadIfAbsent")
	}

	got2, err := s.UploadByBackendID(u.BackendID)
	if err != nil {
		t.Fatalf("UploadByBackendID: %v", err)
	}
	if got2 == nil {
		t.Fatal("upload not found by backend ID after PutUploadIfAbsent")
	}

	uploads, err := s.ListByStatus(store.StatusUploading)
	if err != nil {
		t.Fatalf("ListByStatus: %v", err)
	}
	if len(uploads) != 1 {
		t.Errorf("expected 1 uploading, got %d", len(uploads))
	}
}

// ---------------------------------------------------------------------------
// GetUpload
// ---------------------------------------------------------------------------

func TestGetUpload_NotFound(t *testing.T) {
	s := openTestStore(t)

	_, err := s.GetUpload("nonexistent-id")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got: %v", err)
	}
}

func TestGetUpload_EmptyID(t *testing.T) {
	s := openTestStore(t)

	_, err := s.GetUpload("")
	if !errors.Is(err, store.ErrNotFound) {
		t.Log("note: empty ID should result in ErrNotFound")
	}
}

// ---------------------------------------------------------------------------
// UploadByLocalIdentifier
// ---------------------------------------------------------------------------

func TestUploadByLocalIdentifier_NotFound(t *testing.T) {
	s := openTestStore(t)

	got, err := s.UploadByLocalIdentifier("does-not-exist")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("UploadByLocalIdentifier failed: %v", err)
	}
	if got != nil {
		t.Fatal("expected nil for unknown local identifier")
	}
}

func TestUploadByLocalIdentifier_WithSpecialChars(t *testing.T) {
	s := openTestStore(t)
	// A localIdentifier with '/' and other special characters
	id := "ABCD1234-1234-1234-1234-123456789012/L0/040"
	u := testUpload(id, nil)

	if err := s.CreateUpload(u); err != nil {
		t.Fatalf("CreateUpload failed: %v", err)
	}

	got, err := s.UploadByLocalIdentifier(id)
	if err != nil {
		t.Fatalf("UploadByLocalIdentifier failed: %v", err)
	}
	if got == nil {
		t.Fatal("expected upload, got nil")
	}
	assertUploadEqual(t, got, u)
}

// ---------------------------------------------------------------------------
// UploadByBackendID
// ---------------------------------------------------------------------------

func TestUploadByBackendID_NotFound(t *testing.T) {
	s := openTestStore(t)

	got, err := s.UploadByBackendID("nonexistent-backend")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("UploadByBackendID failed: %v", err)
	}
	if got != nil {
		t.Fatal("expected nil for unknown backend ID")
	}
}

func TestUploadByBackendID_Success(t *testing.T) {
	s := openTestStore(t)
	u := testUpload("asset-backend-lookup", nil)

	if err := s.CreateUpload(u); err != nil {
		t.Fatalf("CreateUpload failed: %v", err)
	}

	got, err := s.UploadByBackendID(u.BackendID)
	if err != nil {
		t.Fatalf("UploadByBackendID failed: %v", err)
	}
	if got == nil {
		t.Fatal("expected upload, got nil")
	}
	assertUploadEqual(t, got, u)
}

// ---------------------------------------------------------------------------
// UpdateStatus
// ---------------------------------------------------------------------------

func TestUpdateStatus_Success(t *testing.T) {
	s := openTestStore(t)
	u := testUpload("asset-status-1", nil)

	if err := s.CreateUpload(u); err != nil {
		t.Fatalf("CreateUpload failed: %v", err)
	}

	updated, err := s.UpdateStatus(u.ID, store.StatusComplete)
	if err != nil {
		t.Fatalf("UpdateStatus failed: %v", err)
	}
	if updated == nil {
		t.Fatal("UpdateStatus returned nil upload")
	}
	if updated.Status != store.StatusComplete {
		t.Errorf("status after update: got %q, want %q", updated.Status, store.StatusComplete)
	}

	// Verify the store reflects the change
	got, err := s.GetUpload(u.ID)
	if err != nil {
		t.Fatalf("GetUpload failed: %v", err)
	}
	if got.Status != store.StatusComplete {
		t.Errorf("stored status: got %q, want %q", got.Status, store.StatusComplete)
	}
}

func TestUpdateStatus_MultipleTransitions(t *testing.T) {
	s := openTestStore(t)
	u := testUpload("asset-status-multi", nil)

	if err := s.CreateUpload(u); err != nil {
		t.Fatalf("CreateUpload failed: %v", err)
	}

	// uploading → completing
	updated, err := s.UpdateStatus(u.ID, store.StatusCompleting)
	if err != nil {
		t.Fatalf("UpdateStatus to completing failed: %v", err)
	}
	if updated.Status != store.StatusCompleting {
		t.Errorf("expected status %q, got %q", store.StatusCompleting, updated.Status)
	}

	// completing → complete
	updated, err = s.UpdateStatus(u.ID, store.StatusComplete)
	if err != nil {
		t.Fatalf("UpdateStatus to complete failed: %v", err)
	}
	if updated.Status != store.StatusComplete {
		t.Errorf("expected status %q, got %q", store.StatusComplete, updated.Status)
	}

	// complete → deleted (via UpdateStatus)
	updated, err = s.UpdateStatus(u.ID, store.StatusDeleted)
	if err != nil {
		t.Fatalf("UpdateStatus to deleted failed: %v", err)
	}
	if updated.Status != store.StatusDeleted {
		t.Errorf("expected status %q, got %q", store.StatusDeleted, updated.Status)
	}
}

func TestUpdateStatus_NotFound(t *testing.T) {
	s := openTestStore(t)

	_, err := s.UpdateStatus("nonexistent-id", store.StatusComplete)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got: %v", err)
	}
}

func TestUpdateStatus_NoGhostKeys(t *testing.T) {
	// Critical invariant: after UpdateStatus, the old status index key MUST
	// not exist. A ghost key would corrupt every status-based scan.
	s := openTestStore(t)
	u := testUpload("asset-noghost", nil)

	if err := s.CreateUpload(u); err != nil {
		t.Fatalf("CreateUpload failed: %v", err)
	}

	// Verify it shows up in the uploading list
	before, err := s.ListByStatus(store.StatusUploading)
	if err != nil {
		t.Fatalf("ListByStatus(uploading) before: %v", err)
	}
	if len(before) != 1 {
		t.Fatalf("expected 1 uploading upload, got %d", len(before))
	}

	// Transition to complete
	if _, err := s.UpdateStatus(u.ID, store.StatusComplete); err != nil {
		t.Fatalf("UpdateStatus failed: %v", err)
	}

	// Verify it no longer shows up in uploading
	afterUploading, err := s.ListByStatus(store.StatusUploading)
	if err != nil {
		t.Fatalf("ListByStatus(uploading) after: %v", err)
	}
	if len(afterUploading) != 0 {
		t.Errorf("expected 0 uploading uploads after status change, got %d", len(afterUploading))
	}

	// Verify it shows up in complete
	afterComplete, err := s.ListByStatus(store.StatusComplete)
	if err != nil {
		t.Fatalf("ListByStatus(complete): %v", err)
	}
	if len(afterComplete) != 1 {
		t.Errorf("expected 1 complete upload, got %d", len(afterComplete))
	}

	// Transition to backend_lost
	if _, err := s.UpdateStatus(u.ID, store.StatusBackendLost); err != nil {
		t.Fatalf("UpdateStatus to backend_lost failed: %v", err)
	}

	// Verify complete is empty (no ghost)
	afterLost, err := s.ListByStatus(store.StatusBackendLost)
	if err != nil {
		t.Fatalf("ListByStatus(backend_lost): %v", err)
	}
	if len(afterLost) != 1 {
		t.Errorf("expected 1 backend_lost upload, got %d", len(afterLost))
	}

	afterComplete2, err := s.ListByStatus(store.StatusComplete)
	if err != nil {
		t.Fatalf("ListByStatus(complete) after transition: %v", err)
	}
	if len(afterComplete2) != 0 {
		t.Errorf("expected 0 complete uploads after status change (ghost!), got %d", len(afterComplete2))
	}
}

func TestUpdateStatus_MultipleRecordsSameStatus(t *testing.T) {
	s := openTestStore(t)

	// Create three uploading records
	for i := range 3 {
		u := testUpload(fmt.Sprintf("asset-bulk-%d", i), nil)
		if err := s.CreateUpload(u); err != nil {
			t.Fatalf("CreateUpload %d failed: %v", i, err)
		}
	}

	before, err := s.ListByStatus(store.StatusUploading)
	if err != nil {
		t.Fatalf("ListByStatus failed: %v", err)
	}
	if len(before) != 3 {
		t.Fatalf("expected 3 uploading records, got %d", len(before))
	}

	// Complete two of them
	ids := make([]string, 3)
	for i, u := range before {
		ids[i] = u.ID
	}

	if _, err := s.UpdateStatus(ids[0], store.StatusComplete); err != nil {
		t.Fatalf("UpdateStatus 0 failed: %v", err)
	}
	if _, err := s.UpdateStatus(ids[2], store.StatusComplete); err != nil {
		t.Fatalf("UpdateStatus 2 failed: %v", err)
	}

	// Check
	afterUploading, err := s.ListByStatus(store.StatusUploading)
	if err != nil {
		t.Fatalf("ListByStatus after: %v", err)
	}
	if len(afterUploading) != 1 {
		t.Errorf("expected 1 uploading after completing 2, got %d", len(afterUploading))
	}
	if len(afterUploading) > 0 && afterUploading[0].ID != ids[1] {
		t.Errorf("expected remaining uploading to be id[1]=%s, got %s", ids[1], afterUploading[0].ID)
	}

	afterComplete, err := s.ListByStatus(store.StatusComplete)
	if err != nil {
		t.Fatalf("ListByStatus complete: %v", err)
	}
	if len(afterComplete) != 2 {
		t.Errorf("expected 2 complete, got %d", len(afterComplete))
	}
}

// ---------------------------------------------------------------------------
// UpdateComplete
// ---------------------------------------------------------------------------

func TestUpdateComplete_Success(t *testing.T) {
	s := openTestStore(t)
	u := testUpload("asset-complete-1", nil)

	if err := s.CreateUpload(u); err != nil {
		t.Fatalf("CreateUpload failed: %v", err)
	}

	organizedPath := "organized/2024/03/15/IMG_1234.jpg"
	updated, err := s.UpdateComplete(u.ID, organizedPath)
	if err != nil {
		t.Fatalf("UpdateComplete failed: %v", err)
	}
	if updated == nil {
		t.Fatal("UpdateComplete returned nil")
	}
	if updated.Status != store.StatusComplete {
		t.Errorf("status: got %q, want %q", updated.Status, store.StatusComplete)
	}
	if updated.OrganizedPath != organizedPath {
		t.Errorf("OrganizedPath: got %q, want %q", updated.OrganizedPath, organizedPath)
	}

	// Verify the store reflects the change
	got, err := s.GetUpload(u.ID)
	if err != nil {
		t.Fatalf("GetUpload failed: %v", err)
	}
	if got.Status != store.StatusComplete {
		t.Errorf("stored status: got %q, want %q", got.Status, store.StatusComplete)
	}
	if got.OrganizedPath != organizedPath {
		t.Errorf("stored OrganizedPath: got %q, want %q", got.OrganizedPath, organizedPath)
	}
}

func TestUpdateComplete_NotFound(t *testing.T) {
	s := openTestStore(t)

	_, err := s.UpdateComplete("nonexistent-id", "organized/path.jpg")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got: %v", err)
	}
}

func TestUpdateComplete_GhostKeyPrevention(t *testing.T) {
	s := openTestStore(t)
	u := testUpload("asset-complete-ghost", nil)

	if err := s.CreateUpload(u); err != nil {
		t.Fatalf("CreateUpload failed: %v", err)
	}

	// Verify it shows up in uploading list
	before, err := s.ListByStatus(store.StatusUploading)
	if err != nil {
		t.Fatalf("ListByStatus(uploading) before: %v", err)
	}
	if len(before) != 1 {
		t.Fatalf("expected 1 uploading, got %d", len(before))
	}

	// Complete it
	if _, err := s.UpdateComplete(u.ID, "organized/2024/path.jpg"); err != nil {
		t.Fatalf("UpdateComplete failed: %v", err)
	}

	// Verify it no longer shows up in uploading
	afterUploading, err := s.ListByStatus(store.StatusUploading)
	if err != nil {
		t.Fatalf("ListByStatus(uploading) after: %v", err)
	}
	if len(afterUploading) != 0 {
		t.Errorf("expected 0 uploading after Complete, got %d (ghost!)", len(afterUploading))
	}

	// Verify it shows up in complete
	afterComplete, err := s.ListByStatus(store.StatusComplete)
	if err != nil {
		t.Fatalf("ListByStatus(complete): %v", err)
	}
	if len(afterComplete) != 1 {
		t.Errorf("expected 1 complete, got %d", len(afterComplete))
	}
	if afterComplete[0].OrganizedPath != "organized/2024/path.jpg" {
		t.Errorf("OrganizedPath in complete list: got %q, want %q",
			afterComplete[0].OrganizedPath, "organized/2024/path.jpg")
	}
}

func TestUpdateComplete_OrganizedPathPersisted(t *testing.T) {
	s := openTestStore(t)
	u := testUpload("asset-complete-path", nil)

	if err := s.CreateUpload(u); err != nil {
		t.Fatalf("CreateUpload failed: %v", err)
	}

	paths := []string{
		"organized/2024/01/01/IMG_0001.jpg",
		"organized/2024/12/31/IMG_9999_abc123.jpg",
		"organized/some/deep/nested/path/file.txt",
	}

	// Test three records with different paths
	for i, path := range paths {
		localID := fmt.Sprintf("asset-complete-path-%d", i)
		u := testUpload(localID, nil)
		if err := s.CreateUpload(u); err != nil {
			t.Fatalf("CreateUpload %d failed: %v", i, err)
		}

		updated, err := s.UpdateComplete(u.ID, path)
		if err != nil {
			t.Fatalf("UpdateComplete %d failed: %v", i, err)
		}
		if updated.OrganizedPath != path {
			t.Errorf("returned OrganizedPath %d: got %q, want %q", i, updated.OrganizedPath, path)
		}

		got, err := s.GetUpload(u.ID)
		if err != nil {
			t.Fatalf("GetUpload %d failed: %v", i, err)
		}
		if got.OrganizedPath != path {
			t.Errorf("stored OrganizedPath %d: got %q, want %q", i, got.OrganizedPath, path)
		}
	}
}

func TestUpdateComplete_WithStatusIndexConsistency(t *testing.T) {
	s := openTestStore(t)

	// Create two uploading records
	u1 := testUpload("asset-complete-consist-1", nil)
	u2 := testUpload("asset-complete-consist-2", nil)

	if err := s.CreateUpload(u1); err != nil {
		t.Fatalf("CreateUpload u1 failed: %v", err)
	}
	if err := s.CreateUpload(u2); err != nil {
		t.Fatalf("CreateUpload u2 failed: %v", err)
	}

	// Complete one
	if _, err := s.UpdateComplete(u1.ID, "organized/path1.jpg"); err != nil {
		t.Fatalf("UpdateComplete u1 failed: %v", err)
	}

	// Verify status lists
	uploading, err := s.ListByStatus(store.StatusUploading)
	if err != nil {
		t.Fatalf("ListByStatus uploading: %v", err)
	}
	if len(uploading) != 1 {
		t.Errorf("expected 1 uploading, got %d", len(uploading))
	}
	if len(uploading) > 0 && uploading[0].ID != u2.ID {
		t.Errorf("remaining uploading should be u2 (%q), got %q", u2.ID, uploading[0].ID)
	}

	complete, err := s.ListByStatus(store.StatusComplete)
	if err != nil {
		t.Fatalf("ListByStatus complete: %v", err)
	}
	if len(complete) != 1 {
		t.Errorf("expected 1 complete, got %d", len(complete))
	}
}

// ---------------------------------------------------------------------------
// DeleteUpload
// ---------------------------------------------------------------------------

func TestDeleteUpload_Success(t *testing.T) {
	s := openTestStore(t)
	u := testUpload("asset-del-1", nil)

	if err := s.CreateUpload(u); err != nil {
		t.Fatalf("CreateUpload failed: %v", err)
	}

	if err := s.DeleteUpload(u.ID); err != nil {
		t.Fatalf("DeleteUpload failed: %v", err)
	}

	// Record should be gone
	_, err := s.GetUpload(u.ID)
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expected ErrNotFound after delete, got: %v", err)
	}

	// Index entries should be gone
	got, err := s.UploadByLocalIdentifier(u.LocalIdentifier)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("UploadByLocalIdentifier after delete: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil after delete, got upload %s", got.ID)
	}

	got2, err := s.UploadByBackendID(u.BackendID)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("UploadByBackendID after delete: %v", err)
	}
	if got2 != nil {
		t.Errorf("expected nil after delete, got upload %s", got2.ID)
	}
}

func TestDeleteUpload_NotFound(t *testing.T) {
	s := openTestStore(t)

	err := s.DeleteUpload("nonexistent-id")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got: %v", err)
	}
}

func TestDeleteUpload_RemovesIndexEntries(t *testing.T) {
	s := openTestStore(t)
	u := testUpload("asset-del-idx", nil)

	if err := s.CreateUpload(u); err != nil {
		t.Fatalf("CreateUpload failed: %v", err)
	}

	// Verify indexes exist before delete
	if _, err := s.UploadByLocalIdentifier(u.LocalIdentifier); err != nil {
		t.Fatalf("local index missing before delete: %v", err)
	}

	if err := s.DeleteUpload(u.ID); err != nil {
		t.Fatalf("DeleteUpload failed: %v", err)
	}

	// Verify indexes do not exist after delete
	got, err := s.UploadByLocalIdentifier(u.LocalIdentifier)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("UploadByLocalIdentifier after delete failed: %v", err)
	}
	if got != nil {
		t.Errorf("local index still exists after delete")
	}

	got2, err := s.UploadByBackendID(u.BackendID)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("UploadByBackendID after delete failed: %v", err)
	}
	if got2 != nil {
		t.Errorf("backend index still exists after delete")
	}

	// Status list should be empty
	uploads, err := s.ListByStatus(store.StatusUploading)
	if err != nil {
		t.Fatalf("ListByStatus after delete failed: %v", err)
	}
	if len(uploads) != 0 {
		t.Errorf("status index still exists after delete, got %d uploads", len(uploads))
	}
}

// ---------------------------------------------------------------------------
// ListByStatus
// ---------------------------------------------------------------------------

func TestListByStatus_AllStatuses(t *testing.T) {
	s := openTestStore(t)

	// Create one upload for each status
	statuses := []store.Status{
		store.StatusUploading,
		store.StatusCompleting,
		store.StatusComplete,
		store.StatusDeleted,
		store.StatusBackendLost,
	}
	for _, st := range statuses {
		u := testUpload(fmt.Sprintf("asset-status-%s", st), map[string]string{
			"status": string(st),
		})
		if err := s.CreateUpload(u); err != nil {
			t.Fatalf("CreateUpload for %s failed: %v", st, err)
		}
	}

	// Check each status list
	for _, st := range statuses {
		uploads, err := s.ListByStatus(st)
		if err != nil {
			t.Fatalf("ListByStatus(%q) failed: %v", st, err)
		}
		if len(uploads) != 1 {
			t.Errorf("ListByStatus(%q): got %d uploads, want 1", st, len(uploads))
		}
		if len(uploads) > 0 && uploads[0].Status != st {
			t.Errorf("expected status %q, got %q", st, uploads[0].Status)
		}
	}
}

func TestListByStatus_Empty(t *testing.T) {
	s := openTestStore(t)

	uploads, err := s.ListByStatus(store.StatusUploading)
	if err != nil {
		t.Fatalf("ListByStatus failed: %v", err)
	}
	if len(uploads) != 0 {
		t.Errorf("expected empty list, got %d uploads", len(uploads))
	}
}

// ---------------------------------------------------------------------------
// ListByDateRange
// ---------------------------------------------------------------------------

func TestListByDateRange_AllDates(t *testing.T) {
	s := openTestStore(t)

	// Create uploads on different dates
	dates := []string{
		"2024-01-15T10:00:00Z",
		"2024-02-20T11:00:00Z",
		"2024-03-25T12:00:00Z",
	}
	for i, d := range dates {
		u := testUpload(fmt.Sprintf("asset-date-%d", i), map[string]string{
			"creationDate": d,
		})
		if err := s.CreateUpload(u); err != nil {
			t.Fatalf("CreateUpload %d failed: %v", i, err)
		}
	}

	from := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)
	uploads, nextCursor, err := s.ListByDateRange(from, to, "", 100, "")
	if err != nil {
		t.Fatalf("ListByDateRange failed: %v", err)
	}
	if len(uploads) != 3 {
		t.Errorf("expected 3 uploads, got %d", len(uploads))
	}
	if nextCursor != "" {
		t.Errorf("expected empty nextCursor for full range, got %q", nextCursor)
	}

	// Verify ordering is by creation date ascending
	if len(uploads) >= 2 {
		for i := 1; i < len(uploads); i++ {
			if uploads[i].CreationDate < uploads[i-1].CreationDate {
				t.Errorf("uploads not sorted by creation date at index %d", i)
			}
		}
	}
}

func TestListByDateRange_SubRange(t *testing.T) {
	s := openTestStore(t)

	dates := []string{
		"2024-01-15T10:00:00Z",
		"2024-02-20T11:00:00Z",
		"2024-03-25T12:00:00Z",
	}
	for i, d := range dates {
		u := testUpload(fmt.Sprintf("asset-sub-%d", i), map[string]string{
			"creationDate": d,
		})
		if err := s.CreateUpload(u); err != nil {
			t.Fatalf("CreateUpload %d failed: %v", i, err)
		}
	}

	// Query February only
	from := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2024, 2, 29, 0, 0, 0, 0, time.UTC)
	uploads, _, err := s.ListByDateRange(from, to, "", 100, "")
	if err != nil {
		t.Fatalf("ListByDateRange failed: %v", err)
	}
	if len(uploads) != 1 {
		t.Errorf("expected 1 upload in February, got %d", len(uploads))
	}
	if len(uploads) > 0 && uploads[0].CreationDate != dates[1] {
		t.Errorf("expected date %q, got %q", dates[1], uploads[0].CreationDate)
	}
}

func TestListByDateRange_Pagination(t *testing.T) {
	s := openTestStore(t)

	// Create 5 uploads on the same date but with ascending IDs
	for i := range 5 {
		u := testUpload(fmt.Sprintf("asset-pag-%d", i), map[string]string{
			"creationDate": "2024-03-15T10:00:00Z",
		})
		if err := s.CreateUpload(u); err != nil {
			t.Fatalf("CreateUpload %d failed: %v", i, err)
		}
	}

	from := time.Date(2024, 3, 15, 0, 0, 0, 0, time.UTC)
	to := time.Date(2024, 3, 15, 23, 59, 59, 0, time.UTC)

	// Page 1: limit 2
	page1, cursor, err := s.ListByDateRange(from, to, "", 2, "")
	if err != nil {
		t.Fatalf("ListByDateRange page 1 failed: %v", err)
	}
	if len(page1) != 2 {
		t.Errorf("page 1: expected 2 uploads, got %d", len(page1))
	}
	if cursor == "" {
		t.Fatal("page 1: expected non-empty cursor")
	}
	t.Logf("page 1 cursor: %s", cursor)

	// Page 2: limit 2
	page2, cursor, err := s.ListByDateRange(from, to, "", 2, cursor)
	if err != nil {
		t.Fatalf("ListByDateRange page 2 failed: %v", err)
	}
	if len(page2) != 2 {
		t.Errorf("page 2: expected 2 uploads, got %d", len(page2))
	}
	if cursor == "" {
		t.Fatal("page 2: expected non-empty cursor")
	}
	t.Logf("page 2 cursor: %s", cursor)

	// Verify no overlap between page 1 and page 2
	allIDs := make(map[string]bool)
	for _, u := range page1 {
		allIDs[u.ID] = true
	}
	for _, u := range page2 {
		if allIDs[u.ID] {
			t.Errorf("duplicate ID %q across pages", u.ID)
		}
		allIDs[u.ID] = true
	}

	// Page 3: limit 2 (should return only 1)
	page3, cursor, err := s.ListByDateRange(from, to, "", 2, cursor)
	if err != nil {
		t.Fatalf("ListByDateRange page 3 failed: %v", err)
	}
	if len(page3) != 1 {
		t.Errorf("page 3: expected 1 upload, got %d", len(page3))
	}
	if cursor != "" {
		t.Errorf("page 3: expected empty cursor, got %q", cursor)
	}

	// Verify all 5 were returned across 3 pages
	totalIDs := make(map[string]bool)
	for _, u := range page1 {
		totalIDs[u.ID] = true
	}
	for _, u := range page2 {
		totalIDs[u.ID] = true
	}
	for _, u := range page3 {
		totalIDs[u.ID] = true
	}
	if len(totalIDs) != 5 {
		t.Errorf("expected 5 unique IDs across all pages, got %d", len(totalIDs))
	}
}

// TestListByDateRange_PaginationCursorDeleted verifies that pagination does
// not silently drop a record when the cursor's upload was deleted between
// pages. The cursor entry no longer exists in the date index, so the iterator
// lands on the next valid entry — which must be returned, not skipped.
func TestListByDateRange_PaginationCursorDeleted(t *testing.T) {
	s := openTestStore(t)

	// Create 4 uploads on the same date with ascending IDs.
	ids := make([]string, 4)
	for i := range 4 {
		u := testUpload(fmt.Sprintf("asset-cursor-del-%d", i), map[string]string{
			"creationDate": "2024-03-15T10:00:00Z",
		})
		if err := s.CreateUpload(u); err != nil {
			t.Fatalf("CreateUpload %d failed: %v", i, err)
		}
		ids[i] = u.ID
	}

	from := time.Date(2024, 3, 15, 0, 0, 0, 0, time.UTC)
	to := time.Date(2024, 3, 15, 23, 59, 59, 0, time.UTC)

	// Page 1: limit 2 → returns ids[0], ids[1]; cursor points at ids[1].
	page1, cursor, err := s.ListByDateRange(from, to, "", 2, "")
	if err != nil {
		t.Fatalf("page 1 failed: %v", err)
	}
	if len(page1) != 2 || cursor == "" {
		t.Fatalf("page 1: expected 2 uploads and a cursor, got %d and %q", len(page1), cursor)
	}

	// Delete the record the cursor points at (ids[1]) between pages.
	if err := s.DeleteUpload(page1[1].ID); err != nil {
		t.Fatalf("DeleteUpload cursor record failed: %v", err)
	}

	// Page 2 must return the remaining two records (ids[2], ids[3]), not skip
	// ids[2] as the old skipFirst logic would have.
	page2, nextCursor, err := s.ListByDateRange(from, to, "", 2, cursor)
	if err != nil {
		t.Fatalf("page 2 failed: %v", err)
	}
	if len(page2) != 2 {
		t.Fatalf("page 2: expected 2 uploads (cursor record deleted, next must not be skipped), got %d", len(page2))
	}

	seen := map[string]bool{page1[0].ID: true}
	for _, u := range page2 {
		if seen[u.ID] {
			t.Errorf("unexpected duplicate ID %q on page 2", u.ID)
		}
		seen[u.ID] = true
	}
	if len(seen) != 3 {
		t.Errorf("expected 3 unique IDs across pages (1 deleted), got %d", len(seen))
	}
	// When the page fills the limit exactly but there are no more items in
	// the iterator, no cursor is returned — the peek-ahead optimization
	// avoids an unnecessary trailing round-trip.
	if nextCursor != "" {
		t.Fatalf("page 2: expected no trailing cursor when no more items exist, got %q", nextCursor)
	}
}

func TestListByDateRange_StatusFilter(t *testing.T) {
	s := openTestStore(t)

	// Create two uploads on the same date with different statuses
	u1 := testUpload("asset-flt-complete", map[string]string{
		"creationDate": "2024-04-01T10:00:00Z",
		"status":       string(store.StatusComplete),
	})
	if err := s.CreateUpload(u1); err != nil {
		t.Fatalf("CreateUpload 1 failed: %v", err)
	}

	u2 := testUpload("asset-flt-uploading", map[string]string{
		"creationDate": "2024-04-01T10:00:00Z",
		"status":       string(store.StatusUploading),
	})
	if err := s.CreateUpload(u2); err != nil {
		t.Fatalf("CreateUpload 2 failed: %v", err)
	}

	from := time.Date(2024, 4, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2024, 4, 1, 23, 59, 59, 0, time.UTC)

	// Filter by uploading only
	uploads, _, err := s.ListByDateRange(from, to, store.StatusUploading, 100, "")
	if err != nil {
		t.Fatalf("ListByDateRange with status filter failed: %v", err)
	}
	if len(uploads) != 1 {
		t.Errorf("expected 1 uploading upload, got %d", len(uploads))
	}
	if len(uploads) > 0 && uploads[0].Status != store.StatusUploading {
		t.Errorf("expected status %q, got %q", store.StatusUploading, uploads[0].Status)
	}
}

func TestListByDateRange_EmptyRange(t *testing.T) {
	s := openTestStore(t)

	from := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)

	uploads, _, err := s.ListByDateRange(from, to, "", 100, "")
	if err != nil {
		t.Fatalf("ListByDateRange on empty store failed: %v", err)
	}
	if len(uploads) != 0 {
		t.Errorf("expected 0 uploads, got %d", len(uploads))
	}
}

// ---------------------------------------------------------------------------
// Integration: multiple records, indexes in sync
// ---------------------------------------------------------------------------

func TestMultipleRecords_AllIndexesConsistent(t *testing.T) {
	s := openTestStore(t)

	// Create records with different dates, statuses, and identifiers
	records := []struct {
		localID  string
		date     string
		status   store.Status
		filename string
	}{
		{"alice/001", "2024-01-01T00:00:00Z", store.StatusUploading, "IMG_0001.jpg"},
		{"bob/002", "2024-02-02T00:00:00Z", store.StatusUploading, "IMG_0002.jpg"},
		{"carol/003", "2024-03-03T00:00:00Z", store.StatusComplete, "IMG_0003.jpg"},
		{"dave/004", "2024-04-04T00:00:00Z", store.StatusBackendLost, "IMG_0004.jpg"},
	}

	for _, r := range records {
		u := testUpload(r.localID, map[string]string{
			"creationDate": r.date,
			"status":       string(r.status),
			"filename":     r.filename,
		})
		if err := s.CreateUpload(u); err != nil {
			t.Fatalf("CreateUpload %s failed: %v", r.localID, err)
		}
	}

	// Verify local identifier index
	for _, r := range records {
		got, err := s.UploadByLocalIdentifier(r.localID)
		if err != nil {
			t.Fatalf("UploadByLocalIdentifier(%q): %v", r.localID, err)
		}
		if got == nil {
			t.Fatalf("UploadByLocalIdentifier(%q) returned nil", r.localID)
		}
		if got.Status != r.status {
			t.Errorf("UploadByLocalIdentifier(%q) status = %q, want %q",
				r.localID, got.Status, r.status)
		}
	}

	// Verify status index
	for _, st := range []store.Status{
		store.StatusUploading,
		store.StatusComplete,
		store.StatusBackendLost,
	} {
		uploads, err := s.ListByStatus(st)
		if err != nil {
			t.Fatalf("ListByStatus(%q): %v", st, err)
		}
		expected := 0
		for _, r := range records {
			if r.status == st {
				expected++
			}
		}
		if len(uploads) != expected {
			t.Errorf("ListByStatus(%q) count = %d, want %d", st, len(uploads), expected)
		}
	}

	// Verify date range returns all records
	from := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)
	uploads, _, err := s.ListByDateRange(from, to, "", 100, "")
	if err != nil {
		t.Fatalf("ListByDateRange: %v", err)
	}
	if len(uploads) != 4 {
		t.Errorf("ListByDateRange returned %d, want 4", len(uploads))
	}

	// Verify ordering
	if len(uploads) >= 2 {
		for i := 1; i < len(uploads); i++ {
			if uploads[i].CreationDate < uploads[i-1].CreationDate {
				t.Errorf("uploads not sorted by date at index %d", i)
			}
		}
	}
}

func TestDeleteUpload_MultipleRecords_OnlyDeletesOne(t *testing.T) {
	s := openTestStore(t)

	// Create 3 records
	ids := make([]string, 3)
	for i := range 3 {
		u := testUpload(fmt.Sprintf("asset-delmulti-%d", i), nil)
		if err := s.CreateUpload(u); err != nil {
			t.Fatalf("CreateUpload %d failed: %v", i, err)
		}
		ids[i] = u.ID
	}

	// Delete the middle one
	if err := s.DeleteUpload(ids[1]); err != nil {
		t.Fatalf("DeleteUpload failed: %v", err)
	}

	// First and third should still exist
	_, err := s.GetUpload(ids[0])
	if err != nil {
		t.Errorf("first record should exist after deleting other: %v", err)
	}
	_, err = s.GetUpload(ids[2])
	if err != nil {
		t.Errorf("third record should exist after deleting other: %v", err)
	}

	// Middle should be gone
	_, err = s.GetUpload(ids[1])
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("middle record should be deleted, got: %v", err)
	}

	// Uploading list should have 2
	uploads, err := s.ListByStatus(store.StatusUploading)
	if err != nil {
		t.Fatalf("ListByStatus failed: %v", err)
	}
	if len(uploads) != 2 {
		t.Errorf("expected 2 uploading, got %d", len(uploads))
	}
}

func TestListByDateRange_CursorDecodesSeekPosition(t *testing.T) {
	s := openTestStore(t)

	// Create uploads spread across dates
	uploadData := []struct {
		localID string
		date    string
	}{
		{"cursor-a", "2024-01-01T00:00:00Z"},
		{"cursor-b", "2024-01-02T00:00:00Z"},
		{"cursor-c", "2024-01-03T00:00:00Z"},
		{"cursor-d", "2024-01-04T00:00:00Z"},
	}
	for _, d := range uploadData {
		u := testUpload(d.localID, map[string]string{
			"creationDate": d.date,
		})
		if err := s.CreateUpload(u); err != nil {
			t.Fatalf("CreateUpload %s failed: %v", d.localID, err)
		}
	}

	from := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2024, 1, 31, 0, 0, 0, 0, time.UTC)

	// First page: limit 2
	page1, cursor, err := s.ListByDateRange(from, to, "", 2, "")
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(page1) != 2 {
		t.Fatalf("first page: expected 2, got %d", len(page1))
	}
	if cursor == "" {
		t.Fatal("first page: expected cursor")
	}

	// Second page: the cursor should position us after the first page
	page2, cursor, err := s.ListByDateRange(from, to, "", 2, cursor)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(page2) != 2 {
		t.Fatalf("second page: expected 2, got %d", len(page2))
	}

	// Verify page1[1] is before page2[0] chronologically
	if page1[1].CreationDate > page2[0].CreationDate {
		t.Errorf("page1 last item is after page2 first item (ordering problem)")
	}
}

func TestListByDateRange_InvalidCursor(t *testing.T) {
	s := openTestStore(t)

	from := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2024, 1, 31, 0, 0, 0, 0, time.UTC)

	_, _, err := s.ListByDateRange(from, to, "", 100, "invalid-base64!!!")
	if err == nil {
		t.Error("expected error for invalid cursor")
	}
}

func TestListByDateRange_LimitClamping(t *testing.T) {
	s := openTestStore(t)

	// Create 5 uploads
	for i := range 5 {
		u := testUpload(fmt.Sprintf("asset-clamp-%d", i), map[string]string{
			"creationDate": "2024-05-15T10:00:00Z",
		})
		if err := s.CreateUpload(u); err != nil {
			t.Fatalf("CreateUpload %d failed: %v", i, err)
		}
	}

	from := time.Date(2024, 5, 15, 0, 0, 0, 0, time.UTC)
	to := time.Date(2024, 5, 15, 23, 59, 59, 0, time.UTC)

	// Zero limit should default to 500
	uploads, cursor, err := s.ListByDateRange(from, to, "", 0, "")
	if err != nil {
		t.Fatalf("ListByDateRange with 0 limit failed: %v", err)
	}
	if len(uploads) != 5 {
		t.Errorf("expected 5 uploads with default limit, got %d", len(uploads))
	}
	if cursor != "" {
		t.Errorf("expected no cursor when all results fit, got %q", cursor)
	}

	// Negative limit should default to 500
	uploads, cursor, err = s.ListByDateRange(from, to, "", -1, "")
	if err != nil {
		t.Fatalf("ListByDateRange with negative limit failed: %v", err)
	}
	if len(uploads) != 5 {
		t.Errorf("expected 5 uploads with negative limit, got %d", len(uploads))
	}
	if cursor != "" {
		t.Errorf("expected no cursor when limit <= 0 defaults, got %q", cursor)
	}
}

// ---------------------------------------------------------------------------
// Concurrent access smoke test
// ---------------------------------------------------------------------------

func TestConcurrentCreateAndRead(t *testing.T) {
	s := openTestStore(t)

	const n = 10
	errCh := make(chan error, n*2)

	// Concurrently create uploads
	for i := range n {
		go func() {
			u := testUpload(fmt.Sprintf("concurrent-%d", i), nil)
			errCh <- s.CreateUpload(u)
		}()
	}

	// Collect create results
	for i := range n {
		if err := <-errCh; err != nil && !errors.Is(err, store.ErrConflict) {
			t.Errorf("concurrent create %d: unexpected error: %v", i, err)
		}
	}

	// Concurrently read all
	for i := range n {
		go func() {
			id := api.SafeID(fmt.Sprintf("concurrent-%d", i))
			_, err := s.GetUpload(id)
			errCh <- err
		}()
	}

	for i := range n {
		if err := <-errCh; err != nil {
			t.Errorf("concurrent read %d: %v", i, err)
		}
	}

	// Verify total count
	uploads, err := s.ListByStatus(store.StatusUploading)
	if err != nil {
		t.Fatalf("ListByStatus: %v", err)
	}
	if len(uploads) != n {
		t.Errorf("expected %d uploading uploads after concurrent creates, got %d", n, len(uploads))
	}
}

// ---------------------------------------------------------------------------
// UpdateStatus with concurrent updates on same record
// ---------------------------------------------------------------------------

func TestUpdateStatus_ConcurrentOnSameRecord(t *testing.T) {
	s := openTestStore(t)
	u := testUpload("asset-race-status", nil)

	if err := s.CreateUpload(u); err != nil {
		t.Fatalf("CreateUpload failed: %v", err)
	}

	errCh := make(chan error, 2)

	// Two concurrent status updates. BadgerDB uses optimistic concurrency, so
	// one transaction may get a conflict error — that's expected and safe.
	go func() {
		_, err := s.UpdateStatus(u.ID, store.StatusCompleting)
		errCh <- err
	}()
	go func() {
		_, err := s.UpdateStatus(u.ID, store.StatusBackendLost)
		errCh <- err
	}()

	successCount := 0
	for range 2 {
		if err := <-errCh; err == nil {
			successCount++
		}
		// Transaction conflict errors are expected with concurrent writes
	}

	// At least one update should have succeeded
	if successCount == 0 {
		t.Fatal("both concurrent updates failed — at least one should succeed")
	}

	// Verify the record still exists with one of the expected statuses
	got, err := s.GetUpload(u.ID)
	if err != nil {
		t.Fatalf("GetUpload after concurrent updates: %v", err)
	}
	validStatuses := map[store.Status]bool{
		store.StatusCompleting:  true,
		store.StatusBackendLost: true,
	}
	if !validStatuses[got.Status] {
		t.Errorf("final status %q is not one of the expected statuses", got.Status)
	}

	// Verify only one status index entry exists (no ghost from the race)
	uploadsCompleting, err := s.ListByStatus(store.StatusCompleting)
	if err != nil {
		t.Fatalf("ListByStatus completing: %v", err)
	}
	uploadsLost, err := s.ListByStatus(store.StatusBackendLost)
	if err != nil {
		t.Fatalf("ListByStatus backend_lost: %v", err)
	}
	if len(uploadsCompleting)+len(uploadsLost) != 1 {
		t.Errorf("expected 1 total status entry (completing + backend_lost), got completing=%d, backend_lost=%d",
			len(uploadsCompleting), len(uploadsLost))
	}
}

// ---------------------------------------------------------------------------
// Utility: test that Now() timestamp helpers work
// ---------------------------------------------------------------------------

func TestUpload_TimestampsSet(t *testing.T) {
	s := openTestStore(t)
	u := testUpload("asset-ts-check", nil)

	if err := s.CreateUpload(u); err != nil {
		t.Fatalf("CreateUpload failed: %v", err)
	}

	got, err := s.GetUpload(u.ID)
	if err != nil {
		t.Fatalf("GetUpload failed: %v", err)
	}

	if got.CreatedAt == "" {
		t.Error("CreatedAt should not be empty")
	}
	if got.UpdatedAt == "" {
		t.Error("UpdatedAt should not be empty")
	}
}

func TestUpload_BundleID(t *testing.T) {
	s := openTestStore(t)
	bundleID := "BUNDLE1234-5678/L0/000"
	u := testUpload("asset-bundle", nil)
	u.BundleID = bundleID

	if err := s.CreateUpload(u); err != nil {
		t.Fatalf("CreateUpload failed: %v", err)
	}

	got, err := s.GetUpload(u.ID)
	if err != nil {
		t.Fatalf("GetUpload failed: %v", err)
	}
	if got.BundleID != bundleID {
		t.Errorf("BundleID: got %q, want %q", got.BundleID, bundleID)
	}
}

// ---------------------------------------------------------------------------
// ReRegister
// ---------------------------------------------------------------------------

// TestReRegister_ResetsBackendAndStatus verifies that ReRegister swaps the
// backend_id, resets status to uploading, and clears a stale organized_path.
func TestReRegister_ResetsBackendAndStatus(t *testing.T) {
	s := openTestStore(t)
	u := testUpload("asset-rereg-1", nil)
	if err := s.CreateUpload(u); err != nil {
		t.Fatalf("CreateUpload: %v", err)
	}

	// Move the record into a complete-like state with a stale organized path
	// and a lost backend, to confirm ReRegister clears everything.
	if _, err := s.UpdateComplete(u.ID, "organized/2024/03/15/IMG_1234.jpg"); err != nil {
		t.Fatalf("UpdateComplete: %v", err)
	}
	if _, err := s.UpdateStatus(u.ID, store.StatusBackendLost); err != nil {
		t.Fatalf("UpdateStatus backend_lost: %v", err)
	}

	const newBackend = "tusd-fresh-backend-id"
	got, err := s.ReRegister(u.ID, newBackend)
	if err != nil {
		t.Fatalf("ReRegister: %v", err)
	}
	if got.BackendID != newBackend {
		t.Errorf("BackendID: got %q want %q", got.BackendID, newBackend)
	}
	if got.Status != store.StatusUploading {
		t.Errorf("Status: got %q want uploading", got.Status)
	}
	if got.OrganizedPath != "" {
		t.Errorf("OrganizedPath should be cleared: got %q", got.OrganizedPath)
	}

	// The old backend index entry must be gone; the new one must resolve back.
	byOld, err := s.UploadByBackendID(u.BackendID)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("UploadByBackendID old: %v", err)
	}
	if byOld != nil {
		t.Errorf("old backend index should be removed, still resolved to %v", byOld.ID)
	}
	byNew, err := s.UploadByBackendID(newBackend)
	if err != nil {
		t.Fatalf("UploadByBackendID new: %v", err)
	}
	if byNew == nil || byNew.ID != u.ID {
		t.Errorf("new backend index should resolve to %q, got %v", u.ID, byNew)
	}
}

// TestReRegister_NoGhostStatusKeys verifies that ReRegister does not leave the
// old status index entry behind (no ghost keys after re-registration).
func TestReRegister_NoGhostStatusKeys(t *testing.T) {
	s := openTestStore(t)
	u := testUpload("asset-rereg-ghost", nil)
	if err := s.CreateUpload(u); err != nil {
		t.Fatalf("CreateUpload: %v", err)
	}
	if _, err := s.UpdateStatus(u.ID, store.StatusDeleted); err != nil {
		t.Fatalf("UpdateStatus deleted: %v", err)
	}

	if _, err := s.ReRegister(u.ID, "tusd-new-backend"); err != nil {
		t.Fatalf("ReRegister: %v", err)
	}

	deleted, err := s.ListByStatus(store.StatusDeleted)
	if err != nil {
		t.Fatalf("ListByStatus deleted: %v", err)
	}
	if len(deleted) != 0 {
		t.Errorf("expected 0 deleted index entries after re-register, got %d", len(deleted))
	}
	uploading, err := s.ListByStatus(store.StatusUploading)
	if err != nil {
		t.Fatalf("ListByStatus uploading: %v", err)
	}
	if len(uploading) != 1 || uploading[0].ID != u.ID {
		t.Errorf("expected exactly 1 uploading entry for %s, got %d", u.ID, len(uploading))
	}
}

// TestReRegister_NotFound verifies that ReRegister returns ErrNotFound for a
// missing record.
func TestReRegister_NotFound(t *testing.T) {
	s := openTestStore(t)
	_, err := s.ReRegister("nonexistent-id", "tusd-x")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}
