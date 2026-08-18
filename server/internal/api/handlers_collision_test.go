package api_test

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Regression: completing two uploads with the same filename + creation date
// must produce distinct organized_path values, each pointing to its own file.
// The collision suffix (<backend_id> before the extension) must be reflected
// in BOTH the absolute move target and the stored organized_path.
func TestHandlePatchUploadStatus_CollisionDeduplicates(t *testing.T) {
	h, st, _ := setupHandler(t)

	complete := func(t *testing.T, localID, content string) {
		t.Helper()

		created := createTestUpload(t, h, localID, "IMG_0001.jpg", creationDate)
		patchRec := tusPatchRequest(h.HandlePatchUploadData, created.ID, 0,
			strconv.Itoa(len(content)), strings.NewReader(content))
		if patchRec.Code != http.StatusNoContent {
			t.Fatalf("PATCH data expected 204, got %d: %s", patchRec.Code, patchRec.Body.String())
		}
		rec := statusPatchRequest(h.HandlePatchUploadStatus, created.ID, `{"status": "complete"}`)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("PATCH status expected 204, got %d: %s", rec.Code, rec.Body.String())
		}
		up, err := st.GetUpload(created.ID)
		if err != nil {
			t.Fatalf("GetUpload: %v", err)
		}
		abs := filepath.Join(h.StoragePath(), up.OrganizedPath)
		//nolint:gosec // test-only read of a temp-dir file, not attacker-controlled
		got, err := os.ReadFile(abs)
		if err != nil {
			t.Fatalf("reading organized file at %s: %v", abs, err)
		}
		if string(got) != content {
			t.Fatalf("organized_path %q points to wrong content: got %q, want %q",
				up.OrganizedPath, string(got), content)
		}
	}

	complete(t, "collision-repro/A", "content-A")
	complete(t, "collision-repro/B", "content-B")
}
