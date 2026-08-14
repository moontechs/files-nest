package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/moontechs/files-nest/server/internal/api"
	"github.com/moontechs/files-nest/server/internal/store"
)

// createUploadBody builds a POST /uploads JSON body for the given fields.
func createUploadBody(localID, filename, creationDate string) string {
	return `{
		"local_identifier": "` + localID + `",
		"filename": "` + filename + `",
		"creation_date": "` + creationDate + `"
	}`
}

// TestHandleCreateUpload_ReRegistersAfterBackendLost verifies that a
// POST /uploads for a localIdentifier whose existing record is in the
// backend_lost state triggers a fresh upload: a new tusd backend is bound
// to the existing record and the status is reset to uploading. This is the
// recovery contract documented in the README status lifecycle.
func TestHandleCreateUpload_ReRegistersAfterBackendLost(t *testing.T) {
	h, st, _ := setupHandler(t)

	created := createTestUpload(t, h, "RE-REG/BL/001", "IMG_0001.jpg", creationDate)
	originalBackend := created.BackendID

	// Simulate a lost backend by manually terminating the tusd upload and
	// marking the record backend_lost (mirroring what the handlers do).
	if _, err := st.UpdateStatus(created.ID, store.StatusBackendLost); err != nil {
		t.Fatalf("UpdateStatus backend_lost: %v", err)
	}

	// Re-POST with the same localIdentifier — should re-register, not return
	// the stale backend_lost record.
	rec := executeRequest(h.HandleCreateUpload, "POST", "/uploads",
		strings.NewReader(createUploadBody("RE-REG/BL/001", "IMG_0001.jpg", creationDate)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("re-register expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp api.CreateUploadResponse
	decodeResponse(t, rec, &resp)

	if resp.ID != created.ID {
		t.Errorf("re-register should keep the same safe id: got %q want %q", resp.ID, created.ID)
	}
	if resp.Status != string(store.StatusUploading) {
		t.Errorf("re-register should reset status to uploading: got %q", resp.Status)
	}
	if resp.BackendID == "" {
		t.Fatal("re-register should produce a new backend_id")
	}
	if resp.BackendID == originalBackend {
		t.Errorf("re-register should bind a NEW backend_id: got the same %q", originalBackend)
	}

	// The DB record must reflect the new backend and uploading status.
	up, err := st.GetUpload(created.ID)
	if err != nil {
		t.Fatalf("GetUpload: %v", err)
	}
	if up.Status != store.StatusUploading {
		t.Errorf("DB status after re-register: got %q want uploading", up.Status)
	}
	if up.BackendID != resp.BackendID {
		t.Errorf("DB backend_id after re-register: got %q want %q", up.BackendID, resp.BackendID)
	}
	if up.OrganizedPath != "" {
		t.Errorf("OrganizedPath should be cleared on re-register: got %q", up.OrganizedPath)
	}

	// The new backend must actually exist in tusd (offset query succeeds).
	recHead := tusHeadRequest(h.HandleHeadUploadData, created.ID)
	if recHead.Code != http.StatusOK {
		t.Errorf("HEAD after re-register expected 200, got %d: %s", recHead.Code, recHead.Body.String())
	}
}

// TestHandleCreateUpload_ReRegistersAfterDeleted verifies that a previously
// deleted record can be re-registered by POST /uploads with the same
// localIdentifier, producing a fresh uploading record instead of returning
// the stale deleted record (which would permanently block that
// localIdentifier from being uploaded again).
func TestHandleCreateUpload_ReRegistersAfterDeleted(t *testing.T) {
	h, st, _ := setupHandler(t)

	created := createTestUpload(t, h, "RE-REG/DEL/001", "IMG_0001.jpg", creationDate)
	originalBackend := created.BackendID

	// Soft-delete via the DELETE handler (keeps the record + local index).
	rec := deleteUploadRequest(h.HandleDeleteUpload, created.ID)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE expected 204, got %d: %s", rec.Code, rec.Body.String())
	}

	// Re-POST — should re-register a fresh upload.
	rec = executeRequest(h.HandleCreateUpload, "POST", "/uploads",
		strings.NewReader(createUploadBody("RE-REG/DEL/001", "IMG_0001.jpg", creationDate)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("re-register after delete expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp api.CreateUploadResponse
	decodeResponse(t, rec, &resp)

	if resp.ID != created.ID {
		t.Errorf("re-register should keep the same safe id: got %q want %q", resp.ID, created.ID)
	}
	if resp.Status != string(store.StatusUploading) {
		t.Errorf("re-register should reset status to uploading: got %q", resp.Status)
	}
	if resp.BackendID == originalBackend {
		t.Errorf("re-register should bind a NEW backend_id: got the same %q", originalBackend)
	}

	up, err := st.GetUpload(created.ID)
	if err != nil {
		t.Fatalf("GetUpload: %v", err)
	}
	if up.Status != store.StatusUploading {
		t.Errorf("DB status after re-register: got %q want uploading", up.Status)
	}
}

// TestHandleCreateUpload_DuplicateUploadingStillIdempotent verifies that the
// re-register change did not break the existing idempotency contract: a
// duplicate POST while the upload is still uploading (or complete) returns
// the existing record with 200 and terminates the redundant tusd upload.
func TestHandleCreateUpload_DuplicateUploadingStillIdempotent(t *testing.T) {
	h, st, _ := setupHandler(t)

	created := createTestUpload(t, h, "RE-REG/DUP/001", "IMG_0001.jpg", creationDate)

	rec := executeRequest(h.HandleCreateUpload, "POST", "/uploads",
		strings.NewReader(createUploadBody("RE-REG/DUP/001", "IMG_0001.jpg", creationDate)))
	if rec.Code != http.StatusOK {
		t.Fatalf("duplicate uploading expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp api.CreateUploadResponse
	decodeResponse(t, rec, &resp)

	if resp.BackendID != created.BackendID {
		t.Errorf("duplicate uploading should return the same backend_id: got %q want %q", resp.BackendID, created.BackendID)
	}
	if resp.Status != string(store.StatusUploading) {
		t.Errorf("duplicate uploading should return status uploading: got %q", resp.Status)
	}

	up, err := st.GetUpload(created.ID)
	if err != nil {
		t.Fatalf("GetUpload: %v", err)
	}
	if up.BackendID != created.BackendID {
		t.Errorf("DB backend_id should be unchanged: got %q want %q", up.BackendID, created.BackendID)
	}
}
