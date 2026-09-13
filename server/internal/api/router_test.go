package api_test

import (
	"context"
	"crypto/tls"
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

// testRouterVersion is the version string wired into routers built by
// newRouterForTest/newRouterWithLimiterForTest so status-page assertions check
// against a known value rather than the build-time default.
const testRouterVersion = "0.3.1-test"

// newRouterForTest builds a router wired to a temp store + backend with auth
// disabled (both credentials empty) so handlers are reachable in tests. It uses
// a deliberately large concurrency cap (1000) so existing router/handler tests
// aren't incidentally rate-limited. See newRouterWithLimiterForTest for tests
// that need to exercise a specific cap.
func newRouterForTest(t *testing.T) http.Handler {
	t.Helper()

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
	return api.NewRouter(h, api.AuthConfig{}, limiter, testRouterVersion)
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
	return &blockingReader{entered: entered, release: release, done: false}
}

type blockingReader struct {
	entered chan<- struct{}
	release <-chan struct{}
	done    bool
}

func (b *blockingReader) Read(_ []byte) (int, error) {
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
	const maxConcurrent = 2
	router := newRouterWithLimiterForTest(t, api.NewConcurrencyLimiter(maxConcurrent))

	// Create maxConcurrent+1 distinct uploads via the router so each held PATCH runs
	// against a real (distinct) upload and the handler would succeed if allowed.
	ids := make([]string, 0, maxConcurrent+1)
	for i := range maxConcurrent + 1 {
		ids = append(ids, createUploadViaRouter(t, router, fmt.Sprintf("WIRE-%d/L0/000", i)))
	}

	// Fire maxConcurrent concurrent PATCHes whose bodies block after entering, holding
	// every concurrency slot open.
	entered := make(chan struct{}, maxConcurrent)
	release := make(chan struct{})

	var wg sync.WaitGroup
	heldCodes := make([]int, maxConcurrent)
	heldBodies := make([]io.Reader, maxConcurrent)
	for i := range maxConcurrent {
		heldBodies[i] = blockingPatchBody(entered, release)
	}
	for i := range maxConcurrent {
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

	// Wait until all maxConcurrent requests have entered the handler (slots occupied).
	for range maxConcurrent {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for held PATCH requests to reach the handler")
		}
	}

	// The (maxConcurrent+1)th concurrent PATCH must be rejected by the router's limiter.
	overReq := httptest.NewRequestWithContext(
		context.Background(), http.MethodPatch, "/uploads/"+ids[maxConcurrent]+"/data", strings.NewReader("data"))
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

// TestRouter_StatusPage verifies the unauthenticated GET / status page renders
// through the real router: 200, HTML content type, the version threaded into
// NewRouter, the request's Host header as the address, and the auth-warning
// card when auth is disabled (newRouterForTest builds an auth-disabled router).
func TestRouter_StatusPage(t *testing.T) {
	router := newRouterForTest(t)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	req.Host = "backup.example.com:8080"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("GET / Content-Type = %q, want text/html", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"FilesNest — Server is running",
		testRouterVersion,
		"http://backup.example.com:8080",
		"Authentication disabled",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("GET / body does not contain %q", want)
		}
	}
}

// TestRouter_StatusPageAddressScheme verifies GET / renders the address with
// https when the request arrived over TLS or via a reverse proxy that set
// X-Forwarded-Proto, and defaults to http otherwise.
func TestRouter_StatusPageAddressScheme(t *testing.T) {
	tests := []struct {
		name       string
		forwarded  string
		tls        bool
		wantPrefix string
	}{
		{name: "plain http", forwarded: "", tls: false, wantPrefix: "http://"},
		{name: "TLS connection", forwarded: "", tls: true, wantPrefix: "https://"},
		{name: "X-Forwarded-Proto https", forwarded: "https", tls: false, wantPrefix: "https://"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := newRouterForTest(t)

			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
			req.Host = "backup.example.com:8080"
			if tt.forwarded != "" {
				req.Header.Set("X-Forwarded-Proto", tt.forwarded)
			}
			if tt.tls {
				//nolint:exhaustruct // test-only stand-in; only presence of *tls.ConnectionState matters
				req.TLS = &tls.ConnectionState{}
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			want := tt.wantPrefix + "backup.example.com:8080"
			if !strings.Contains(rec.Body.String(), want) {
				t.Errorf("GET / body does not contain %q", want)
			}
		})
	}
}

// TestRouter_StatusPageAuthConfigured verifies GET / stays reachable with no
// credentials when Basic Auth is configured (warning card absent), while the
// API routes keep requiring auth — the regression check that wiring the new
// unauthenticated route did not touch AuthMiddleware elsewhere.
func TestRouter_StatusPageAuthConfigured(t *testing.T) {
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
		api.NewConcurrencyLimiter(4), testRouterVersion)

	// The status page must render without credentials even when auth is on.
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	req.Host = "status.example.com"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET / with auth configured = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), testRouterVersion) {
		t.Error("GET / body does not contain the configured version")
	}
	if strings.Contains(rec.Body.String(), "Authentication disabled") {
		t.Error("GET / with auth configured unexpectedly shows the auth-warning card")
	}

	// Regression: other routes must still 401 without credentials.
	req = httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/config", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET /config without credentials = %d, want 401: %s", rec.Code, rec.Body.String())
	}
}

// TestRouter_StatusPageNotCatchAll verifies GET /{$} does not swallow undefined
// paths: an unknown GET route must 404 rather than render the status page,
// which a plain "GET /" subtree pattern would have turned into a 200.
func TestRouter_StatusPageNotCatchAll(t *testing.T) {
	router := newRouterForTest(t)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/nonexistent", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /nonexistent status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "Server is running") {
		t.Error("GET /nonexistent returned the status page — GET /{$} regressed into a catch-all")
	}
}

// TestRouter_ConfigEndpoint verifies the authenticated GET /config endpoint
// returns the limiter's maxConcurrent as {maxConcurrentUploads:<n>} with JSON content.
func TestRouter_ConfigEndpoint(t *testing.T) {
	const maxConcurrent = 4
	limiter := api.NewConcurrencyLimiter(maxConcurrent)
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
	if body.MaxConcurrentUploads != maxConcurrent {
		t.Errorf("config maxConcurrentUploads = %d, want %d", body.MaxConcurrentUploads, maxConcurrent)
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
		api.NewConcurrencyLimiter(4), testRouterVersion)

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
