//go:build e2e

// Package e2e provides black-box end-to-end tests for the iCloud Backup
// server. This file contains focused tests for upload resume behaviour
// and offset-conflict error handling:
//
//   - Basic offset tracking: HEAD reports initial offset, PATCH advances it,
//     subsequent HEAD confirms the new offset.
//   - Resume after client interruption: client loses state, queries HEAD to
//     discover the current offset, then resumes from there.
//   - Offset conflict: PATCH with too-low or too-high Upload-Offset returns
//     409 Conflict.
//   - Empty PATCH: zero-byte body with correct offset succeeds but does not
//     advance the offset.
//   - Many small sequential chunks with HEAD verification after each one.
//   - Repeated HEAD calls return consistent results when no PATCH occurs.
package e2e

import (
	"bytes"
	"net/http"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Basic offset tracking
// ---------------------------------------------------------------------------

// TestResume_BasicOffsetTracking verifies that HEAD returns offset 0 for a
// fresh upload, that PATCH with correct offset advances it, and that HEAD
// after each PATCH returns the updated offset.
func TestResume_BasicOffsetTracking(t *testing.T) {
	localID := MakeLocalIdentifier(t, t.Name())
	cr := CreateTestUpload(t, localID, "IMG_resume_basic.jpg")
	require.Equal(t, "uploading", cr.Status)

	// HEAD should report offset 0 on a fresh upload.
	headResp, status, err := HeadUploadData(cr.ID)
	require.NoError(t, err, "HEAD should not error")
	require.Equal(t, http.StatusOK, status, "HEAD should return 200")
	require.Equal(t, int64(0), headResp.UploadOffset, "initial offset must be 0")
	require.Equal(t, "1.0.0", headResp.TusResumable,
		"Tus-Resumable header must be present")

	// PATCH first chunk (500 bytes) from offset 0, no Upload-Length.
	chunk1 := make([]byte, 500)
	for i := range chunk1 {
		chunk1[i] = byte(i % 256)
	}

	patchResp, status, err := PatchUploadData(cr.ID, bytes.NewReader(chunk1), 0, "")
	require.NoError(t, err, "PATCH first chunk should not error")
	require.Equal(t, http.StatusNoContent, status, "PATCH should return 204")
	require.Equal(t, int64(500), patchResp.UploadOffset,
		"offset must advance by chunk size")

	// HEAD should now report offset 500.
	headResp, status, err = HeadUploadData(cr.ID)
	require.NoError(t, err, "HEAD after first chunk should not error")
	require.Equal(t, http.StatusOK, status, "HEAD should return 200")
	require.Equal(t, int64(500), headResp.UploadOffset,
		"HEAD must report offset 500 after 500-byte write")

	// PATCH second chunk (300 bytes) from offset 500.
	chunk2 := make([]byte, 300)
	for i := range chunk2 {
		chunk2[i] = byte((i + 128) % 256)
	}

	patchResp, status, err = PatchUploadData(cr.ID, bytes.NewReader(chunk2), 500, "")
	require.NoError(t, err, "PATCH second chunk should not error")
	require.Equal(t, http.StatusNoContent, status, "PATCH should return 204")
	require.Equal(t, int64(800), patchResp.UploadOffset,
		"offset must be 800 after appending 300 bytes to 500")

	// HEAD should now report offset 800.
	headResp, status, err = HeadUploadData(cr.ID)
	require.NoError(t, err, "HEAD after second chunk should not error")
	require.Equal(t, http.StatusOK, status, "HEAD should return 200")
	require.Equal(t, int64(800), headResp.UploadOffset,
		"HEAD must report offset 800 after second write")

	// PATCH final chunk (200 bytes) WITH Upload-Length to signal completion.
	chunk3 := make([]byte, 200)
	for i := range chunk3 {
		chunk3[i] = byte((i + 64) % 256)
	}
	totalLength := int64(1000) // 500 + 300 + 200

	patchResp, status, err = PatchUploadData(cr.ID, bytes.NewReader(chunk3), 800,
		strconv.FormatInt(totalLength, 10))
	require.NoError(t, err, "PATCH final chunk should not error")
	require.Equal(t, http.StatusNoContent, status, "PATCH final should return 204")
	require.Equal(t, totalLength, patchResp.UploadOffset,
		"offset must equal total length after final write")

	// Complete and verify.
	statusCode, err := PatchUploadStatus(cr.ID, PatchStatusBody{Status: "complete"})
	require.NoError(t, err, "PATCH status to complete should not error")
	require.Equal(t, http.StatusNoContent, statusCode, "completion should return 204")

	rec, _, err := GetUpload(cr.ID)
	require.NoError(t, err, "GET after completion should not error")
	require.Equal(t, "complete", rec.Status, "upload must be complete")
	require.NotEmpty(t, rec.OrganizedPath, "completed upload must have organized_path")
}

// ---------------------------------------------------------------------------
// Resume after interruption
// ---------------------------------------------------------------------------

// TestResume_ResumeAfterInterruption simulates a client that loses its upload
// state mid-way, queries HEAD to discover the current offset, then resumes
// from that point.
//
// Scenario:
//  1. Client writes chunk A.
//  2. Client disconnects / crashes.
//  3. Client reconnects, calls HEAD to learn the current offset.
//  4. Client resumes by PATCHing chunk B from the discovered offset.
//  5. Client writes the final chunk C with Upload-Length.
//  6. Client completes and verifies.
func TestResume_ResumeAfterInterruption(t *testing.T) {
	localID := MakeLocalIdentifier(t, t.Name())
	cr := CreateTestUpload(t, localID, "IMG_resume_interrupt.jpg")
	require.Equal(t, "uploading", cr.Status)

	chunkA := []byte("AAAA-prewritten-data-before-interrupt-")
	chunkB := []byte("BBBB-resumed-data-")
	chunkC := []byte("CCCC-final-chunk")
	fullData := append(append(chunkA, chunkB...), chunkC...)
	totalLength := int64(len(fullData))

	// Phase 1: Write chunk A (pre-interruption).
	patchResp, status, err := PatchUploadData(cr.ID, bytes.NewReader(chunkA), 0, "")
	require.NoError(t, err, "PATCH chunk A should not error")
	require.Equal(t, http.StatusNoContent, status, "PATCH chunk A should return 204")
	require.Equal(t, int64(len(chunkA)), patchResp.UploadOffset,
		"offset must equal chunk A length")

	// Phase 2: Client reconnects and discovers offset via HEAD.
	headResp, status, err := HeadUploadData(cr.ID)
	require.NoError(t, err, "HEAD after interruption should not error")
	require.Equal(t, http.StatusOK, status, "HEAD should return 200")
	require.Equal(t, int64(len(chunkA)), headResp.UploadOffset,
		"HEAD must report the offset from chunk A")
	require.Equal(t, "1.0.0", headResp.TusResumable,
		"Tus-Resumable header must be present")

	// Phase 3: Resume by writing chunk B from the discovered offset.
	resumeOffset := headResp.UploadOffset
	patchResp, status, err = PatchUploadData(cr.ID, bytes.NewReader(chunkB),
		resumeOffset, "")
	require.NoError(t, err, "PATCH chunk B (resume) should not error")
	require.Equal(t, http.StatusNoContent, status,
		"PATCH chunk B should return 204")
	require.Equal(t, int64(len(chunkA)+len(chunkB)), patchResp.UploadOffset,
		"offset must equal chunks A + B combined")

	// Phase 4: Write final chunk C with Upload-Length.
	patchResp, status, err = PatchUploadData(cr.ID, bytes.NewReader(chunkC),
		patchResp.UploadOffset, strconv.FormatInt(totalLength, 10))
	require.NoError(t, err, "PATCH chunk C (final) should not error")
	require.Equal(t, http.StatusNoContent, status,
		"PATCH chunk C should return 204")
	require.Equal(t, totalLength, patchResp.UploadOffset,
		"offset must equal total data length")

	// Phase 5: Complete and verify.
	statusCode, err := PatchUploadStatus(cr.ID, PatchStatusBody{Status: "complete"})
	require.NoError(t, err, "PATCH status to complete should not error")
	require.Equal(t, http.StatusNoContent, statusCode,
		"completion should return 204")

	rec, _, err := GetUpload(cr.ID)
	require.NoError(t, err, "GET after completion should not error")
	require.Equal(t, "complete", rec.Status,
		"resumed upload must be complete")
	require.NotEmpty(t, rec.OrganizedPath,
		"completed upload must have organized_path")
	require.Equal(t, localID, rec.LocalIdentifier,
		"local_identifier must be preserved through resume")
	require.Equal(t, "IMG_resume_interrupt.jpg", rec.Filename,
		"filename must be preserved through resume")
}

// TestResume_MultipleResumeCycles simulates a client that resumes multiple
// times after successive interruptions, each time using HEAD to discover
// the current offset.
func TestResume_MultipleResumeCycles(t *testing.T) {
	localID := MakeLocalIdentifier(t, t.Name())
	cr := CreateTestUpload(t, localID, "IMG_resume_multi.jpg")
	require.Equal(t, "uploading", cr.Status)

	chunks := [][]byte{
		[]byte("chunk-01-"),
		[]byte("chunk-02-"),
		[]byte("chunk-03-"),
		[]byte("chunk-04-"),
		[]byte("chunk-05-"),
	}
	var fullData []byte
	for _, c := range chunks {
		fullData = append(fullData, c...)
	}
	totalLength := int64(len(fullData))

	currentOffset := int64(0)

	for i, chunk := range chunks {
		// Phase 1: HEAD to discover offset (simulating resume after interruption).
		// For the first chunk this is just a consistency check.
		headResp, status, err := HeadUploadData(cr.ID)
		require.NoError(t, err,
			"HEAD before chunk %d should not error", i)
		require.Equal(t, http.StatusOK, status,
			"HEAD before chunk %d should return 200", i)
		require.Equal(t, currentOffset, headResp.UploadOffset,
			"HEAD must report correct offset before chunk %d", i)
		require.Equal(t, "1.0.0", headResp.TusResumable,
			"Tus-Resumable must be present before chunk %d", i)

		// Phase 2: PATCH from the current offset.
		isFinal := i == len(chunks)-1
		uploadLength := ""
		if isFinal {
			uploadLength = strconv.FormatInt(totalLength, 10)
		}

		patchResp, status, err := PatchUploadData(cr.ID,
			bytes.NewReader(chunk), currentOffset, uploadLength)
		require.NoError(t, err,
			"PATCH chunk %d should not error", i)
		require.Equal(t, http.StatusNoContent, status,
			"PATCH chunk %d should return 204", i)

		currentOffset += int64(len(chunk))
		require.Equal(t, currentOffset, patchResp.UploadOffset,
			"offset must advance correctly after chunk %d", i)

		// Phase 3: HEAD to confirm offset after write (simulating recovery
		// checkpoint).
		headResp, status, err = HeadUploadData(cr.ID)
		require.NoError(t, err,
			"HEAD after chunk %d should not error", i)
		require.Equal(t, http.StatusOK, status,
			"HEAD after chunk %d should return 200", i)
		require.Equal(t, currentOffset, headResp.UploadOffset,
			"HEAD must report updated offset after chunk %d", i)
	}

	// Complete and verify.
	statusCode, err := PatchUploadStatus(cr.ID, PatchStatusBody{Status: "complete"})
	require.NoError(t, err, "PATCH status to complete should not error")
	require.Equal(t, http.StatusNoContent, statusCode,
		"completion should return 204")

	rec, _, err := GetUpload(cr.ID)
	require.NoError(t, err, "GET after completion should not error")
	require.Equal(t, "complete", rec.Status,
		"multi-resume upload must be complete")
	require.NotEmpty(t, rec.OrganizedPath,
		"completed upload must have organized_path")
}

// ---------------------------------------------------------------------------
// Offset conflict (409 Conflict)
// ---------------------------------------------------------------------------

// TestResume_OffsetConflictTooLow verifies that PATCH /data with an
// Upload-Offset lower than the current backend offset returns 409 Conflict.
func TestResume_OffsetConflictTooLow(t *testing.T) {
	localID := MakeLocalIdentifier(t, t.Name())
	cr := CreateTestUpload(t, localID, "IMG_conflict_low.jpg")

	// Write some data to establish a non-zero offset.
	chunk := []byte("data-to-establish-offset-12345")
	patchResp, status, err := PatchUploadData(cr.ID, bytes.NewReader(chunk), 0, "")
	require.NoError(t, err, "PATCH initial data should not error")
	require.Equal(t, http.StatusNoContent, status,
		"PATCH initial data should return 204")

	actualOffset := patchResp.UploadOffset
	require.Greater(t, actualOffset, int64(0),
		"offset must be non-zero after writing data")

	// Attempt PATCH from offset 0 (too low for the current state).
	_, status, err = PatchUploadData(cr.ID, bytes.NewReader([]byte("more")), 0, "")
	require.NoError(t, err, "PATCH with offset 0 should not error")
	require.Equal(t, http.StatusConflict, status,
		"PATCH with offset 0 after data was written should return 409")
}

// TestResume_OffsetConflictTooHigh verifies that PATCH /data with an
// Upload-Offset higher than the current backend offset returns 409 Conflict.
func TestResume_OffsetConflictTooHigh(t *testing.T) {
	localID := MakeLocalIdentifier(t, t.Name())
	cr := CreateTestUpload(t, localID, "IMG_conflict_high.jpg")

	// Write some data.
	chunk := []byte("base-data-for-offset")
	patchResp, status, err := PatchUploadData(cr.ID, bytes.NewReader(chunk), 0, "")
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, status)

	actualOffset := patchResp.UploadOffset
	require.Greater(t, actualOffset, int64(0))

	// Attempt PATCH from offset much higher than actual.
	_, status, err = PatchUploadData(cr.ID, bytes.NewReader([]byte("more")),
		actualOffset+9999, "")
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, status,
		"PATCH with offset higher than actual should return 409")

	// Attempt PATCH from offset that is 1 byte too low.
	_, status, err = PatchUploadData(cr.ID, bytes.NewReader([]byte("more")),
		actualOffset-1, "")
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, status,
		"PATCH with offset 1 byte too low should return 409")

	// Attempt PATCH from offset that is 1 byte too high.
	_, status, err = PatchUploadData(cr.ID, bytes.NewReader([]byte("more")),
		actualOffset+1, "")
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, status,
		"PATCH with offset 1 byte too high should return 409")
}

// TestResume_OffsetConflictZeroAfterPartial tests that writing some data,
// then attempting PATCH from offset 0 (as if starting over) is rejected.
func TestResume_OffsetConflictZeroAfterPartial(t *testing.T) {
	localID := MakeLocalIdentifier(t, t.Name())
	cr := CreateTestUpload(t, localID, "IMG_conflict_zero.jpg")

	// Write a partial chunk.
	offset := UploadSomeData(t, cr.ID, []byte("partial-data-for-conflict-test"))
	require.Greater(t, offset, int64(0), "offset must be non-zero after partial write")

	// Attempt PATCH from offset 0.
	_, status, err := PatchUploadData(cr.ID, bytes.NewReader([]byte("restart")), 0, "")
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, status,
		"PATCH from offset 0 after a partial write should return 409")

	// HEAD to verify the offset was not corrupted.
	headResp, status, err := HeadUploadData(cr.ID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, "HEAD should still return 200")
	require.Greater(t, headResp.UploadOffset, int64(0),
		"offset must remain intact after rejected PATCH")
}

// ---------------------------------------------------------------------------
// Empty PATCH
// ---------------------------------------------------------------------------

// TestResume_EmptyPatch verifies that a PATCH with a zero-byte body and
// correct offset succeeds (204) but does NOT advance the Upload-Offset.
func TestResume_EmptyPatch(t *testing.T) {
	localID := MakeLocalIdentifier(t, t.Name())
	cr := CreateTestUpload(t, localID, "IMG_empty_patch.jpg")
	require.Equal(t, "uploading", cr.Status)

	// HEAD to get baseline offset (should be 0).
	headResp, status, err := HeadUploadData(cr.ID)
	require.NoError(t, err, "HEAD before empty PATCH should not error")
	require.Equal(t, http.StatusOK, status, "HEAD should return 200")
	require.Equal(t, int64(0), headResp.UploadOffset,
		"initial offset should be 0")

	// PATCH with empty body from offset 0.
	patchResp, status, err := PatchUploadData(cr.ID, bytes.NewReader([]byte{}), 0, "")
	require.NoError(t, err, "PATCH with empty body should not error")
	require.Equal(t, http.StatusNoContent, status,
		"PATCH with empty body should return 204")
	require.Equal(t, int64(0), patchResp.UploadOffset,
		"offset must remain 0 after empty PATCH")

	// Write some real data.
	chunk := []byte("some-real-data")
	patchResp, status, err = PatchUploadData(cr.ID, bytes.NewReader(chunk), 0, "")
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, status)
	require.Equal(t, int64(len(chunk)), patchResp.UploadOffset)

	// Empty PATCH from the current offset should also succeed and not advance.
	patchResp, status, err = PatchUploadData(cr.ID, bytes.NewReader([]byte{}),
		patchResp.UploadOffset, "")
	require.NoError(t, err, "empty PATCH after write should not error")
	require.Equal(t, http.StatusNoContent, status,
		"empty PATCH after write should return 204")
	require.Equal(t, int64(len(chunk)), patchResp.UploadOffset,
		"offset must not advance after empty PATCH")

	// HEAD should confirm offset unchanged.
	headResp, status, err = HeadUploadData(cr.ID)
	require.NoError(t, err, "HEAD after empty PATCH should not error")
	require.Equal(t, http.StatusOK, status, "HEAD should return 200")
	require.Equal(t, int64(len(chunk)), headResp.UploadOffset,
		"HEAD must confirm offset unchanged after empty PATCH")
}

// ---------------------------------------------------------------------------
// Many small sequential chunks
// ---------------------------------------------------------------------------

// TestResume_ManySmallChunks tests that a large number of very small
// sequential PATCH operations all succeed with correct offset tracking
// after each one.
func TestResume_ManySmallChunks(t *testing.T) {
	localID := MakeLocalIdentifier(t, t.Name())
	cr := CreateTestUpload(t, localID, "IMG_many_chunks.jpg")
	require.Equal(t, "uploading", cr.Status)

	chunkSize := 64       // 64 bytes per chunk
	numChunks := 10       // 10 chunks = 640 bytes total
	totalLength := int64(chunkSize * numChunks)

	currentOffset := int64(0)

	for i := 0; i < numChunks; i++ {
		chunk := make([]byte, chunkSize)
		for j := range chunk {
			chunk[j] = byte((i*chunkSize + j) % 256)
		}

		// PATCH the chunk from the current offset.
		isFinal := i == numChunks-1
		uploadLength := ""
		if isFinal {
			uploadLength = strconv.FormatInt(totalLength, 10)
		}

		patchResp, status, err := PatchUploadData(cr.ID,
			bytes.NewReader(chunk), currentOffset, uploadLength)
		require.NoError(t, err,
			"PATCH chunk %d/%d should not error", i+1, numChunks)
		require.Equal(t, http.StatusNoContent, status,
			"PATCH chunk %d/%d should return 204", i+1, numChunks)

		currentOffset += int64(len(chunk))
		require.Equal(t, currentOffset, patchResp.UploadOffset,
			"offset must be %d after chunk %d/%d",
			currentOffset, i+1, numChunks)

		// HEAD after each chunk to confirm.
		headResp, status, err := HeadUploadData(cr.ID)
		require.NoError(t, err,
			"HEAD after chunk %d/%d should not error", i+1, numChunks)
		require.Equal(t, http.StatusOK, status,
			"HEAD after chunk %d/%d should return 200", i+1, numChunks)
		require.Equal(t, currentOffset, headResp.UploadOffset,
			"HEAD must report offset %d after chunk %d/%d",
			currentOffset, i+1, numChunks)
	}

	// Complete and verify.
	statusCode, err := PatchUploadStatus(cr.ID, PatchStatusBody{Status: "complete"})
	require.NoError(t, err, "PATCH status to complete should not error")
	require.Equal(t, http.StatusNoContent, statusCode,
		"completion should return 204")

	rec, _, err := GetUpload(cr.ID)
	require.NoError(t, err, "GET after completion should not error")
	require.Equal(t, "complete", rec.Status,
		"upload must be complete after all chunks")
	require.NotEmpty(t, rec.OrganizedPath,
		"completed upload must have organized_path")

	// Verify the data size by checking the organized path exists.
	// We can't read the file from the e2e client, but we can verify
	// the record reflects the correct filename and status.
	require.Equal(t, "IMG_many_chunks.jpg", rec.Filename,
		"filename must be preserved through many chunks")
}

// ---------------------------------------------------------------------------
// Repeated HEAD consistency
// ---------------------------------------------------------------------------

// TestResume_RepeatedHeadConsistency verifies that calling HEAD multiple
// times in succession always returns the same Upload-Offset when no PATCH
// happens between calls.
func TestResume_RepeatedHeadConsistency(t *testing.T) {
	localID := MakeLocalIdentifier(t, t.Name())
	cr := CreateTestUpload(t, localID, "IMG_head_consist.jpg")
	require.Equal(t, "uploading", cr.Status)

	// Write some data to establish a non-zero offset.
	offset := UploadSomeData(t, cr.ID, []byte("data-for-head-consistency-test"))
	require.Greater(t, offset, int64(0), "offset must be non-zero after write")

	// Call HEAD several times and verify the offset is consistent.
	var previousOffset int64 = -1
	for i := 0; i < 5; i++ {
		headResp, status, err := HeadUploadData(cr.ID)
		require.NoError(t, err,
			"HEAD call %d should not error", i+1)
		require.Equal(t, http.StatusOK, status,
			"HEAD call %d should return 200", i+1)
		require.Equal(t, "1.0.0", headResp.TusResumable,
			"Tus-Resumable must be present on HEAD call %d", i+1)

		if previousOffset >= 0 {
			require.Equal(t, previousOffset, headResp.UploadOffset,
				"HEAD call %d must return the same offset as previous calls", i+1)
		}
		previousOffset = headResp.UploadOffset
	}

	require.Greater(t, previousOffset, int64(0),
		"offset must be non-zero after data was written")
}

// TestResume_HeadZeroAfterCreate verifies that HEAD on a freshly created
// upload (before any PATCH) returns offset 0, and calling HEAD multiple
// times before any write consistently returns 0.
func TestResume_HeadZeroAfterCreate(t *testing.T) {
	localID := MakeLocalIdentifier(t, t.Name())
	cr := CreateTestUpload(t, localID, "IMG_head_zero.jpg")
	require.Equal(t, "uploading", cr.Status)

	for i := 0; i < 3; i++ {
		headResp, status, err := HeadUploadData(cr.ID)
		require.NoError(t, err,
			"HEAD call %d before any data should not error", i+1)
		require.Equal(t, http.StatusOK, status,
			"HEAD call %d should return 200", i+1)
		require.Equal(t, int64(0), headResp.UploadOffset,
			"HEAD call %d must report offset 0 before any write", i+1)
		require.Equal(t, "1.0.0", headResp.TusResumable,
			"Tus-Resumable must be present on HEAD call %d", i+1)
	}
}
