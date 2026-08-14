package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/moontechs/files-nest/server/internal/filestore"
	"github.com/moontechs/files-nest/server/internal/store"
	"github.com/moontechs/files-nest/server/internal/uploadbackend"
)

// dirPerm is the permission mode used when creating directories in the
// storage tree (owner/group read-write-execute, no world access).
const dirPerm = 0o750

// ---------------------------------------------------------------------------
// Recoverer
// ---------------------------------------------------------------------------

// Recoverer handles startup crash recovery. It processes pending completion
// intents left by crashes during the completion flow (file moved from incoming/
// to organized/ but DB record not yet marked complete), and checks for backend
// state inconsistencies where the tusd backend has been lost but the DB record
// still shows the upload as in-progress.
//
// Recovery is called early in main(), before the HTTP server starts serving
// requests, to ensure the system is in a consistent state.
type Recoverer struct {
	store       *store.Store
	backend     *uploadbackend.TUSHandler
	storagePath string
	locks       *UploadLocker
}

// NewRecoverer creates a Recoverer wired to the given store, tusd backend,
// and storage path root.
func NewRecoverer(st *store.Store, bk *uploadbackend.TUSHandler, storagePath string) *Recoverer {
	return &Recoverer{
		store:       st,
		backend:     bk,
		storagePath: storagePath,
		locks:       &UploadLocker{},
	}
}

// Recover runs all startup recovery procedures. It is idempotent — calling
// it multiple times is safe. It logs all actions and continues recovering
// as much state as possible, even if individual steps fail.
func (r *Recoverer) Recover() error {
	log.Printf("recovery: starting startup recovery...")

	// Phase 1: Recover any pending completion intents from crashed completions.
	err := r.recoverCompletionIntents()
	if err != nil {
		return fmt.Errorf("completion intent recovery failed: %w", err)
	}

	// Phase 2: Check for uploading records whose tusd backend has gone missing.
	err = r.recoverBackendLost()
	if err != nil {
		log.Printf("recovery: backend lost recovery error: %v", err)
	}

	log.Printf("recovery: startup recovery complete")

	return nil
}

// ---------------------------------------------------------------------------
// Phase 1: Completion intent recovery
// ---------------------------------------------------------------------------

// recoverCompletionIntents iterates all pending completion intents and
// resolves each one. Intents are persisted before the file move and removed
// only after the DB record is safely marked complete. A crash between the
// file move and the DB update leaves an orphaned intent pointing at a file
// that was already moved, or — if the crash happened before the move — an
// intent with neither file moved yet.
func (r *Recoverer) recoverCompletionIntents() error {
	intents, err := r.store.ListCompletionIntents()
	if err != nil {
		return fmt.Errorf("list completion intents: %w", err)
	}

	if len(intents) == 0 {
		log.Printf("recovery: no pending completion intents found")

		return nil
	}

	log.Printf("recovery: found %d pending completion intents", len(intents))

	for _, intent := range intents {
		// Acquire the per-upload lock before recovery to serialize with
		// any handler operations that might also touch this upload. At
		// startup this is strictly defensive (no handlers are running yet),
		// but it documents the shared-state contract.
		err := r.locks.Do(intent.ID, func() error {
			return r.recoverIntent(intent)
		})
		if err != nil {
			log.Printf("recovery: failed to recover intent %s: %v", intent.ID, err)
			// Continue processing remaining intents.
		}
	}

	return nil
}

// recoverIntent resolves a single CompletionIntent by checking the state of
// the source and destination files, moving the file if needed, updating the
// DB record, cleaning up the intent, and removing the tusd sidecar.
func (r *Recoverer) recoverIntent(intent *store.CompletionIntent) error {
	log.Printf("recovery: processing intent %s (src=%s dst=%s)", intent.ID, intent.Src, intent.Dst)

	// If the upload record already shows complete, the file was already
	// moved and the DB was already updated. The intent is stale — delete
	// it and retry tusd cleanup.
	if r.recoverAlreadyCompleteIntent(intent) {
		return nil
	}

	srcExists := fileExists(intent.Src)
	dstExists := fileExists(intent.Dst)

	switch {
	case dstExists && srcExists:
		// Both files exist — the intent was saved but the file was moved
		// successfully and both copies remain. Remove the source file and
		// complete the DB update.
		log.Printf("recovery: intent %s: both src and dst exist, removing src", intent.ID)

		err := os.Remove(intent.Src)
		if err != nil {
			log.Printf("recovery: intent %s: failed to remove source file after move: %v", intent.ID, err)
		}
		// Fall through to complete the DB update.

	case dstExists:
		// Destination exists but source is gone — the file was moved
		// successfully but the server crashed before the DB update.
		log.Printf("recovery: intent %s: file already moved, completing DB record", intent.ID)

	case srcExists:
		// Source exists but destination does not — the intent was saved
		// but the file was never moved (crash before the os.Rename).
		log.Printf("recovery: intent %s: moving file to organized directory", intent.ID)

		err := moveIntentSource(intent)
		if err != nil {
			return err
		}

	default:
		// Neither src nor dst exists — data was lost. Leave the record
		// unchanged and keep the intent for manual inspection. Do not
		// delete data or change status — an operator should investigate.
		log.Printf(
			"recovery: intent %s: neither src (%s) nor dst (%s) exists for upload record %s "+
				"(backend %s); keeping intent for manual repair",
			intent.ID, intent.Src, intent.Dst, intent.ID, intent.BackendID)

		return nil
	}

	// Update the DB record to complete.
	// Use UpdateComplete which atomically sets status=complete and organized_path.
	_, err := r.store.UpdateComplete(intent.ID, intent.DstRel)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			log.Printf("recovery: intent %s: upload record not found, cleaning up", intent.ID)
		} else {
			log.Printf("recovery: intent %s: UpdateComplete failed: %v", intent.ID, err)

			return fmt.Errorf("update complete for %s: %w", intent.ID, err)
		}
	}

	// Delete the intent — the DB is now consistent.
	err = r.store.DeleteCompletionIntent(intent.ID)
	if err != nil {
		log.Printf("recovery: intent %s: failed to delete completion intent: %v", intent.ID, err)
	}

	// Best-effort cleanup of the tusd sidecar (.info file).
	r.cleanupTusdBackend(intent)

	return nil
}

// recoverAlreadyCompleteIntent handles the stale-intent case where the upload
// record already shows complete (the file was moved and the DB updated before
// a crash). It deletes the stale intent, retries tusd cleanup, and reports
// whether the intent was already complete.
func (r *Recoverer) recoverAlreadyCompleteIntent(intent *store.CompletionIntent) bool {
	upload, err := r.store.GetUpload(intent.ID)
	if err != nil || upload.Status != store.StatusComplete {
		return false
	}

	log.Printf("recovery: intent %s: record already complete, deleting stale intent", intent.ID)

	delErr := r.store.DeleteCompletionIntent(intent.ID)
	if delErr != nil {
		log.Printf("recovery: intent %s: failed to delete stale intent: %v", intent.ID, delErr)
	}

	r.cleanupTusdBackend(intent)

	return true
}

// cleanupTusdBackend best-effort cleans up the tusd sidecar for an intent.
// ErrNotFound is ignored (the backend may already be gone); any other error
// is logged but not surfaced, matching the recovery flow's best-effort policy.
func (r *Recoverer) cleanupTusdBackend(intent *store.CompletionIntent) {
	if intent.BackendID == "" {
		return
	}

	err := r.backend.TerminateOrCleanup(context.Background(), intent.BackendID)
	if err != nil && !errors.Is(err, uploadbackend.ErrNotFound) {
		log.Printf("recovery: intent %s: failed to clean up tusd backend: %v", intent.ID, err)
	}
}

// moveIntentSource moves a pending intent's source file into its organized
// destination, creating the destination directory first.
func moveIntentSource(intent *store.CompletionIntent) error {
	dstDir := filepath.Dir(intent.Dst)

	err := os.MkdirAll(dstDir, dirPerm)
	if err != nil {
		return fmt.Errorf("create destination directory %s: %w", dstDir, err)
	}

	err = filestore.MoveFile(intent.Src, intent.Dst)
	if err != nil {
		return fmt.Errorf("move file %s -> %s: %w", intent.Src, intent.Dst, err)
	}

	return nil
}

// ---------------------------------------------------------------------------
// Phase 2: Backend lost recovery
// ---------------------------------------------------------------------------

// recoverBackendLost scans uploading and completing records and checks whether
// their tusd backend still exists. If the backend is gone, the record status
// is updated to backend_lost so the next POST /uploads from the client will
// trigger a fresh upload.
func (r *Recoverer) recoverBackendLost() error {
	statuses := []store.Status{store.StatusUploading, store.StatusCompleting}
	total := 0
	lost := 0

	for _, status := range statuses {
		records, err := r.store.ListByStatus(status)
		if err != nil {
			return fmt.Errorf("list by status %s: %w", status, err)
		}

		for _, rec := range records {
			total++

			if rec.BackendID == "" {
				continue
			}

			// Skip records that have a pending completion intent. Phase 1
			// (completion intent recovery) handles or preserves those
			// records; Phase 2 should not override that decision.
			intent, err := r.store.GetCompletionIntent(rec.ID)
			if err == nil && intent != nil {
				log.Printf("recovery: skipping backend check for %s (has pending completion intent)", rec.ID)

				continue
			}

			// Check if the backend still exists by querying its info.
			_, err = r.backend.GetInfo(context.Background(), rec.BackendID)
			if errors.Is(err, uploadbackend.ErrNotFound) {
				log.Printf("recovery: backend lost for upload %s (status %s, backend %s)",
					rec.ID, status, rec.BackendID)

				_, updateErr := r.store.UpdateStatus(rec.ID, store.StatusBackendLost)
				if updateErr != nil {
					log.Printf("recovery: failed to set backend_lost for %s: %v", rec.ID, updateErr)
				}

				lost++
			} else if err != nil {
				// Non-ErrNotFound error — log but don't change status (the
				// backend might be temporarily unavailable).
				log.Printf("recovery: backend check failed for %s: %v", rec.ID, err)
			}
		}
	}

	if total > 0 {
		log.Printf("recovery: checked %d in-progress uploads, %d backends lost", total, lost)
	}

	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// fileExists returns true if the given path exists and is not a directory.
// It follows symlinks (via os.Stat).
func fileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}

	return !info.IsDir()
}
