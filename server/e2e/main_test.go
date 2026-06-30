//go:build e2e

// Package e2e provides black-box end-to-end tests for the iCloud Backup
// server. Tests run against a live deployment (typically Docker Compose),
// exercise the full HTTP API through Caddy, and verify real behavior
// without importing any internal packages.
//
// All tests are gated by the "e2e" build tag so normal test runs
// (go test ./...) are unaffected. Run with:
//
//	SERVER_URL=http://127.0.0.1:18080 \
//	  BACKUP_USER=testuser \
//	  BACKUP_PASS=testpass \
//	  go test -tags=e2e -v ./e2e/
package e2e

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Shared test state
// ---------------------------------------------------------------------------

var (
	// serverURL is the base URL of the deployed server (including scheme).
	// This is set once in TestMain from the SERVER_URL environment variable.
	serverURL string

	// httpClient is a shared HTTP client used by all tests. It uses a
	// reasonable timeout to prevent hung tests against a misconfigured
	// environment, but is long enough for uploads of typical e2e payloads.
	httpClient = &http.Client{
		Timeout: 30 * time.Second,
	}

	// backupUser and backupPass hold the optional Basic Auth credentials
	// read from the environment. When both are empty, no auth header is
	// sent (used for unauthenticated development/staging deployments).
	backupUser string
	backupPass string
)

// ---------------------------------------------------------------------------
// TestMain — readiness gate and environment validation
// ---------------------------------------------------------------------------

// TestMain performs one-time setup before any test in this package runs.
// It validates the environment, polls the health endpoint until the server
// is ready, and then delegates to m.Run().
//
// Environment variables:
//   - SERVER_URL (required): base URL of the server, e.g. "http://127.0.0.1:18080".
//     When empty or unset, all tests are skipped.
//   - BACKUP_USER (optional): username for HTTP Basic Auth.
//   - BACKUP_PASS (optional): password for HTTP Basic Auth.
//
// Behaviour:
//   - Empty SERVER_URL → skip all tests (call os.Exit(0)).
//   - Configured SERVER_URL but unreachable → fail with fatal error after
//     exhausting the retry budget (call os.Exit(1)).
//   - Configured SERVER_URL and reachable → run all tests normally.
func TestMain(m *testing.M) {
	// -----------------------------------------------------------------------
	// Phase 1: Read and validate environment
	// -----------------------------------------------------------------------

	serverURL = os.Getenv("SERVER_URL")
	if serverURL == "" {
		fmt.Println("e2e: SERVER_URL not set — skipping all e2e tests")
		os.Exit(0)
	}

	backupUser = os.Getenv("BACKUP_USER")
	backupPass = os.Getenv("BACKUP_PASS")

	log.Printf("e2e: SERVER_URL=%s", serverURL)
	if backupUser != "" || backupPass != "" {
		log.Printf("e2e: auth enabled (user=%s)", backupUser)
	} else {
		log.Println("e2e: no auth credentials — running without authentication")
	}

	// -----------------------------------------------------------------------
	// Phase 2: Poll health endpoint until ready
	// -----------------------------------------------------------------------

	healthURL := serverURL + "/health"
	log.Printf("e2e: polling %s for readiness ...", healthURL)

	if err := pollUntilHealthy(healthURL, 30*time.Second, 500*time.Millisecond); err != nil {
		log.Fatalf("e2e: server not healthy within timeout: %v", err)
	}

	log.Println("e2e: server is healthy — starting tests")

	// -----------------------------------------------------------------------
	// Phase 3: Run tests
	// -----------------------------------------------------------------------

	os.Exit(m.Run())
}

// pollUntilHealthy repeatedly GETs healthURL until it receives a 200 response
// or until timeout expires. It waits waitBetween between attempts.
func pollUntilHealthy(healthURL string, timeout, waitBetween time.Duration) error {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		resp, err := httpClient.Get(healthURL)
		if err != nil {
			log.Printf("e2e: health check failed (will retry): %v", err)
			time.Sleep(waitBetween)
			continue
		}
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			return nil
		}

		log.Printf("e2e: health check returned %d (will retry)", resp.StatusCode)
		time.Sleep(waitBetween)
	}

	return fmt.Errorf("health endpoint %s did not return 200 within %v", healthURL, timeout)
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// authenticatedRequest creates a new HTTP request with the given method and
// path (e.g. "/uploads"). If backup credentials are configured, the request
// includes Basic Authentication headers. The caller is responsible for
// setting the body and any additional headers, and for closing the response
// body after use.
//
// Example:
//
//	req := authenticatedRequest("GET", "/uploads", nil)
//	resp, err := httpClient.Do(req)
func authenticatedRequest(method, path string, body any) (*http.Request, error) {
	// TODO: implement body encoding in a follow-up — for now, this is a
	// placeholder that creates a request without a body. The concrete test
	// files in this package (uploads_test.go etc.) will provide full request
	// builders with proper body handling.
	return authenticatedRequestNoBody(method, path)
}

// authenticatedRequestNoBody creates a GET-style request without a body.
func authenticatedRequestNoBody(method, path string) (*http.Request, error) {
	req, err := http.NewRequest(method, serverURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("creating %s %s: %w", method, path, err)
	}

	if backupUser != "" || backupPass != "" {
		req.SetBasicAuth(backupUser, backupPass)
	}

	return req, nil
}
