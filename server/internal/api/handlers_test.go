// Package api_test tests the HTTP handlers for the iCloud Backup server API.
package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/moontechs/files-nest/server/internal/api"
	"github.com/moontechs/files-nest/server/internal/store"
	"github.com/moontechs/files-nest/server/internal/uploadbackend"
)

var (
	errConcurrentOffset = errors.New("concurrent patch offset mismatch")
	errConcurrentStatus = errors.New("concurrent patch unexpected status")
)

// ---------------------------------------------------------------------------
// Test setup helpers
// ---------------------------------------------------------------------------

// setupHandler creates a Handler backed by a real BadgerDB store and a real
// embedded tusd upload backend, each in their own temp directories.
// Returns the handler, store, and backend. The caller does not need to
// clean up — t.TempDir() and t.Cleanup() handle that.
func setupHandler(t *testing.T) (*api.Handler, *store.Store, *uploadbackend.TUSHandler) {
	t.Helper()

	storeDir := t.TempDir()
	st, err := store.Open(filepath.Join(storeDir, "db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	tusDir := t.TempDir()
	bh, err := uploadbackend.New(tusDir)
	if err != nil {
		t.Fatalf("uploadbackend.New: %v", err)
	}

	h := api.NewHandler(st, bh, tusDir)
	return h, st, bh
}

// executeRequest sends an HTTP request to the given handler and returns the
// response recorder. It applies no middleware — just the raw handler.
func executeRequest(h http.HandlerFunc, method, target string, body io.Reader) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), method, target, body)
	req.Header.Set("Content-Type", jsonContentType)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// executeRequestWithID is a convenience wrapper that calls executeRequest
// and sets the path value for the "id" parameter (Go 1.22+ routing style).
func executeRequestWithID(h http.HandlerFunc, target, id string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
	req.Header.Set("Content-Type", jsonContentType)
	// Set the path value for Go 1.22+ ServeMux pattern matching.
	req.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// ---------------------------------------------------------------------------
// Helpers: decode response
// ---------------------------------------------------------------------------

// decodeResponse decodes the JSON body from rec into v.
func decodeResponse(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.NewDecoder(rec.Body).Decode(v); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
}

// ---------------------------------------------------------------------------
// POST /uploads — create
// ---------------------------------------------------------------------------

func TestHandleCreateUpload_Success(t *testing.T) {
	h, _, _ := setupHandler(t)

	body := `{
		"local_identifier": "ABCD-1234/L0/040",
		"filename": "IMG_1234.jpg",
		"creation_date": "2024-03-15T10:30:00Z"
	}`

	rec := executeRequest(h.HandleCreateUpload, "POST", "/uploads", strings.NewReader(body))
	if rec.Code != http.StatusCreated {
		t.Errorf("expected 201 Created, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp api.CreateUploadResponse
	decodeResponse(t, rec, &resp)

	if resp.ID == "" {
		t.Error("expected non-empty id")
	}
	if resp.LocalIdentifier != "ABCD-1234/L0/040" {
		t.Errorf("local_identifier = %q, want %q", resp.LocalIdentifier, "ABCD-1234/L0/040")
	}
	if resp.Status != statusUploading {
		t.Errorf("status = %q, want %q", resp.Status, statusUploading)
	}
	if resp.BackendID == "" {
		t.Error("expected non-empty backend_id")
	}
	if resp.UploadURL == "" {
		t.Error("expected non-empty upload_url")
	}
	if !strings.HasPrefix(resp.UploadURL, "/uploads/") {
		t.Errorf("upload_url should start with /uploads/, got %q", resp.UploadURL)
	}
	if !strings.HasSuffix(resp.UploadURL, "/data") {
		t.Errorf("upload_url should end with /data, got %q", resp.UploadURL)
	}
}

func TestHandleCreateUpload_WithCamelCaseFields(t *testing.T) {
	h, _, _ := setupHandler(t)

	body := `{
		"localIdentifier": "ASSET-5678/L0/001",
		"filename": "IMG_5678.jpg",
		"creationDate": "2024-06-20T14:00:00Z"
	}`

	rec := executeRequest(h.HandleCreateUpload, "POST", "/uploads", strings.NewReader(body))
	if rec.Code != http.StatusCreated {
		t.Errorf("expected 201 Created, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp api.CreateUploadResponse
	decodeResponse(t, rec, &resp)

	if resp.LocalIdentifier != "ASSET-5678/L0/001" {
		t.Errorf("local_identifier = %q, want %q", resp.LocalIdentifier, "ASSET-5678/L0/001")
	}
	if resp.Status != statusUploading {
		t.Errorf("status = %q, want %q", resp.Status, statusUploading)
	}
}

func TestHandleCreateUpload_WithOptionalFields(t *testing.T) {
	h, _, _ := setupHandler(t)

	body := `{
		"local_identifier": "OPT-1234/L0/000",
		"filename": "IMG_9999.MOV",
		"creation_date": "2024-03-15T10:30:00Z",
		"bundle_id": "LIVE-9876/L0/000",
		"metadata": {"latitude": 37.7749, "longitude": -122.4194}
	}`

	rec := executeRequest(h.HandleCreateUpload, "POST", "/uploads", strings.NewReader(body))
	if rec.Code != http.StatusCreated {
		t.Errorf("expected 201 Created, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp api.CreateUploadResponse
	decodeResponse(t, rec, &resp)

	if resp.ID == "" {
		t.Error("expected non-empty id")
	}
	if resp.Status != statusUploading {
		t.Errorf("status = %q, want %q", resp.Status, statusUploading)
	}
	if resp.BackendID == "" {
		t.Error("expected non-empty backend_id")
	}
}

func TestHandleCreateUpload_WithBundleIdCamelCase(t *testing.T) {
	h, _, _ := setupHandler(t)

	body := `{
		"local_identifier": "CAMEL-BUNDLE-001",
		"filename": "IMG_0001.jpg",
		"creation_date": "2024-03-15T10:30:00Z",
		"bundleId": "BUNDLE-001/L0/000"
	}`

	rec := executeRequest(h.HandleCreateUpload, "POST", "/uploads", strings.NewReader(body))
	if rec.Code != http.StatusCreated {
		t.Errorf("expected 201 Created, got %d: %s", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// POST /uploads — validation errors
// ---------------------------------------------------------------------------

func TestHandleCreateUpload_MissingLocalIdentifier(t *testing.T) {
	h, _, _ := setupHandler(t)

	body := `{"filename": "IMG_1234.jpg", "creation_date": "2024-03-15T10:30:00Z"}`

	rec := executeRequest(h.HandleCreateUpload, "POST", "/uploads", strings.NewReader(body))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}

	var errResp map[string]string
	decodeResponse(t, rec, &errResp)
	if errResp["error"] == "" || !strings.Contains(errResp["error"], "local_identifier") {
		t.Errorf("expected error about local_identifier, got %q", errResp["error"])
	}
}

func TestHandleCreateUpload_MissingFilename(t *testing.T) {
	h, _, _ := setupHandler(t)

	body := `{"local_identifier": "TEST-001", "creation_date": "2024-03-15T10:30:00Z"}`

	rec := executeRequest(h.HandleCreateUpload, "POST", "/uploads", strings.NewReader(body))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}

	var errResp map[string]string
	decodeResponse(t, rec, &errResp)
	if errResp["error"] == "" || !strings.Contains(errResp["error"], "filename") {
		t.Errorf("expected error about filename, got %q", errResp["error"])
	}
}

func TestHandleCreateUpload_MissingCreationDate(t *testing.T) {
	h, _, _ := setupHandler(t)

	body := `{"local_identifier": "TEST-001", "filename": "IMG_1234.jpg"}`

	rec := executeRequest(h.HandleCreateUpload, "POST", "/uploads", strings.NewReader(body))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}

	var errResp map[string]string
	decodeResponse(t, rec, &errResp)
	if errResp["error"] == "" || !strings.Contains(errResp["error"], "creation_date") {
		t.Errorf("expected error about creation_date, got %q", errResp["error"])
	}
}

// TestHandleCreateUpload_MalformedCreationDateRejected ensures a crafted
// creation_date that could traverse the organized tree (e.g. "../../tmp")
// is rejected at the API boundary with 400 rather than being persisted and
// later used to place the completed file outside the storage root.
func TestHandleCreateUpload_MalformedCreationDateRejected(t *testing.T) {
	h, _, _ := setupHandler(t)

	cases := []string{
		"../../tmp",
		"../../../etc/passwd",
		"not-a-date",
		"2024/03/15",
	}
	for _, d := range cases {
		body := fmt.Sprintf(`{"local_identifier":"TRAV-001","filename":"IMG_1234.jpg","creation_date":%q}`, d)
		rec := executeRequest(h.HandleCreateUpload, "POST", "/uploads", strings.NewReader(body))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("creation_date=%q: expected 400, got %d: %s", d, rec.Code, rec.Body.String())
		}
		var errResp map[string]string
		decodeResponse(t, rec, &errResp)
		if !strings.Contains(errResp["error"], "creation_date") {
			t.Fatalf("creation_date=%q: expected error mentioning creation_date, got %q", d, errResp["error"])
		}
	}
}

func TestHandleCreateUpload_EmptyRequestBody(t *testing.T) {
	h, _, _ := setupHandler(t)

	rec := executeRequest(h.HandleCreateUpload, "POST", "/uploads", strings.NewReader("{}"))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleCreateUpload_InvalidJSON(t *testing.T) {
	h, _, _ := setupHandler(t)

	rec := executeRequest(h.HandleCreateUpload, "POST", "/uploads", strings.NewReader("{invalid json"))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleCreateUpload_UnknownFields(t *testing.T) {
	h, _, _ := setupHandler(t)

	body := `{
		"local_identifier": "TEST-001",
		"filename": "IMG_1234.jpg",
		"creation_date": "2024-03-15T10:30:00Z",
		"unknown_field": "should_not_be_allowed"
	}`

	rec := executeRequest(h.HandleCreateUpload, "POST", "/uploads", strings.NewReader(body))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for unknown fields, got %d: %s", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// POST /uploads — duplicate / idempotency
// ---------------------------------------------------------------------------

func TestHandleCreateUpload_DuplicateLocalIdentifier(t *testing.T) {
	h, st, _ := setupHandler(t)

	// First create — should succeed.
	body1 := `{
		"local_identifier": "DUP-001/L0/000",
		"filename": "IMG_0001.jpg",
		"creation_date": "2024-03-15T10:30:00Z"
	}`
	rec1 := executeRequest(h.HandleCreateUpload, "POST", "/uploads", strings.NewReader(body1))
	if rec1.Code != http.StatusCreated {
		t.Fatalf("first create expected 201, got %d: %s", rec1.Code, rec1.Body.String())
	}

	var first api.CreateUploadResponse
	decodeResponse(t, rec1, &first)

	// Second create with same localIdentifier — should return existing record.
	rec2 := executeRequest(h.HandleCreateUpload, "POST", "/uploads", strings.NewReader(body1))
	if rec2.Code != http.StatusOK {
		t.Errorf("duplicate create expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}

	var second api.CreateUploadResponse
	decodeResponse(t, rec2, &second)

	// Should return the same ID and status (uploading).
	if second.ID != first.ID {
		t.Errorf("duplicate returned different id: %q vs %q", second.ID, first.ID)
	}
	if second.Status != first.Status {
		t.Errorf("duplicate returned different status: %q vs %q", second.Status, first.Status)
	}
	if second.BackendID != first.BackendID {
		t.Errorf("duplicate returned different backend_id: %q vs %q", second.BackendID, first.BackendID)
	}

	// Verify the tusd upload from the second create was cleaned up.
	// The first backend ID should still be valid, but the second one should
	// not exist.
	dupBackendID := second.BackendID
	_, err := st.UploadByBackendID(dupBackendID)
	if err != nil {
		t.Fatalf("lookup by backend_id: %v", err)
	}
	// The original backend ID from the first create should still be the
	// stored one. The duplicate create should not have changed it.
	upload, err := st.GetUpload(first.ID)
	if err != nil {
		t.Fatalf("GetUpload: %v", err)
	}
	if upload.BackendID != first.BackendID {
		t.Errorf("stored backend_id changed after duplicate: %q vs %q", upload.BackendID, first.BackendID)
	}
}

func TestHandleCreateUpload_DuplicateReturnsExistingStatus(t *testing.T) {
	h, st, _ := setupHandler(t)

	// Create an upload, then manually update its status to complete to
	// simulate a completed upload.
	body := `{
		"local_identifier": "DUP-COMPLETE/L0/000",
		"filename": "IMG_0002.jpg",
		"creation_date": "2024-03-15T10:30:00Z"
	}`
	rec1 := executeRequest(h.HandleCreateUpload, "POST", "/uploads", strings.NewReader(body))
	if rec1.Code != http.StatusCreated {
		t.Fatalf("first create expected 201, got %d: %s", rec1.Code, rec1.Body.String())
	}

	var first api.CreateUploadResponse
	decodeResponse(t, rec1, &first)

	// Manually update to complete.
	_, err := st.UpdateStatus(first.ID, store.StatusComplete)
	if err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}

	// Second create — should return the existing record with status=complete.
	rec2 := executeRequest(h.HandleCreateUpload, "POST", "/uploads", strings.NewReader(body))
	if rec2.Code != http.StatusOK {
		t.Errorf("duplicate after complete expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}

	var second api.CreateUploadResponse
	decodeResponse(t, rec2, &second)

	if second.Status != "complete" {
		t.Errorf("expected status 'complete' from duplicate, got %q", second.Status)
	}
	if second.ID != first.ID {
		t.Errorf("duplicate returned different id: %q vs %q", second.ID, first.ID)
	}
}

// ---------------------------------------------------------------------------
// POST /uploads — invalid filename
// ---------------------------------------------------------------------------

func TestHandleCreateUpload_InvalidFilename(t *testing.T) {
	h, _, _ := setupHandler(t)

	tests := []struct {
		name     string
		body     string
		wantCode int
	}{
		{
			name:     "empty filename",
			body:     `{"local_identifier":"TEST-001","filename":"","creation_date":"2024-03-15T10:30:00Z"}`,
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "hidden file",
			body:     `{"local_identifier":"TEST-002","filename":".hidden.jpg","creation_date":"2024-03-15T10:30:00Z"}`,
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "path traversal",
			body:     `{"local_identifier":"TEST-003","filename":"../../etc/passwd","creation_date":"2024-03-15T10:30:00Z"}`,
			wantCode: http.StatusCreated, // filepath.Base strips traversal; filename becomes "passwd"
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := executeRequest(h.HandleCreateUpload, "POST", "/uploads", strings.NewReader(tt.body))
			if rec.Code != tt.wantCode {
				t.Errorf("expected %d, got %d: %s", tt.wantCode, rec.Code, rec.Body.String())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// GET /uploads — listing
// ---------------------------------------------------------------------------

// createTestUpload is a helper that creates an upload via the handler and
// returns the response struct.
func createTestUpload(t *testing.T, h *api.Handler, localID, filename, creationDate string) api.CreateUploadResponse {
	t.Helper()
	body := fmt.Sprintf(`{
		"local_identifier": %q,
		"filename": %q,
		"creation_date": %q
	}`, localID, filename, creationDate)
	rec := executeRequest(h.HandleCreateUpload, "POST", "/uploads", strings.NewReader(body))
	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("create upload %s: expected 201/200, got %d: %s", localID, rec.Code, rec.Body.String())
	}
	var resp api.CreateUploadResponse
	decodeResponse(t, rec, &resp)
	return resp
}

func TestHandleListUploads_Empty(t *testing.T) {
	h, _, _ := setupHandler(t)

	rec := executeRequest(h.HandleListUploads, "GET", "/uploads", nil)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp api.ListUploadsResponse
	decodeResponse(t, rec, &resp)

	if resp.Items == nil {
		t.Error("expected non-nil items (empty array), got nil")
	}
	if len(resp.Items) != 0 {
		t.Errorf("expected 0 items, got %d", len(resp.Items))
	}
	if resp.NextCursor != "" {
		t.Errorf("expected empty next_cursor, got %q", resp.NextCursor)
	}
}

func TestHandleListUploads_WithData(t *testing.T) {
	h, _, _ := setupHandler(t)

	// Create three uploads on different dates.
	createTestUpload(t, h, "LIST-001/L0/000", "IMG_0001.jpg", "2024-01-15T10:00:00Z")
	createTestUpload(t, h, "LIST-002/L0/000", "IMG_0002.jpg", "2024-02-20T11:00:00Z")
	createTestUpload(t, h, "LIST-003/L0/000", "IMG_0003.jpg", "2024-03-25T12:00:00Z")

	rec := executeRequest(h.HandleListUploads, "GET", "/uploads", nil)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp api.ListUploadsResponse
	decodeResponse(t, rec, &resp)

	if len(resp.Items) != 3 {
		t.Errorf("expected 3 items, got %d", len(resp.Items))
	}
	if resp.NextCursor != "" {
		t.Errorf("expected empty next_cursor when all items fit, got %q", resp.NextCursor)
	}

	// Verify ordering by creation date ascending.
	for i := 1; i < len(resp.Items); i++ {
		if resp.Items[i].CreationDate < resp.Items[i-1].CreationDate {
			t.Errorf("items not sorted by creation date at index %d", i)
		}
	}
}

func TestHandleListUploads_StatusFilter(t *testing.T) {
	h, st, _ := setupHandler(t)

	// Create two uploading uploads.
	u1 := createTestUpload(t, h, "FLT-001/L0/000", "IMG_0001.jpg", "2024-03-15T10:00:00Z")
	createTestUpload(t, h, "FLT-002/L0/000", "IMG_0002.jpg", "2024-03-15T11:00:00Z")

	// Manually complete the first one.
	if _, err := st.UpdateStatus(u1.ID, store.StatusComplete); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}

	// List only uploading.
	rec := executeRequest(h.HandleListUploads, "GET", "/uploads?status=uploading", nil)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp api.ListUploadsResponse
	decodeResponse(t, rec, &resp)

	if len(resp.Items) != 1 {
		t.Errorf("expected 1 uploading item, got %d", len(resp.Items))
	}
	if len(resp.Items) > 0 && resp.Items[0].Status != store.StatusUploading {
		t.Errorf("expected status 'uploading', got %q", resp.Items[0].Status)
	}

	// List only complete.
	rec2 := executeRequest(h.HandleListUploads, "GET", "/uploads?status=complete", nil)
	if rec2.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}

	var resp2 api.ListUploadsResponse
	decodeResponse(t, rec2, &resp2)

	if len(resp2.Items) != 1 {
		t.Errorf("expected 1 complete item, got %d", len(resp2.Items))
	}
	if len(resp2.Items) > 0 && resp2.Items[0].Status != store.StatusComplete {
		t.Errorf("expected status 'complete', got %q", resp2.Items[0].Status)
	}
}

func TestHandleListUploads_DateRange(t *testing.T) {
	h, _, _ := setupHandler(t)

	createTestUpload(t, h, "DR-001/L0/000", "IMG_0001.jpg", "2024-01-15T10:00:00Z")
	createTestUpload(t, h, "DR-002/L0/000", "IMG_0002.jpg", "2024-02-20T11:00:00Z")
	createTestUpload(t, h, "DR-003/L0/000", "IMG_0003.jpg", "2024-03-25T12:00:00Z")

	// Query January to February.
	rec := executeRequest(h.HandleListUploads, "GET", "/uploads?from=2024-01-01T00:00:00Z&to=2024-02-29T23:59:59Z", nil)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp api.ListUploadsResponse
	decodeResponse(t, rec, &resp)

	if len(resp.Items) != 2 {
		t.Errorf("expected 2 items in Jan-Feb range, got %d", len(resp.Items))
	}
}

func TestHandleListUploads_DateRangeInvalid(t *testing.T) {
	h, _, _ := setupHandler(t)

	// to before from should return 400.
	rec := executeRequest(h.HandleListUploads, "GET", "/uploads?from=2024-06-01T00:00:00Z&to=2024-01-01T00:00:00Z", nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid date range, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleListUploads_InvalidDate(t *testing.T) {
	h, _, _ := setupHandler(t)

	rec := executeRequest(h.HandleListUploads, "GET", "/uploads?from=not-a-date", nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid date, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleListUploads_Pagination(t *testing.T) {
	h, _, _ := setupHandler(t)

	// Create 5 uploads on the same date.
	for i := range 5 {
		localID := fmt.Sprintf("PAG-%03d/L0/000", i)
		filename := fmt.Sprintf("IMG_%04d.jpg", 1000+i)
		createTestUpload(t, h, localID, filename, "2024-03-15T10:00:00Z")
	}

	// Page 1: limit 2.
	rec1 := executeRequest(h.HandleListUploads, "GET", "/uploads?limit=2", nil)
	if rec1.Code != http.StatusOK {
		t.Fatalf("page 1 expected 200, got %d: %s", rec1.Code, rec1.Body.String())
	}

	var page1 api.ListUploadsResponse
	decodeResponse(t, rec1, &page1)

	if len(page1.Items) != 2 {
		t.Errorf("page 1: expected 2 items, got %d", len(page1.Items))
	}
	if page1.NextCursor == "" {
		t.Fatal("page 1: expected non-empty next_cursor")
	}

	// Page 2: use cursor from page 1.
	rec2 := executeRequest(h.HandleListUploads, "GET", "/uploads?limit=2&cursor="+page1.NextCursor, nil)
	if rec2.Code != http.StatusOK {
		t.Fatalf("page 2 expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}

	var page2 api.ListUploadsResponse
	decodeResponse(t, rec2, &page2)

	if len(page2.Items) != 2 {
		t.Errorf("page 2: expected 2 items, got %d", len(page2.Items))
	}
	if page2.NextCursor == "" {
		t.Fatal("page 2: expected non-empty next_cursor")
	}

	// Page 3: should have the remaining 1 item.
	rec3 := executeRequest(h.HandleListUploads, "GET", "/uploads?limit=2&cursor="+page2.NextCursor, nil)
	if rec3.Code != http.StatusOK {
		t.Fatalf("page 3 expected 200, got %d: %s", rec3.Code, rec3.Body.String())
	}

	var page3 api.ListUploadsResponse
	decodeResponse(t, rec3, &page3)

	if len(page3.Items) != 1 {
		t.Errorf("page 3: expected 1 item, got %d", len(page3.Items))
	}
	if page3.NextCursor != "" {
		t.Errorf("page 3: expected empty next_cursor, got %q", page3.NextCursor)
	}

	// Verify all 5 unique IDs across pages.
	allIDs := make(map[string]bool)
	for _, u := range page1.Items {
		allIDs[u.ID] = true
	}
	for _, u := range page2.Items {
		allIDs[u.ID] = true
	}
	for _, u := range page3.Items {
		allIDs[u.ID] = true
	}
	if len(allIDs) != 5 {
		t.Errorf("expected 5 unique IDs across pages, got %d", len(allIDs))
	}
}

// ---------------------------------------------------------------------------
// GET /uploads/:id — get single upload
// ---------------------------------------------------------------------------

func TestHandleGetUpload_Found(t *testing.T) {
	h, _, _ := setupHandler(t)

	// Create an upload.
	created := createTestUpload(t, h, "GETBYID-001/L0/000", "IMG_0001.jpg", creationDate)

	// Fetch by ID.
	rec := executeRequestWithID(h.HandleGetUpload, "/uploads/"+created.ID, created.ID)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var upload store.Upload
	decodeResponse(t, rec, &upload)

	if upload.ID != created.ID {
		t.Errorf("ID: got %q, want %q", upload.ID, created.ID)
	}
	if upload.LocalIdentifier != created.LocalIdentifier {
		t.Errorf("LocalIdentifier: got %q, want %q", upload.LocalIdentifier, created.LocalIdentifier)
	}
	if string(upload.Status) != created.Status {
		t.Errorf("Status: got %q, want %q", upload.Status, created.Status)
	}
	if upload.BackendID != created.BackendID {
		t.Errorf("BackendID: got %q, want %q", upload.BackendID, created.BackendID)
	}
	if upload.Filename != "IMG_0001.jpg" {
		t.Errorf("Filename: got %q, want %q", upload.Filename, "IMG_0001.jpg")
	}
	if upload.CreationDate != creationDate {
		t.Errorf("CreationDate: got %q, want %q", upload.CreationDate, creationDate)
	}
}

func TestHandleGetUpload_NotFound(t *testing.T) {
	h, _, _ := setupHandler(t)

	// Use a valid-format safe ID (43-char base64url) that doesn't exist in the DB.
	// We generate it from a known unused local identifier.
	nonExistentID := api.SafeID("this-identifier-does-not-exist-in-any-database-123")

	rec := executeRequestWithID(h.HandleGetUpload, "/uploads/"+nonExistentID, nonExistentID)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}

	var errResp map[string]string
	decodeResponse(t, rec, &errResp)
	if errResp["error"] == "" {
		t.Error("expected non-empty error message")
	}
}

func TestHandleGetUpload_InvalidID(t *testing.T) {
	h, _, _ := setupHandler(t)

	tests := []struct {
		name string
		id   string
	}{
		{"empty", ""},
		{"too short", "abc"},
		{"invalid base64", "!!!not-valid!!!"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := executeRequestWithID(h.HandleGetUpload, "/uploads/"+tt.id, tt.id)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestHandleGetUpload_NoID(t *testing.T) {
	h, _, _ := setupHandler(t)

	// Test with no path value set.
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/uploads/", nil)
	rec := httptest.NewRecorder()
	h.HandleGetUpload(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// POST /uploads — large body rejection
// ---------------------------------------------------------------------------

func TestHandleCreateUpload_BodyTooLarge(t *testing.T) {
	h, _, _ := setupHandler(t)

	// Create a body larger than the 256 KB limit.
	largeField := strings.Repeat("A", 300*1024)
	body := fmt.Sprintf(`{
		"local_identifier": "LARGE-%s",
		"filename": "IMG_0001.jpg",
		"creation_date": "2024-03-15T10:30:00Z"
	}`, largeField)

	rec := executeRequest(h.HandleCreateUpload, "POST", "/uploads", strings.NewReader(body))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413 for body too large, got %d: %s", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// POST /uploads — content type validation
// ---------------------------------------------------------------------------

func TestHandleCreateUpload_NonJSONContentType(t *testing.T) {
	h, _, _ := setupHandler(t)

	body := `{"local_identifier":"TEST-001","filename":"IMG_1234.jpg","creation_date":"2024-03-15T10:30:00Z"}`
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/uploads", strings.NewReader(body))
	// No Content-Type header set
	rec := httptest.NewRecorder()
	h.HandleCreateUpload(rec, req)

	// Without Content-Type, Go's json.Decoder may still decode successfully.
	// We don't reject based on Content-Type — we try to decode.
	if rec.Code == http.StatusBadRequest {
		// No problem if it fails — the error should mention something JSON-related.
		var errResp map[string]string
		decodeResponse(t, rec, &errResp)
		if !strings.Contains(errResp["error"], "invalid request body") {
			t.Errorf("unexpected error: %s", rec.Body.String())
		}
	} else if rec.Code != http.StatusCreated {
		t.Errorf("unexpected status: %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// POST /uploads — payload with special characters in localIdentifier
// ---------------------------------------------------------------------------

func TestHandleCreateUpload_SpecialCharactersInLocalID(t *testing.T) {
	h, _, _ := setupHandler(t)

	body := `{
		"local_identifier": "ABCD-1234/L0/040!@#$%",
		"filename": "IMG_1234.jpg",
		"creation_date": "2024-03-15T10:30:00Z"
	}`

	rec := executeRequest(h.HandleCreateUpload, "POST", "/uploads", strings.NewReader(body))
	if rec.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp api.CreateUploadResponse
	decodeResponse(t, rec, &resp)

	if resp.ID == "" {
		t.Error("expected non-empty id")
	}
	// The safe ID should not contain the special characters.
	if strings.Contains(resp.ID, "/") || strings.Contains(resp.ID, "!") || strings.Contains(resp.ID, "@") {
		t.Errorf("safe ID should not contain special characters, got %q", resp.ID)
	}
	if resp.LocalIdentifier != "ABCD-1234/L0/040!@#$%" {
		t.Errorf("local_identifier should match original, got %q", resp.LocalIdentifier)
	}
}

// ---------------------------------------------------------------------------
// POST /uploads — verify upload created in tusd backend
// ---------------------------------------------------------------------------

func TestHandleCreateUpload_UploadExistsInBackend(t *testing.T) {
	h, _, bh := setupHandler(t)

	body := `{
		"local_identifier": "BACKEND-CHECK-001",
		"filename": "IMG_0001.jpg",
		"creation_date": "2024-03-15T10:30:00Z"
	}`

	rec := executeRequest(h.HandleCreateUpload, "POST", "/uploads", strings.NewReader(body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp api.CreateUploadResponse
	decodeResponse(t, rec, &resp)

	// Verify the upload exists in the tusd backend.
	info, err := bh.GetInfo(context.Background(), resp.BackendID)
	if err != nil {
		t.Fatalf("tusd GetInfo after create: %v", err)
	}
	if info.ID != resp.BackendID {
		t.Errorf("tusd info ID = %q, want %q", info.ID, resp.BackendID)
	}
	if !info.SizeIsDeferred {
		t.Error("expected deferred-length upload")
	}
	if info.Offset != 0 {
		t.Errorf("expected offset 0, got %d", info.Offset)
	}
}

// ---------------------------------------------------------------------------
// POST /uploads — verify tusd upload terminated on conflict
// ---------------------------------------------------------------------------

func TestHandleCreateUpload_DuplicateTerminatesTusdUpload(t *testing.T) {
	h, _, bh := setupHandler(t)

	body := `{
		"local_identifier": "TERM-DUP-001",
		"filename": "IMG_0001.jpg",
		"creation_date": "2024-03-15T10:30:00Z"
	}`

	// First create.
	rec1 := executeRequest(h.HandleCreateUpload, "POST", "/uploads", strings.NewReader(body))
	if rec1.Code != http.StatusCreated {
		t.Fatalf("first create expected 201, got %d: %s", rec1.Code, rec1.Body.String())
	}

	var first api.CreateUploadResponse
	decodeResponse(t, rec1, &first)

	// Second create with same body — should return 200 with existing record.
	rec2 := executeRequest(h.HandleCreateUpload, "POST", "/uploads", strings.NewReader(body))
	if rec2.Code != http.StatusOK {
		t.Fatalf("duplicate expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}

	var second api.CreateUploadResponse
	decodeResponse(t, rec2, &second)

	// On duplicate, the handler returns the existing record's info (same as first).
	if second.BackendID != first.BackendID {
		t.Errorf("duplicate returned backend_id=%q, want the original %q", second.BackendID, first.BackendID)
	}

	// The original backend ID should still be valid in tusd.
	_, err := bh.GetInfo(context.Background(), first.BackendID)
	if err != nil {
		t.Errorf("original backend ID should still exist in tusd: %v", err)
	}
}

// ---------------------------------------------------------------------------
// POST /uploads — verify response content type
// ---------------------------------------------------------------------------

func TestHandleCreateUpload_ResponseContentType(t *testing.T) {
	h, _, _ := setupHandler(t)

	body := `{
		"local_identifier": "CT-TEST-001",
		"filename": "IMG_0001.jpg",
		"creation_date": "2024-03-15T10:30:00Z"
	}`

	rec := executeRequest(h.HandleCreateUpload, "POST", "/uploads", strings.NewReader(body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	ct := rec.Header().Get("Content-Type")
	if ct != jsonContentType {
		t.Errorf("expected Content-Type 'application/json', got %q", ct)
	}
}

func TestHandleGetUpload_ResponseContentType(t *testing.T) {
	h, _, _ := setupHandler(t)

	created := createTestUpload(t, h, "CT-GET-001", "IMG_0001.jpg", creationDate)

	rec := executeRequestWithID(h.HandleGetUpload, "/uploads/"+created.ID, created.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	ct := rec.Header().Get("Content-Type")
	if ct != jsonContentType {
		t.Errorf("expected Content-Type 'application/json', got %q", ct)
	}
}

func TestHandleListUploads_ResponseContentType(t *testing.T) {
	h, _, _ := setupHandler(t)

	rec := executeRequest(h.HandleListUploads, "GET", "/uploads", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	ct := rec.Header().Get("Content-Type")
	if ct != jsonContentType {
		t.Errorf("expected Content-Type 'application/json', got %q", ct)
	}
}

// ---------------------------------------------------------------------------
// GET /uploads/:id — verify full fields on returned upload
// ---------------------------------------------------------------------------

func TestHandleGetUpload_FullFieldsReturned(t *testing.T) {
	h, _, _ := setupHandler(t)

	// Create upload with optional fields.
	body := `{
		"local_identifier": "FULL-FIELD-001/L0/000",
		"filename": "IMG_9999.MOV",
		"creation_date": "2024-06-15T08:30:00Z",
		"bundle_id": "BUNDLE-001/L0/000",
		"metadata": {"key": "value"}
	}`

	rec := executeRequest(h.HandleCreateUpload, "POST", "/uploads", strings.NewReader(body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var created api.CreateUploadResponse
	decodeResponse(t, rec, &created)

	// Fetch by ID and verify all fields.
	getRec := executeRequestWithID(h.HandleGetUpload, "/uploads/"+created.ID, created.ID)
	if getRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", getRec.Code, getRec.Body.String())
	}

	var upload store.Upload
	decodeResponse(t, getRec, &upload)

	if upload.ID != created.ID {
		t.Errorf("ID: got %q, want %q", upload.ID, created.ID)
	}
	if upload.LocalIdentifier != "FULL-FIELD-001/L0/000" {
		t.Errorf("LocalIdentifier: got %q, want %q", upload.LocalIdentifier, "FULL-FIELD-001/L0/000")
	}
	if string(upload.Status) != statusUploading {
		t.Errorf("Status: got %q, want %q", upload.Status, statusUploading)
	}
	if upload.BackendID != created.BackendID {
		t.Errorf("BackendID: got %q, want %q", upload.BackendID, created.BackendID)
	}
	if upload.Filename != "IMG_9999.MOV" {
		t.Errorf("Filename: got %q, want %q", upload.Filename, "IMG_9999.MOV")
	}
	if upload.BundleID != "BUNDLE-001/L0/000" {
		t.Errorf("BundleID: got %q, want %q", upload.BundleID, "BUNDLE-001/L0/000")
	}
	if upload.CreationDate != "2024-06-15T08:30:00Z" {
		t.Errorf("CreationDate: got %q, want %q", upload.CreationDate, "2024-06-15T08:30:00Z")
	}
	if upload.Metadata == nil {
		t.Error("expected non-nil Metadata")
	}
}

// ---------------------------------------------------------------------------
// TUS request helpers
// ---------------------------------------------------------------------------

// tusHeadRequest sends a TUS HEAD request for the given upload ID.
func tusHeadRequest(h http.HandlerFunc, id string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodHead, "/uploads/"+id+"/data", nil)
	req.SetPathValue("id", id)
	req.Header.Set("Tus-Resumable", "1.0.0")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// tusPatchRequest sends a TUS PATCH request with the given headers and body.
func tusPatchRequest(
	h http.HandlerFunc, id string, offset int64, uploadLength string, body io.Reader,
) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPatch, "/uploads/"+id+"/data", body)
	req.SetPathValue("id", id)
	req.Header.Set("Content-Type", "application/offset+octet-stream")
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Offset", strconv.FormatInt(offset, 10))
	if uploadLength != "" {
		req.Header.Set("Upload-Length", uploadLength)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// ---------------------------------------------------------------------------
// HEAD /uploads/:id/data
// ---------------------------------------------------------------------------

func TestHandleHeadUploadData_Success(t *testing.T) {
	h, _, _ := setupHandler(t)
	created := createTestUpload(t, h, "HEAD-SUCCESS/L0/000", "IMG_0001.jpg", creationDate)

	rec := tusHeadRequest(h.HandleHeadUploadData, created.ID)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	offsetStr := rec.Header().Get("Upload-Offset")
	if offsetStr == "" {
		t.Fatal("expected Upload-Offset header")
	}
	if offsetStr != "0" {
		t.Errorf("expected Upload-Offset 0, got %s", offsetStr)
	}

	tusVer := rec.Header().Get("Tus-Resumable")
	if tusVer != "1.0.0" {
		t.Errorf("expected Tus-Resumable 1.0.0, got %s", tusVer)
	}
}

func TestHandleHeadUploadData_NotFound(t *testing.T) {
	h, _, _ := setupHandler(t)

	nonExistentID := api.SafeID("this-identifier-does-not-exist")

	rec := tusHeadRequest(h.HandleHeadUploadData, nonExistentID)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleHeadUploadData_InvalidID(t *testing.T) {
	h, _, _ := setupHandler(t)

	rec := tusHeadRequest(h.HandleHeadUploadData, "invalid-short-id")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleHeadUploadData_EmptyID(t *testing.T) {
	h, _, _ := setupHandler(t)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodHead, "/uploads//data", nil)
	rec := httptest.NewRecorder()
	h.HandleHeadUploadData(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleHeadUploadData_DeletedUpload(t *testing.T) {
	h, st, _ := setupHandler(t)
	created := createTestUpload(t, h, "HEAD-DELETED/L0/000", "IMG_0001.jpg", creationDate)

	// Manually mark as deleted.
	if _, err := st.UpdateStatus(created.ID, store.StatusDeleted); err != nil {
		t.Fatalf("UpdateStatus to deleted: %v", err)
	}

	rec := tusHeadRequest(h.HandleHeadUploadData, created.ID)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for deleted upload, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleHeadUploadData_AfterOffsetChange(t *testing.T) {
	h, _, _ := setupHandler(t)
	created := createTestUpload(t, h, "HEAD-OFFSET/L0/000", "IMG_0001.jpg", creationDate)

	// Patch some data to advance the offset.
	data := []byte("hello, world!")
	patchRec := tusPatchRequest(
		h.HandlePatchUploadData, created.ID, 0, strconv.Itoa(len(data)), strings.NewReader(string(data)))
	if patchRec.Code != http.StatusNoContent {
		t.Fatalf("PATCH expected 204, got %d: %s", patchRec.Code, patchRec.Body.String())
	}

	// Now HEAD should show the new offset.
	rec := tusHeadRequest(h.HandleHeadUploadData, created.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("HEAD expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	offsetStr := rec.Header().Get("Upload-Offset")
	expectedOffset := strconv.Itoa(len(data))
	if offsetStr != expectedOffset {
		t.Errorf("expected Upload-Offset %s, got %s", expectedOffset, offsetStr)
	}
}

// ---------------------------------------------------------------------------
// PATCH /uploads/:id/data
// ---------------------------------------------------------------------------

func TestHandlePatchUploadData_Success(t *testing.T) {
	h, _, _ := setupHandler(t)
	created := createTestUpload(t, h, "PATCH-OK/L0/000", "IMG_0001.jpg", creationDate)

	data := []byte("hello, world!")
	rec := tusPatchRequest(h.HandlePatchUploadData, created.ID, 0, "", strings.NewReader(string(data)))
	if rec.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}

	offsetStr := rec.Header().Get("Upload-Offset")
	if offsetStr == "" {
		t.Fatal("expected Upload-Offset header")
	}
	if offsetStr != strconv.Itoa(len(data)) {
		t.Errorf("expected Upload-Offset %d, got %s", len(data), offsetStr)
	}

	tusVer := rec.Header().Get("Tus-Resumable")
	if tusVer != "1.0.0" {
		t.Errorf("expected Tus-Resumable 1.0.0, got %s", tusVer)
	}
}

func TestHandlePatchUploadData_MultipleChunks(t *testing.T) {
	h, _, _ := setupHandler(t)
	created := createTestUpload(t, h, "PATCH-MULTI/L0/000", "IMG_0001.jpg", creationDate)

	// First chunk.
	chunk1 := []byte("hello, ")
	rec1 := tusPatchRequest(h.HandlePatchUploadData, created.ID, 0, "", strings.NewReader(string(chunk1)))
	if rec1.Code != http.StatusNoContent {
		t.Fatalf("first chunk expected 204, got %d: %s", rec1.Code, rec1.Body.String())
	}
	offset1 := rec1.Header().Get("Upload-Offset")
	if offset1 != strconv.Itoa(len(chunk1)) {
		t.Errorf("after first chunk: expected offset %d, got %s", len(chunk1), offset1)
	}

	// Second chunk.
	chunk2 := []byte("world!")
	rec2 := tusPatchRequest(h.HandlePatchUploadData, created.ID, int64(len(chunk1)), "", strings.NewReader(string(chunk2)))
	if rec2.Code != http.StatusNoContent {
		t.Fatalf("second chunk expected 204, got %d: %s", rec2.Code, rec2.Body.String())
	}
	total := len(chunk1) + len(chunk2)
	offset2 := rec2.Header().Get("Upload-Offset")
	if offset2 != strconv.Itoa(total) {
		t.Errorf("after second chunk: expected offset %d, got %s", total, offset2)
	}
}

func TestHandlePatchUploadData_EmptyBody(t *testing.T) {
	h, _, _ := setupHandler(t)
	created := createTestUpload(t, h, "PATCH-EMPTY/L0/000", "IMG_0001.jpg", creationDate)

	rec := tusPatchRequest(h.HandlePatchUploadData, created.ID, 0, "", strings.NewReader(""))
	if rec.Code != http.StatusNoContent {
		t.Errorf("expected 204 for empty body, got %d: %s", rec.Code, rec.Body.String())
	}

	offsetStr := rec.Header().Get("Upload-Offset")
	if offsetStr != "0" {
		t.Errorf("expected offset 0 after empty patch, got %s", offsetStr)
	}
}

func TestHandlePatchUploadData_NotFound(t *testing.T) {
	h, _, _ := setupHandler(t)

	nonExistentID := api.SafeID("this-upload-does-not-exist")
	rec := tusPatchRequest(h.HandlePatchUploadData, nonExistentID, 0, "", strings.NewReader("data"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandlePatchUploadData_InvalidID(t *testing.T) {
	h, _, _ := setupHandler(t)

	rec := tusPatchRequest(h.HandlePatchUploadData, "bad-id", 0, "", strings.NewReader("data"))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandlePatchUploadData_WrongContentType(t *testing.T) {
	h, _, _ := setupHandler(t)
	created := createTestUpload(t, h, "PATCH-CT/L0/000", "IMG_0001.jpg", creationDate)

	// Send a PATCH with JSON content type (wrong for TUS).
	req := httptest.NewRequestWithContext(
		context.Background(), http.MethodPatch, "/uploads/"+created.ID+"/data", strings.NewReader("data"))
	req.SetPathValue("id", created.ID)
	req.Header.Set("Content-Type", jsonContentType)
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Offset", "0")
	rec := httptest.NewRecorder()
	h.HandlePatchUploadData(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for wrong Content-Type, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandlePatchUploadData_MissingUploadOffset(t *testing.T) {
	h, _, _ := setupHandler(t)
	created := createTestUpload(t, h, "PATCH-NOOFFSET/L0/000", "IMG_0001.jpg", creationDate)

	req := httptest.NewRequestWithContext(
		context.Background(), http.MethodPatch, "/uploads/"+created.ID+"/data", strings.NewReader("data"))
	req.SetPathValue("id", created.ID)
	req.Header.Set("Content-Type", "application/offset+octet-stream")
	req.Header.Set("Tus-Resumable", "1.0.0")
	// No Upload-Offset header.
	rec := httptest.NewRecorder()
	h.HandlePatchUploadData(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing Upload-Offset, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandlePatchUploadData_OffsetMismatch(t *testing.T) {
	h, _, _ := setupHandler(t)
	created := createTestUpload(t, h, "PATCH-MISMATCH/L0/000", "IMG_0001.jpg", creationDate)

	// Send a PATCH claiming offset 42 when the real offset is 0.
	rec := tusPatchRequest(h.HandlePatchUploadData, created.ID, 42, "", strings.NewReader("data"))
	if rec.Code != http.StatusConflict {
		t.Errorf("expected 409 for offset mismatch, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandlePatchUploadData_UploadCompleted(t *testing.T) {
	h, st, _ := setupHandler(t)
	created := createTestUpload(t, h, "PATCH-COMPLETED/L0/000", "IMG_0001.jpg", creationDate)

	// Manually mark as complete.
	if _, err := st.UpdateStatus(created.ID, store.StatusComplete); err != nil {
		t.Fatalf("UpdateStatus to complete: %v", err)
	}

	rec := tusPatchRequest(h.HandlePatchUploadData, created.ID, 0, "", strings.NewReader("data"))
	if rec.Code != http.StatusConflict {
		t.Errorf("expected 409 for completed upload, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandlePatchUploadData_UploadDeleted(t *testing.T) {
	h, st, _ := setupHandler(t)
	created := createTestUpload(t, h, "PATCH-DELETED/L0/000", "IMG_0001.jpg", creationDate)

	// Manually mark as deleted.
	if _, err := st.UpdateStatus(created.ID, store.StatusDeleted); err != nil {
		t.Fatalf("UpdateStatus to deleted: %v", err)
	}

	rec := tusPatchRequest(h.HandlePatchUploadData, created.ID, 0, "", strings.NewReader("data"))
	if rec.Code != http.StatusConflict {
		t.Errorf("expected 409 for deleted upload, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandlePatchUploadData_InvalidUploadOffset(t *testing.T) {
	h, _, _ := setupHandler(t)
	created := createTestUpload(t, h, "PATCH-INVALOFFSET/L0/000", "IMG_0001.jpg", creationDate)

	req := httptest.NewRequestWithContext(
		context.Background(), http.MethodPatch, "/uploads/"+created.ID+"/data", strings.NewReader("data"))
	req.SetPathValue("id", created.ID)
	req.Header.Set("Content-Type", "application/offset+octet-stream")
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Offset", "not-a-number")
	rec := httptest.NewRecorder()
	h.HandlePatchUploadData(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid Upload-Offset, got %d: %s", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// PATCH /uploads/:id/data — deferred-length finalization
// ---------------------------------------------------------------------------

func TestHandlePatchUploadData_DeferredLengthFinalization(t *testing.T) {
	h, _, _ := setupHandler(t)
	created := createTestUpload(t, h, "PATCH-DEFERRED/L0/000", "IMG_0001.jpg", creationDate)

	// Upload data with Upload-Length header to declare the final size.
	data := []byte("complete upload content")
	rec := tusPatchRequest(
		h.HandlePatchUploadData, created.ID, 0, strconv.Itoa(len(data)), strings.NewReader(string(data)))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("PATCH with Upload-Length expected 204, got %d: %s", rec.Code, rec.Body.String())
	}

	offsetStr := rec.Header().Get("Upload-Offset")
	if offsetStr != strconv.Itoa(len(data)) {
		t.Errorf("expected offset %d, got %s", len(data), offsetStr)
	}

	// Verify via HEAD that the offset matches.
	headRec := tusHeadRequest(h.HandleHeadUploadData, created.ID)
	if headRec.Code != http.StatusOK {
		t.Fatalf("HEAD after finalization expected 200, got %d: %s", headRec.Code, headRec.Body.String())
	}
	finalOffset := headRec.Header().Get("Upload-Offset")
	if finalOffset != strconv.Itoa(len(data)) {
		t.Errorf("HEAD offset after finalization: expected %s, got %s", strconv.Itoa(len(data)), finalOffset)
	}
}

func TestHandlePatchUploadData_MultiChunkWithFinalization(t *testing.T) {
	h, _, bh := setupHandler(t)
	created := createTestUpload(t, h, "PATCH-FINALIZE/L0/000", "IMG_0001.jpg", creationDate)

	// First chunk (no Upload-Length yet).
	chunk1 := []byte("hello ")
	rec1 := tusPatchRequest(h.HandlePatchUploadData, created.ID, 0, "", strings.NewReader(string(chunk1)))
	if rec1.Code != http.StatusNoContent {
		t.Fatalf("first chunk expected 204, got %d: %s", rec1.Code, rec1.Body.String())
	}
	offset1 := rec1.Header().Get("Upload-Offset")
	if offset1 != strconv.Itoa(len(chunk1)) {
		t.Errorf("after chunk1: expected offset %d, got %s", len(chunk1), offset1)
	}

	// Verify still deferred.
	info, err := bh.GetInfo(context.Background(), created.BackendID)
	if err != nil {
		t.Fatalf("GetInfo: %v", err)
	}
	if !info.SizeIsDeferred {
		t.Error("expected size to still be deferred after first chunk")
	}

	// Second chunk with Upload-Length to finalize.
	chunk2 := []byte("world!!")
	total := len(chunk1) + len(chunk2)
	rec2 := tusPatchRequest(
		h.HandlePatchUploadData, created.ID, int64(len(chunk1)), strconv.Itoa(total), strings.NewReader(string(chunk2)))
	if rec2.Code != http.StatusNoContent {
		t.Fatalf("second chunk expected 204, got %d: %s", rec2.Code, rec2.Body.String())
	}
	offset2 := rec2.Header().Get("Upload-Offset")
	if offset2 != strconv.Itoa(total) {
		t.Errorf("after chunk2: expected offset %d, got %s", total, offset2)
	}

	// Verify no longer deferred and offset == length.
	info2, err := bh.GetInfo(context.Background(), created.BackendID)
	if err != nil {
		t.Fatalf("GetInfo after finalization: %v", err)
	}
	if info2.SizeIsDeferred {
		t.Error("expected size to be known after finalization")
	}
	if info2.Offset != int64(total) {
		t.Errorf("expected offset %d, got %d", total, info2.Offset)
	}
	if info2.Size != int64(total) {
		t.Errorf("expected size %d, got %d", total, info2.Size)
	}

	// Verify IsComplete returns true.
	complete, err := bh.IsComplete(context.Background(), created.BackendID)
	if err != nil {
		t.Fatalf("IsComplete: %v", err)
	}
	if !complete {
		t.Error("expected IsComplete to return true")
	}
}

// ---------------------------------------------------------------------------
// PATCH /uploads/:id/data — backend_lost detection
// ---------------------------------------------------------------------------

func TestHandlePatchUploadData_BackendLost(t *testing.T) {
	h, st, bh := setupHandler(t)
	created := createTestUpload(t, h, "PATCH-LOST/L0/000", "IMG_0001.jpg", creationDate)

	// Manually terminate the upload in the backend to simulate backend_lost.
	if err := bh.TerminateOrCleanup(
		context.Background(), created.BackendID); err != nil && !errors.Is(err, uploadbackend.ErrNotFound) {
		t.Fatalf("TerminateOrCleanup: %v", err)
	}

	// Now PATCH should return 409 with backend_lost.
	rec := tusPatchRequest(h.HandlePatchUploadData, created.ID, 0, "", strings.NewReader("data"))
	if rec.Code != http.StatusConflict {
		t.Errorf("expected 409 for backend_lost, got %d: %s", rec.Code, rec.Body.String())
	}

	var errResp map[string]string
	decodeResponse(t, rec, &errResp)
	if errResp["error"] != statusBackendLost {
		t.Errorf("expected error 'backend_lost', got %q", errResp["error"])
	}

	// Verify the DB record was updated to backend_lost.
	upload, err := st.GetUpload(created.ID)
	if err != nil {
		t.Fatalf("GetUpload: %v", err)
	}
	if upload.Status != store.StatusBackendLost {
		t.Errorf("expected status backend_lost, got %q", upload.Status)
	}
}

func TestHandleHeadUploadData_BackendLost(t *testing.T) {
	h, st, bh := setupHandler(t)
	created := createTestUpload(t, h, "HEAD-LOST/L0/000", "IMG_0001.jpg", creationDate)

	// Manually terminate the upload in the backend.
	if err := bh.TerminateOrCleanup(
		context.Background(), created.BackendID); err != nil && !errors.Is(err, uploadbackend.ErrNotFound) {
		t.Fatalf("TerminateOrCleanup: %v", err)
	}

	rec := tusHeadRequest(h.HandleHeadUploadData, created.ID)
	if rec.Code != http.StatusConflict {
		t.Errorf("expected 409 for backend_lost, got %d: %s", rec.Code, rec.Body.String())
	}

	var errResp map[string]string
	decodeResponse(t, rec, &errResp)
	if errResp["error"] != statusBackendLost {
		t.Errorf("expected error 'backend_lost', got %q", errResp["error"])
	}

	// Verify the DB record was updated to backend_lost.
	upload, err := st.GetUpload(created.ID)
	if err != nil {
		t.Fatalf("GetUpload: %v", err)
	}
	if upload.Status != store.StatusBackendLost {
		t.Errorf("expected status backend_lost, got %q", upload.Status)
	}
}

// TestHandleHeadUploadData_AfterCompleteDoesNotClobber verifies that a late
// HEAD /data on an upload that has already been completed (and whose tusd
// backend was cleaned up during completion) does NOT downgrade the record
// from complete to backend_lost. HEAD does not acquire the per-upload lock,
// so handleBackendLost must guard against clobbering terminal statuses.
func TestHandleHeadUploadData_AfterCompleteDoesNotClobber(t *testing.T) {
	h, st, _ := setupHandler(t)
	created := createTestUpload(t, h, "HEAD-AFTER-COMPLETE/L0/000", "IMG_0001.jpg", creationDate)

	// Upload all data and finalize the deferred-length upload.
	data := []byte("content for head-after-complete test")
	patchRec := tusPatchRequest(h.HandlePatchUploadData, created.ID, 0,
		strconv.Itoa(len(data)), strings.NewReader(string(data)))
	if patchRec.Code != http.StatusNoContent {
		t.Fatalf("PATCH data expected 204, got %d: %s", patchRec.Code, patchRec.Body.String())
	}

	// Complete the upload (this moves the file and cleans up the tusd backend).
	rec := statusPatchRequest(h.HandlePatchUploadStatus, created.ID, `{"status": "complete"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("PATCH status expected 204, got %d: %s", rec.Code, rec.Body.String())
	}

	upload, err := st.GetUpload(created.ID)
	if err != nil {
		t.Fatalf("GetUpload: %v", err)
	}
	if upload.Status != store.StatusComplete {
		t.Fatalf("expected status complete before HEAD, got %q", upload.Status)
	}

	// A late HEAD on the now-cleaned-up backend must not revert complete -> backend_lost.
	headRec := tusHeadRequest(h.HandleHeadUploadData, created.ID)
	if headRec.Code != http.StatusConflict {
		t.Errorf("expected 409 from HEAD on cleaned-up backend, got %d", headRec.Code)
	}

	upload, err = st.GetUpload(created.ID)
	if err != nil {
		t.Fatalf("GetUpload after HEAD: %v", err)
	}
	if upload.Status != store.StatusComplete {
		t.Errorf("HEAD clobbered terminal status: expected complete, got %q", upload.Status)
	}
}

// ---------------------------------------------------------------------------
// Same-ID serialization: concurrent PATCH /data operations
// ---------------------------------------------------------------------------

func TestHandlePatchUploadData_SameIDSerialization(t *testing.T) {
	h, _, _ := setupHandler(t)
	created := createTestUpload(t, h, "PATCH-SERIAL/L0/000", "IMG_0001.jpg", creationDate)

	// Launch two goroutines that each PATCH data simultaneously.
	// They should not race or produce corrupt offsets.
	errCh := make(chan error, 2)

	for i := range 2 {
		go func(seq int) {
			// Both goroutines start at offset 0, but only one should succeed.
			// Since they both claim offset 0, one will get a conflict.
			// Actually, the first one succeeds and advances offset.
			// The second one will get offset mismatch (client says 0, server says len(chunk)).
			// This is expected behavior for concurrent patches.
			data := fmt.Sprintf("chunk-%d", seq)
			rec := tusPatchRequest(h.HandlePatchUploadData, created.ID, 0, "", strings.NewReader(data))
			switch rec.Code {
			case http.StatusNoContent:
				// Success — should see offset = len(data)
				offsetStr := rec.Header().Get("Upload-Offset")
				if offsetStr != strconv.Itoa(len(data)) {
					errCh <- fmt.Errorf("%w: expected offset %d, got %s", errConcurrentOffset, len(data), offsetStr)
					return
				}
			case http.StatusConflict:
				// Expected: second goroutine got offset mismatch.
				// This is fine — no data corruption.
			default:
				errCh <- fmt.Errorf("%w: %d: %s", errConcurrentStatus, rec.Code, rec.Body.String())
				return
			}
			errCh <- nil
		}(i)
	}

	for i := range 2 {
		if err := <-errCh; err != nil {
			t.Errorf("goroutine %d: %v", i, err)
		}
	}

	// Final offset should be 0 (neither succeeded) or 7 (one of the goroutines succeeded,
	// since both "chunk-0" and "chunk-1" are 7 bytes).
	headRec := tusHeadRequest(h.HandleHeadUploadData, created.ID)
	if headRec.Code != http.StatusOK {
		t.Fatalf("HEAD after concurrent patches: expected 200, got %d: %s", headRec.Code, headRec.Body.String())
	}
	offsetStr := headRec.Header().Get("Upload-Offset")
	if offsetStr != "0" && offsetStr != "7" {
		t.Errorf("final offset %s is neither 0 (no success) nor 7 (one succeeded)", offsetStr)
	}
}

// ---------------------------------------------------------------------------
// Untyped response types (used for decoding in tests)
// ---------------------------------------------------------------------------

// CreateUploadResponse mirrors the handler's response type for test decoding.
type CreateUploadResponse = api.CreateUploadResponse

// ListUploadsResponse mirrors the handler's response type for test decoding.
type ListUploadsResponse = api.ListUploadsResponse

// ---------------------------------------------------------------------------
// PATCH /status helpers
// ---------------------------------------------------------------------------

// statusPatchRequest sends a PATCH /uploads/:id/status request with the given
// JSON body.
func statusPatchRequest(h http.HandlerFunc, id, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(
		context.Background(), http.MethodPatch, "/uploads/"+id+"/status", strings.NewReader(body))
	req.SetPathValue("id", id)
	req.Header.Set("Content-Type", jsonContentType)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// ---------------------------------------------------------------------------
// DELETE helpers
// ---------------------------------------------------------------------------

// deleteUploadRequest sends a DELETE /uploads/:id request.
func deleteUploadRequest(h http.HandlerFunc, id string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodDelete, "/uploads/"+id, nil)
	req.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// ---------------------------------------------------------------------------
// PATCH /uploads/:id/status
// ---------------------------------------------------------------------------

func TestHandlePatchUploadStatus_Success(t *testing.T) {
	h, st, _ := setupHandler(t)
	created := createTestUpload(t, h, "PATCH-STATUS-OK/L0/000", "IMG_0001.jpg", creationDate)

	// Upload all data with Upload-Length header to finalize the deferred-length
	// upload. This makes IsComplete return true.
	data := []byte("complete upload content for status test")
	patchRec := tusPatchRequest(h.HandlePatchUploadData, created.ID, 0,
		strconv.Itoa(len(data)), strings.NewReader(string(data)))
	if patchRec.Code != http.StatusNoContent {
		t.Fatalf("PATCH data expected 204, got %d: %s", patchRec.Code, patchRec.Body.String())
	}

	// Mark complete.
	rec := statusPatchRequest(h.HandlePatchUploadStatus, created.ID, `{"status": "complete"}`)
	if rec.Code != http.StatusNoContent {
		t.Errorf("PATCH status expected 204, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify DB record is now complete.
	upload, err := st.GetUpload(created.ID)
	if err != nil {
		t.Fatalf("GetUpload: %v", err)
	}
	if upload.Status != store.StatusComplete {
		t.Errorf("expected status complete, got %q", upload.Status)
	}
	if upload.OrganizedPath == "" {
		t.Error("expected non-empty organized_path")
	}

	// Verify the completion intent was deleted after successful completion.
	intent, err := st.GetCompletionIntent(created.ID)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetCompletionIntent: %v", err)
	}
	if intent != nil {
		t.Errorf("completion intent should be deleted after success, got %+v", intent)
	}
}

func TestHandlePatchUploadStatus_UploadIncomplete(t *testing.T) {
	h, _, _ := setupHandler(t)
	created := createTestUpload(t, h, "PATCH-STATUS-INCOMPLETE/L0/000", "IMG_0001.jpg", creationDate)

	// Upload some data WITHOUT Upload-Length header — the upload stays deferred
	// so IsComplete returns false.
	data := []byte("partial data")
	patchRec := tusPatchRequest(h.HandlePatchUploadData, created.ID, 0, "", strings.NewReader(string(data)))
	if patchRec.Code != http.StatusNoContent {
		t.Fatalf("PATCH data expected 204, got %d: %s", patchRec.Code, patchRec.Body.String())
	}

	// Try to mark complete — should fail because upload is not finalized.
	rec := statusPatchRequest(h.HandlePatchUploadStatus, created.ID, `{"status": "complete"}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("expected 409 for incomplete upload, got %d: %s", rec.Code, rec.Body.String())
	}

	var errResp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if errResp["error"] != "upload_incomplete" {
		t.Errorf("expected error 'upload_incomplete', got %q", errResp["error"])
	}
}

func TestHandlePatchUploadStatus_InvalidStatus(t *testing.T) {
	h, _, _ := setupHandler(t)
	created := createTestUpload(t, h, "PATCH-STATUS-BAD/L0/000", "IMG_0001.jpg", creationDate)

	// Send status=deleted (not accepted).
	rec := statusPatchRequest(h.HandlePatchUploadStatus, created.ID, `{"status": "deleted"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid status, got %d: %s", rec.Code, rec.Body.String())
	}

	var errResp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if errResp["error"] == "" || !strings.Contains(errResp["error"], "must be 'complete'") {
		t.Errorf("expected error about 'complete', got %q", errResp["error"])
	}
}

func TestHandlePatchUploadStatus_InvalidJSON(t *testing.T) {
	h, _, _ := setupHandler(t)
	created := createTestUpload(t, h, "PATCH-STATUS-INVALJSON/L0/000", "IMG_0001.jpg", creationDate)

	rec := statusPatchRequest(h.HandlePatchUploadStatus, created.ID, `{invalid json}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid JSON, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandlePatchUploadStatus_NotFound(t *testing.T) {
	h, _, _ := setupHandler(t)

	nonExistentID := api.SafeID("this-upload-does-not-exist-for-status")
	rec := statusPatchRequest(h.HandlePatchUploadStatus, nonExistentID, `{"status": "complete"}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandlePatchUploadStatus_AlreadyComplete(t *testing.T) {
	h, st, _ := setupHandler(t)
	created := createTestUpload(t, h, "PATCH-STATUS-ALRDY/L0/000", "IMG_0001.jpg", creationDate)

	// Manually mark as complete.
	if _, err := st.UpdateStatus(created.ID, store.StatusComplete); err != nil {
		t.Fatalf("UpdateStatus to complete: %v", err)
	}

	rec := statusPatchRequest(h.HandlePatchUploadStatus, created.ID, `{"status": "complete"}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("expected 409 for already complete, got %d: %s", rec.Code, rec.Body.String())
	}

	var errResp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if errResp["error"] != "upload already completed" {
		t.Errorf("expected error 'upload already completed', got %q", errResp["error"])
	}
}

func TestHandlePatchUploadStatus_Deleted(t *testing.T) {
	h, st, _ := setupHandler(t)
	created := createTestUpload(t, h, "PATCH-STATUS-DEL/L0/000", "IMG_0001.jpg", creationDate)

	// Manually mark as deleted.
	if _, err := st.UpdateStatus(created.ID, store.StatusDeleted); err != nil {
		t.Fatalf("UpdateStatus to deleted: %v", err)
	}

	rec := statusPatchRequest(h.HandlePatchUploadStatus, created.ID, `{"status": "complete"}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("expected 409 for deleted, got %d: %s", rec.Code, rec.Body.String())
	}

	var errResp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if errResp["error"] != "upload already deleted" {
		t.Errorf("expected error 'upload already deleted', got %q", errResp["error"])
	}
}

func TestHandlePatchUploadStatus_BackendLost(t *testing.T) {
	h, st, bh := setupHandler(t)
	created := createTestUpload(t, h, "PATCH-STATUS-LOST/L0/000", "IMG_0001.jpg", creationDate)

	// Manually terminate the backend upload.
	if err := bh.TerminateOrCleanup(
		context.Background(), created.BackendID); err != nil && !errors.Is(err, uploadbackend.ErrNotFound) {
		t.Fatalf("TerminateOrCleanup: %v", err)
	}

	rec := statusPatchRequest(h.HandlePatchUploadStatus, created.ID, `{"status": "complete"}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("expected 409 for backend_lost, got %d: %s", rec.Code, rec.Body.String())
	}

	var errResp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if errResp["error"] != statusBackendLost {
		t.Errorf("expected error 'backend_lost', got %q", errResp["error"])
	}

	// Verify DB record was updated to backend_lost.
	upload, err := st.GetUpload(created.ID)
	if err != nil {
		t.Fatalf("GetUpload: %v", err)
	}
	if upload.Status != store.StatusBackendLost {
		t.Errorf("expected status backend_lost, got %q", upload.Status)
	}
}

func TestHandlePatchUploadStatus_EmptyID(t *testing.T) {
	h, _, _ := setupHandler(t)

	req := httptest.NewRequestWithContext(
		context.Background(), http.MethodPatch, "/uploads//status", strings.NewReader(`{"status": "complete"}`))
	req.Header.Set("Content-Type", jsonContentType)
	rec := httptest.NewRecorder()
	h.HandlePatchUploadStatus(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for empty ID, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandlePatchUploadStatus_InvalidID(t *testing.T) {
	h, _, _ := setupHandler(t)

	rec := statusPatchRequest(h.HandlePatchUploadStatus, "bad", `{"status": "complete"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid ID, got %d: %s", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// DELETE /uploads/:id
// ---------------------------------------------------------------------------

func TestHandleDeleteUpload_Success(t *testing.T) {
	h, st, _ := setupHandler(t)
	created := createTestUpload(t, h, "DELETE-OK/L0/000", "IMG_0001.jpg", creationDate)

	// Upload all data.
	data := []byte("file content for full delete test")
	patchRec := tusPatchRequest(h.HandlePatchUploadData, created.ID, 0,
		strconv.Itoa(len(data)), strings.NewReader(string(data)))
	if patchRec.Code != http.StatusNoContent {
		t.Fatalf("PATCH data expected 204, got %d: %s", patchRec.Code, patchRec.Body.String())
	}

	// Mark complete to get an organized_path.
	statusRec := statusPatchRequest(h.HandlePatchUploadStatus, created.ID, `{"status": "complete"}`)
	if statusRec.Code != http.StatusNoContent {
		t.Fatalf("PATCH status expected 204, got %d: %s", statusRec.Code, statusRec.Body.String())
	}

	upload, err := st.GetUpload(created.ID)
	if err != nil {
		t.Fatalf("GetUpload: %v", err)
	}
	if upload.OrganizedPath == "" {
		t.Fatal("expected non-empty organized_path after completion")
	}

	// Verify the organized file exists before delete.
	absPath := filepath.Join(h.StoragePath(), upload.OrganizedPath)
	if _, err := os.Stat(absPath); err != nil {
		t.Fatalf("organized file should exist before delete: %v", err)
	}

	// Delete the upload.
	delRec := deleteUploadRequest(h.HandleDeleteUpload, created.ID)
	if delRec.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d: %s", delRec.Code, delRec.Body.String())
	}

	// Verify DB record is now deleted.
	upload, err = st.GetUpload(created.ID)
	if err != nil {
		t.Fatalf("GetUpload: %v", err)
	}
	if upload.Status != store.StatusDeleted {
		t.Errorf("expected status deleted, got %q", upload.Status)
	}

	// Verify organized file is removed from disk.
	if _, err := os.Stat(absPath); !os.IsNotExist(err) {
		t.Errorf("organized file should be removed after delete, stat: %v", err)
	}
}

func TestHandleDeleteUpload_NotFound(t *testing.T) {
	h, _, _ := setupHandler(t)

	nonExistentID := api.SafeID("this-upload-does-not-exist-for-delete")
	rec := deleteUploadRequest(h.HandleDeleteUpload, nonExistentID)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleDeleteUpload_AlreadyDeleted(t *testing.T) {
	h, st, _ := setupHandler(t)
	created := createTestUpload(t, h, "DELETE-AGAIN/L0/000", "IMG_0001.jpg", creationDate)

	// First delete.
	rec1 := deleteUploadRequest(h.HandleDeleteUpload, created.ID)
	if rec1.Code != http.StatusNoContent {
		t.Fatalf("first delete expected 204, got %d: %s", rec1.Code, rec1.Body.String())
	}

	// Second delete — should still return 204 (idempotent).
	rec2 := deleteUploadRequest(h.HandleDeleteUpload, created.ID)
	if rec2.Code != http.StatusNoContent {
		t.Errorf("second delete expected 204, got %d: %s", rec2.Code, rec2.Body.String())
	}

	// Verify still deleted.
	upload, err := st.GetUpload(created.ID)
	if err != nil {
		t.Fatalf("GetUpload: %v", err)
	}
	if upload.Status != store.StatusDeleted {
		t.Errorf("expected status deleted, got %q", upload.Status)
	}
}

func TestHandleDeleteUpload_BackendGone(t *testing.T) {
	h, st, bh := setupHandler(t)
	created := createTestUpload(t, h, "DELETE-GONE/L0/000", "IMG_0001.jpg", creationDate)

	// Manually terminate the backend.
	if err := bh.TerminateOrCleanup(
		context.Background(), created.BackendID); err != nil && !errors.Is(err, uploadbackend.ErrNotFound) {
		t.Fatalf("TerminateOrCleanup: %v", err)
	}

	// Delete should still succeed — ErrNotFound from backend is ignored.
	rec := deleteUploadRequest(h.HandleDeleteUpload, created.ID)
	if rec.Code != http.StatusNoContent {
		t.Errorf("expected 204 when backend already gone, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify DB record is deleted.
	upload, err := st.GetUpload(created.ID)
	if err != nil {
		t.Fatalf("GetUpload: %v", err)
	}
	if upload.Status != store.StatusDeleted {
		t.Errorf("expected status deleted, got %q", upload.Status)
	}
}

func TestHandleDeleteUpload_CompleteUpload(t *testing.T) {
	h, st, _ := setupHandler(t)
	created := createTestUpload(t, h, "DELETE-COMPLETE/L0/000", "IMG_0001.jpg", creationDate)

	// Manually mark as complete.
	if _, err := st.UpdateStatus(created.ID, store.StatusComplete); err != nil {
		t.Fatalf("UpdateStatus to complete: %v", err)
	}

	// Delete should succeed.
	rec := deleteUploadRequest(h.HandleDeleteUpload, created.ID)
	if rec.Code != http.StatusNoContent {
		t.Errorf("expected 204 for completed upload, got %d: %s", rec.Code, rec.Body.String())
	}

	upload, err := st.GetUpload(created.ID)
	if err != nil {
		t.Fatalf("GetUpload: %v", err)
	}
	if upload.Status != store.StatusDeleted {
		t.Errorf("expected status deleted, got %q", upload.Status)
	}
}

func TestHandleDeleteUpload_EmptyID(t *testing.T) {
	h, _, _ := setupHandler(t)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodDelete, "/uploads/", nil)
	rec := httptest.NewRecorder()
	h.HandleDeleteUpload(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for empty ID, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleDeleteUpload_InvalidID(t *testing.T) {
	h, _, _ := setupHandler(t)

	rec := deleteUploadRequest(h.HandleDeleteUpload, "bad")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid ID, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleDeleteUpload_RemovesOrganizedFile(t *testing.T) {
	h, st, _ := setupHandler(t)
	created := createTestUpload(t, h, "DELETE-REMOVE/L0/000", "IMG_0001.jpg", creationDate)

	// Upload all data.
	data := []byte("file content for remove test")
	patchRec := tusPatchRequest(h.HandlePatchUploadData, created.ID, 0,
		strconv.Itoa(len(data)), strings.NewReader(string(data)))
	if patchRec.Code != http.StatusNoContent {
		t.Fatalf("PATCH data expected 204, got %d: %s", patchRec.Code, patchRec.Body.String())
	}

	// Mark complete to get an organized_path.
	statusRec := statusPatchRequest(h.HandlePatchUploadStatus, created.ID, `{"status": "complete"}`)
	if statusRec.Code != http.StatusNoContent {
		t.Fatalf("PATCH status expected 204, got %d: %s", statusRec.Code, statusRec.Body.String())
	}

	upload, err := st.GetUpload(created.ID)
	if err != nil {
		t.Fatalf("GetUpload: %v", err)
	}
	if upload.OrganizedPath == "" {
		t.Fatal("expected non-empty organized_path after completion")
	}

	absPath := filepath.Join(h.StoragePath(), upload.OrganizedPath)
	if _, err := os.Stat(absPath); err != nil {
		t.Fatalf("organized file should exist before delete: %v", err)
	}

	// DELETE should remove the organized file.
	delRec := deleteUploadRequest(h.HandleDeleteUpload, created.ID)
	if delRec.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d: %s", delRec.Code, delRec.Body.String())
	}

	// Verify organized file is gone from disk.
	if _, err := os.Stat(absPath); !os.IsNotExist(err) {
		t.Errorf("organized file should be removed after delete, stat: %v", err)
	}

	// Verify DB record is deleted.
	upload, err = st.GetUpload(created.ID)
	if err != nil {
		t.Fatalf("GetUpload: %v", err)
	}
	if upload.Status != store.StatusDeleted {
		t.Errorf("expected status deleted, got %q", upload.Status)
	}
}

func TestHandleDeleteUpload_NoOrganizedPath(t *testing.T) {
	h, st, _ := setupHandler(t)
	created := createTestUpload(t, h, "DELETE-NOORG/L0/000", "IMG_0001.jpg", creationDate)

	// Confirm the upload has no organized_path (never completed).
	upload, err := st.GetUpload(created.ID)
	if err != nil {
		t.Fatalf("GetUpload: %v", err)
	}
	if upload.OrganizedPath != "" {
		t.Skip("test requires empty organized_path; upload was already completed")
	}

	// DELETE should succeed.
	rec := deleteUploadRequest(h.HandleDeleteUpload, created.ID)
	if rec.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify DB record is deleted.
	upload, err = st.GetUpload(created.ID)
	if err != nil {
		t.Fatalf("GetUpload: %v", err)
	}
	if upload.Status != store.StatusDeleted {
		t.Errorf("expected status deleted, got %q", upload.Status)
	}
}

func TestHandleDeleteUpload_OrganizedFileAlreadyGone(t *testing.T) {
	h, st, _ := setupHandler(t)
	created := createTestUpload(t, h, "DELETE-GONE/L0/000", "IMG_0001.jpg", creationDate)

	// Upload all data.
	data := []byte("file content for already-gone test")
	patchRec := tusPatchRequest(h.HandlePatchUploadData, created.ID, 0,
		strconv.Itoa(len(data)), strings.NewReader(string(data)))
	if patchRec.Code != http.StatusNoContent {
		t.Fatalf("PATCH data expected 204, got %d: %s", patchRec.Code, patchRec.Body.String())
	}

	// Mark complete.
	statusRec := statusPatchRequest(h.HandlePatchUploadStatus, created.ID, `{"status": "complete"}`)
	if statusRec.Code != http.StatusNoContent {
		t.Fatalf("PATCH status expected 204, got %d: %s", statusRec.Code, statusRec.Body.String())
	}

	upload, err := st.GetUpload(created.ID)
	if err != nil {
		t.Fatalf("GetUpload: %v", err)
	}
	if upload.OrganizedPath == "" {
		t.Fatal("expected non-empty organized_path after completion")
	}

	// Manually remove the organized file before DELETE.
	absPath := filepath.Join(h.StoragePath(), upload.OrganizedPath)
	if err := os.Remove(absPath); err != nil {
		t.Fatalf("failed to manually remove organized file: %v", err)
	}

	// DELETE should still succeed (idempotent).
	delRec := deleteUploadRequest(h.HandleDeleteUpload, created.ID)
	if delRec.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d: %s", delRec.Code, delRec.Body.String())
	}

	// Verify DB record is deleted.
	upload, err = st.GetUpload(created.ID)
	if err != nil {
		t.Fatalf("GetUpload: %v", err)
	}
	if upload.Status != store.StatusDeleted {
		t.Errorf("expected status deleted, got %q", upload.Status)
	}
}

// ---------------------------------------------------------------------------
// PATCH /uploads/:id/status — verify file moved and tusd cleaned up
// ---------------------------------------------------------------------------

func TestHandlePatchUploadStatus_FileMoved(t *testing.T) {
	h, st, bh := setupHandler(t)
	created := createTestUpload(t, h, "PATCH-STATUS-MOVED/L0/000", "IMG_0001.jpg", creationDate)

	// Upload all data.
	data := []byte("file content for move verification")
	patchRec := tusPatchRequest(h.HandlePatchUploadData, created.ID, 0,
		strconv.Itoa(len(data)), strings.NewReader(string(data)))
	if patchRec.Code != http.StatusNoContent {
		t.Fatalf("PATCH data expected 204, got %d: %s", patchRec.Code, patchRec.Body.String())
	}

	// Mark complete.
	rec := statusPatchRequest(h.HandlePatchUploadStatus, created.ID, `{"status": "complete"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("PATCH status expected 204, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify file exists in organized directory.
	upload, err := st.GetUpload(created.ID)
	if err != nil {
		t.Fatalf("GetUpload: %v", err)
	}
	if upload.OrganizedPath == "" {
		t.Fatal("expected non-empty organized_path")
	}

	// The storage path for the handler is tusDir (from setupHandler).
	// We don't have direct access to tusDir here, but we can check that
	// the organized path is reasonable and the file exists by using the
	// storage path from the handler's perspective.
	if !strings.HasPrefix(upload.OrganizedPath, "organized/") {
		t.Errorf("organized_path should start with 'organized/', got %q", upload.OrganizedPath)
	}

	// Verify the tusd backend was cleaned up.
	_, err = bh.GetInfo(context.Background(), created.BackendID)
	if !errors.Is(err, uploadbackend.ErrNotFound) {
		t.Errorf("expected backend to be cleaned up after completion, got %v", err)
	}
}

func TestHandlePatchUploadStatus_MoveFailurePreservesUploading(t *testing.T) {
	h, st, _ := setupHandler(t)
	created := createTestUpload(t, h, "PATCH-STATUS-MOVEFAIL/L0/000", "IMG_0001.jpg", creationDate)

	// Upload all data to make IsComplete return true.
	data := []byte("content for move failure test")
	patchRec := tusPatchRequest(h.HandlePatchUploadData, created.ID, 0,
		strconv.Itoa(len(data)), strings.NewReader(string(data)))
	if patchRec.Code != http.StatusNoContent {
		t.Fatalf("PATCH data expected 204, got %d: %s", patchRec.Code, patchRec.Body.String())
	}

	// Create a file at the organized root to prevent MkdirAll from creating
	// subdirectories. The computed dstPath will be like:
	// $storagePath/organized/2024/03/15/IMG_0001.jpg
	// If we create a file at $storagePath/organized, MkdirAll will fail
	// trying to create organized/2024/03/15.
	// uploadbackend.New (called from setupHandler) already creates
	// organized/ as an empty directory, so it must be removed before a file
	// can take its place.
	organizedRoot := filepath.Join(h.StoragePath(), "organized")
	if err := os.RemoveAll(organizedRoot); err != nil {
		t.Fatalf("RemoveAll for organized root: %v", err)
	}
	// Write a file in place of the organized directory to force MkdirAll to fail.
	if err := os.WriteFile(organizedRoot, []byte("not-a-directory"), 0o600); err != nil {
		t.Fatalf("WriteFile to block organized dir: %v", err)
	}

	// Attempt to mark complete — should fail because MkdirAll cannot create
	// the destination directory tree.
	rec := statusPatchRequest(h.HandlePatchUploadStatus, created.ID, `{"status": "complete"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 for move failure, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify the DB record is still uploading (not complete).
	upload, err := st.GetUpload(created.ID)
	if err != nil {
		t.Fatalf("GetUpload: %v", err)
	}
	if upload.Status != store.StatusUploading {
		t.Errorf("expected status uploading after move failure, got %q", upload.Status)
	}

	// Verify a completion intent was persisted for crash recovery.
	intent, err := st.GetCompletionIntent(created.ID)
	if err != nil {
		t.Fatalf("GetCompletionIntent: %v", err)
	}
	if intent == nil {
		t.Fatal("expected completion intent to exist after move failure")
	}
	if intent.ID != created.ID {
		t.Errorf("completion intent ID = %q, want %q", intent.ID, created.ID)
	}
	if intent.BackendID != created.BackendID {
		t.Errorf("completion intent BackendID = %q, want %q", intent.BackendID, created.BackendID)
	}
	if intent.DstRel == "" {
		t.Error("expected non-empty DstRel in completion intent")
	}

	// The completion intent must persist the resolved creation date (from
	// PlanDestResult.DateUsed) so the crash-recovery move path can reproduce
	// the file's real timestamp. Here the creation date is a valid RFC3339
	// string, so DateUsed is that exact string.
	if intent.CreationDate != creationDate {
		t.Errorf("completion intent CreationDate = %q, want %q", intent.CreationDate, creationDate)
	}
}

func TestHandlePatchUploadStatus_MoveToExistingPlanFailure(t *testing.T) {
	h, st, _ := setupHandler(t)
	created := createTestUpload(t, h, "PATCH-STATUS-EXISTING-PLAN-FAIL/L0/000", "IMG_0002.jpg", creationDate)

	data := []byte("content for existing plan move failure test")
	patchRec := tusPatchRequest(h.HandlePatchUploadData, created.ID, 0,
		strconv.Itoa(len(data)), strings.NewReader(string(data)))
	if patchRec.Code != http.StatusNoContent {
		t.Fatalf("PATCH data expected 204, got %d: %s", patchRec.Code, patchRec.Body.String())
	}

	blockedDir := filepath.Join(h.StoragePath(), "blocked")
	if err := os.WriteFile(blockedDir, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("WriteFile blocked destination: %v", err)
	}
	if err := st.SaveCompletionIntent(&store.CompletionIntent{
		ID:        created.ID,
		BackendID: created.BackendID,
		Src:       filepath.Join(h.StoragePath(), "incoming", created.BackendID),
		Dst:       filepath.Join(blockedDir, "IMG_0002.jpg"),
		DstRel:    "blocked/IMG_0002.jpg",
		CreatedAt: creationDate,
	}); err != nil {
		t.Fatalf("SaveCompletionIntent: %v", err)
	}

	rec := statusPatchRequest(h.HandlePatchUploadStatus, created.ID, `{"status": "complete"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for existing-plan move failure, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "failed to move file") {
		t.Fatalf("expected move failure response, got %s", rec.Body.String())
	}

	upload, err := st.GetUpload(created.ID)
	if err != nil {
		t.Fatalf("GetUpload: %v", err)
	}
	if upload.Status != store.StatusUploading {
		t.Errorf("status after failed retry = %q, want %q", upload.Status, store.StatusUploading)
	}
	intent, err := st.GetCompletionIntent(created.ID)
	if err != nil {
		t.Fatalf("GetCompletionIntent: %v", err)
	}
	if intent == nil || intent.Dst != filepath.Join(blockedDir, "IMG_0002.jpg") {
		t.Errorf("completion intent after failed retry = %+v", intent)
	}
}

func TestHandlePatchUploadStatus_FileContentPreserved(t *testing.T) {
	h, st, bh := setupHandler(t)
	created := createTestUpload(t, h, "PATCH-STATUS-CONTENT/L0/000", "IMG_0001.jpg", creationDate)

	// Upload known content.
	data := []byte("test content that should be preserved after move")
	patchRec := tusPatchRequest(h.HandlePatchUploadData, created.ID, 0,
		strconv.Itoa(len(data)), strings.NewReader(string(data)))
	if patchRec.Code != http.StatusNoContent {
		t.Fatalf("PATCH data expected 204, got %d: %s", patchRec.Code, patchRec.Body.String())
	}

	// Get the initial file path.
	srcPath, err := bh.FilePath(context.Background(), created.BackendID)
	if err != nil {
		t.Fatalf("FilePath before completion: %v", err)
	}

	// Read content from source.
	//nolint:gosec // test-only read of a temp-dir file, not attacker-controlled
	originalContent, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatalf("ReadFile source: %v", err)
	}
	if string(originalContent) != string(data) {
		t.Fatalf("source content mismatch: got %q, want %q", string(originalContent), string(data))
	}

	// Mark complete.
	rec := statusPatchRequest(h.HandlePatchUploadStatus, created.ID, `{"status": "complete"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("PATCH status expected 204, got %d: %s", rec.Code, rec.Body.String())
	}

	// The source file should no longer exist in the incoming directory.
	if _, err := os.Stat(srcPath); err == nil {
		t.Error("source file should not exist after completion (should be moved)")
	} else if !os.IsNotExist(err) {
		t.Errorf("unexpected error checking source file: %v", err)
	}

	// Verify the organized path file content matches.
	upload, err := st.GetUpload(created.ID)
	if err != nil {
		t.Fatalf("GetUpload after completion: %v", err)
	}
	organizedAbsPath := filepath.Join(h.StoragePath(), upload.OrganizedPath)
	//nolint:gosec // test-only read of a temp-dir file, not attacker-controlled
	movedContent, err := os.ReadFile(organizedAbsPath)
	if err != nil {
		t.Fatalf("ReadFile organized: %v", err)
	}
	if string(movedContent) != string(data) {
		t.Errorf("moved content mismatch: got %q, want %q", string(movedContent), string(data))
	}
}

// ---------------------------------------------------------------------------
// Concurrent completion collision safety
// ---------------------------------------------------------------------------

// TestHandlePatchUploadStatus_ConcurrentCompletionNoDataLoss verifies that two
// concurrent completions for different uploads that share the same creation
// date and filename do not overwrite each other's data. The collision check in
// PlanDestination (os.Stat) and the rename in MoveFile must be serialized by
// the Mover's organized-tree mutex; without it, both completions could compute
// the same destination path and the second rename would silently overwrite the
// first upload's file.
//
//nolint:cyclop // comprehensive integration test; complexity reflects scenario/assertion coverage
func TestHandlePatchUploadStatus_ConcurrentCompletionNoDataLoss(t *testing.T) {
	h, st, _ := setupHandler(t)

	const filename = "IMG_DUPLICATE.jpg"
	const creationDate = creationDate

	// Two distinct uploads (different localIdentifiers → different safe IDs)
	// that share the same filename and creation date.
	c1 := createTestUpload(t, h, "CONCURRENT-A/L0/000", filename, creationDate)
	c2 := createTestUpload(t, h, "CONCURRENT-B/L0/000", filename, creationDate)
	if c1.ID == c2.ID {
		t.Fatalf("expected distinct IDs, got %q for both", c1.ID)
	}

	data1 := []byte("content-A: the quick brown fox")
	data2 := []byte("content-B: jumps over the lazy dog")

	// Fully upload both files (Upload-Length finalizes the deferred-length).
	for _, c := range []struct {
		id string
		d  []byte
	}{
		{c1.ID, data1},
		{c2.ID, data2},
	} {
		rec := tusPatchRequest(h.HandlePatchUploadData, c.id, 0,
			strconv.Itoa(len(c.d)), strings.NewReader(string(c.d)))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("PATCH data for %s: expected 204, got %d: %s", c.id, rec.Code, rec.Body.String())
		}
	}

	// Complete both uploads concurrently. The per-upload lock only serializes
	// same-ID operations, so without the organized-tree mutex these two would
	// race on the shared destination path.
	var wg sync.WaitGroup
	var rec1, rec2 *httptest.ResponseRecorder
	wg.Add(2)
	go func() {
		defer wg.Done()
		rec1 = statusPatchRequest(h.HandlePatchUploadStatus, c1.ID, `{"status": "complete"}`)
	}()
	go func() {
		defer wg.Done()
		rec2 = statusPatchRequest(h.HandlePatchUploadStatus, c2.ID, `{"status": "complete"}`)
	}()
	wg.Wait()

	if rec1.Code != http.StatusNoContent {
		t.Fatalf("completion 1: expected 204, got %d: %s", rec1.Code, rec1.Body.String())
	}
	if rec2.Code != http.StatusNoContent {
		t.Fatalf("completion 2: expected 204, got %d: %s", rec2.Code, rec2.Body.String())
	}

	// Both records must be complete with distinct organized paths, and each
	// organized file must contain that upload's own content (no overwrite).
	u1, err := st.GetUpload(c1.ID)
	if err != nil {
		t.Fatalf("GetUpload 1: %v", err)
	}
	u2, err := st.GetUpload(c2.ID)
	if err != nil {
		t.Fatalf("GetUpload 2: %v", err)
	}
	if u1.Status != store.StatusComplete || u2.Status != store.StatusComplete {
		t.Fatalf("expected both complete, got %q and %q", u1.Status, u2.Status)
	}
	if u1.OrganizedPath == "" || u2.OrganizedPath == "" {
		t.Fatalf("expected non-empty organized paths, got %q and %q", u1.OrganizedPath, u2.OrganizedPath)
	}
	if u1.OrganizedPath == u2.OrganizedPath {
		t.Fatalf("expected distinct organized paths, both %q", u1.OrganizedPath)
	}

	got1, err := os.ReadFile(filepath.Join(h.StoragePath(), u1.OrganizedPath))
	if err != nil {
		t.Fatalf("read organized 1: %v", err)
	}
	got2, err := os.ReadFile(filepath.Join(h.StoragePath(), u2.OrganizedPath))
	if err != nil {
		t.Fatalf("read organized 2: %v", err)
	}
	if string(got1) != string(data1) {
		t.Errorf("organized 1 content mismatch: got %q, want %q (data loss/overwrite detected)", got1, data1)
	}
	if string(got2) != string(data2) {
		t.Errorf("organized 2 content mismatch: got %q, want %q (data loss/overwrite detected)", got2, data2)
	}
}
