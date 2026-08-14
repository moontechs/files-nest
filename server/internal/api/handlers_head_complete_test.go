package api_test

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/moontechs/files-nest/server/internal/store"
)

// TestHandleHeadUploadData_CompletedUpload does a real completion flow
// (which terminates the tusd backend) and then issues a HEAD request.
// Regression: previously HEAD called GetOffset on the terminated backend,
// observed ErrNotFound, and flipped the already-complete record to
// backend_lost — corrupting a successful completion.
func TestHandleHeadUploadData_CompletedUpload(t *testing.T) {
	h, st, _ := setupHandler(t)
	created := createTestUpload(t, h, "HEAD-COMPLETE/L0/000", "IMG_0001.jpg", creationDate)

	data := []byte("completed content for head test")
	patchRec := tusPatchRequest(h.HandlePatchUploadData, created.ID, 0,
		strconv.Itoa(len(data)), strings.NewReader(string(data)))
	if patchRec.Code != http.StatusNoContent {
		t.Fatalf("PATCH data expected 204, got %d: %s", patchRec.Code, patchRec.Body.String())
	}

	rec := statusPatchRequest(h.HandlePatchUploadStatus, created.ID, `{"status": "complete"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("PATCH status expected 204, got %d: %s", rec.Code, rec.Body.String())
	}

	// HEAD the completed upload — the tusd backend has been terminated.
	// It must not corrupt the record's status.
	headRec := tusHeadRequest(h.HandleHeadUploadData, created.ID)
	if headRec.Code != http.StatusConflict {
		t.Errorf("expected 409 for HEAD on completed upload, got %d: %s", headRec.Code, headRec.Body.String())
	}

	upload, err := st.GetUpload(created.ID)
	if err != nil {
		t.Fatalf("GetUpload: %v", err)
	}
	if upload.Status != store.StatusComplete {
		t.Errorf("BUG: HEAD on completed upload flipped status to %q (expected complete)", upload.Status)
	}
}
