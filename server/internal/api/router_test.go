package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moontechs/files-nest/server/internal/api"
	"github.com/moontechs/files-nest/server/internal/store"
	"github.com/moontechs/files-nest/server/internal/uploadbackend"
)

// newRouterForTest builds a router wired to a temp store + backend with auth
// disabled (both credentials empty) so handlers are reachable in tests. It uses
// a deliberately large concurrency cap (1000) so existing router/handler tests
// aren't incidentally rate-limited. See newRouterWithLimiterForTest for tests
// that need to exercise a specific cap.
func newRouterForTest(t *testing.T) http.Handler {
	return newRouterWithLimiterForTest(t, api.NewConcurrencyLimiter(1000))
}

// newRouterWithLimiterForTest is like newRouterForTest but wires the router
// with the given concurrency limiter, so tests can exercise the real NewRouter
// under a bounded (or otherwise custom) cap.
func newRouterWithLimiterForTest(t *testing.T, limiter *api.ConcurrencyLimiter) http.Handler {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	bk, err := uploadbackend.New(dir)
	if err != nil {
		t.Fatalf("uploadbackend.New: %v", err)
	}
	h := api.NewHandler(st, bk, dir)
	return api.NewRouter(h, api.AuthConfig{}, limiter)
}

// TestRouter_HealthEndpoint verifies the unauthenticated health endpoint
// returns 200 with a JSON ok body and does not require credentials.
func TestRouter_HealthEndpoint(t *testing.T) {
	router := newRouterForTest(t)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != jsonContentType {
		t.Errorf("health Content-Type = %q, want application/json", ct)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("health body not JSON: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("health body = %v, want status=ok", body)
	}
}

// TestRouter_AuthDisabledWhenCredsEmpty verifies that when both credentials
// are empty, API routes are reachable without an Authorization header.
func TestRouter_AuthDisabledWhenCredsEmpty(t *testing.T) {
	router := newRouterForTest(t)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/uploads", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("expected auth disabled (no 401), got 401: %s", rec.Body.String())
	}
}

// createUploadViaRouter creates an upload through the real router (POST
// /uploads) and returns its server ID.
func createUploadViaRouter(t *testing.T, router http.Handler, localID string) string {
	t.Helper()
	body := fmt.Sprintf(`{
		"local_identifier": %q,
		"filename": "IMG_0001.jpg",
		"creation_date": "2024-06-15T08:30:00Z"
	}`, localID)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/uploads", strings.NewReader(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("create upload %s: expected 201/200, got %d: %s", localID, rec.Code, rec.Body.String())
	}
	var resp api.CreateUploadResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode create upload response: %v", err)
	}
	return resp.ID
}

// blockingPatchBody is an io.Reader for a PATCH /uploads/{id}/data body that,
// on its first Read, signals that the request has reached the handler body
// read (and therefore holds a concurrency slot) and then blocks until release
// is closed. This forces genuine in-flight overlap through the real router
// instead of relying on goroutine scheduling.
func blockingPatchBody(entered chan<- struct{}, release <-chan struct{}) io.Reader {
	return &blockingReader{entered: entered, release: release}
}

type blockingReader struct {
	entered chan<- struct{}
	release <-chan struct{}
	done    bool
}

func (b *blockingReader) Read(p []byte) (int, error) {
	if !b.done {
		b.done = true
		b.entered <- struct{}{}
		<-b.release
	}
	return 0, io.EOF
}

// TestRouter_ConcurrencyLimitAppliedToPatchData verifies the wiring: when the
// real NewRouter is constructed with a bounded limiter, >cap concurrent PATCH
// /uploads/{id}/data requests are rejected with 503 + Retry-After, while at-cap
// requests are admitted. This confirms the limiter is actually applied to the
// route, not just unit-tested in isolation.
func TestRouter_ConcurrencyLimitAppliedToPatchData(t *testing.T) {
	const cap = 2
	router := newRouterWithLimiterForTest(t, api.NewConcurrencyLimiter(cap))

	// Create cap+1 distinct uploads via the router so each held PATCH runs
	// against a real (distinct) upload and the handler would succeed if allowed.
	ids := make([]string, 0, cap+1)
	for i := 0; i < cap+1; i++ {
		ids = append(ids, createUploadViaRouter(t, router, fmt.Sprintf("WIRE-%d/L0/000", i)))
	}

	// Fire cap concurrent PATCHes whose bodies block after entering, holding
	// every concurrency slot open.
	entered := make(chan struct{}, cap)
	release := make(chan struct{})

	var wg sync.WaitGroup
	heldCodes := make([]int, cap)
	heldBodies := make([]io.Reader, cap)
	for i := 0; i < cap; i++ {
		heldBodies[i] = blockingPatchBody(entered, release)
	}
	for i := 0; i < cap; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := httptest.NewRequestWithContext(
				context.Background(), http.MethodPatch, "/uploads/"+ids[i]+"/data", heldBodies[i])
			req.Header.Set("Content-Type", "application/offset+octet-stream")
			req.Header.Set("Tus-Resumable", "1.0.0")
			req.Header.Set("Upload-Offset", "0")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			heldCodes[i] = rec.Code
		}(i)
	}

	// Wait until all cap requests have entered the handler (slots occupied).
	for i := 0; i < cap; i++ {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for held PATCH requests to reach the handler")
		}
	}

	// The (cap+1)th concurrent PATCH must be rejected by the router's limiter.
	overReq := httptest.NewRequestWithContext(
		context.Background(), http.MethodPatch, "/uploads/"+ids[cap]+"/data", strings.NewReader("data"))
	overReq.Header.Set("Content-Type", "application/offset+octet-stream")
	overReq.Header.Set("Tus-Resumable", "1.0.0")
	overReq.Header.Set("Upload-Offset", "0")
	overRec := httptest.NewRecorder()
	router.ServeHTTP(overRec, overReq)
	if overRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("over-cap PATCH status = %d, want 503 (body: %s)", overRec.Code, overRec.Body.String())
	}
	if ra := overRec.Header().Get("Retry-After"); ra != "1" {
		t.Errorf("over-cap Retry-After = %q, want \"1\"", ra)
	}

	// Release the held requests; all at-cap requests should complete with 204.
	close(release)
	wg.Wait()
	for i, code := range heldCodes {
		if code != http.StatusNoContent {
			t.Errorf("held request %d status = %d, want 204", i, code)
		}
	}
}

// TestRouter_ConfigEndpoint verifies the authenticated GET /config endpoint
// returns the limiter's cap as {maxConcurrentUploads:<n>} with JSON content.
func TestRouter_ConfigEndpoint(t *testing.T) {
	const cap = 4
	limiter := api.NewConcurrencyLimiter(cap)
	router := newRouterWithLimiterForTest(t, limiter)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/config", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != jsonContentType {
		t.Errorf("config Content-Type = %q, want application/json", ct)
	}
	var body struct {
		MaxConcurrentUploads int `json:"maxConcurrentUploads"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("config body not JSON: %v", err)
	}
	if body.MaxConcurrentUploads != cap {
		t.Errorf("config maxConcurrentUploads = %d, want %d", body.MaxConcurrentUploads, cap)
	}
}

// TestRouter_ConfigEndpointReflectsPassedLimiter verifies the value served by
// /config matches the limiter actually passed to NewRouter, not a hardcoded
// constant.
func TestRouter_ConfigEndpointReflectsPassedLimiter(t *testing.T) {
	router := newRouterWithLimiterForTest(t, api.NewConcurrencyLimiter(17))

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/config", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		MaxConcurrentUploads int `json:"maxConcurrentUploads"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("config body not JSON: %v", err)
	}
	if body.MaxConcurrentUploads != 17 {
		t.Errorf("config maxConcurrentUploads = %d, want 17", body.MaxConcurrentUploads)
	}
}

// TestRouter_ConfigRequiresAuth verifies GET /config is protected by the same
// Basic Auth as the other API routes: unauthenticated requests get 401.
func TestRouter_ConfigRequiresAuth(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	bk, err := uploadbackend.New(dir)
	if err != nil {
		t.Fatalf("uploadbackend.New: %v", err)
	}
	h := api.NewHandler(st, bk, dir)
	router := api.NewRouter(h, api.AuthConfig{Username: testUsername, Password: testPassword},
		api.NewConcurrencyLimiter(4))

	// No credentials -> 401.
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/config", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without credentials, got %d: %s", rec.Code, rec.Body.String())
	}

	// Wrong credentials -> 401.
	req.SetBasicAuth("admin", "wrong")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with wrong credentials, got %d: %s", rec.Code, rec.Body.String())
	}

	// Correct credentials -> 200.
	req.SetBasicAuth(testUsername, testPassword)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with valid credentials, got %d: %s", rec.Code, rec.Body.String())
	}
}
