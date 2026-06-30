//go:build e2e

// Package e2e provides black-box end-to-end tests for the iCloud Backup
// server. This file implements a thin typed HTTP client that is shared
// by all e2e test files.
package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// ---------------------------------------------------------------------------
// Request body types
// ---------------------------------------------------------------------------

// CreateUploadBody is the JSON body for POST /uploads.
type CreateUploadBody struct {
	LocalIdentifier string          `json:"local_identifier"`
	Filename        string          `json:"filename"`
	CreationDate    string          `json:"creation_date"`
	BundleID        string          `json:"bundle_id,omitempty"`
	Metadata        json.RawMessage `json:"metadata,omitempty"`
}

// PatchStatusBody is the JSON body for PATCH /uploads/{id}/status.
type PatchStatusBody struct {
	Status string `json:"status"`
}

// ---------------------------------------------------------------------------
// Response body types
// ---------------------------------------------------------------------------

// CreateUploadResponse is the JSON payload returned by POST /uploads.
type CreateUploadResponse struct {
	ID              string `json:"id"`
	LocalIdentifier string `json:"local_identifier"`
	Status          string `json:"status"`
	BackendID       string `json:"backend_id"`
	UploadURL       string `json:"upload_url"`
}

// UploadRecord models a single upload record returned by GET endpoints.
// It mirrors store.Upload without importing internal packages.
type UploadRecord struct {
	ID              string          `json:"id"`
	LocalIdentifier string          `json:"local_identifier"`
	Status          string          `json:"status"`
	BackendID       string          `json:"backend_id"`
	Filename        string          `json:"filename"`
	BundleID        string          `json:"bundle_id,omitempty"`
	CreationDate    string          `json:"creation_date"`
	CreatedAt       string          `json:"created_at"`
	UpdatedAt       string          `json:"updated_at"`
	Metadata        json.RawMessage `json:"metadata,omitempty"`
	OrganizedPath   string          `json:"organized_path,omitempty"`
}

// ListUploadsResponse is the JSON envelope from GET /uploads.
type ListUploadsResponse struct {
	Items      []UploadRecord `json:"items"`
	NextCursor string         `json:"next_cursor"`
}

// ErrorResponse is the standard JSON error body returned on 4xx/5xx.
type ErrorResponse struct {
	Error string `json:"error"`
}

// HeadDataResponse holds the TUS protocol headers returned by
// HEAD /uploads/{id}/data.
type HeadDataResponse struct {
	UploadOffset   int64
	UploadLength   int64
	SizeIsDeferred bool
	TusResumable   string
}

// PatchDataResponse holds the response headers from a successful
// PATCH /uploads/{id}/data.
type PatchDataResponse struct {
	UploadOffset int64
}

// ---------------------------------------------------------------------------
// Endpoint helpers
// ---------------------------------------------------------------------------

// HealthCheck performs GET /health and returns nil only when the response
// status is 200 OK. It is used by TestMain readiness polling and by tests
// that want to verify the server is still alive.
func HealthCheck() error {
	resp, err := doRequest("GET", "/health", nil)
	if err != nil {
		return err
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health check: unexpected status %d", resp.StatusCode)
	}
	return nil
}

// CreateUpload sends POST /uploads with the given body and returns the
// parsed response along with the HTTP status code. The caller should
// assert the expected status (201 for new, 200 for existing active, etc.).
func CreateUpload(body CreateUploadBody) (*CreateUploadResponse, int, error) {
	resp, err := doJSONRequest("POST", "/uploads", body)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	var cr CreateUploadResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("decode create upload response: %w", err)
	}
	return &cr, resp.StatusCode, nil
}

// ListUploads sends GET /uploads with optional query parameters.
// Pass zero/empty values to omit a parameter.
func ListUploads(from, to, status string, limit int, cursor string) (*ListUploadsResponse, error) {
	q := url.Values{}
	if from != "" {
		q.Set("from", from)
	}
	if to != "" {
		q.Set("to", to)
	}
	if status != "" {
		q.Set("status", status)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if cursor != "" {
		q.Set("cursor", cursor)
	}

	path := "/uploads"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}

	resp, err := doRequest("GET", path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("list uploads: status %d, body: %s", resp.StatusCode, string(body))
	}

	var lr ListUploadsResponse
	if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil {
		return nil, fmt.Errorf("decode list uploads response: %w", err)
	}
	return &lr, nil
}

// GetUpload performs GET /uploads/{id} and returns the upload record and
// HTTP status code.
func GetUpload(id string) (*UploadRecord, int, error) {
	resp, err := doRequest("GET", "/uploads/"+id, nil)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	var ur UploadRecord
	if err := json.NewDecoder(resp.Body).Decode(&ur); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("decode get upload response: %w", err)
	}
	return &ur, resp.StatusCode, nil
}

// HeadUploadData performs HEAD /uploads/{id}/data and returns the TUS
// protocol headers together with the HTTP status code.
func HeadUploadData(id string) (*HeadDataResponse, int, error) {
	resp, err := doRequest("HEAD", "/uploads/"+id+"/data", nil)
	if err != nil {
		return nil, 0, err
	}
	resp.Body.Close()

	hdr := &HeadDataResponse{}

	if offsetStr := resp.Header.Get("Upload-Offset"); offsetStr != "" {
		offset, err := strconv.ParseInt(offsetStr, 10, 64)
		if err != nil {
			return nil, resp.StatusCode, fmt.Errorf("parse Upload-Offset: %w", err)
		}
		hdr.UploadOffset = offset
	}

	if lengthStr := resp.Header.Get("Upload-Length"); lengthStr != "" {
		length, err := strconv.ParseInt(lengthStr, 10, 64)
		if err != nil {
			return nil, resp.StatusCode, fmt.Errorf("parse Upload-Length: %w", err)
		}
		hdr.UploadLength = length
	}

	if resp.Header.Get("Upload-Defer-Length") == "1" {
		hdr.SizeIsDeferred = true
	}

	hdr.TusResumable = resp.Header.Get("Tus-Resumable")

	return hdr, resp.StatusCode, nil
}

// PatchUploadData performs PATCH /uploads/{id}/data with the given data
// reader and TUS protocol headers. The caller must set Upload-Offset and
// Content-Type; Upload-Length is required when the client is finishing
// the upload.
func PatchUploadData(id string, data io.Reader, offset int64, uploadLength string) (*PatchDataResponse, int, error) {
	path := "/uploads/" + id + "/data"
	req, err := http.NewRequest("PATCH", serverURL+path, data)
	if err != nil {
		return nil, 0, fmt.Errorf("create PATCH request: %w", err)
	}

	req.Header.Set("Content-Type", "application/offset+octet-stream")
	req.Header.Set("Upload-Offset", strconv.FormatInt(offset, 10))
	req.Header.Set("Tus-Resumable", "1.0.0")
	if uploadLength != "" {
		req.Header.Set("Upload-Length", uploadLength)
	}

	if backupUser != "" || backupPass != "" {
		req.SetBasicAuth(backupUser, backupPass)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("PATCH %s: %w", id, err)
	}
	resp.Body.Close()

	pr := &PatchDataResponse{}
	if offsetStr := resp.Header.Get("Upload-Offset"); offsetStr != "" {
		offset, err := strconv.ParseInt(offsetStr, 10, 64)
		if err != nil {
			return nil, resp.StatusCode, fmt.Errorf("parse Upload-Offset response: %w", err)
		}
		pr.UploadOffset = offset
	}

	return pr, resp.StatusCode, nil
}

// PatchUploadStatus performs PATCH /uploads/{id}/status and returns the
// HTTP status code. On error responses (4xx/5xx) the body is discarded.
func PatchUploadStatus(id string, body PatchStatusBody) (int, error) {
	resp, err := doJSONRequest("PATCH", "/uploads/"+id+"/status", body)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return resp.StatusCode, nil
}

// DeleteUpload performs DELETE /uploads/{id} and returns the HTTP status
// code (204 on success).
func DeleteUpload(id string) (int, error) {
	resp, err := doRequest("DELETE", "/uploads/"+id, nil)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return resp.StatusCode, nil
}

// ---------------------------------------------------------------------------
// Internal HTTP helpers
// ---------------------------------------------------------------------------

// doRequest creates an authenticated HTTP request with the given method and
// path, attaches Basic Auth when credentials are configured, and executes
// it. The caller MUST close resp.Body after use.
func doRequest(method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, serverURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("create %s %s: %w", method, path, err)
	}

	if backupUser != "" || backupPass != "" {
		req.SetBasicAuth(backupUser, backupPass)
	}

	return httpClient.Do(req)
}

// doJSONRequest marshals v as JSON, creates a POST/PATCH request with the
// JSON body and Content-Type: application/json, sends it, and returns the
// response. Pass nil for v when there is no request body (e.g. for some
// PATCH endpoints that expect an empty body — but here we always send JSON).
// The caller MUST close resp.Body after use.
func doJSONRequest(method, path string, v any) (*http.Response, error) {
	var body io.Reader
	if v != nil {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("marshal %s %s body: %w", method, path, err)
		}
		body = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, serverURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("create %s %s: %w", method, path, err)
	}

	req.Header.Set("Content-Type", "application/json")

	if backupUser != "" || backupPass != "" {
		req.SetBasicAuth(backupUser, backupPass)
	}

	return httpClient.Do(req)
}
