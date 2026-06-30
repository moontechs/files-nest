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
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
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

	// healthClient is a short-timeout client used exclusively by the
	// readiness poller in TestMain. A tight timeout lets the poller retry
	// quickly instead of burning the entire E2E_WAIT budget on a single
	// hung request.
	healthClient = &http.Client{
		Timeout: 5 * time.Second,
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
//   - BACKUP_USER (required when SERVER_URL is set): username for HTTP Basic Auth.
//   - BACKUP_PASS (required when SERVER_URL is set): password for HTTP Basic Auth.
//   - E2E_WAIT (optional, default 30): seconds to wait for /health readiness.
//
// Behaviour:
//   - Empty SERVER_URL → skip all tests (call os.Exit(0)).
//   - SERVER_URL set but BACKUP_USER or BACKUP_PASS missing → fail with a
//     clear configuration error (call os.Exit(1)).
//   - Configured SERVER_URL but unreachable → fail with fatal error after
//     exhausting the E2E_WAIT retry budget (call os.Exit(1)).
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

	// SERVER_URL is configured, so the target stack is expected to enforce
	// Basic Auth. Both credentials are required to drive the authenticated
	// endpoints; bail out early with a clear configuration error rather
	// than a cascade of opaque 401 test failures.
	if backupUser == "" || backupPass == "" {
		fmt.Fprintln(os.Stderr, "e2e: SERVER_URL is set but BACKUP_USER and/or BACKUP_PASS is missing — set both credentials to run e2e tests")
		os.Exit(1)
	}

	log.Printf("e2e: auth enabled (user=%s)", backupUser)

	// E2E_WAIT overrides the readiness poll timeout (default 30s). A
	// malformed value falls back to the default with a warning.
	waitSeconds := 30
	if raw := os.Getenv("E2E_WAIT"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			waitSeconds = v
		} else {
			log.Printf("e2e: invalid E2E_WAIT=%q, using default %ds", raw, waitSeconds)
		}
	}

	// -----------------------------------------------------------------------
	// Phase 2: Poll health endpoint until ready
	// -----------------------------------------------------------------------

	healthURL := serverURL + "/health"
	log.Printf("e2e: polling %s for readiness ...", healthURL)

	if err := pollUntilHealthy(healthURL, time.Duration(waitSeconds)*time.Second, 500*time.Millisecond); err != nil {
		log.Fatalf("e2e: server not healthy within %ds: %v", waitSeconds, err)
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
		resp, err := healthClient.Get(healthURL)
		if err != nil {
			log.Printf("e2e: health check failed (will retry): %v", err)
			time.Sleep(waitBetween)
			continue
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			return nil
		}

		log.Printf("e2e: health check returned %d (will retry)", resp.StatusCode)
		time.Sleep(waitBetween)
	}

	return fmt.Errorf("health endpoint %s did not return 200 within %v", healthURL, timeout)
}
