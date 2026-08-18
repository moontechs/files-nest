// Package uploadbackend_test verifies the tusd v2 library API used by the
// upload backend adapter. These tests serve as a verification spike to prove
// exact method names, handler configuration, deferred-length support,
// upload-info access, file path resolution, not-found errors, and termination
// cleanup behavior — as documented in the design decisions.
//
// All tests use real FileStore and MemoryLocker instances against temp
// directories. They compile and run against the imported tusd version.
package uploadbackend_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tus/tusd/v2/pkg/filestore"
	"github.com/tus/tusd/v2/pkg/handler"
	"github.com/tus/tusd/v2/pkg/memorylocker"
)

// setupHandler creates a tusd routed handler backed by a real FileStore +
// MemoryLocker against a temp directory. Returns the handler, the store path,
// and a cleanup function.
func setupHandler(t *testing.T) (http.Handler, string) {
	t.Helper()

	storePath := t.TempDir()

	fs := filestore.New(storePath)
	ml := memorylocker.New()

	composer := handler.NewStoreComposer()
	fs.UseIn(composer)
	ml.UseIn(composer)

	// Disable download, termination is enabled by default
	handlerInstance, err := handler.NewHandler(handler.Config{
		StoreComposer: composer,
		BasePath:      "/",
	})
	if err != nil {
		t.Fatalf("failed to create tusd handler: %v", err)
	}

	return handlerInstance, storePath
}

// httpDo is a convenience that sends an HTTP request to the test server and
// returns the response.
func httpDo(t *testing.T, srv *httptest.Server, req *http.Request) *http.Response {
	t.Helper()
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	return resp
}

// readAll reads the full response body and returns it as a string.
func readAll(t *testing.T, r io.Reader) string {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("failed to read body: %v", err)
	}
	return string(b)
}

// ---------------------------------------------------------------------------
// Test: HandlerConfiguration
// ---------------------------------------------------------------------------

// TestHandlerConfiguration verifies that we can construct a tusd routed
// handler with FileStore and MemoryLocker, and that it responds to OPTIONS
// with the expected tus extensions.
func TestHandlerConfiguration(t *testing.T) {
	h, _ := setupHandler(t)
	srv := httptest.NewServer(h)
	defer srv.Close()

	// OPTIONS request for protocol discovery
	req, err := http.NewRequestWithContext(context.Background(), http.MethodOptions, srv.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Tus-Resumable", "1.0.0")

	resp := httpDo(t, srv, req)
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("OPTIONS returned %d, want 200", resp.StatusCode)
	}

	// Verify tus protocol headers
	if v := resp.Header.Get("Tus-Version"); v != "1.0.0" {
		t.Errorf("Tus-Version = %q, want %q", v, "1.0.0")
	}
	if v := resp.Header.Get("Tus-Resumable"); v != "1.0.0" {
		t.Errorf("Tus-Resumable = %q, want %q", v, "1.0.0")
	}

	ext := resp.Header.Get("Tus-Extension")
	if ext == "" {
		t.Error("Tus-Extension header is empty")
	}
	// Verify known extensions are present
	for _, want := range []string{"creation", "creation-with-upload", "termination", "creation-defer-length"} {
		if !strings.Contains(ext, want) {
			t.Errorf("Tus-Extension missing %q: got %q", want, ext)
		}
	}
}

// ---------------------------------------------------------------------------
// Test: UploadCreation
// ---------------------------------------------------------------------------

// TestUploadCreation verifies that POST creates an upload and returns a 201
// with a Location header pointing to the new resource.
func TestUploadCreation(t *testing.T) {
	h, _ := setupHandler(t)
	srv := httptest.NewServer(h)
	defer srv.Close()

	body := bytes.NewReader(nil)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/", body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Length", "100")

	resp := httpDo(t, srv, req)
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST returned %d, want 201", resp.StatusCode)
	}

	location := resp.Header.Get("Location")
	if location == "" {
		t.Fatal("Location header is empty")
	}
	if !strings.HasPrefix(location, srv.URL+"/") {
		t.Errorf("Location = %q, want prefix %q", location, srv.URL+"/")
	}
}

// TestUploadCreationWithMetadata verifies that Upload-Metadata is preserved
// and accessible via the HEAD response.
func TestUploadCreationWithMetadata(t *testing.T) {
	h, _ := setupHandler(t)
	srv := httptest.NewServer(h)
	defer srv.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Length", "42")
	req.Header.Set("Upload-Metadata", "filename aW1nXzEyMzQuanBn,filetype aW1hZ2UvanBlZw==")

	resp := httpDo(t, srv, req)
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST returned %d, want 201: %s", resp.StatusCode, readAll(t, resp.Body))
	}

	location := resp.Header.Get("Location")
	if location == "" {
		t.Fatal("Location header is empty")
	}

	// HEAD the new resource and verify metadata is preserved
	headReq, err := http.NewRequestWithContext(context.Background(), http.MethodHead, location, nil)
	if err != nil {
		t.Fatal(err)
	}
	headReq.Header.Set("Tus-Resumable", "1.0.0")

	headResp := httpDo(t, srv, headReq)
	defer func() { _ = headResp.Body.Close() }()

	if headResp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD returned %d, want 200: %s", headResp.StatusCode, readAll(t, headResp.Body))
	}

	meta := headResp.Header.Get("Upload-Metadata")
	if !strings.Contains(meta, "filename aW1nXzEyMzQuanBn") {
		t.Errorf("Upload-Metadata missing filename, got %q", meta)
	}
	if !strings.Contains(meta, "filetype aW1hZ2UvanBlZw==") {
		t.Errorf("Upload-Metadata missing filetype, got %q", meta)
	}
}

// ---------------------------------------------------------------------------
// Test: UploadInfo
// ---------------------------------------------------------------------------

// TestUploadInfo verifies that HEAD returns Upload-Offset, Upload-Length, and
// related headers.
func TestUploadInfo(t *testing.T) {
	h, _ := setupHandler(t)
	srv := httptest.NewServer(h)
	defer srv.Close()

	// Create an upload
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Length", "300")

	resp := httpDo(t, srv, req)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST returned %d, want 201", resp.StatusCode)
	}
	location := resp.Header.Get("Location")

	// HEAD the upload
	headReq, err := http.NewRequestWithContext(context.Background(), http.MethodHead, location, nil)
	if err != nil {
		t.Fatal(err)
	}
	headReq.Header.Set("Tus-Resumable", "1.0.0")

	headResp := httpDo(t, srv, headReq)
	defer func() { _ = headResp.Body.Close() }()
	if headResp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD returned %d, want 200: %s", headResp.StatusCode, readAll(t, headResp.Body))
	}

	// Verify offset is 0 (no data written yet)
	offsetStr := headResp.Header.Get("Upload-Offset")
	if offsetStr == "" {
		t.Fatal("Upload-Offset header is missing")
	}
	offset, err := strconv.ParseInt(offsetStr, 10, 64)
	if err != nil {
		t.Fatalf("invalid Upload-Offset %q: %v", offsetStr, err)
	}
	if offset != 0 {
		t.Errorf("Upload-Offset = %d, want 0", offset)
	}

	// Verify Upload-Length
	lengthStr := headResp.Header.Get("Upload-Length")
	if lengthStr == "" {
		t.Fatal("Upload-Length header is missing")
	}
	length, err := strconv.ParseInt(lengthStr, 10, 64)
	if err != nil {
		t.Fatalf("invalid Upload-Length %q: %v", lengthStr, err)
	}
	if length != 300 {
		t.Errorf("Upload-Length = %d, want 300", length)
	}
}

// ---------------------------------------------------------------------------
// Test: UploadChunkWrite
// ---------------------------------------------------------------------------

// TestUploadChunkWrite verifies that PATCH writes data and returns the updated
// Upload-Offset header.
func TestUploadChunkWrite(t *testing.T) {
	h, _ := setupHandler(t)
	srv := httptest.NewServer(h)
	defer srv.Close()

	// Create upload with capacity for 100 bytes
	payload := []byte("hello world, this is a test upload chunk")
	uploadLength := len(payload)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Length", strconv.Itoa(uploadLength))

	resp := httpDo(t, srv, req)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST returned %d, want 201", resp.StatusCode)
	}
	location := resp.Header.Get("Location")

	// PATCH: write the data
	patchReq, err := http.NewRequestWithContext(context.Background(), http.MethodPatch, location, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	patchReq.Header.Set("Tus-Resumable", "1.0.0")
	patchReq.Header.Set("Upload-Offset", "0")
	patchReq.Header.Set("Content-Type", "application/offset+octet-stream")

	patchResp := httpDo(t, srv, patchReq)
	defer func() { _ = patchResp.Body.Close() }()
	if patchResp.StatusCode != http.StatusNoContent {
		t.Fatalf("PATCH returned %d, want 204: %s", patchResp.StatusCode, readAll(t, patchResp.Body))
	}

	// Verify updated offset
	offsetStr := patchResp.Header.Get("Upload-Offset")
	offset, err := strconv.ParseInt(offsetStr, 10, 64)
	if err != nil {
		t.Fatalf("invalid Upload-Offset %q: %v", offsetStr, err)
	}
	if offset != int64(uploadLength) {
		t.Errorf("Upload-Offset = %d after PATCH, want %d", offset, uploadLength)
	}

	// HEAD to verify persisted offset
	headReq, err := http.NewRequestWithContext(context.Background(), http.MethodHead, location, nil)
	if err != nil {
		t.Fatal(err)
	}
	headReq.Header.Set("Tus-Resumable", "1.0.0")

	headResp := httpDo(t, srv, headReq)
	defer func() { _ = headResp.Body.Close() }()
	if headResp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD returned %d, want 200: %s", headResp.StatusCode, readAll(t, headResp.Body))
	}

	headOffsetStr := headResp.Header.Get("Upload-Offset")
	headOffset, err := strconv.ParseInt(headOffsetStr, 10, 64)
	if err != nil {
		t.Fatalf("invalid HEAD Upload-Offset %q: %v", headOffsetStr, err)
	}
	if headOffset != int64(uploadLength) {
		t.Errorf("HEAD Upload-Offset = %d, want %d", headOffset, uploadLength)
	}
}

// TestUploadChunkWriteIncremental verifies that multiple PATCH requests can
// incrementally upload a file.
func TestUploadChunkWriteIncremental(t *testing.T) {
	h, _ := setupHandler(t)
	srv := httptest.NewServer(h)
	defer srv.Close()

	totalLength := 50
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Length", strconv.Itoa(totalLength))
	resp := httpDo(t, srv, req)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST returned %d, want 201", resp.StatusCode)
	}
	location := resp.Header.Get("Location")

	// Write first 20 bytes
	chunk1 := []byte("aaaaaaaaaaaaaaaaaaaa")
	patchReq, err := http.NewRequestWithContext(context.Background(), http.MethodPatch, location, bytes.NewReader(chunk1))
	if err != nil {
		t.Fatal(err)
	}
	patchReq.Header.Set("Tus-Resumable", "1.0.0")
	patchReq.Header.Set("Upload-Offset", "0")
	patchReq.Header.Set("Content-Type", "application/offset+octet-stream")
	patchResp := httpDo(t, srv, patchReq)
	defer func() { _ = patchResp.Body.Close() }()
	if patchResp.StatusCode != http.StatusNoContent {
		t.Fatalf("PATCH1 returned %d, want 204", patchResp.StatusCode)
	}
	offset1, _ := strconv.ParseInt(patchResp.Header.Get("Upload-Offset"), 10, 64)
	if offset1 != 20 {
		t.Errorf("after PATCH1 offset = %d, want 20", offset1)
	}

	// Write remaining 30 bytes
	chunk2 := []byte("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	patchReq2, err := http.NewRequestWithContext(context.Background(), http.MethodPatch, location, bytes.NewReader(chunk2))
	if err != nil {
		t.Fatal(err)
	}
	patchReq2.Header.Set("Tus-Resumable", "1.0.0")
	patchReq2.Header.Set("Upload-Offset", "20")
	patchReq2.Header.Set("Content-Type", "application/offset+octet-stream")
	patchResp2 := httpDo(t, srv, patchReq2)
	defer func() { _ = patchResp2.Body.Close() }()
	if patchResp2.StatusCode != http.StatusNoContent {
		t.Fatalf("PATCH2 returned %d, want 204", patchResp2.StatusCode)
	}
	offset2, _ := strconv.ParseInt(patchResp2.Header.Get("Upload-Offset"), 10, 64)
	if offset2 != 50 {
		t.Errorf("after PATCH2 offset = %d, want 50", offset2)
	}
}

// ---------------------------------------------------------------------------
// Test: FilePathResolution
// ---------------------------------------------------------------------------

// TestFilePathResolution verifies that the filestore creates actual files on
// disk and that the Storage map in FileInfo contains the expected file paths.
func TestFilePathResolution(t *testing.T) {
	h, storePath := setupHandler(t)
	srv := httptest.NewServer(h)
	defer srv.Close()

	// Create an upload
	payload := []byte("file path resolution test data")
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Length", strconv.Itoa(len(payload)))
	resp := httpDo(t, srv, req)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST returned %d, want 201", resp.StatusCode)
	}

	location := resp.Header.Get("Location")
	id := filepath.Base(location)
	if id == "" || id == "." {
		t.Fatalf("could not extract upload ID from Location: %q", location)
	}

	// Write data so the binary file appears on disk
	patchReq, err := http.NewRequestWithContext(context.Background(), http.MethodPatch, location, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	patchReq.Header.Set("Tus-Resumable", "1.0.0")
	patchReq.Header.Set("Upload-Offset", "0")
	patchReq.Header.Set("Content-Type", "application/offset+octet-stream")
	patchResp := httpDo(t, srv, patchReq)
	defer func() { _ = patchResp.Body.Close() }()
	if patchResp.StatusCode != http.StatusNoContent {
		t.Fatalf("PATCH returned %d, want 204", patchResp.StatusCode)
	}

	// Verify the binary file exists on disk
	binPath := filepath.Join(storePath, id)
	if _, err := os.Stat(binPath); os.IsNotExist(err) {
		t.Errorf("binary file not found at %s", binPath)
	}

	// Verify the .info file exists on disk
	infoPath := filepath.Join(storePath, id+".info")
	if _, err := os.Stat(infoPath); os.IsNotExist(err) {
		t.Errorf("info file not found at %s", infoPath)
	}

	// Verify the binary file has the correct content
	//nolint:gosec // test-only read of a temp-dir file, not attacker-controlled
	content, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatalf("failed to read binary file: %v", err)
	}
	if !bytes.Equal(content, payload) {
		t.Errorf("binary file content mismatch: got %d bytes, want %d bytes", len(content), len(payload))
	}
}

// ---------------------------------------------------------------------------
// Test: NotFoundError
// ---------------------------------------------------------------------------

// TestNotFoundError verifies that tusd returns 404 (handler.ErrNotFound) for
// HEAD and PATCH requests targeting non-existent upload IDs.
func TestNotFoundError_Head(t *testing.T) {
	h, _ := setupHandler(t)
	srv := httptest.NewServer(h)
	defer srv.Close()

	// HEAD a non-existent upload
	req, err := http.NewRequestWithContext(context.Background(), http.MethodHead, srv.URL+"/nonexistent-upload-id", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Tus-Resumable", "1.0.0")

	resp := httpDo(t, srv, req)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("HEAD nonexistent returned %d, want 404", resp.StatusCode)
	}

	// tusd intentionally omits the response body for HEAD requests (even error responses),
	// so we only check the status code here.
}

func TestNotFoundError_Patch(t *testing.T) {
	h, _ := setupHandler(t)
	srv := httptest.NewServer(h)
	defer srv.Close()

	// PATCH a non-existent upload
	req, err := http.NewRequestWithContext(
		context.Background(), http.MethodPatch, srv.URL+"/nonexistent-upload-id", bytes.NewReader([]byte("data")))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Offset", "0")
	req.Header.Set("Content-Type", "application/offset+octet-stream")

	resp := httpDo(t, srv, req)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("PATCH nonexistent returned %d, want 404: %s", resp.StatusCode, readAll(t, resp.Body))
	}

	body := readAll(t, resp.Body)
	if !strings.Contains(body, "ERR_UPLOAD_NOT_FOUND") {
		t.Errorf("response body should contain ERR_UPLOAD_NOT_FOUND, got %q", body)
	}
}

func TestNotFoundError_Delete(t *testing.T) {
	h, _ := setupHandler(t)
	srv := httptest.NewServer(h)
	defer srv.Close()

	// DELETE a non-existent upload
	req, err := http.NewRequestWithContext(context.Background(), http.MethodDelete, srv.URL+"/nonexistent-upload-id", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Tus-Resumable", "1.0.0")

	resp := httpDo(t, srv, req)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("DELETE nonexistent returned %d, want 404: %s", resp.StatusCode, readAll(t, resp.Body))
	}
}

// ---------------------------------------------------------------------------
// Test: UploadTermination
// ---------------------------------------------------------------------------

// TestUploadTerminationCleanup verifies that DELETE terminates an upload and
// removes both the binary file and the .info file from disk.
func TestUploadTerminationCleanup(t *testing.T) {
	h, storePath := setupHandler(t)
	srv := httptest.NewServer(h)
	defer srv.Close()

	// Create upload and write some data
	payload := []byte("data to be cleaned up")
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Length", strconv.Itoa(len(payload)))
	resp := httpDo(t, srv, req)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST returned %d, want 201", resp.StatusCode)
	}
	location := resp.Header.Get("Location")
	id := filepath.Base(location)

	// Write data
	patchReq, err := http.NewRequestWithContext(context.Background(), http.MethodPatch, location, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	patchReq.Header.Set("Tus-Resumable", "1.0.0")
	patchReq.Header.Set("Upload-Offset", "0")
	patchReq.Header.Set("Content-Type", "application/offset+octet-stream")
	patchResp := httpDo(t, srv, patchReq)
	defer func() { _ = patchResp.Body.Close() }()
	if patchResp.StatusCode != http.StatusNoContent {
		t.Fatalf("PATCH returned %d, want 204", patchResp.StatusCode)
	}

	// Verify files exist before termination
	binPath := filepath.Join(storePath, id)
	infoPath := filepath.Join(storePath, id+".info")
	if _, err := os.Stat(binPath); os.IsNotExist(err) {
		t.Fatal("binary file should exist before DELETE")
	}
	if _, err := os.Stat(infoPath); os.IsNotExist(err) {
		t.Fatal("info file should exist before DELETE")
	}

	// DELETE the upload
	delReq, err := http.NewRequestWithContext(context.Background(), http.MethodDelete, location, nil)
	if err != nil {
		t.Fatal(err)
	}
	delReq.Header.Set("Tus-Resumable", "1.0.0")

	delResp := httpDo(t, srv, delReq)
	defer func() { _ = delResp.Body.Close() }()
	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE returned %d, want 204: %s", delResp.StatusCode, readAll(t, delResp.Body))
	}

	// Verify files are removed from disk
	if _, err := os.Stat(binPath); !os.IsNotExist(err) {
		t.Errorf("binary file should be removed after DELETE, stat err = %v", err)
	}
	if _, err := os.Stat(infoPath); !os.IsNotExist(err) {
		t.Errorf("info file should be removed after DELETE, stat err = %v", err)
	}

	// Verify HEAD on terminated upload returns 404
	headReq, err := http.NewRequestWithContext(context.Background(), http.MethodHead, location, nil)
	if err != nil {
		t.Fatal(err)
	}
	headReq.Header.Set("Tus-Resumable", "1.0.0")
	headResp := httpDo(t, srv, headReq)
	defer func() { _ = headResp.Body.Close() }()
	if headResp.StatusCode != http.StatusNotFound {
		t.Errorf("HEAD after DELETE returned %d, want 404: %s", headResp.StatusCode, readAll(t, headResp.Body))
	}
}

// ---------------------------------------------------------------------------
// Test: DeferredLengthUpload
// ---------------------------------------------------------------------------

// TestDeferredLengthUpload verifies that creating an upload with
// Upload-Defer-Length: 1 succeeds, and that the length can be declared later
// via the Upload-Length header in a PATCH request.
//
//nolint:cyclop,funlen // TUS protocol flow test; complexity/statements reflect asserted steps
func TestDeferredLengthUpload(t *testing.T) {
	h, _ := setupHandler(t)
	srv := httptest.NewServer(h)
	defer srv.Close()

	// Create upload with deferred length
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Defer-Length", "1")

	resp := httpDo(t, srv, req)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST (deferred) returned %d, want 201: %s", resp.StatusCode, readAll(t, resp.Body))
	}

	location := resp.Header.Get("Location")
	if location == "" {
		t.Fatal("Location header is empty")
	}

	// HEAD should report Upload-Defer-Length: 1
	headReq, err := http.NewRequestWithContext(context.Background(), http.MethodHead, location, nil)
	if err != nil {
		t.Fatal(err)
	}
	headReq.Header.Set("Tus-Resumable", "1.0.0")
	headResp := httpDo(t, srv, headReq)
	defer func() { _ = headResp.Body.Close() }()
	if headResp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD after deferred POST returned %d, want 200: %s", headResp.StatusCode, readAll(t, headResp.Body))
	}

	if v := headResp.Header.Get("Upload-Defer-Length"); v != "1" {
		t.Errorf("Upload-Defer-Length = %q, want %q", v, "1")
	}
	if v := headResp.Header.Get("Upload-Length"); v != "" {
		t.Errorf("Upload-Length should be empty for deferred upload, got %q", v)
	}

	// Write some data
	payload := []byte("deferred upload data")
	patchReq, err := http.NewRequestWithContext(context.Background(), http.MethodPatch, location, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	patchReq.Header.Set("Tus-Resumable", "1.0.0")
	patchReq.Header.Set("Upload-Offset", "0")
	patchReq.Header.Set("Content-Type", "application/offset+octet-stream")
	patchReq.Header.Set("Upload-Length", strconv.Itoa(len(payload)))

	patchResp := httpDo(t, srv, patchReq)
	defer func() { _ = patchResp.Body.Close() }()
	if patchResp.StatusCode != http.StatusNoContent {
		t.Fatalf("PATCH (declaring length) returned %d, want 204: %s", patchResp.StatusCode, readAll(t, patchResp.Body))
	}

	// Verify offset equals payload length (upload complete)
	offsetStr := patchResp.Header.Get("Upload-Offset")
	offset, err := strconv.ParseInt(offsetStr, 10, 64)
	if err != nil {
		t.Fatalf("invalid Upload-Offset %q: %v", offsetStr, err)
	}
	if offset != int64(len(payload)) {
		t.Errorf("Upload-Offset after deferred PATCH = %d, want %d", offset, len(payload))
	}

	// HEAD should now have Upload-Length set and no Upload-Defer-Length
	headReq2, err := http.NewRequestWithContext(context.Background(), http.MethodHead, location, nil)
	if err != nil {
		t.Fatal(err)
	}
	headReq2.Header.Set("Tus-Resumable", "1.0.0")
	headResp2 := httpDo(t, srv, headReq2)
	defer func() { _ = headResp2.Body.Close() }()
	if headResp2.StatusCode != http.StatusOK {
		t.Fatalf("HEAD after completion returned %d, want 200: %s", headResp2.StatusCode, readAll(t, headResp2.Body))
	}

	if v := headResp2.Header.Get("Upload-Defer-Length"); v != "" {
		t.Errorf("Upload-Defer-Length should be empty after length declared, got %q", v)
	}
	if v := headResp2.Header.Get("Upload-Length"); v != strconv.Itoa(len(payload)) {
		t.Errorf("Upload-Length = %q, want %q", v, strconv.Itoa(len(payload)))
	}
}

// TestDeferredLengthWithoutStoreRejected verifies that creating a
// deferred-length upload fails when the data store does not support it.
func TestDeferredLength_NotSupported(t *testing.T) {
	storePath := t.TempDir()
	fs := filestore.New(storePath)
	composer := handler.NewStoreComposer()
	composer.UseCore(fs)
	// Do NOT register LengthDeferrer

	h, err := handler.NewHandler(handler.Config{
		StoreComposer: composer,
		BasePath:      "/",
	})
	if err != nil {
		t.Fatalf("failed to create handler without LengthDeferrer: %v", err)
	}

	srv := httptest.NewServer(h)
	defer srv.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Defer-Length", "1")

	resp := httpDo(t, srv, req)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("POST with defer-length on unsupported store returned %d, want 501: %s",
			resp.StatusCode, readAll(t, resp.Body))
	}
}

// ---------------------------------------------------------------------------
// Test: ErrNotFoundSentinel
// ---------------------------------------------------------------------------

// TestErrNotFoundSentinel verifies that the handler.ErrNotFound sentinel error
// is used by the filestore when an upload is not found. This confirms that our
// uploadbackend adapter can use errors.Is(err, handler.ErrNotFound) to detect
// "not found" conditions.
func TestErrNotFoundSentinel(t *testing.T) {
	storePath := t.TempDir()
	fs := filestore.New(storePath)

	// GetUpload for a non-existent ID should return handler.ErrNotFound
	_, err := fs.GetUpload(context.TODO(), "does-not-exist")
	if err == nil {
		t.Fatal("expected error from GetUpload for non-existent ID")
	}

	// Verify it matches using errors.Is
	if !errors.Is(err, handler.ErrNotFound) {
		t.Errorf("got error %v (%T), want handler.ErrNotFound", err, err)
	}
}

// ---------------------------------------------------------------------------
// Test: PATCHWithMismatchedOffset
// ---------------------------------------------------------------------------

// TestPatchMismatchedOffset verifies that PATCH with an incorrect
// Upload-Offset returns 409 Conflict.
func TestPatchMismatchedOffset(t *testing.T) {
	h, _ := setupHandler(t)
	srv := httptest.NewServer(h)
	defer srv.Close()

	// Create upload
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Length", "100")
	resp := httpDo(t, srv, req)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST returned %d, want 201", resp.StatusCode)
	}
	location := resp.Header.Get("Location")

	// PATCH with wrong offset
	patchReq, err := http.NewRequestWithContext(
		context.Background(), http.MethodPatch, location, bytes.NewReader([]byte("data")))
	if err != nil {
		t.Fatal(err)
	}
	patchReq.Header.Set("Tus-Resumable", "1.0.0")
	patchReq.Header.Set("Upload-Offset", "42") // wrong offset
	patchReq.Header.Set("Content-Type", "application/offset+octet-stream")

	patchResp := httpDo(t, srv, patchReq)
	defer func() { _ = patchResp.Body.Close() }()
	if patchResp.StatusCode != http.StatusConflict {
		t.Errorf("PATCH with mismatched offset returned %d, want 409: %s",
			patchResp.StatusCode, readAll(t, patchResp.Body))
	}
}

// ---------------------------------------------------------------------------
// Test: FileStoreUsesTerminater
// ---------------------------------------------------------------------------

// TestFileStoreUsesTerminater verifies that FileStore implements
// TerminaterDataStore, so DELETE requests are routed correctly. This also
// verifies the StoreComposer.UseTerminater pattern.
func TestFileStoreUsesTerminater(t *testing.T) {
	storePath := t.TempDir()
	fs := filestore.New(storePath)
	composer := handler.NewStoreComposer()
	fs.UseIn(composer)

	if !composer.UsesTerminater {
		t.Fatal("FileStore should register as TerminaterDataStore via UseIn")
	}
	//nolint:misspell // Terminater is the upstream tusd API name, not a typo
	if composer.Terminater == nil {
		//nolint:misspell // Terminater is the upstream tusd API name, not a typo
		t.Fatal("Terminater should be non-nil after UseIn")
	}
}

// ---------------------------------------------------------------------------
// Test: PreUploadCreateCallback
// ---------------------------------------------------------------------------

// TestPreUploadCreateCallback verifies that the PreUploadCreateCallback can
// influence the upload ID and metadata during creation. This is the hook
// mechanism we will use for assigning deterministic server IDs.
func TestPreUploadCreateCallback(t *testing.T) {
	storePath := t.TempDir()
	fs := filestore.New(storePath)
	ml := memorylocker.New()

	composer := handler.NewStoreComposer()
	fs.UseIn(composer)
	ml.UseIn(composer)

	h, err := handler.NewHandler(handler.Config{
		StoreComposer: composer,
		BasePath:      "/",
		PreUploadCreateCallback: func(_ handler.HookEvent) (handler.HTTPResponse, handler.FileInfoChanges, error) {
			// Override the upload ID and metadata
			changes := handler.FileInfoChanges{
				ID: "my-custom-id-42",
				MetaData: handler.MetaData{
					"server_assigned": "true",
				},
			}
			return handler.HTTPResponse{}, changes, nil
		},
	})
	if err != nil {
		t.Fatalf("failed to create handler with PreUploadCreateCallback: %v", err)
	}

	srv := httptest.NewServer(h)
	defer srv.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Length", "50")
	req.Header.Set("Upload-Metadata", "original meta b3JpZ2luYWw=")

	resp := httpDo(t, srv, req)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST returned %d, want 201: %s", resp.StatusCode, readAll(t, resp.Body))
	}

	location := resp.Header.Get("Location")
	if !strings.HasSuffix(location, "/my-custom-id-42") {
		t.Errorf("Location = %q, want suffix %q", location, "/my-custom-id-42")
	}

	// HEAD the upload and verify overridden metadata
	headReq, err := http.NewRequestWithContext(context.Background(), http.MethodHead, location, nil)
	if err != nil {
		t.Fatal(err)
	}
	headReq.Header.Set("Tus-Resumable", "1.0.0")
	headResp := httpDo(t, srv, headReq)
	defer func() { _ = headResp.Body.Close() }()
	if headResp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD returned %d, want 200: %s", headResp.StatusCode, readAll(t, headResp.Body))
	}

	meta := headResp.Header.Get("Upload-Metadata")
	if !strings.Contains(meta, "server_assigned dHJ1ZQ==") {
		t.Errorf("Upload-Metadata should contain server_assigned, got %q", meta)
	}
	// Original metadata should be replaced (since we set MetaData non-nil)
	if strings.Contains(meta, "original") {
		t.Errorf("Upload-Metadata should NOT contain original metadata after override, got %q", meta)
	}
}

// ---------------------------------------------------------------------------
// Test: NotifyCompleteUploads
// ---------------------------------------------------------------------------

// TestNotifyCompleteUploads verifies that when NotifyCompleteUploads is enabled,
// the CompleteUploads channel receives a HookEvent after an upload finishes.
func TestNotifyCompleteUploads(t *testing.T) {
	storePath := t.TempDir()
	fs := filestore.New(storePath)
	ml := memorylocker.New()

	composer := handler.NewStoreComposer()
	fs.UseIn(composer)
	ml.UseIn(composer)

	h, err := handler.NewHandler(handler.Config{
		StoreComposer:         composer,
		BasePath:              "/",
		NotifyCompleteUploads: true,
	})
	if err != nil {
		t.Fatalf("failed to create handler: %v", err)
	}

	// Replace the unbuffered CompleteUploads channel with a buffered one so the
	// handler can send without blocking on the HTTP handler goroutine.
	h.CompleteUploads = make(chan handler.HookEvent, 1)

	srv := httptest.NewServer(h)
	defer srv.Close()

	// Create upload with 0 bytes (immediately complete)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Length", "0")

	resp := httpDo(t, srv, req)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST returned %d, want 201: %s", resp.StatusCode, readAll(t, resp.Body))
	}

	// Read completion notification
	select {
	case event := <-h.CompleteUploads:
		if event.Upload.Size != 0 {
			t.Errorf("completed upload Size = %d, want 0", event.Upload.Size)
		}
		if event.Upload.Offset != 0 {
			t.Errorf("completed upload Offset = %d, want 0", event.Upload.Offset)
		}
		if event.HTTPRequest.Method != http.MethodPost {
			t.Errorf("event HTTP method = %q, want %q", event.HTTPRequest.Method, "POST")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for completion notification")
	}
}

// ---------------------------------------------------------------------------
// Test: LargeUploadStress
// ---------------------------------------------------------------------------

// TestLargeUploadStress writes a larger payload to exercise the filestore and
// verify that offset tracking works correctly for multi-kilobyte uploads.
func TestLargeUploadStress(t *testing.T) {
	h, _ := setupHandler(t)
	srv := httptest.NewServer(h)
	defer srv.Close()

	size := 64 * 1024 // 64 KB
	payload := make([]byte, size)
	_, err := rand.Read(payload)
	if err != nil {
		t.Fatal(err)
	}

	// Create
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Length", strconv.Itoa(size))
	resp := httpDo(t, srv, req)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST returned %d, want 201", resp.StatusCode)
	}
	location := resp.Header.Get("Location")

	// Upload in a single PATCH
	patchReq, err := http.NewRequestWithContext(context.Background(), http.MethodPatch, location, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	patchReq.Header.Set("Tus-Resumable", "1.0.0")
	patchReq.Header.Set("Upload-Offset", "0")
	patchReq.Header.Set("Content-Type", "application/offset+octet-stream")

	patchResp := httpDo(t, srv, patchReq)
	defer func() { _ = patchResp.Body.Close() }()
	if patchResp.StatusCode != http.StatusNoContent {
		t.Fatalf("PATCH returned %d, want 204: %s", patchResp.StatusCode, readAll(t, patchResp.Body))
	}

	offsetStr := patchResp.Header.Get("Upload-Offset")
	offset, err := strconv.ParseInt(offsetStr, 10, 64)
	if err != nil {
		t.Fatalf("invalid Upload-Offset %q: %v", offsetStr, err)
	}
	if offset != int64(size) {
		t.Errorf("Upload-Offset = %d, want %d", offset, size)
	}
}

// ---------------------------------------------------------------------------
// Test: GET returns not found for non-existent upload
// ---------------------------------------------------------------------------

// TestGetFileNotFound verifies GET on a non-existent upload returns 404 when
// download is enabled.
func TestGetFileNotFound(t *testing.T) {
	h, _ := setupHandler(t)
	srv := httptest.NewServer(h)
	defer srv.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/nonexistent-upload-id", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Tus-Resumable", "1.0.0")

	resp := httpDo(t, srv, req)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET nonexistent returned %d, want 404: %s", resp.StatusCode, readAll(t, resp.Body))
	}
}

// ---------------------------------------------------------------------------
// Test: Disable termination
// ---------------------------------------------------------------------------

// TestDisableTermination verifies that when DisableTermination is set, DELETE
// returns 405 Method Not Allowed instead of routing to DelFile.
func TestDisableTermination(t *testing.T) {
	storePath := t.TempDir()
	fs := filestore.New(storePath)
	ml := memorylocker.New()

	composer := handler.NewStoreComposer()
	fs.UseIn(composer)
	ml.UseIn(composer)

	h, err := handler.NewHandler(handler.Config{
		StoreComposer:      composer,
		BasePath:           "/",
		DisableTermination: true,
	})
	if err != nil {
		t.Fatalf("failed to create handler with DisableTermination: %v", err)
	}

	srv := httptest.NewServer(h)
	defer srv.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodDelete, srv.URL+"/some-id", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Tus-Resumable", "1.0.0")

	resp := httpDo(t, srv, req)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("DELETE with DisableTermination returned %d, want 405: %s",
			resp.StatusCode, readAll(t, resp.Body))
	}
}
