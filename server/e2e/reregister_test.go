//go:build e2e

// Package e2e provides black-box end-to-end tests for the iCloud Backup
// server. This file contains focused tests for soft-deletion and
// re-registration of upload records:
//
//   - Deleting uploads in various states (uploading, complete, already
//     deleted).
//   - Idempotent deletion (DELETE on an already-deleted upload returns 204).
//   - Re-registration after deletion: creating a new upload with the same
//     local_identifier as a deleted record returns 201 with status
//     "uploading" and a fresh backend_id.
//   - Full lifecycle after re-registration: writing data and completing the
//     re-registered upload.
//   - Listing behaviour across delete/re-register transitions.
//   - BundleID and metadata preservation after delete/re-register.
//   - Error handling: DELETE on a non-existent upload returns 404.
package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Deletion in various states
// ---------------------------------------------------------------------------

// TestReRegister_DeleteUploading verifies that deleting an uploading upload
// returns 204, and a subsequent GET returns the record with status
// "deleted".
func TestReRegister_DeleteUploading(t *testing.T) {
	localID := MakeLocalIdentifier(t, t.Name())
	cr := CreateTestUpload(t, localID, "IMG_del_up.jpg")
	require.Equal(t, "uploading", cr.Status)

	MustDeleteUpload(t, cr.ID)

	rec, status, err := GetUpload(cr.ID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status,
		"GET after soft-delete must return 200")
	require.Equal(t, "deleted", rec.Status,
		"upload status must be deleted after soft-delete")
	require.Equal(t, localID, rec.LocalIdentifier,
		"local_identifier must be preserved after deletion")
	require.Equal(t, "IMG_del_up.jpg", rec.Filename,
		"filename must be preserved after deletion")
}

// TestReRegister_DeleteComplete verifies that deleting a completed upload
// returns 204, and a subsequent GET returns the record with status
// "deleted".
func TestReRegister_DeleteComplete(t *testing.T) {
	localID := MakeLocalIdentifier(t, t.Name())
	rec := CreateCompleteUpload(t, localID, "IMG_del_cmpl.jpg",
		[]byte("data-for-delete-complete-test"))
	require.Equal(t, "complete", rec.Status)
	require.NotEmpty(t, rec.OrganizedPath,
		"completed upload must have an organized_path")

	MustDeleteUpload(t, rec.ID)

	updated, status, err := GetUpload(rec.ID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status,
		"GET after soft-delete must return 200")
	require.Equal(t, "deleted", updated.Status,
		"completed upload status must be deleted after soft-delete")
	require.Equal(t, localID, updated.LocalIdentifier,
		"local_identifier must be preserved after deletion of complete upload")
	require.Equal(t, rec.OrganizedPath, updated.OrganizedPath,
		"organized_path must be preserved after deletion")
}

// TestReRegister_DeleteIdempotent verifies that deleting an already-deleted
// upload returns 204 (idempotent).
func TestReRegister_DeleteIdempotent(t *testing.T) {
	localID := MakeLocalIdentifier(t, t.Name())
	cr := CreateTestUpload(t, localID, "IMG_del_idem.jpg")
	MustDeleteUpload(t, cr.ID)

	// Second delete must also return 204.
	status, err := DeleteUpload(cr.ID)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, status,
		"second DELETE on deleted upload should return 204")

	// Third delete also 204.
	status, err = DeleteUpload(cr.ID)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, status,
		"third DELETE on deleted upload should return 204")
}

// TestReRegister_DeleteNonExistent verifies that DELETE on a non-existent
// upload ID returns 404.
func TestReRegister_DeleteNonExistent(t *testing.T) {
	status, err := DeleteUpload(nonExistentID)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, status,
		"DELETE on non-existent ID should return 404")
}

// TestReRegister_DeleteUploadingThenGetStatus verifies that after deleting
// an uploading upload, the record is still retrievable via GET and shows
// status "deleted".
func TestReRegister_DeleteUploadingThenGetStatus(t *testing.T) {
	localID := MakeLocalIdentifier(t, t.Name())
	cr := CreateTestUpload(t, localID, "IMG_del_get.jpg")
	MustDeleteUpload(t, cr.ID)

	rec, status, err := GetUpload(cr.ID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "deleted", rec.Status)
	require.Equal(t, localID, rec.LocalIdentifier)
	require.Equal(t, "IMG_del_get.jpg", rec.Filename)
}

// ---------------------------------------------------------------------------
// Re-registration after deletion
// ---------------------------------------------------------------------------

// TestReRegister_AfterDelete verifies that posting the same
// local_identifier after a soft-delete returns 201 (re-register) with
// status "uploading" and a fresh backend_id.
func TestReRegister_AfterDelete(t *testing.T) {
	localID := MakeLocalIdentifier(t, t.Name())
	cr := CreateTestUpload(t, localID, "IMG_rereg.jpg")
	MustDeleteUpload(t, cr.ID)

	// Re-register with the same local_identifier.
	reReg, status, err := CreateUpload(CreateUploadBody{
		LocalIdentifier: localID,
		Filename:        "IMG_rereg.jpg",
		CreationDate:    time.Now().UTC().Format(time.RFC3339),
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status,
		"re-register after delete should return 201")
	require.Equal(t, localID, reReg.LocalIdentifier,
		"local_identifier must match")
	require.Equal(t, "uploading", reReg.Status,
		"re-registered upload must have uploading status")
	require.NotEmpty(t, reReg.BackendID,
		"re-registered upload must have a new backend_id")
	require.NotEqual(t, cr.BackendID, reReg.BackendID,
		"re-registered upload must have a different backend_id from the original")

	// Verify the re-registered record can be fetched.
	rec, _, err := GetUpload(reReg.ID)
	require.NoError(t, err)
	require.Equal(t, "uploading", rec.Status,
		"re-registered upload must be in uploading state")
	require.Equal(t, localID, rec.LocalIdentifier,
		"local_identifier must be preserved through re-register")
}

// TestReRegister_DeleteThenReRegisterThenComplete verifies the full
// lifecycle: create → delete → re-register → upload data → complete.
func TestReRegister_DeleteThenReRegisterThenComplete(t *testing.T) {
	localID := MakeLocalIdentifier(t, t.Name())
	cr := CreateTestUpload(t, localID, "IMG_rereg_full.jpg")
	MustDeleteUpload(t, cr.ID)

	// Re-register.
	reReg, status, err := CreateUpload(CreateUploadBody{
		LocalIdentifier: localID,
		Filename:        "IMG_rereg_full.jpg",
		CreationDate:    time.Now().UTC().Format(time.RFC3339),
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status)
	require.Equal(t, "uploading", reReg.Status)

	// Upload data and complete.
	data := []byte("re-registered-upload-data")
	length := int64(len(data))

	patchResp, status, err := PatchUploadData(reReg.ID,
		bytes.NewReader(data), 0, fmt.Sprintf("%d", length))
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, status)
	require.Equal(t, length, patchResp.UploadOffset)

	statusCode, err := PatchUploadStatus(reReg.ID, PatchStatusBody{Status: "complete"})
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, statusCode)

	rec, _, err := GetUpload(reReg.ID)
	require.NoError(t, err)
	require.Equal(t, "complete", rec.Status)
	require.NotEmpty(t, rec.OrganizedPath)
	require.Equal(t, localID, rec.LocalIdentifier)
}

// TestReRegister_DeleteCompleteThenReRegister verifies that deleting a
// completed upload and then re-registering creates a fresh uploading record.
func TestReRegister_DeleteCompleteThenReRegister(t *testing.T) {
	localID := MakeLocalIdentifier(t, t.Name())
	rec := CreateCompleteUpload(t, localID, "IMG_del_cmpl_rereg.jpg",
		[]byte("data-for-delete-complete-rereg"))
	require.Equal(t, "complete", rec.Status)
	require.NotEmpty(t, rec.OrganizedPath)

	MustDeleteUpload(t, rec.ID)

	// Verify status is now deleted.
	deletedRec, _, err := GetUpload(rec.ID)
	require.NoError(t, err)
	require.Equal(t, "deleted", deletedRec.Status)

	// Re-register.
	reReg, status, err := CreateUpload(CreateUploadBody{
		LocalIdentifier: localID,
		Filename:        "IMG_del_cmpl_rereg.jpg",
		CreationDate:    time.Now().UTC().Format(time.RFC3339),
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status,
		"re-register after delete of completed upload should return 201")
	require.Equal(t, "uploading", reReg.Status,
		"re-registered upload must have uploading status")
	require.NotEmpty(t, reReg.BackendID,
		"re-registered upload must have a new backend_id")
	require.Equal(t, rec.ID, reReg.ID,
		"re-registered upload must keep the same deterministic ID")
}

// TestReRegister_MultipleDeleteReRegister verifies that the cycle of
// delete → re-register → delete → re-register can be repeated multiple
// times without issues.
func TestReRegister_MultipleDeleteReRegister(t *testing.T) {
	localID := MakeLocalIdentifier(t, t.Name())

	// Cycle 1.
	cr1 := CreateTestUpload(t, localID, "IMG_multi_cycle.jpg")
	MustDeleteUpload(t, cr1.ID)

	reReg1, status, err := CreateUpload(CreateUploadBody{
		LocalIdentifier: localID,
		Filename:        "IMG_multi_cycle.jpg",
		CreationDate:    time.Now().UTC().Format(time.RFC3339),
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status)
	require.Equal(t, "uploading", reReg1.Status)

	// Cycle 2: delete the re-registered upload and re-register again.
	MustDeleteUpload(t, reReg1.ID)

	reReg2, status, err := CreateUpload(CreateUploadBody{
		LocalIdentifier: localID,
		Filename:        "IMG_multi_cycle.jpg",
		CreationDate:    time.Now().UTC().Format(time.RFC3339),
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status,
		"second re-register after second delete should return 201")
	require.Equal(t, "uploading", reReg2.Status,
		"second re-registered upload must have uploading status")
	require.NotEmpty(t, reReg2.BackendID,
		"second re-registered upload must have a backend_id")

	// Verify the deterministic ID is the same across all cycles.
	deterministicID := SafeIDFromLocal(localID)
	require.Equal(t, deterministicID, cr1.ID, "first upload ID must be deterministic")
	require.Equal(t, deterministicID, reReg1.ID, "first re-register ID must be deterministic")
	require.Equal(t, deterministicID, reReg2.ID, "second re-register ID must be deterministic")

	// Complete the final re-registered upload to verify it works end-to-end.
	data := []byte("multi-cycle-data")
	length := int64(len(data))
	patchResp, status, err := PatchUploadData(reReg2.ID,
		bytes.NewReader(data), 0, fmt.Sprintf("%d", length))
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, status)
	require.Equal(t, length, patchResp.UploadOffset)

	statusCode, err := PatchUploadStatus(reReg2.ID, PatchStatusBody{Status: "complete"})
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, statusCode)

	rec, _, err := GetUpload(reReg2.ID)
	require.NoError(t, err)
	require.Equal(t, "complete", rec.Status)
	require.NotEmpty(t, rec.OrganizedPath)
}

// ---------------------------------------------------------------------------
// Re-registration: behaviour of HEAD, PATCH /data, PATCH /status on
// deleted uploads
// ---------------------------------------------------------------------------

// TestReRegister_HeadDataAfterDelete verifies that HEAD /data on a
// deleted upload returns 404.
func TestReRegister_HeadDataAfterDelete(t *testing.T) {
	localID := MakeLocalIdentifier(t, t.Name())
	cr := CreateTestUpload(t, localID, "IMG_head_del.jpg")
	MustDeleteUpload(t, cr.ID)

	_, status, err := HeadUploadData(cr.ID)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, status,
		"HEAD /data on deleted upload should return 404")
}

// TestReRegister_PatchDataAfterDelete verifies that PATCH /data on a
// deleted upload returns 409 Conflict.
func TestReRegister_PatchDataAfterDelete(t *testing.T) {
	localID := MakeLocalIdentifier(t, t.Name())
	cr := CreateTestUpload(t, localID, "IMG_patchdata_del.jpg")
	MustDeleteUpload(t, cr.ID)

	_, status, err := PatchUploadData(cr.ID, bytes.NewReader([]byte("data")), 0, "")
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, status,
		"PATCH /data on deleted upload should return 409")
}

// TestReRegister_PatchStatusAfterDelete verifies that PATCH /status on a
// deleted upload returns 409 Conflict.
func TestReRegister_PatchStatusAfterDelete(t *testing.T) {
	localID := MakeLocalIdentifier(t, t.Name())
	cr := CreateTestUpload(t, localID, "IMG_status_del.jpg")
	MustDeleteUpload(t, cr.ID)

	status, err := PatchUploadStatus(cr.ID, PatchStatusBody{Status: "complete"})
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, status,
		"PATCH /status on deleted upload should return 409")
}

// ---------------------------------------------------------------------------
// Listing after delete and re-register
// ---------------------------------------------------------------------------

// TestReRegister_ListAfterDelete verifies that a deleted upload still
// appears in the run-scoped listing with status "deleted".
func TestReRegister_ListAfterDelete(t *testing.T) {
	localID := MakeLocalIdentifier(t, t.Name())
	cr := CreateTestUpload(t, localID, "IMG_list_del.jpg")
	MustDeleteUpload(t, cr.ID)

	// List with no status filter.
	list := ListRunUploads(t, "", 0, "")
	found := false
	for _, item := range list.Items {
		if item.ID == cr.ID {
			found = true
			require.Equal(t, "deleted", item.Status,
				"upload must have deleted status in listing")
			require.Equal(t, localID, item.LocalIdentifier,
				"local_identifier must be preserved in listing after delete")
			require.Equal(t, "IMG_list_del.jpg", item.Filename,
				"filename must be preserved in listing after delete")
			break
		}
	}
	require.True(t, found, "deleted upload must appear in run-scoped listing")
}

// TestReRegister_ListAfterReRegister verifies that after re-registering a
// deleted upload, the listing shows only the latest state (uploading) and
// does not contain duplicate entries — the re-register replaces the deleted
// record's status.
func TestReRegister_ListAfterReRegister(t *testing.T) {
	localID := MakeLocalIdentifier(t, t.Name())
	cr := CreateTestUpload(t, localID, "IMG_list_rereg.jpg")
	MustDeleteUpload(t, cr.ID)

	// Re-register.
	reReg, status, err := CreateUpload(CreateUploadBody{
		LocalIdentifier: localID,
		Filename:        "IMG_list_rereg.jpg",
		CreationDate:    time.Now().UTC().Format(time.RFC3339),
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status)
	require.Equal(t, "uploading", reReg.Status)
	require.Equal(t, cr.ID, reReg.ID, "re-registered upload must keep the same ID")

	// List and verify no duplicates — there should be exactly one record
	// with this ID and its status should be "uploading".
	list := ListRunUploads(t, "", 0, "")
	count := 0
	for _, item := range list.Items {
		if item.ID == reReg.ID {
			count++
			require.Equal(t, "uploading", item.Status,
				"re-registered upload must appear as uploading in listing")
			require.Equal(t, localID, item.LocalIdentifier,
				"local_identifier must be preserved")
		}
	}
	require.Equal(t, 1, count,
		"there must be exactly one listing entry for the re-registered upload (no duplicates)")
}

// TestReRegister_ListAfterDeleteComplete verifies that a deleted completed
// upload appears with status "deleted" in the listing, and its
// organized_path is preserved.
func TestReRegister_ListAfterDeleteComplete(t *testing.T) {
	localID := MakeLocalIdentifier(t, t.Name())
	rec := CreateCompleteUpload(t, localID, "IMG_list_del_cmpl.jpg",
		[]byte("data-for-list-delete-complete"))
	require.NotEmpty(t, rec.OrganizedPath)

	MustDeleteUpload(t, rec.ID)

	list := ListRunUploads(t, "", 0, "")
	found := false
	for _, item := range list.Items {
		if item.ID == rec.ID {
			found = true
			require.Equal(t, "deleted", item.Status,
				"deleted completed upload must appear as deleted in listing")
			require.NotEmpty(t, item.OrganizedPath,
				"organized_path must be preserved in listing after delete")
			require.Equal(t, rec.OrganizedPath, item.OrganizedPath,
				"organized_path value must match the original")
			break
		}
	}
	require.True(t, found, "deleted completed upload must appear in run-scoped listing")
}

// ---------------------------------------------------------------------------
// Re-registration: optional field preservation
// ---------------------------------------------------------------------------

// TestReRegister_BundleIDAndMetadataPreservation verifies that bundle_id
// and metadata are preserved after a delete → re-register cycle. The
// re-register response should include the metadata from the original
// creation (the initial record's metadata is replaced by the new POST).
func TestReRegister_BundleIDAndMetadataPreservation(t *testing.T) {
	localID := MakeLocalIdentifier(t, t.Name())

	// Create with bundle_id and metadata.
	meta := json.RawMessage(`{"app":"photos","version":3}`)

	cr, status, err := CreateUpload(CreateUploadBody{
		LocalIdentifier: localID,
		Filename:        "IMG_meta_rereg.jpg",
		CreationDate:    time.Now().UTC().Format(time.RFC3339),
		BundleID:        "com.example.photos",
		Metadata:        meta,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status)
	require.Equal(t, "uploading", cr.Status)

	MustDeleteUpload(t, cr.ID)

	// Re-register with the same bundle_id and metadata.
	reReg, status, err := CreateUpload(CreateUploadBody{
		LocalIdentifier: localID,
		Filename:        "IMG_meta_rereg.jpg",
		CreationDate:    time.Now().UTC().Format(time.RFC3339),
		BundleID:        "com.example.photos",
		Metadata:        meta,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status,
		"re-register after delete should return 201")
	require.Equal(t, "uploading", reReg.Status)

	// Verify the full record via GET.
	rec, _, err := GetUpload(reReg.ID)
	require.NoError(t, err)
	require.Equal(t, "com.example.photos", rec.BundleID,
		"bundle_id must be preserved through delete/re-register")
	require.NotNil(t, rec.Metadata,
		"metadata must be preserved through delete/re-register")

	var storedMeta map[string]any
	err = json.Unmarshal(rec.Metadata, &storedMeta)
	require.NoError(t, err, "stored metadata must be valid JSON")
	require.Equal(t, "photos", storedMeta["app"])
	require.Equal(t, float64(3), storedMeta["version"])
}

// ---------------------------------------------------------------------------
// Re-registration after completing the re-registered upload
// ---------------------------------------------------------------------------

// TestReRegister_DeleteReRegisteredUpload verifies that a re-registered
// (uploading) upload can be deleted, and then re-registered again.
func TestReRegister_DeleteReRegisteredUpload(t *testing.T) {
	localID := MakeLocalIdentifier(t, t.Name())
	cr := CreateTestUpload(t, localID, "IMG_del_rereg_again.jpg")
	MustDeleteUpload(t, cr.ID)

	// Re-register.
	reReg, status, err := CreateUpload(CreateUploadBody{
		LocalIdentifier: localID,
		Filename:        "IMG_del_rereg_again.jpg",
		CreationDate:    time.Now().UTC().Format(time.RFC3339),
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status)
	require.Equal(t, "uploading", reReg.Status)

	// Delete the re-registered upload.
	MustDeleteUpload(t, reReg.ID)

	// Verify it's deleted.
	rec, _, err := GetUpload(reReg.ID)
	require.NoError(t, err)
	require.Equal(t, "deleted", rec.Status)

	// Re-register again.
	reReg2, status, err := CreateUpload(CreateUploadBody{
		LocalIdentifier: localID,
		Filename:        "IMG_del_rereg_again.jpg",
		CreationDate:    time.Now().UTC().Format(time.RFC3339),
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status,
		"re-register after second delete should return 201")
	require.Equal(t, "uploading", reReg2.Status,
		"second re-register must have uploading status")
	require.Equal(t, reReg.ID, reReg2.ID,
		"deterministic ID must remain the same across all cycles")
}

// ---------------------------------------------------------------------------
// Full lifecycle after re-register: partial upload + resume
// ---------------------------------------------------------------------------

// TestReRegister_PartialUploadAfterReRegister verifies that a
// re-registered upload supports partial (chunked) uploads with resume,
// matching the behaviour of a fresh upload.
func TestReRegister_PartialUploadAfterReRegister(t *testing.T) {
	localID := MakeLocalIdentifier(t, t.Name())
	cr := CreateTestUpload(t, localID, "IMG_rereg_partial.jpg")
	MustDeleteUpload(t, cr.ID)

	// Re-register.
	reReg, status, err := CreateUpload(CreateUploadBody{
		LocalIdentifier: localID,
		Filename:        "IMG_rereg_partial.jpg",
		CreationDate:    time.Now().UTC().Format(time.RFC3339),
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status)
	require.Equal(t, "uploading", reReg.Status)

	// Perform partial upload with resume.
	chunk1 := []byte("first-chunk-")
	chunk2 := []byte("second-chunk-")
	chunk3 := []byte("final")
	fullData := append(append(chunk1, chunk2...), chunk3...)
	totalLength := int64(len(fullData))

	// Write first chunk without Upload-Length.
	offset := UploadSomeData(t, reReg.ID, chunk1)
	require.Equal(t, int64(len(chunk1)), offset)

	// HEAD to verify offset.
	headResp, status, err := HeadUploadData(reReg.ID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, offset, headResp.UploadOffset)

	// Write second chunk without Upload-Length.
	offset = UploadSomeData(t, reReg.ID, chunk2)
	require.Equal(t, int64(len(chunk1)+len(chunk2)), offset)

	// HEAD again.
	headResp, status, err = HeadUploadData(reReg.ID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, offset, headResp.UploadOffset)

	// Write final chunk WITH Upload-Length.
	patchResp, status, err := PatchUploadData(reReg.ID,
		bytes.NewReader(chunk3), offset, fmt.Sprintf("%d", totalLength))
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, status)
	require.Equal(t, totalLength, patchResp.UploadOffset)

	// Complete.
	statusCode, err := PatchUploadStatus(reReg.ID, PatchStatusBody{Status: "complete"})
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, statusCode)

	// Verify.
	rec, _, err := GetUpload(reReg.ID)
	require.NoError(t, err)
	require.Equal(t, "complete", rec.Status)
	require.NotEmpty(t, rec.OrganizedPath)
	require.Equal(t, localID, rec.LocalIdentifier)
	require.Equal(t, "IMG_rereg_partial.jpg", rec.Filename)
}
