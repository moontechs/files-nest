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
	"syscall"
	"time"

	"github.com/dgraph-io/badger/v4"
	"github.com/moontechs/files-nest/server/internal/api"
	"github.com/moontechs/files-nest/server/internal/store"
	"github.com/moontechs/files-nest/server/internal/uploadbackend"
)

const (
	readHeaderTimeout      = 15 * time.Second
	idleTimeout            = 60 * time.Second
	valueLogGCDiscardRatio = 0.5
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

	mux := api.NewRouter(handler, authCfg)

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

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return fallback
}
