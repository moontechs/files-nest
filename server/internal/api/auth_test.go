// Package api_test tests the HTTP handlers and middleware for the iCloud
// Backup server API.
package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/moontechs/files-nest/server/internal/api"
)

// ---------------------------------------------------------------------------
// AuthMiddleware: valid credentials
// ---------------------------------------------------------------------------

func TestAuthMiddleware_ValidCredentials(t *testing.T) {
	middleware := api.AuthMiddleware(api.AuthConfig{
		Username: "admin",
		Password: "secret123",
	})

	var capturedUser string
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedUser = api.AuthUserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/uploads", nil)
	req.SetBasicAuth("admin", "secret123")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if capturedUser != "admin" {
		t.Errorf("expected context user 'admin', got %q", capturedUser)
	}
}

func TestAuthMiddleware_ValidCredentialsDifferentUser(t *testing.T) {
	middleware := api.AuthMiddleware(api.AuthConfig{
		Username: "backup-bot",
		Password: "s3cr3t!",
	})

	var capturedUser string
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedUser = api.AuthUserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("PUT", "/uploads", nil)
	req.SetBasicAuth("backup-bot", "s3cr3t!")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if capturedUser != "backup-bot" {
		t.Errorf("expected context user 'backup-bot', got %q", capturedUser)
	}
}

// ---------------------------------------------------------------------------
// AuthMiddleware: invalid credentials
// ---------------------------------------------------------------------------

func TestAuthMiddleware_WrongPassword(t *testing.T) {
	middleware := api.AuthMiddleware(api.AuthConfig{
		Username: "admin",
		Password: "secret123",
	})

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not have been called for wrong password")
	}))

	req := httptest.NewRequest("GET", "/uploads", nil)
	req.SetBasicAuth("admin", "wrong-password")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}

	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
	if body["error"] != "invalid credentials" {
		t.Errorf("expected error 'invalid credentials', got %q", body["error"])
	}
}

func TestAuthMiddleware_WrongUsername(t *testing.T) {
	middleware := api.AuthMiddleware(api.AuthConfig{
		Username: "admin",
		Password: "secret123",
	})

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not have been called for wrong username")
	}))

	req := httptest.NewRequest("GET", "/uploads", nil)
	req.SetBasicAuth("hacker", "secret123")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}

	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
	if body["error"] != "invalid credentials" {
		t.Errorf("expected error 'invalid credentials', got %q", body["error"])
	}
}

func TestAuthMiddleware_EmptyCredentials(t *testing.T) {
	middleware := api.AuthMiddleware(api.AuthConfig{
		Username: "admin",
		Password: "secret123",
	})

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not have been called for empty credentials")
	}))

	req := httptest.NewRequest("GET", "/uploads", nil)
	// No Authorization header set
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestAuthMiddleware_MalformedAuthHeader(t *testing.T) {
	middleware := api.AuthMiddleware(api.AuthConfig{
		Username: "admin",
		Password: "secret123",
	})

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not have been called for malformed header")
	}))

	req := httptest.NewRequest("GET", "/uploads", nil)
	// Set a non-Basic auth scheme
	req.Header.Set("Authorization", "Bearer some-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestAuthMiddleware_InvalidBase64Credentials(t *testing.T) {
	middleware := api.AuthMiddleware(api.AuthConfig{
		Username: "admin",
		Password: "secret123",
	})

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not have been called for invalid base64")
	}))

	req := httptest.NewRequest("GET", "/uploads", nil)
	// "Basic " followed by invalid base64
	req.Header.Set("Authorization", "Basic !!!invalid-base64!!!")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestAuthMiddleware_WWWAuthenticateHeader(t *testing.T) {
	middleware := api.AuthMiddleware(api.AuthConfig{
		Username: "admin",
		Password: "secret123",
	})

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not have been called")
	}))

	req := httptest.NewRequest("GET", "/uploads", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	wwwAuth := rec.Header().Get("WWW-Authenticate")
	if wwwAuth == "" {
		t.Error("expected WWW-Authenticate header, got empty")
	}
	if !strings.Contains(wwwAuth, "Basic") {
		t.Errorf("WWW-Authenticate should contain 'Basic', got %q", wwwAuth)
	}
	if !strings.Contains(wwwAuth, "iCloud Backup Server") {
		t.Errorf("WWW-Authenticate should contain realm, got %q", wwwAuth)
	}
}

// ---------------------------------------------------------------------------
// AuthMiddleware: no auth configured (development mode)
// ---------------------------------------------------------------------------

func TestAuthMiddleware_NoAuthConfigured_PassesRequests(t *testing.T) {
	middleware := api.AuthMiddleware(api.AuthConfig{
		Username: "",
		Password: "",
	})

	called := false
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/uploads", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !called {
		t.Error("handler should have been called when auth is not configured")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestAuthMiddleware_NoAuthConfigured_EmptyContextUser(t *testing.T) {
	middleware := api.AuthMiddleware(api.AuthConfig{
		Username: "",
		Password: "",
	})

	var capturedUser string
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedUser = api.AuthUserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/uploads", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if capturedUser != "" {
		t.Errorf("expected empty context user in dev mode, got %q", capturedUser)
	}
}

func TestAuthMiddleware_NoAuthConfigured_EvenWithAuthHeader(t *testing.T) {
	middleware := api.AuthMiddleware(api.AuthConfig{
		Username: "",
		Password: "",
	})

	var capturedUser string
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedUser = api.AuthUserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/uploads", nil)
	req.SetBasicAuth("admin", "secret")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 (no auth configured), got %d", rec.Code)
	}
	// In dev mode, the context user is not set even if a header was sent
	if capturedUser != "" {
		t.Errorf("expected empty context user in dev mode, got %q", capturedUser)
	}
}

// ---------------------------------------------------------------------------
// AuthMiddleware: edge cases
// ---------------------------------------------------------------------------

func TestAuthMiddleware_EmptyPassword(t *testing.T) {
	middleware := api.AuthMiddleware(api.AuthConfig{
		Username: "admin",
		Password: "",
	})

	var capturedUser string
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedUser = api.AuthUserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	// An empty password credential: "admin:"
	req := httptest.NewRequest("GET", "/uploads", nil)
	req.SetBasicAuth("admin", "")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for empty password match, got %d", rec.Code)
	}
	if capturedUser != "admin" {
		t.Errorf("expected context user 'admin', got %q", capturedUser)
	}
}

func TestAuthMiddleware_WrongPasswordWithEmptyConfigPassword(t *testing.T) {
	middleware := api.AuthMiddleware(api.AuthConfig{
		Username: "admin",
		Password: "",
	})

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not have been called")
	}))

	req := httptest.NewRequest("GET", "/uploads", nil)
	req.SetBasicAuth("admin", "some-password")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestAuthMiddleware_EmptyUsername(t *testing.T) {
	middleware := api.AuthMiddleware(api.AuthConfig{
		Username: "",
		Password: "secret123",
	})

	var capturedUser string
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedUser = api.AuthUserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/uploads", nil)
	req.SetBasicAuth("", "secret123")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for empty username match, got %d", rec.Code)
	}
	if capturedUser != "" {
		t.Errorf("expected empty context user (empty username), got %q", capturedUser)
	}
}

func TestAuthMiddleware_ColonInPassword(t *testing.T) {
	middleware := api.AuthMiddleware(api.AuthConfig{
		Username: "admin",
		Password: "pass:word:with:colons",
	})

	var capturedUser string
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedUser = api.AuthUserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/uploads", nil)
	req.SetBasicAuth("admin", "pass:word:with:colons")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if capturedUser != "admin" {
		t.Errorf("expected context user 'admin', got %q", capturedUser)
	}
}

func TestAuthMiddleware_UnicodeCredentials(t *testing.T) {
	middleware := api.AuthMiddleware(api.AuthConfig{
		Username: "用户",
		Password: "密码123!",
	})

	var capturedUser string
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedUser = api.AuthUserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/uploads", nil)
	req.SetBasicAuth("用户", "密码123!")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if capturedUser != "用户" {
		t.Errorf("expected context user '用户', got %q", capturedUser)
	}
}

// ---------------------------------------------------------------------------
// AuthMiddleware: response body on failure
// ---------------------------------------------------------------------------

func TestAuthMiddleware_ErrorResponseBody(t *testing.T) {
	middleware := api.AuthMiddleware(api.AuthConfig{
		Username: "admin",
		Password: "secret123",
	})

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not have been called")
	}))

	req := httptest.NewRequest("GET", "/uploads", nil)
	// Missing auth header
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("expected JSON error body, got: %v", err)
	}
	if body["error"] == "" {
		t.Error("expected non-empty error message in response body")
	}
}

func TestAuthMiddleware_ContentTypeOnFailure(t *testing.T) {
	middleware := api.AuthMiddleware(api.AuthConfig{
		Username: "admin",
		Password: "secret123",
	})

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not have been called")
	}))

	req := httptest.NewRequest("GET", "/uploads", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected Content-Type 'application/json', got %q", ct)
	}
}

// ---------------------------------------------------------------------------
// AuthUserFromContext: edge cases
// ---------------------------------------------------------------------------

func TestAuthUserFromContext_NoAuthSet(t *testing.T) {
	user := api.AuthUserFromContext(nil)
	if user != "" {
		t.Errorf("expected empty user from nil context, got %q", user)
	}
}

func TestAuthUserFromContext_BackgroundContext(t *testing.T) {
	user := api.AuthUserFromContext(context.Background())
	if user != "" {
		t.Errorf("expected empty user from background context, got %q", user)
	}
}

func TestAuthUserFromContext_WrongContextKeyType(t *testing.T) {
	// Using a different key type should not return a value
	ctx := context.WithValue(context.Background(), "some-other-key", "admin")
	user := api.AuthUserFromContext(ctx)
	if user != "" {
		t.Errorf("expected empty user for wrong key type, got %q", user)
	}
}

func TestAuthUserFromContext_WrongContextValueType(t *testing.T) {
	// Using a different type for the value should not return the value
	ctx := context.WithValue(context.Background(), api.UserKey, 12345)
	user := api.AuthUserFromContext(ctx)
	if user != "" {
		t.Errorf("expected empty user for wrong value type, got %q", user)
	}
}
