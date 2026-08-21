// iCloud Backup Server — Go HTTP API + embedded tusd upload handler + BadgerDB state store.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/dgraph-io/badger/v4"
	"github.com/moontechs/files-nest/server/internal/api"
	"github.com/moontechs/files-nest/server/internal/orphans"
	"github.com/moontechs/files-nest/server/internal/store"
	"github.com/moontechs/files-nest/server/internal/uploadbackend"
)

const (
	readHeaderTimeout      = 15 * time.Second
	idleTimeout            = 60 * time.Second
	valueLogGCDiscardRatio = 0.5
	orphanMinCandidateAge  = 3 * time.Hour
)

// errPartialBackupCredentials is returned when only one of BACKUP_USER /
// BACKUP_PASS is set.
var errPartialBackupCredentials = errors.New("BACKUP_USER and BACKUP_PASS must be set together")

func main() {
	err := run()
	if err != nil {
		log.Printf("fatal error: %v", err)
		os.Exit(1)
	}
}

func run() error {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	storagePath := getEnv("STORAGE_PATH", "./data")
	port := getEnv("PORT", "8080")

	// Concurrency limit for in-flight PATCH /uploads/{id}/data requests.
	// A missing/invalid/zero-or-negative configured value falls back to the
	// default of 4 with a logged warning: NewConcurrencyLimiter uses the value
	// as a buffered-channel capacity, so 0 would make every upload rejected
	// with no distinguishing signal (see ADR-0003 for the reject-over-limit
	// decision).
	maxConcurrentUploads, err := strconv.Atoi(getEnv("MAX_CONCURRENT_UPLOADS", "4"))
	if err != nil || maxConcurrentUploads <= 0 {
		log.Printf("WARNING: invalid MAX_CONCURRENT_UPLOADS=%q (must be a positive integer), "+
			"falling back to default of 4", getEnv("MAX_CONCURRENT_UPLOADS", "4"))
		maxConcurrentUploads = 4
	}
	limiter := api.NewConcurrencyLimiter(maxConcurrentUploads)

	// Interval for the background orphan-file cleanup cycle. A
	// missing/invalid/non-positive configured value falls back to the default
	// of 48h with a logged warning, mirroring the MAX_CONCURRENT_UPLOADS
	// fallback-with-warning pattern.
	gcOrphansInterval, err := time.ParseDuration(getEnv("GC_ORPHANS_INTERVAL", "48h"))
	if err != nil || gcOrphansInterval <= 0 {
		log.Printf("WARNING: invalid GC_ORPHANS_INTERVAL=%q (must be a positive duration like 48h), "+
			"falling back to default of 48h", getEnv("GC_ORPHANS_INTERVAL", "48h"))
		gcOrphansInterval = 48 * time.Hour
	}

	// Open BadgerDB
	dbPath := filepath.Join(storagePath, "db")
	log.Printf("opening store at %s", dbPath)

	dbStore, err := store.Open(dbPath)
	if err != nil {
		return fmt.Errorf("failed to open store: %w", err)
	}
	defer func() { _ = dbStore.Close() }()

	// Start BadgerDB GC goroutine
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go runBadgerGC(ctx, dbStore.DB())

	// Create the embedded tusd upload backend (incoming/ directory).
	tusdHandler, err := uploadbackend.New(storagePath)
	if err != nil {
		return fmt.Errorf("failed to create upload backend: %w", err)
	}

	// Run startup crash recovery before serving traffic.
	// This ensures that any pending completion intents from a previous crash
	// are resolved, and any uploading records with lost backends are marked.
	recoverer := api.NewRecoverer(dbStore, tusdHandler, storagePath)

	err = recoverer.Recover()
	if err != nil {
		log.Printf("startup recovery completed with errors: %v", err)
	}

	// Start the orphan-file cleanup goroutine only after crash recovery has
	// completed, so its immediate first cycle can never race a file Recover()
	// is still reconciling (which, after a long-enough outage, could carry an
	// mtime old enough to pass the age guard).
	go runGCOrphans(ctx, dbStore, storagePath, gcOrphansInterval, orphanMinCandidateAge)

	// Create the API handler with per-upload locks and storage path.
	handler := api.NewHandler(dbStore, tusdHandler, storagePath)

	// Wire the chi-style router with BasicAuth middleware on all routes.
	// The auth config is read from BACKUP_USER / BACKUP_PASS env vars.
	authCfg := api.AuthConfig{
		Username: getEnv("BACKUP_USER", ""),
		Password: getEnv("BACKUP_PASS", ""),
	}
	switch {
	case authCfg.Username == "" && authCfg.Password == "":
		log.Printf("WARNING: BACKUP_USER/BACKUP_PASS not set — HTTP Basic Auth is DISABLED. " +
			"All upload routes are unauthenticated. Do NOT run like this in production.")
	case authCfg.Username == "" || authCfg.Password == "":
		// Partial credentials are a misconfiguration: the auth middleware only
		// skips when BOTH are empty, so a partially-set config would enforce
		// Basic Auth but accept an empty password for the configured username
		// (subtle.ConstantTimeCompare of two empty byte slices returns 1).
		// Refuse to start rather than expose an unauthenticated-with-username
		// backdoor.
		return fmt.Errorf("%w: got user=%q pass=%q. "+
			"Set both to enable Basic Auth, or leave both empty for unauthenticated dev mode",
			errPartialBackupCredentials, getEnv("BACKUP_USER", ""), getEnv("BACKUP_PASS", ""))
	}

	mux := api.NewRouter(handler, authCfg, limiter)

	// Timeouts: this is an upload server that streams potentially large
	// photo/video files via PATCH /uploads/:id/data. A fixed ReadTimeout or
	// WriteTimeout would abort large or slow uploads mid-stream, so we use
	// ReadHeaderTimeout (which only bounds reading the request headers and
	// still protects against slowloris-style attacks) and leave the body /
	// response streams unbounded. IdleTimeout bounds keep-alive waits.
	server := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
	}

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		log.Printf("received %v, shutting down", sig)
		cancel()

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()

		err := server.Shutdown(shutdownCtx)
		if err != nil {
			log.Printf("server shutdown error: %v", err)
		}
	}()

	log.Printf("server listening on :%s", port)

	err = server.ListenAndServe()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("server error: %w", err)
	}

	log.Println("server stopped")

	return nil
}

// runBadgerGC periodically runs BadgerDB value log GC to reclaim disk space.
// Without this, the value log grows unboundedly as records are updated.
func runBadgerGC(ctx context.Context, db BadgerGCer) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			again := true
			for again {
				err := db.RunValueLogGC(valueLogGCDiscardRatio)
				if err != nil {
					again = false
					// ErrNoRewrite is expected when there is nothing to reclaim;
					// surface any other error so silent disk/GC failures are
					// visible in logs.
					if !errors.Is(err, badger.ErrNoRewrite) {
						log.Printf("badger value log GC error: %v", err)
					}
				}
			}
		}
	}
}

// BadgerGCer is the subset of badger.DB used by the GC goroutine.
type BadgerGCer interface {
	RunValueLogGC(discardRatio float64) error
}

// runGCOrphans periodically scans organized/ for files with no live
// (complete-status) record and deletes them. It mirrors runBadgerGC's shape
// (a time.Ticker plus a select on ctx.Done()/ticker.C) with two deliberate
// differences: it runs one cycle immediately upon startup before entering the
// ticker wait, so pre-existing orphans aren't left for up to a full interval
// after a restart; and it is started strictly after crash recovery completes
// (see run()), so its immediate first cycle can never delete a file recovery
// is still reconciling. Unlike runBadgerGC this goroutine deletes user files
// with no undo, so the circuit breaker inside gcOrphansCycle is what caps the
// blast radius of any future Scan regression.
func runGCOrphans(ctx context.Context, db *store.Store, storagePath string, interval, minAge time.Duration) {
	gcOrphansCycle(db, storagePath, minAge)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			gcOrphansCycle(db, storagePath, minAge)
		}
	}
}

// gcOrphansCycle runs a single orphan scan/filter/apply pass: scan the
// organized tree against complete-status records, drop candidates younger
// than minAge, apply a circuit breaker before deleting anything, then delete
// survivors (best-effort) and log what happened. Any single error is isolated
// to this cycle; the caller's ticker tries again next interval.
func gcOrphansCycle(db *store.Store, storagePath string, minAge time.Duration) {
	result, err := orphans.Scan(db, storagePath)
	if err != nil {
		log.Printf("gc-orphans: scan failed: %v", err)
		return // this cycle only; the ticker will try again next interval
	}

	candidates := orphans.FilterMinAge(result.Candidates, minAge, time.Now())

	// Circuit breaker: if more than 20% of the known-complete set (with a
	// floor of 50) show up as candidates, Scan's known-path matching probably
	// regressed. Skip the delete and log loudly rather than risk mass
	// deletion; the next cycle tries again from scratch.
	if breaker := max(50, result.KnownComplete/5); len(candidates) > breaker {
		log.Printf("gc-orphans: ERROR: %d candidates exceeds circuit breaker "+
			"(%d, known-complete=%d) — skipping delete this cycle",
			len(candidates), breaker, result.KnownComplete)
		return
	}

	applied := orphans.Apply(candidates)
	for _, c := range applied.Removed {
		log.Printf("gc-orphans: removed orphan %s", c.Path)
	}
	for _, e := range result.Errors {
		log.Printf("gc-orphans: error: %v", e)
	}
	for _, e := range applied.Errors {
		log.Printf("gc-orphans: error: %v", e)
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return fallback
}
