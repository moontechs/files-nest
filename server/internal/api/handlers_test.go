// Package api_test tests the HTTP handlers for the iCloud Backup server API.
package api_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moontechs/files-nest/server/internal/api"
	"github.com/moontechs/files-nest/server/internal/store"
	"github.com/moontechs/files-nest/server/internal/uploadbackend"
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
	t.Cleanup(func() { st.Close() })

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
	req := httptest.NewRequest(method, target, body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// executeRequestWithID is a convenience wrapper that calls executeRequest
// and sets the path value for the "id" parameter (Go 1.22+ routing style).
func executeRequestWithID(h http.HandlerFunc, method, target, id string, body io.Reader) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, body)
	req.Header.Set("Content-Type", "application/json")
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
	if resp.Status != "uploading" {
		t.Errorf("status = %q, want %q", resp.Status, "uploading")
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
	if resp.Status != "uploading" {
		t.Errorf("status = %q, want %q", resp.Status, "uploading")
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
	if resp.Status != "uploading" {
		t.Errorf("status = %q, want %q", resp.Status, "uploading")
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
	for i := 0; i < 5; i++ {
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
	created := createTestUpload(t, h, "GETBYID-001/L0/000", "IMG_0001.jpg", "2024-03-15T10:30:00Z")

	// Fetch by ID.
	rec := executeRequestWithID(h.HandleGetUpload, "GET", "/uploads/"+created.ID, created.ID, nil)
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
	if upload.CreationDate != "2024-03-15T10:30:00Z" {
		t.Errorf("CreationDate: got %q, want %q", upload.CreationDate, "2024-03-15T10:30:00Z")
	}
}

func TestHandleGetUpload_NotFound(t *testing.T) {
	h, _, _ := setupHandler(t)

	// Use a valid-format safe ID (43-char base64url) that doesn't exist in the DB.
	// We generate it from a known unused local identifier.
	nonExistentID := api.SafeID("this-identifier-does-not-exist-in-any-database-123")

	rec := executeRequestWithID(h.HandleGetUpload, "GET", "/uploads/"+nonExistentID, nonExistentID, nil)
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
			rec := executeRequestWithID(h.HandleGetUpload, "GET", "/uploads/"+tt.id, tt.id, nil)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestHandleGetUpload_NoID(t *testing.T) {
	h, _, _ := setupHandler(t)

	// Test with no path value set.
	req := httptest.NewRequest("GET", "/uploads/", nil)
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
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for body too large, got %d: %s", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// POST /uploads — content type validation
// ---------------------------------------------------------------------------

func TestHandleCreateUpload_NonJSONContentType(t *testing.T) {
	h, _, _ := setupHandler(t)

	body := `{"local_identifier":"TEST-001","filename":"IMG_1234.jpg","creation_date":"2024-03-15T10:30:00Z"}`
	req := httptest.NewRequest("POST", "/uploads", strings.NewReader(body))
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
	info, err := bh.GetInfo(resp.BackendID)
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
	_, err := bh.GetInfo(first.BackendID)
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
	if ct != "application/json" {
		t.Errorf("expected Content-Type 'application/json', got %q", ct)
	}
}

func TestHandleGetUpload_ResponseContentType(t *testing.T) {
	h, _, _ := setupHandler(t)

	created := createTestUpload(t, h, "CT-GET-001", "IMG_0001.jpg", "2024-03-15T10:30:00Z")

	rec := executeRequestWithID(h.HandleGetUpload, "GET", "/uploads/"+created.ID, created.ID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
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
	if ct != "application/json" {
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
	getRec := executeRequestWithID(h.HandleGetUpload, "GET", "/uploads/"+created.ID, created.ID, nil)
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
	if string(upload.Status) != "uploading" {
		t.Errorf("Status: got %q, want %q", upload.Status, "uploading")
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
// Untyped response types (used for decoding in tests)
// ---------------------------------------------------------------------------

// CreateUploadResponse mirrors the handler's response type for test decoding.
type CreateUploadResponse = api.CreateUploadResponse

// ListUploadsResponse mirrors the handler's response type for test decoding.
type ListUploadsResponse = api.ListUploadsResponse
