//go:build e2e

// Package e2e provides black-box end-to-end tests for the iCloud Backup
// server. This file contains the core upload lifecycle test (an ordered
// sequence of subtests sharing closure-scoped upload state) and additional
// focused tests for error conditions, validation, pagination, and status
// transitions.
//
// All tests use the shared fixtures from fixtures_test.go for run-scoped
// identifiers, listing scoping, and convenient lifecycle helpers.
package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Ordered lifecycle (TestE2E_Lifecycle)
//
// This single test function exercises the full upload lifecycle via ordered
// subtests that share closure-scoped upload state. Each subtest builds on
// the previous one, simulating how a real client would interact with the
// server.
// ---------------------------------------------------------------------------

// TestE2E_Lifecycle runs the core upload lifecycle as an ordered sequence
// of subtests with closure-scoped state. It verifies:
//  1. Unauthenticated health check returns 200 with status "ok".
//  2. Authenticated POST /uploads returns 201, status "uploading", the
//     expected deterministic ID (43 bytes, URL-safe base64), and an upload
//     URL under /uploads/{id}/data.
//  3. HEAD /uploads/{id}/data reports offset 0; then a 1 KiB PATCH with
//     Upload-Length: 1024 advances the offset to 1024.
//  4. PATCH /uploads/{id}/status with {"status":"complete"} succeeds, and
//     a subsequent GET returns status "complete" with a non-empty
//     organized_path.
//  5. Re-POSTing the same local_identifier returns 200 with the same ID
//     and status "complete".
func TestE2E_Lifecycle(t *testing.T) {
	// -----------------------------------------------------------------------
	// Closure-scoped upload state shared by all subtests.
	// -----------------------------------------------------------------------
	var (
		uploadID        string // safe server ID returned by POST
		localIdentifier string // unique per-run local identifier
		deterministicID string // computed from localIdentifier for comparison
	)

	localIdentifier = MakeLocalIdentifier(t, t.Name())
	deterministicID = SafeIDFromLocal(localIdentifier)

	// -----------------------------------------------------------------------
	// Step 1: Unauthenticated health check
	// -----------------------------------------------------------------------
	t.Run("health", func(t *testing.T) {
		err := HealthCheck()
		require.NoError(t, err, "GET /health should return 200")
	})

	// -----------------------------------------------------------------------
	// Step 2: Create upload
	// -----------------------------------------------------------------------
	t.Run("create", func(t *testing.T) {
		body := CreateUploadBody{
			LocalIdentifier: localIdentifier,
			Filename:        "IMG_9876.jpg",
			CreationDate:    time.Now().UTC().Format(time.RFC3339),
		}

		cr, status, err := CreateUpload(body)
		require.NoError(t, err, "POST /uploads should not error")
		require.Equal(t, http.StatusCreated, status,
			"fresh upload should return 201")
		require.Equal(t, "uploading", cr.Status,
			"fresh upload should have uploading status")
		require.Len(t, cr.ID, 43,
			"upload ID should be 43 characters (base64url of SHA-256)")
		require.Equal(t, deterministicID, cr.ID,
			"upload ID must be deterministic: base64url(SHA-256(local_identifier))")
		require.NotEmpty(t, cr.BackendID,
			"backend_id must not be empty")
		require.Equal(t, "/uploads/"+cr.ID+"/data", cr.UploadURL,
			"upload_url must reference the data endpoint")
		require.Equal(t, localIdentifier, cr.LocalIdentifier,
			"local_identifier must match the request")

		uploadID = cr.ID
	})

	// -----------------------------------------------------------------------
	// Step 3: HEAD offset and PATCH data
	// -----------------------------------------------------------------------
	t.Run("head_and_patch", func(t *testing.T) {
		require.NotEmpty(t, uploadID, "uploadID must be set by 'create' subtest")

		// HEAD should report offset 0.
		headResp, status, err := HeadUploadData(uploadID)
		require.NoError(t, err, "HEAD /uploads/{id}/data should not error")
		require.Equal(t, http.StatusOK, status,
			"HEAD should return 200 for uploading upload")
		require.Equal(t, int64(0), headResp.UploadOffset,
			"initial Upload-Offset must be 0")
		require.Equal(t, "1.0.0", headResp.TusResumable,
			"Tus-Resumable header must be present")

		// PATCH 1 KiB of data with Upload-Length: 1024.
		data := make([]byte, 1024)
		for i := range data {
			data[i] = byte(i % 256)
		}
		totalLength := int64(len(data))

		patchResp, status, err := PatchUploadData(uploadID,
			bytes.NewReader(data), 0, fmt.Sprintf("%d", totalLength))
		require.NoError(t, err, "PATCH /uploads/{id}/data should not error")
		require.Equal(t, http.StatusNoContent, status,
			"PATCH should return 204 on success")
		require.Equal(t, totalLength, patchResp.UploadOffset,
			"Upload-Offset should equal data length after full write")
	})

	// -----------------------------------------------------------------------
	// Step 4: Complete and verify
	// -----------------------------------------------------------------------
	t.Run("complete", func(t *testing.T) {
		require.NotEmpty(t, uploadID, "uploadID must be set by 'create' subtest")

		// Mark complete.
		statusCode, err := PatchUploadStatus(uploadID, PatchStatusBody{Status: "complete"})
		require.NoError(t, err, "PATCH /uploads/{id}/status should not error")
		require.Equal(t, http.StatusNoContent, statusCode,
			"PATCH status to complete should return 204")

		// GET and verify.
		rec, status, err := GetUpload(uploadID)
		require.NoError(t, err, "GET /uploads/{id} should not error")
		require.Equal(t, http.StatusOK, status, "GET should return 200")
		require.Equal(t, "complete", rec.Status,
			"upload status should be complete")
		require.NotEmpty(t, rec.OrganizedPath,
			"completed upload must have organized_path")
		require.Equal(t, localIdentifier, rec.LocalIdentifier,
			"local_identifier must be preserved through lifecycle")
		require.Equal(t, "IMG_9876.jpg", rec.Filename,
			"filename must be preserved through lifecycle")

		// The organized filename must carry the stable upload-ID suffix
		// (never the tusd backend ID): <stem>_<upload.ID><ext>. This is the
		// always-on naming convention this plan introduces — see
		// docs/adr/0009-unify-organized-filename-suffix.md.
		require.Equal(t, "IMG_9876_"+uploadID+".jpg", path.Base(rec.OrganizedPath),
			"organized filename must be <stem>_<upload.ID><ext>, got %q", rec.OrganizedPath)
		require.Contains(t, rec.OrganizedPath, "_"+uploadID,
			"organized path must embed the upload.ID suffix")
		require.NotContains(t, rec.OrganizedPath, "_"+rec.BackendID,
			"organized path must not embed the mutable backend_id")
	})

	// -----------------------------------------------------------------------
	// Step 5: Idempotent create after completion
	// -----------------------------------------------------------------------
	t.Run("idempotent_create", func(t *testing.T) {
		// POST the same local_identifier again.
		body := CreateUploadBody{
			LocalIdentifier: localIdentifier,
			Filename:        "IMG_9876.jpg",
			CreationDate:    time.Now().UTC().Format(time.RFC3339),
		}

		cr, status, err := CreateUpload(body)
		require.NoError(t, err, "re-POST should not error")
		require.Equal(t, http.StatusOK, status,
			"re-POSTing a completed upload should return 200")
		require.Equal(t, deterministicID, cr.ID,
			"upload ID must be the same deterministic ID")
		require.Equal(t, "complete", cr.Status,
			"returned record must still have complete status")
	})
}

// ---------------------------------------------------------------------------
// Additional lifecycle tests: creation and listing
// ---------------------------------------------------------------------------

// TestLifecycle_CreateThenList creates an upload and verifies it appears
// in the run-scoped listing.
func TestLifecycle_CreateThenList(t *testing.T) {
	localID := MakeLocalIdentifier(t, t.Name())

	cr := CreateTestUpload(t, localID, "IMG_0002.jpg")
	require.Equal(t, "uploading", cr.Status)

	list := ListRunUploads(t, "", 0, "")
	require.GreaterOrEqual(t, len(list.Items), 1,
		"should have at least one run-scoped upload")

	found := false
	for _, item := range list.Items {
		if item.ID == cr.ID {
			found = true
			require.Equal(t, localID, item.LocalIdentifier)
			require.Equal(t, "uploading", item.Status)
			break
		}
	}
	require.True(t, found, "upload must appear in run-scoped listing")
}

// TestLifecycle_IdempotentCreate verifies that posting the same upload
// twice returns 200 on the second attempt.
func TestLifecycle_IdempotentCreate(t *testing.T) {
	localID := MakeLocalIdentifier(t, t.Name())
	now := time.Now().UTC().Format(time.RFC3339)

	body := CreateUploadBody{
		LocalIdentifier: localID,
		Filename:        "IMG_0003.jpg",
		CreationDate:    now,
	}

	cr1, status1, err := CreateUpload(body)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status1, "first create should return 201")
	require.NotEmpty(t, cr1.ID)

	cr2, status2, err := CreateUpload(body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status2, "duplicate create should return 200")
	require.Equal(t, cr1.ID, cr2.ID, "both responses must have the same upload ID")
	require.Equal(t, cr1.Status, cr2.Status, "both responses must have the same status")
}

// TestLifecycle_IdempotentCreateAfterComplete verifies that after
// completing an upload, posting the same local_identifier returns 200.
func TestLifecycle_IdempotentCreateAfterComplete(t *testing.T) {
	localID := MakeLocalIdentifier(t, t.Name())

	CreateCompleteUpload(t, localID, "IMG_0004.jpg", []byte("data-for-idempotent-test"))

	cr, status, err := CreateUpload(CreateUploadBody{
		LocalIdentifier: localID,
		Filename:        "IMG_0004.jpg",
		CreationDate:    time.Now().UTC().Format(time.RFC3339),
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status,
		"duplicate create after complete should return 200")
	require.Equal(t, "complete", cr.Status,
		"returned record must still be complete")
}

// ---------------------------------------------------------------------------
// Deletion and re-registration
// ---------------------------------------------------------------------------

// TestLifecycle_DeleteThenGet verifies that after soft-deleting an upload,
// GET returns 200 with status "deleted".
func TestLifecycle_DeleteThenGet(t *testing.T) {
	localID := MakeLocalIdentifier(t, t.Name())
	cr := CreateTestUpload(t, localID, "IMG_0005.jpg")

	MustDeleteUpload(t, cr.ID)

	rec, status, err := GetUpload(cr.ID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status,
		"GET after soft-delete must return 200")
	require.Equal(t, "deleted", rec.Status,
		"record status must be deleted after soft-delete")
	require.Equal(t, localID, rec.LocalIdentifier,
		"local_identifier must be preserved after deletion")
}

// TestLifecycle_DeleteThenReRegister verifies that after soft-deleting,
// creating a new upload with the same local_identifier returns 201
// (re-register) with status "uploading".
func TestLifecycle_DeleteThenReRegister(t *testing.T) {
	localID := MakeLocalIdentifier(t, t.Name())
	cr := CreateTestUpload(t, localID, "IMG_0006.jpg")

	MustDeleteUpload(t, cr.ID)

	reReg, status, err := CreateUpload(CreateUploadBody{
		LocalIdentifier: localID,
		Filename:        "IMG_0006.jpg",
		CreationDate:    time.Now().UTC().Format(time.RFC3339),
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status,
		"re-register after delete should return 201")
	require.Equal(t, localID, reReg.LocalIdentifier)
	require.Equal(t, "uploading", reReg.Status,
		"re-registered upload must have uploading status")
	require.NotEmpty(t, reReg.BackendID,
		"re-registered upload must have a new backend_id")
}

// TestLifecycle_DeleteIdempotent verifies that deleting the same upload
// twice returns 204 both times.
func TestLifecycle_DeleteIdempotent(t *testing.T) {
	localID := MakeLocalIdentifier(t, t.Name())
	cr := CreateTestUpload(t, localID, "IMG_0007.jpg")

	MustDeleteUpload(t, cr.ID)

	status, err := DeleteUpload(cr.ID)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, status,
		"second delete should still return 204")
}

// ---------------------------------------------------------------------------
// Error conditions
// ---------------------------------------------------------------------------

// TestLifecycle_OffsetMismatch verifies that PATCH with a wrong
// Upload-Offset returns 409.
func TestLifecycle_OffsetMismatch(t *testing.T) {
	localID := MakeLocalIdentifier(t, t.Name())
	cr := CreateTestUpload(t, localID, "IMG_0008.jpg")

	_, status, err := PatchUploadData(cr.ID, bytes.NewReader([]byte("some-data")), 999, "")
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, status,
		"PATCH with wrong offset should return 409")
}

// TestLifecycle_MarkCompleteWithoutData verifies that marking an upload
// complete without uploading data returns 409.
func TestLifecycle_MarkCompleteWithoutData(t *testing.T) {
	localID := MakeLocalIdentifier(t, t.Name())
	cr := CreateTestUpload(t, localID, "IMG_0009.jpg")

	status, err := PatchUploadStatus(cr.ID, PatchStatusBody{Status: "complete"})
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, status,
		"completing without data should return 409")
}

// TestLifecycle_HeadAfterCompletion verifies that HEAD /data on a
// completed upload returns 409 Conflict.
func TestLifecycle_HeadAfterCompletion(t *testing.T) {
	localID := MakeLocalIdentifier(t, t.Name())
	rec := CreateCompleteUpload(t, localID, "IMG_0010.jpg", []byte("data-for-head-test"))

	_, status, err := HeadUploadData(rec.ID)
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, status,
		"HEAD on completed upload should return 409")
}

// TestLifecycle_PatchDataAfterCompletion verifies that PATCH /data on a
// completed upload returns 409 Conflict.
func TestLifecycle_PatchDataAfterCompletion(t *testing.T) {
	localID := MakeLocalIdentifier(t, t.Name())
	rec := CreateCompleteUpload(t, localID, "IMG_0011.jpg", []byte("data-for-patch-test"))

	_, status, err := PatchUploadData(rec.ID, bytes.NewReader([]byte("more-data")), 0, "")
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, status,
		"PATCH on completed upload should return 409")
}

// TestLifecycle_PatchStatusAlreadyComplete verifies that completing an
// already-complete upload returns 409.
func TestLifecycle_PatchStatusAlreadyComplete(t *testing.T) {
	localID := MakeLocalIdentifier(t, t.Name())
	rec := CreateCompleteUpload(t, localID, "IMG_0012.jpg", []byte("data-for-recomplete"))

	status, err := PatchUploadStatus(rec.ID, PatchStatusBody{Status: "complete"})
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, status,
		"completing an already-complete upload should return 409")
}

// TestLifecycle_PatchStatusDeleted verifies that changing status on a
// deleted upload returns 409.
func TestLifecycle_PatchStatusDeleted(t *testing.T) {
	localID := MakeLocalIdentifier(t, t.Name())
	cr := CreateTestUpload(t, localID, "IMG_0013.jpg")
	MustDeleteUpload(t, cr.ID)

	status, err := PatchUploadStatus(cr.ID, PatchStatusBody{Status: "complete"})
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, status,
		"PATCH status on deleted upload should return 409")
}

// TestLifecycle_PatchDataDeleted verifies that PATCH /data on a deleted
// upload returns 409.
func TestLifecycle_PatchDataDeleted(t *testing.T) {
	localID := MakeLocalIdentifier(t, t.Name())
	cr := CreateTestUpload(t, localID, "IMG_0014.jpg")
	MustDeleteUpload(t, cr.ID)

	_, status, err := PatchUploadData(cr.ID, bytes.NewReader([]byte("data")), 0, "")
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, status,
		"PATCH data on deleted upload should return 409")
}

// ---------------------------------------------------------------------------
// Non-existent resources
// ---------------------------------------------------------------------------

// nonExistentID is a well-formed (length-43) but non-existent upload ID.
// It is a valid base64url-encoded SHA-256 hash (SHA-256 of the empty
// string) but does not correspond to any record in the database.
const nonExistentID = "47DEQpj8HBSa-_TImW-5JCeuQeRkm5NMpJWZG3hSuFU"

// TestLifecycle_GetNonExistent verifies GET on a non-existent ID is 404.
func TestLifecycle_GetNonExistent(t *testing.T) {
	_, status, err := GetUpload(nonExistentID)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, status,
		"GET on non-existent ID should return 404")
}

// TestLifecycle_HeadNonExistent verifies HEAD /data on a non-existent
// ID is 404.
func TestLifecycle_HeadNonExistent(t *testing.T) {
	_, status, err := HeadUploadData(nonExistentID)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, status,
		"HEAD on non-existent ID should return 404")
}

// TestLifecycle_PatchDataNonExistent verifies PATCH /data on a
// non-existent ID is 404.
func TestLifecycle_PatchDataNonExistent(t *testing.T) {
	_, status, err := PatchUploadData(nonExistentID, bytes.NewReader([]byte("data")), 0, "5")
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, status,
		"PATCH data on non-existent ID should return 404")
}

// TestLifecycle_PatchStatusNonExistent verifies PATCH /status on a
// non-existent ID is 404.
func TestLifecycle_PatchStatusNonExistent(t *testing.T) {
	status, err := PatchUploadStatus(nonExistentID, PatchStatusBody{Status: "complete"})
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, status,
		"PATCH status on non-existent ID should return 404")
}

// TestLifecycle_DeleteNonExistent verifies DELETE on a non-existent ID
// is 404.
func TestLifecycle_DeleteNonExistent(t *testing.T) {
	status, err := DeleteUpload(nonExistentID)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, status,
		"DELETE on non-existent ID should return 404")
}

// ---------------------------------------------------------------------------
// Validation errors
// ---------------------------------------------------------------------------

// TestLifecycle_InvalidIDFormat verifies that requests with invalid
// upload ID formats return 400.
//
// NOTE: empty string is not tested here because Go's ServeMux redirects
// /uploads/ (empty {id}) to /uploads (list), so the validation handler
// is never reached. The empty-ID case is tested implicitly via other
// path-based error handling.
func TestLifecycle_InvalidIDFormat(t *testing.T) {
	invalidIDs := []string{
		"short",
		"not-base64!!!",
		strings.Repeat("x", 200),
	}

	for _, id := range invalidIDs {
		t.Run("id="+limitString(id, 20), func(t *testing.T) {
			checkIDReturns400(t, id)
		})
	}
}

// checkIDReturns400 asserts that all endpoints return 400 Bad Request
// for the given (invalid) upload ID.
func checkIDReturns400(t *testing.T, id string) {
	t.Helper()

	_, status, err := GetUpload(id)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, status,
		"GET with invalid ID should return 400")

	_, status, err = HeadUploadData(id)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, status,
		"HEAD with invalid ID should return 400")

	status, err = DeleteUpload(id)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, status,
		"DELETE with invalid ID should return 400")

	status, err = PatchUploadStatus(id, PatchStatusBody{Status: "complete"})
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, status,
		"PATCH status with invalid ID should return 400")

	_, status, err = PatchUploadData(id, bytes.NewReader([]byte("x")), 0, "")
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, status,
		"PATCH data with invalid ID should return 400")
}

// limitString returns s truncated to at most max runes, appending "…"
// when truncated. Used for test names.
func limitString(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// TestLifecycle_CreateUploadMissingFields verifies that POST /uploads
// without required fields returns 400.
func TestLifecycle_CreateUploadMissingFields(t *testing.T) {
	tests := []struct {
		name string
		body CreateUploadBody
	}{
		{
			name: "missing local_identifier",
			body: CreateUploadBody{
				Filename:     "test.jpg",
				CreationDate: time.Now().UTC().Format(time.RFC3339),
			},
		},
		{
			name: "missing filename",
			body: CreateUploadBody{
				LocalIdentifier: MakeLocalIdentifier(t, "no-filename"),
				CreationDate:    time.Now().UTC().Format(time.RFC3339),
			},
		},
		{
			name: "missing creation_date",
			body: CreateUploadBody{
				LocalIdentifier: MakeLocalIdentifier(t, "no-date"),
				Filename:        "test.jpg",
			},
		},
		{
			name: "invalid creation_date format",
			body: CreateUploadBody{
				LocalIdentifier: MakeLocalIdentifier(t, "bad-date"),
				Filename:        "test.jpg",
				CreationDate:    "not-a-date",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := doJSONRequest("POST", "/uploads", tt.body)
			require.NoError(t, err)
			defer resp.Body.Close()

			require.Equal(t, http.StatusBadRequest, resp.StatusCode,
				"POST with %s should return 400", tt.name)
		})
	}
}

// TestLifecycle_PatchDataMissingHeaders verifies that PATCH /data
// without required TUS headers returns 400.
func TestLifecycle_PatchDataMissingHeaders(t *testing.T) {
	localID := MakeLocalIdentifier(t, t.Name())
	cr := CreateTestUpload(t, localID, "IMG_0015.jpg")

	t.Run("missing Content-Type", func(t *testing.T) {
		req, err := http.NewRequest("PATCH", serverURL+"/uploads/"+cr.ID+"/data",
			bytes.NewReader([]byte("data")))
		require.NoError(t, err)
		req.Header.Set("Upload-Offset", "0")
		req.Header.Set("Tus-Resumable", "1.0.0")
		if backupUser != "" || backupPass != "" {
			req.SetBasicAuth(backupUser, backupPass)
		}

		resp, err := httpClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, http.StatusBadRequest, resp.StatusCode,
			"PATCH without Content-Type should return 400")
	})

	t.Run("missing Upload-Offset", func(t *testing.T) {
		req, err := http.NewRequest("PATCH", serverURL+"/uploads/"+cr.ID+"/data",
			bytes.NewReader([]byte("data")))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/offset+octet-stream")
		req.Header.Set("Tus-Resumable", "1.0.0")
		if backupUser != "" || backupPass != "" {
			req.SetBasicAuth(backupUser, backupPass)
		}

		resp, err := httpClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, http.StatusBadRequest, resp.StatusCode,
			"PATCH without Upload-Offset should return 400")
	})
}

// ---------------------------------------------------------------------------
// Partial upload with resume
// ---------------------------------------------------------------------------

// TestLifecycle_PartialUpload verifies that data can be written in
// multiple chunks, that HEAD reports the correct offset after each
// write, and that the upload can be completed once all data has been
// written with Upload-Length.
func TestLifecycle_PartialUpload(t *testing.T) {
	localID := MakeLocalIdentifier(t, t.Name())
	cr := CreateTestUpload(t, localID, "IMG_0016.jpg")
	require.Equal(t, "uploading", cr.Status)

	chunk1 := []byte("hello-")
	chunk2 := []byte("world-")
	chunk3 := []byte("final!")
	fullData := append(append(chunk1, chunk2...), chunk3...)
	totalLength := int64(len(fullData))

	// Write first chunk without Upload-Length (partial write).
	offset := UploadSomeData(t, cr.ID, chunk1)
	require.Equal(t, int64(len(chunk1)), offset,
		"offset should equal first chunk length")

	// HEAD to verify offset.
	headResp, status, err := HeadUploadData(cr.ID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, offset, headResp.UploadOffset,
		"HEAD must report correct offset after first chunk")
	require.Equal(t, "1.0.0", headResp.TusResumable,
		"Tus-Resumable header must be present")

	// Write second chunk without Upload-Length.
	offset = UploadSomeData(t, cr.ID, chunk2)
	require.Equal(t, int64(len(chunk1)+len(chunk2)), offset,
		"offset should equal both chunks combined")

	// HEAD again to verify accumulated offset.
	headResp, status, err = HeadUploadData(cr.ID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, offset, headResp.UploadOffset,
		"HEAD must report accumulated offset")

	// Write final chunk WITH Upload-Length to signal final size.
	patchResp, status, err := PatchUploadData(cr.ID, bytes.NewReader(chunk3),
		offset, fmt.Sprintf("%d", totalLength))
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, status,
		"final PATCH with Upload-Length should return 204")
	require.Equal(t, totalLength, patchResp.UploadOffset,
		"Upload-Offset should equal total data length")

	// Mark complete.
	statusCode, err := PatchUploadStatus(cr.ID, PatchStatusBody{Status: "complete"})
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, statusCode,
		"PATCH status should return 204 after full upload")

	// Verify final record.
	rec, _, err := GetUpload(cr.ID)
	require.NoError(t, err)
	require.Equal(t, "complete", rec.Status)
	require.NotEmpty(t, rec.OrganizedPath)
}

// ---------------------------------------------------------------------------
// Metadata preservation
// ---------------------------------------------------------------------------

// TestLifecycle_CreateWithMetadata verifies that metadata submitted
// at creation time is preserved through the lifecycle.
func TestLifecycle_CreateWithMetadata(t *testing.T) {
	localID := MakeLocalIdentifier(t, t.Name())
	meta := json.RawMessage(`{"source":"icloud","version":2}`)

	resp, status, err := CreateUpload(CreateUploadBody{
		LocalIdentifier: localID,
		Filename:        "IMG_0021.jpg",
		CreationDate:    time.Now().UTC().Format(time.RFC3339),
		Metadata:        meta,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status)

	rec, _, err := GetUpload(resp.ID)
	require.NoError(t, err)
	require.NotNil(t, rec.Metadata, "metadata should be preserved")

	var storedMeta map[string]any
	err = json.Unmarshal(rec.Metadata, &storedMeta)
	require.NoError(t, err, "stored metadata must be valid JSON")
	require.Equal(t, "icloud", storedMeta["source"])
	require.Equal(t, float64(2), storedMeta["version"])
}

// ---------------------------------------------------------------------------
// Multiple uploads
// ---------------------------------------------------------------------------

// TestLifecycle_MultipleCompleteUploads verifies that several uploads
// can be completed sequentially without interference.
func TestLifecycle_MultipleCompleteUploads(t *testing.T) {
	count := 3
	ids := make([]string, count)

	for i := 0; i < count; i++ {
		localID := MakeLocalIdentifier(t, fmt.Sprintf("multi-%d", i))
		rec := CreateCompleteUpload(t, localID,
			fmt.Sprintf("IMG_%04d.jpg", 100+i),
			[]byte(fmt.Sprintf("data-for-multi-upload-%d", i)))
		ids[i] = rec.ID
	}

	for i, id := range ids {
		rec, status, err := GetUpload(id)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status)
		require.Equal(t, "complete", rec.Status,
			"upload %d should be complete", i)
		require.NotEmpty(t, rec.OrganizedPath,
			"upload %d must have organized_path", i)
	}
}

// ---------------------------------------------------------------------------
// Pagination
// ---------------------------------------------------------------------------

// TestLifecycle_ListPagination verifies that cursor-based pagination
// works when there are multiple pages of results.
func TestLifecycle_ListPagination(t *testing.T) {
	count := 3
	for i := 0; i < count; i++ {
		localID := MakeLocalIdentifier(t, fmt.Sprintf("pagination-%d", i))
		CreateTestUpload(t, localID, fmt.Sprintf("PAG_%04d.jpg", 200+i))
	}

	list1 := ListRunUploads(t, "", 1, "")
	require.Len(t, list1.Items, 1, "should return exactly 1 item with limit=1")
	require.NotEmpty(t, list1.NextCursor,
		"should have a next_cursor when more items exist")

	list2 := ListRunUploads(t, "", 1, list1.NextCursor)
	require.Len(t, list2.Items, 1, "second page should have exactly 1 item")
	require.NotEqual(t, list1.Items[0].ID, list2.Items[0].ID,
		"pages must return different records")
}

// ---------------------------------------------------------------------------
// Status filter
// ---------------------------------------------------------------------------

// TestLifecycle_ListWithStatusFilter verifies that listing with a
// status filter returns only matching records.
func TestLifecycle_ListWithStatusFilter(t *testing.T) {
	uploadingID := MakeLocalIdentifier(t, "uploading-"+t.Name())
	cr := CreateTestUpload(t, uploadingID, "IMG_0019.jpg")

	completeID := MakeLocalIdentifier(t, "complete-"+t.Name())
	CreateCompleteUpload(t, completeID, "IMG_0020.jpg", []byte("data-for-list-filter"))

	uploadingList := ListRunUploads(t, "uploading", 0, "")
	foundUploading := false
	for _, item := range uploadingList.Items {
		if item.ID == cr.ID {
			foundUploading = true
			require.Equal(t, "uploading", item.Status)
			break
		}
	}
	require.True(t, foundUploading,
		"uploading record must appear in uploading-filtered list")

	completeList := ListRunUploads(t, "complete", 0, "")
	foundComplete := false
	for _, item := range completeList.Items {
		if item.LocalIdentifier == completeID {
			foundComplete = true
			require.Equal(t, "complete", item.Status)
			break
		}
	}
	require.True(t, foundComplete,
		"complete record must appear in complete-filtered list")
}
