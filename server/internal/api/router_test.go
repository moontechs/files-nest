package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/moontechs/files-nest/server/internal/api"
	"github.com/moontechs/files-nest/server/internal/store"
	"github.com/moontechs/files-nest/server/internal/uploadbackend"
)

// newRouterForTest builds a router wired to a temp store + backend with auth
// disabled (both credentials empty) so handlers are reachable in tests.
func newRouterForTest(t *testing.T) http.Handler {
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
	return api.NewRouter(h, api.AuthConfig{})
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
