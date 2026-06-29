// iCloud Backup Server — Go HTTP API + embedded tusd upload handler + BadgerDB state store.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/moontechs/files-nest/server/internal/api"
	"github.com/moontechs/files-nest/server/internal/store"
	"github.com/moontechs/files-nest/server/internal/uploadbackend"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	storagePath := getEnv("STORAGE_PATH", "./data")
	port := getEnv("PORT", "8080")

	// Open BadgerDB
	dbPath := storagePath + "/db"
	log.Printf("opening store at %s", dbPath)
	st, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("failed to open store: %v", err)
	}
	defer st.Close()

	// Start BadgerDB GC goroutine
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runBadgerGC(ctx, st.DB())

	// Create the embedded tusd upload backend (incoming/ directory).
	tusdHandler, err := uploadbackend.New(storagePath)
	if err != nil {
		log.Fatalf("failed to create upload backend: %v", err)
	}

	// Run startup crash recovery before serving traffic.
	// This ensures that any pending completion intents from a previous crash
	// are resolved, and any uploading records with lost backends are marked.
	recoverer := api.NewRecoverer(st, tusdHandler, storagePath)
	if err := recoverer.Recover(); err != nil {
		log.Printf("startup recovery completed with errors: %v", err)
	}

	// Create the API handler with per-upload locks and storage path.
	handler := api.NewHandler(st, tusdHandler, storagePath)

	// Wire the chi-style router with BasicAuth middleware on all routes.
	// The auth config is read from BACKUP_USER / BACKUP_PASS env vars.
	authCfg := api.AuthConfig{
		Username: getEnv("BACKUP_USER", ""),
		Password: getEnv("BACKUP_PASS", ""),
	}
	mux := api.NewRouter(handler, authCfg)

	server := &http.Server{
		Addr:         ":" + port,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
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
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("server shutdown error: %v", err)
		}
	}()

	log.Printf("server listening on :%s", port)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}

	log.Println("server stopped")
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
				err := db.RunValueLogGC(0.5)
				if err != nil {
					again = false
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
