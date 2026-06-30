//go:build e2e

// Package e2e provides black-box end-to-end tests for the iCloud Backup
// server. This file contains authentication and request-validation tests:
//
//   - Verifying unauthenticated access to protected endpoints returns 401.
//   - Verifying invalid credentials are rejected with 401.
//   - Verifying the WWW-Authenticate header on 401 responses.
//   - Verifying the health endpoint remains publicly accessible.
//   - Verifying request validation returns 400 for malformed inputs not
//     covered in lifecycle_test.go (invalid JSON, disallowed fields,
//     invalid status values, invalid query parameters, and so on).
package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Auth helpers
// ---------------------------------------------------------------------------

// authEnabled returns true when the test environment has configured HTTP
// Basic Authentication credentials (at least one of BACKUP_USER or
// BACKUP_PASS is non-empty). When neither is set, the server's
// AuthMiddleware skips authentication for all requests.
func authEnabled() bool {
	return backupUser != "" || backupPass != ""
}

// noAuthRequest creates an HTTP request with the given method and path
// WITHOUT attaching any Basic Authentication headers, regardless of the
// configured credentials. The caller is responsible for setting any
// required headers (Content-Type, etc.) and for closing the response body.
func noAuthRequest(method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequest(method, serverURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("create %s %s: %w", method, path, err)
	}
	return req, nil
}

// authenticatedRawRequest creates an HTTP request with the given method,
// path, and raw string body. It sets Content-Type: application/json and
// attaches Basic Auth headers when credentials are configured. This is
// useful for validation tests that need to send raw (possibly invalid)
// request bodies.
//
// The caller MUST close the response body after use.
func authenticatedRawRequest(method, path, body string) (*http.Request, error) {
	req, err := http.NewRequest(method, serverURL+path, strings.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create %s %s: %w", method, path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if backupUser != "" || backupPass != "" {
		req.SetBasicAuth(backupUser, backupPass)
	}
	return req, nil
}

// ---------------------------------------------------------------------------
// Auth tests
//
// These tests verify the server's authentication behaviour. They only
// exercise meaningful assertions when authentication is enabled on the
// target deployment. When running against an unauthenticated development
// deployment all auth-assertion tests are skipped.
// ---------------------------------------------------------------------------

// TestAuth_HealthPublic verifies that the health endpoint is accessible
// without any authentication, regardless of the deployment's auth config.
func TestAuth_HealthPublic(t *testing.T) {
	req, err := noAuthRequest("GET", "/health", nil)
	require.NoError(t, err)

	resp, err := httpClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode,
		"GET /health must always return 200, even without auth")

	var body struct {
		Status string `json:"status"`
	}
	err = json.NewDecoder(resp.Body).Decode(&body)
	require.NoError(t, err, "health response must be valid JSON")
	require.Equal(t, "ok", body.Status,
		`health response must have {"status":"ok"}`)
}

// TestAuth_EndpointsRequireAuth verifies that all non-health API endpoints
// reject unauthenticated requests with 401 when auth is configured.
func TestAuth_EndpointsRequireAuth(t *testing.T) {
	if !authEnabled() {
		t.Skip("auth not enabled — skipping auth-required tests")
	}

	tests := []struct {
		name        string
		method      string
		path        string
		body        io.Reader
		contentType string
	}{
		{
			name:   "POST /uploads",
			method: "POST",
			path:   "/uploads",
			body: bytes.NewReader([]byte(
				`{"local_identifier":"auth-test","filename":"t.jpg","creation_date":"2025-01-01T00:00:00Z"}`)),
			contentType: "application/json",
		},
		{
			name:   "GET /uploads",
			method: "GET",
			path:   "/uploads",
		},
		{
			name:   "GET /uploads/{id}",
			method: "GET",
			path:   "/uploads/" + SafeIDFromLocal("e2e-auth-nonexistent"),
		},
		{
			name:   "HEAD /uploads/{id}/data",
			method: "HEAD",
			path:   "/uploads/" + SafeIDFromLocal("e2e-auth-nonexistent") + "/data",
		},
		{
			name:        "PATCH /uploads/{id}/data",
			method:      "PATCH",
			path:        "/uploads/" + SafeIDFromLocal("e2e-auth-nonexistent") + "/data",
			body:        bytes.NewReader([]byte("test-data")),
			contentType: "application/offset+octet-stream",
		},
		{
			name:        "PATCH /uploads/{id}/status",
			method:      "PATCH",
			path:        "/uploads/" + SafeIDFromLocal("e2e-auth-nonexistent") + "/status",
			body:        bytes.NewReader([]byte(`{"status":"complete"}`)),
			contentType: "application/json",
		},
		{
			name:   "DELETE /uploads/{id}",
			method: "DELETE",
			path:   "/uploads/" + SafeIDFromLocal("e2e-auth-nonexistent"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := noAuthRequest(tt.method, tt.path, tt.body)
			require.NoError(t, err)
			if tt.contentType != "" {
				req.Header.Set("Content-Type", tt.contentType)
			}

			resp, err := httpClient.Do(req)
			require.NoError(t, err)
			resp.Body.Close()

			require.Equal(t, http.StatusUnauthorized, resp.StatusCode,
				"%s %s without auth should return 401", tt.method, tt.path)
		})
	}
}

// TestAuth_InvalidCredentials verifies that requests with incorrect
// credentials receive a 401 response and that the WWW-Authenticate header
// is present in the response.
func TestAuth_InvalidCredentials(t *testing.T) {
	if !authEnabled() {
		t.Skip("auth not enabled — skipping invalid-credentials test")
	}

	tests := []struct {
		name string
		user string
		pass string
	}{
		{"wrong password", backupUser, "wrongpass"},
		{"wrong username", "wronguser", backupPass},
		{"both wrong", "wronguser", "wrongpass"},
		{"empty credentials", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest("GET", serverURL+"/uploads", nil)
			require.NoError(t, err)

			if tt.user != "" || tt.pass != "" {
				req.SetBasicAuth(tt.user, tt.pass)
			}
			// When both are empty, no Authorization header is sent — also 401.

			resp, err := httpClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			require.Equal(t, http.StatusUnauthorized, resp.StatusCode,
				"invalid credentials should return 401")

			// Verify the WWW-Authenticate header.
			wwwAuth := resp.Header.Get("WWW-Authenticate")
			require.NotEmpty(t, wwwAuth,
				"401 response must include WWW-Authenticate header")
			require.Contains(t, wwwAuth, "Basic",
				"WWW-Authenticate must specify Basic scheme")
			require.Contains(t, wwwAuth, "iCloud Backup Server",
				"WWW-Authenticate must include the server realm")

			// Verify the body is a JSON error.
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)

			var errResp struct {
				Error string `json:"error"`
			}
			err = json.Unmarshal(body, &errResp)
			require.NoError(t, err, "401 body must be valid JSON")
			require.NotEmpty(t, errResp.Error,
				"401 body must include an error message")
		})
	}
}

// TestAuth_WWWAuthenticateHeader verifies that a 401 response includes
// a properly formatted WWW-Authenticate header with realm and charset.
func TestAuth_WWWAuthenticateHeader(t *testing.T) {
	if !authEnabled() {
		t.Skip("auth not enabled — skipping WWW-Authenticate test")
	}

	req, err := noAuthRequest("POST", "/uploads",
		bytes.NewReader([]byte(
			`{"local_identifier":"x","filename":"x.jpg","creation_date":"2025-01-01T00:00:00Z"}`)))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	wwwAuth := resp.Header.Get("WWW-Authenticate")
	require.NotEmpty(t, wwwAuth, "401 must include WWW-Authenticate")
	require.Contains(t, wwwAuth, `Basic realm="iCloud Backup Server"`,
		"WWW-Authenticate must declare Basic realm")
	require.Contains(t, wwwAuth, `charset="UTF-8"`,
		"WWW-Authenticate must include charset")
}

// ---------------------------------------------------------------------------
// Request validation tests
//
// These tests verify that the server returns appropriate 400 Bad Request
// responses for malformed inputs. Tests that duplicate scenarios already
// covered in lifecycle_test.go are intentionally omitted.
// ---------------------------------------------------------------------------

// TestValidation_InvalidJSONBody verifies that POST /uploads with
// malformed JSON or a non-object JSON value returns 400.
func TestValidation_InvalidJSONBody(t *testing.T) {
	bodies := []string{
		`{bad json`,
		`{"local_identifier"`,
		`not-json-at-all`,
		`[1, 2, 3]`, // valid JSON, but not an object
		`null`,
		`"just a string"`,
	}

	for _, body := range bodies {
		t.Run(limitString(body, 20), func(t *testing.T) {
			req, err := authenticatedRawRequest("POST", "/uploads", body)
			require.NoError(t, err)

			resp, err := httpClient.Do(req)
			require.NoError(t, err)
			resp.Body.Close()

			require.Equal(t, http.StatusBadRequest, resp.StatusCode,
				"POST /uploads with body %q should return 400", body)
		})
	}
}

// TestValidation_EmptyBody verifies that POST /uploads with an empty
// request body returns 400.
func TestValidation_EmptyBody(t *testing.T) {
	req, err := authenticatedRawRequest("POST", "/uploads", "")
	require.NoError(t, err)

	resp, err := httpClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusBadRequest, resp.StatusCode,
		"POST /uploads with empty body should return 400")
}

// TestValidation_DisallowedFields verifies that POST /uploads rejects
// requests containing unknown JSON fields with 400 (the server uses
// DisallowUnknownFields in its JSON decoder).
func TestValidation_DisallowedFields(t *testing.T) {
	body := `{
		"local_identifier": "disallowed-test",
		"filename": "test.jpg",
		"creation_date": "2025-01-01T00:00:00Z",
		"unknown_field": "should-not-be-allowed"
	}`

	req, err := authenticatedRawRequest("POST", "/uploads", body)
	require.NoError(t, err)

	resp, err := httpClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusBadRequest, resp.StatusCode,
		"POST /uploads with unknown fields should return 400")
}

// TestValidation_InvalidStatusValue verifies that PATCH /uploads/{id}/status
// returns 400 when the status value is not "complete".
func TestValidation_InvalidStatusValue(t *testing.T) {
	localID := MakeLocalIdentifier(t, t.Name())
	cr := CreateTestUpload(t, localID, "test.jpg")

	body := `{"status":"invalid_status"}`
	req, err := authenticatedRawRequest("PATCH", "/uploads/"+cr.ID+"/status", body)
	require.NoError(t, err)

	resp, err := httpClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusBadRequest, resp.StatusCode,
		"PATCH /status with invalid value should return 400")

	// Also verify that the record was not modified.
	rec, status, err := GetUpload(cr.ID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "uploading", rec.Status,
		"upload status must remain unchanged after failed status patch")
}

// TestValidation_InvalidStatusJSON verifies that PATCH /uploads/{id}/status
// returns 400 when the request body is not valid JSON.
func TestValidation_InvalidStatusJSON(t *testing.T) {
	localID := MakeLocalIdentifier(t, t.Name())
	cr := CreateTestUpload(t, localID, "test.jpg")

	// Send non-JSON body.
	req, err := http.NewRequest("PATCH", serverURL+"/uploads/"+cr.ID+"/status",
		bytes.NewReader([]byte(`not-json`)))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	if backupUser != "" || backupPass != "" {
		req.SetBasicAuth(backupUser, backupPass)
	}

	resp, err := httpClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()

	require.Equal(t, http.StatusBadRequest, resp.StatusCode,
		"PATCH /status with invalid JSON should return 400")
}

// TestValidation_StatusDisallowedFields verifies that PATCH
// /uploads/{id}/status returns 400 when the body contains unknown fields.
func TestValidation_StatusDisallowedFields(t *testing.T) {
	localID := MakeLocalIdentifier(t, t.Name())
	cr := CreateTestUpload(t, localID, "test.jpg")

	body := `{"status":"complete","extra_field":"should-not-be-allowed"}`
	req, err := authenticatedRawRequest("PATCH", "/uploads/"+cr.ID+"/status", body)
	require.NoError(t, err)

	resp, err := httpClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()

	require.Equal(t, http.StatusBadRequest, resp.StatusCode,
		"PATCH /status with unknown fields should return 400")
}

// TestValidation_InvalidCursor verifies that GET /uploads with an
// invalid cursor value returns 400.
func TestValidation_InvalidCursor(t *testing.T) {
	resp, err := doRequest("GET", "/uploads?cursor=not-valid-base64!!", nil)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusBadRequest, resp.StatusCode,
		"GET /uploads with invalid cursor should return 400")
}

// TestValidation_InvalidDateRange verifies that GET /uploads with
// a from value that is after the to value returns 400.
func TestValidation_InvalidDateRange(t *testing.T) {
	resp, err := doRequest("GET",
		"/uploads?from=2025-06-01T00:00:00Z&to=2024-01-01T00:00:00Z", nil)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusBadRequest, resp.StatusCode,
		"GET /uploads with from > to should return 400")
}

// TestValidation_InvalidDateFormat verifies that invalid date formats
// in the from/to query parameters return 400.
func TestValidation_InvalidDateFormat(t *testing.T) {
	invalidDates := []string{
		"from=not-a-date",
		"to=also-not-a-date",
		"from=2025/01/01&to=2025/06/01",
		"from=01-01-2025&to=06-01-2025",
	}

	for _, qs := range invalidDates {
		t.Run(limitString(qs, 30), func(t *testing.T) {
			resp, err := doRequest("GET", "/uploads?"+qs, nil)
			require.NoError(t, err)
			defer resp.Body.Close()

			require.Equal(t, http.StatusBadRequest, resp.StatusCode,
				"GET /uploads with invalid date %q should return 400", qs)
		})
	}
}

// TestValidation_HiddenFilename verifies that POST /uploads with a
// filename starting with a dot (hidden file) returns 400.
func TestValidation_HiddenFilename(t *testing.T) {
	localID := MakeLocalIdentifier(t, "hidden-file")
	body := fmt.Sprintf(`{
		"local_identifier": %q,
		"filename": ".hidden",
		"creation_date": "2025-01-01T00:00:00Z"
	}`, localID)

	req, err := authenticatedRawRequest("POST", "/uploads", body)
	require.NoError(t, err)

	resp, err := httpClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusBadRequest, resp.StatusCode,
		"POST /uploads with hidden filename should return 400")
}

// TestValidation_InvalidUploadOffset verifies that PATCH /uploads/{id}/data
// returns 400 when the Upload-Offset header is non-numeric.
func TestValidation_InvalidUploadOffset(t *testing.T) {
	localID := MakeLocalIdentifier(t, t.Name())
	cr := CreateTestUpload(t, localID, "test.jpg")

	req, err := http.NewRequest("PATCH", serverURL+"/uploads/"+cr.ID+"/data",
		bytes.NewReader([]byte("test-data")))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/offset+octet-stream")
	req.Header.Set("Upload-Offset", "not-a-number")
	req.Header.Set("Tus-Resumable", "1.0.0")
	if backupUser != "" || backupPass != "" {
		req.SetBasicAuth(backupUser, backupPass)
	}

	resp, err := httpClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()

	require.Equal(t, http.StatusBadRequest, resp.StatusCode,
		"PATCH /data with non-numeric Upload-Offset should return 400")
}

// TestValidation_PostWithNullByteFilename verifies that POST /uploads
// with a filename containing a null byte returns 400.
func TestValidation_PostWithNullByteFilename(t *testing.T) {
	localID := MakeLocalIdentifier(t, "null-byte")
	// The null byte is represented as \u0000 in JSON, which Go's JSON
	// decoder will translate into an actual null byte in the string.
	body := fmt.Sprintf(`{
		"local_identifier": %q,
		"filename": "bad\u0000file.jpg",
		"creation_date": "2025-01-01T00:00:00Z"
	}`, localID)

	req, err := http.NewRequest("POST", serverURL+"/uploads",
		bytes.NewReader([]byte(body)))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	if backupUser != "" || backupPass != "" {
		req.SetBasicAuth(backupUser, backupPass)
	}

	resp, err := httpClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusBadRequest, resp.StatusCode,
		"POST /uploads with null-byte filename should return 400")
}
