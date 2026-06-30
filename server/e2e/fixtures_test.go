//go:build e2e

// Package e2e provides black-box end-to-end tests for the iCloud Backup
// server. This file provides reusable test fixtures — helpers that create,
// inspect, and clean up upload records so that individual test files can
// focus on behaviour rather than boilerplate.
//
// All fixture helpers:
//   - Use RunID-scoped local identifiers so that ListUploads queries can
//     be filtered to records created by the current test run.
//   - Fail the test immediately (via require) on unexpected results.
//   - Close all response bodies.
package e2e

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Run-scoped identifiers and time windows
// ---------------------------------------------------------------------------

// runID is a unique, unpredictable hex string generated once per test run
// via crypto/rand. It is embedded in every local_identifier created through
// MakeLocalIdentifier so that list queries can be scoped strictly to the
// current run.
var runID string

// initTime records the wall clock at package initialisation. It is used by
// FixtureTimeRange to provide a deterministic query window that safely
// encloses all fixture uploads for the current test run.
var initTime time.Time

func init() {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		panic(fmt.Errorf("e2e fixtures: failed to generate run ID: %w", err))
	}
	runID = hex.EncodeToString(buf)
	initTime = time.Now().UTC()
}

// runPrefix returns the literal prefix shared by every local_identifier
// produced by MakeLocalIdentifier for the current test run. Listing
// assertions use it to filter paginated result sets strictly to records
// created by this run, so they do not depend on the database being empty
// (the suite may be re-run against an already-warm stack).
func runPrefix() string {
	return "e2e-" + runID + "-"
}

// IsRunItem reports whether item was created by the current test run, by
// checking that its local_identifier carries this run's prefix.
func IsRunItem(item UploadRecord) bool {
	return strings.HasPrefix(item.LocalIdentifier, runPrefix())
}

// MakeLocalIdentifier returns a deterministic local_identifier that is
// scoped to the current test run and incorporates the given suffix.
// The suffix should describe the individual fixture, e.g. the test name
// or a short label.
//
// Example:
//
//	id := MakeLocalIdentifier(t, t.Name())
//	id := MakeLocalIdentifier(t, "photo-export-1")
func MakeLocalIdentifier(t testing.TB, suffix string) string {
	t.Helper()
	return fmt.Sprintf("e2e-%s-%s", runID, suffix)
}

// FixtureTimeRange returns RFC3339 from/to timestamps that safely enclose
// all fixture uploads created during this test run. Use these values when
// calling ListUploads to scope results to the current run only, avoiding
// pollution from previous runs or unrelated uploads.
//
// The returned window starts one minute before package init (to account for
// any setup that occurs during init) and extends 24 hours into the future
// (so clock skew never causes a late-created record to be excluded).
//
// Example:
//
//	from, to := FixtureTimeRange()
//	list, err := ListUploads(from, to, "", 100, "")
func FixtureTimeRange() (from, to string) {
	from = initTime.Add(-time.Minute).Format(time.RFC3339)
	to = initTime.Add(24 * time.Hour).Format(time.RFC3339)
	return
}

// ---------------------------------------------------------------------------
// Fixture: Upload creation helpers
// ---------------------------------------------------------------------------

// CreateTestUpload creates a new upload via POST /uploads with the given
// local_identifier and filename. It sets creation_date to the current UTC
// time in RFC3339 format.
//
// The helper asserts that the server responds with 201 Created or 200 OK
// (200 is returned when the upload already exists in an active or completed
// state) and that the response body is well-formed. Returns the parsed
// CreateUploadResponse.
//
// Example:
//
//	cr := CreateTestUpload(t, MakeLocalIdentifier(t, "pic"), "IMG_0001.jpg")
//	require.Equal(t, "uploading", cr.Status)
func CreateTestUpload(t testing.TB, localID, filename string) *CreateUploadResponse {
	t.Helper()

	body := CreateUploadBody{
		LocalIdentifier: localID,
		Filename:        filename,
		CreationDate:    time.Now().UTC().Format(time.RFC3339),
	}

	resp, status, err := CreateUpload(body)
	require.NoError(t, err, "POST /uploads should not error")
	require.Contains(t, []int{http.StatusCreated, http.StatusOK}, status,
		"POST /uploads should return 201 (new) or 200 (existing active/completed)")
	require.NotEmpty(t, resp.ID, "upload ID must not be empty")
	require.Equal(t, localID, resp.LocalIdentifier,
		"local_identifier in response must match request")
	require.NotEmpty(t, resp.BackendID, "backend_id must not be empty")
	require.Contains(t, resp.UploadURL, "/uploads/"+resp.ID+"/data",
		"upload_url must reference the data endpoint")

	return resp
}

// CreateCompleteUpload is a convenience helper that performs the full upload
// lifecycle: create → write data → mark complete → verify. It uploads the
// entire data slice in a single PATCH request with Upload-Length set to the
// slice length (the final chunk signals completion to the TUS backend).
//
// The helper asserts every intermediate step and returns the final
// UploadRecord with status "complete". The data length must be greater
// than zero.
//
// Example:
//
//	rec := CreateCompleteUpload(t, MakeLocalIdentifier(t, "done"), "photo.jpg", []byte("fake-jpeg-data"))
//	require.NotEmpty(t, rec.OrganizedPath)
func CreateCompleteUpload(t testing.TB, localID, filename string, data []byte) *UploadRecord {
	t.Helper()

	require.Greater(t, len(data), 0,
		"data must be non-empty for completed uploads")

	// Step 1: Create the upload record.
	cr := CreateTestUpload(t, localID, filename)
	require.Equal(t, "uploading", cr.Status,
		"fresh upload must have uploading status")

	// Step 2: Write the full data with Upload-Length signalling final size.
	length := int64(len(data))
	patchResp, status, err := PatchUploadData(cr.ID, bytes.NewReader(data), 0,
		strconv.FormatInt(length, 10))
	require.NoError(t, err, "PATCH /uploads/{id}/data should not error")
	require.Equal(t, http.StatusNoContent, status,
		"PATCH /uploads/{id}/data should return 204")
	require.Equal(t, length, patchResp.UploadOffset,
		"Upload-Offset must equal data length after full write")

	// Step 3: Mark the upload as complete.
	statusCode, err := PatchUploadStatus(cr.ID, PatchStatusBody{Status: "complete"})
	require.NoError(t, err, "PATCH /uploads/{id}/status should not error")
	require.Equal(t, http.StatusNoContent, statusCode,
		"PATCH /uploads/{id}/status should return 204")

	// Step 4: Verify via GET and return the full record.
	record, getStatus, err := GetUpload(cr.ID)
	require.NoError(t, err, "GET /uploads/{id} should not error")
	require.Equal(t, http.StatusOK, getStatus,
		"GET /uploads/{id} should return 200")
	require.Equal(t, "complete", record.Status,
		"upload status should be complete after successful completion")
	require.NotEmpty(t, record.OrganizedPath,
		"completed upload must have an organized_path")
	require.Equal(t, localID, record.LocalIdentifier,
		"local_identifier must be preserved through lifecycle")

	return record
}

// UploadSomeData appends a chunk of data to an existing uploading upload
// without declaring Upload-Length (no final-size signal). This simulates a
// partial / resumable upload and is useful for testing offset tracking and
// resume scenarios.
//
// The helper first issues HEAD to determine the current offset, then PATCHes
// the data. It asserts 204 and returns the new Upload-Offset.
//
// Example:
//
//	newOffset := UploadSomeData(t, uploadID, []byte("chunk1"))
//	newOffset = UploadSomeData(t, uploadID, []byte("chunk2"))
//	// ... later, signal completion with Upload-Length
func UploadSomeData(t testing.TB, id string, data []byte) int64 {
	t.Helper()

	require.NotEmpty(t, id, "upload ID must not be empty")
	require.Greater(t, len(data), 0, "data must be non-empty")

	// Get the current offset from the backend.
	headResp, status, err := HeadUploadData(id)
	require.NoError(t, err, "HEAD /uploads/{id}/data should not error")
	require.Equal(t, http.StatusOK, status,
		"HEAD /uploads/{id}/data should return 200 for uploading upload")

	currentOffset := headResp.UploadOffset

	// PATCH the chunk without Upload-Length (partial upload, not final).
	patchResp, status, err := PatchUploadData(id, bytes.NewReader(data),
		currentOffset, "")
	require.NoError(t, err, "PATCH /uploads/{id}/data should not error")
	require.Equal(t, http.StatusNoContent, status,
		"PATCH /uploads/{id}/data should return 204 for partial write")
	require.Equal(t, currentOffset+int64(len(data)), patchResp.UploadOffset,
		"Upload-Offset should advance by exactly the chunk size")

	return patchResp.UploadOffset
}

// ---------------------------------------------------------------------------
// Fixture: Deletion and status helpers
// ---------------------------------------------------------------------------

// MustDeleteUpload deletes the upload with the given ID via
// DELETE /uploads/{id} and asserts a 204 No Content response. It is safe to
// call multiple times on the same ID (idempotent).
func MustDeleteUpload(t testing.TB, id string) {
	t.Helper()

	require.NotEmpty(t, id, "upload ID must not be empty")

	statusCode, err := DeleteUpload(id)
	require.NoError(t, err, "DELETE /uploads/{id} should not error")
	require.Equal(t, http.StatusNoContent, statusCode,
		"DELETE /uploads/{id} should return 204")
}

// ---------------------------------------------------------------------------
// Fixture: Query helpers
// ---------------------------------------------------------------------------

// ListRunUploads is a convenience wrapper around ListUploads that
// automatically injects the FixtureTimeRange scope so the result contains
// only uploads created during this test run. Additional filters (status,
// limit, cursor) can be passed as non-zero values.
//
// This is the preferred way to list uploads in e2e tests because it avoids
// flaky results from other runs or test data.
//
// Example:
//
//	list := ListRunUploads(t, "", 0, "")
//	require.Len(t, list.Items, expectedCount)
func ListRunUploads(t testing.TB, status string, limit int, cursor string) *ListUploadsResponse {
	t.Helper()

	from, to := FixtureTimeRange()

	list, err := ListUploads(from, to, status, limit, cursor)
	require.NoError(t, err, "GET /uploads should not error")
	require.NotNil(t, list, "list response must not be nil")
	require.NotNil(t, list.Items, "list items must not be nil (even if empty)")

	return list
}

// ---------------------------------------------------------------------------
// Helpers: ID computation
// ---------------------------------------------------------------------------

// SafeIDFromLocal computes the deterministic safe server ID for a given
// localIdentifier using the same algorithm as the server:
// SHA-256(localIdentifier) → raw URL-safe base64 encoding (43 characters).
//
// Tests use this to verify that POST /uploads returns the expected ID.
func SafeIDFromLocal(localIdentifier string) string {
	h := sha256.Sum256([]byte(localIdentifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}
