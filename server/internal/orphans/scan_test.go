package orphans_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moontechs/files-nest/server/internal/orphans"
	"github.com/moontechs/files-nest/server/internal/store"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newScanEnv returns a storage root (t.TempDir()), an opened Store, and
// cleanup of the store. The storage path and the DB live in separate temp
// dirs; Scan uses only the storage path for the filesystem walk and the
// store for the complete-status record list, so they need not be nested.
func newScanEnv(t *testing.T) (*store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "db"))
	if err != nil {
		t.Fatalf("store.Open failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	storagePath := filepath.Join(dir, "storage")
	if err := os.MkdirAll(storagePath, 0o750); err != nil {
		t.Fatalf("mkdir storage: %v", err)
	}
	return s, storagePath
}

// writeFile creates a file at rel (relative to storagePath) with the given
// content, creating parent directories as needed.
func writeFile(t *testing.T, storagePath, rel string) string {
	t.Helper()
	p := filepath.Join(storagePath, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(p), err)
	}
	if err := os.WriteFile(p, []byte("content"), 0o640); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

// createRecord inserts an upload record with the given status and organized
// path into the store.
func createRecord(t *testing.T, s *store.Store, id, status, organizedPath string) {
	t.Helper()
	upload := &store.Upload{
		ID:              id,
		LocalIdentifier: id,
		Status:          store.Status(status),
		BackendID:       "tusd-" + id,
		Filename:        filepath.Base(organizedPath),
		CreationDate:    "2024-03-15T10:30:00Z",
		CreatedAt:       time.Now().UTC().Format(time.RFC3339),
		UpdatedAt:       time.Now().UTC().Format(time.RFC3339),
		OrganizedPath:   organizedPath,
	}
	if err := s.CreateUpload(upload); err != nil {
		t.Fatalf("CreateUpload(%s) failed: %v", id, err)
	}
}

func candidatePaths(result orphans.Result) []string {
	paths := make([]string, 0, len(result.Candidates))
	for _, c := range result.Candidates {
		paths = append(paths, c.Path)
	}
	return paths
}

func containsPath(paths []string, p string) bool {
	for _, x := range paths {
		if x == p {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestScan_CompleteRecordFileNotFlagged(t *testing.T) {
	s, storagePath := newScanEnv(t)

	p := writeFile(t, storagePath, "organized/2026/08/20/x.jpg")
	createRecord(t, s, "id1", string(store.StatusComplete), "organized/2026/08/20/x.jpg")

	result, err := orphans.Scan(s, storagePath)
	if err != nil {
		t.Fatalf("Scan returned error: %v", err)
	}

	if len(result.Candidates) != 0 {
		t.Fatalf("expected 0 candidates, got %v", candidatePaths(result))
	}
	if result.KnownComplete != 1 {
		t.Fatalf("KnownComplete = %d, want 1", result.KnownComplete)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("unexpected errors: %v", result.Errors)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("matched file should be left untouched: %v", err)
	}
}

func TestScan_DeletedRecordFileFlagged(t *testing.T) {
	s, storagePath := newScanEnv(t)

	p := writeFile(t, storagePath, "organized/2026/08/20/stale.jpg")
	// A deleted-status record's stale OrganizedPath is NOT in the known set
	// (which only includes complete-status records), so the file is orphaned.
	createRecord(t, s, "id1", string(store.StatusDeleted), "organized/2026/08/20/stale.jpg")

	result, err := orphans.Scan(s, storagePath)
	if err != nil {
		t.Fatalf("Scan returned error: %v", err)
	}

	if result.KnownComplete != 0 {
		t.Fatalf("KnownComplete = %d, want 0", result.KnownComplete)
	}
	if !containsPath(candidatePaths(result), p) {
		t.Fatalf("expected stale file %s to be flagged as candidate, got %v", p, candidatePaths(result))
	}
}

func TestScan_NoRecordFileFlaggedWithCTime(t *testing.T) {
	s, storagePath := newScanEnv(t)

	p := writeFile(t, storagePath, "organized/2026/08/20/orphan.jpg")

	result, err := orphans.Scan(s, storagePath)
	if err != nil {
		t.Fatalf("Scan returned error: %v", err)
	}

	if len(result.Candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %v", candidatePaths(result))
	}
	got := result.Candidates[0]
	if got.Path != p {
		t.Fatalf("candidate path = %q, want %q", got.Path, p)
	}
	// CTime is the inode status-change time — recent for a freshly written
	// file. No Chtimes backdating is applied here (ctime cannot be moved
	// backward by any process; that property is proven in ctime_test.go).
	if time.Since(got.CTime) >= 5*time.Second {
		t.Fatalf("candidate CTime = %v, expected within 5s of now", got.CTime)
	}
}

func TestScan_UnreadableSubdirectoryRecordedAndSiblingsScanned(t *testing.T) {
	s, storagePath := newScanEnv(t)

	good := writeFile(t, storagePath, "organized/2026/08/20/good.jpg")
	badDir := filepath.Join(storagePath, "organized", "2026", "08", "21")
	if err := os.MkdirAll(badDir, 0o750); err != nil {
		t.Fatalf("mkdir badDir: %v", err)
	}
	if err := writeFileAt(badDir, "hidden.jpg"); err != nil {
		t.Fatalf("write hidden: %v", err)
	}
	// Make the subdirectory unreadable so WalkDir fails to descend into it.
	if err := os.Chmod(badDir, 0o000); err != nil {
		t.Fatalf("chmod badDir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(badDir, 0o750) })

	result, err := orphans.Scan(s, storagePath)
	if err != nil {
		t.Fatalf("Scan returned error: %v", err)
	}

	if len(result.Errors) == 0 {
		t.Fatal("expected at least one error from the unreadable subdirectory")
	}
	// Sibling file on disk is still scanned (best-effort continuation).
	if !containsPath(candidatePaths(result), good) {
		t.Fatalf("expected sibling file %s to still be flagged, got %v", good, candidatePaths(result))
	}
}

func writeFileAt(dir, name string) error {
	if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o640); err != nil {
		return err
	}
	return nil
}

func TestScan_NonexistentOrganizedRootIsFatal(t *testing.T) {
	s, storagePath := newScanEnv(t)

	// No organized/ directory exists under storagePath.
	_, err := orphans.Scan(s, storagePath)
	if err == nil {
		t.Fatal("expected a fatal error for a nonexistent organized root")
	}
	if !strings.Contains(err.Error(), "organized") {
		t.Fatalf("expected error to mention organized root, got: %v", err)
	}
}

// TestScan_PathJoinRegression pins the path-form bug caught in review: a
// record's OrganizedPath of "organized/2026/08/20/x.jpg" joined with
// storagePath must match the file found by walking storagePath/organized —
// not storagePath/organized/organized/... (doubling the segment).
func TestScan_PathJoinRegression(t *testing.T) {
	s, storagePath := newScanEnv(t)

	p := writeFile(t, storagePath, "organized/2026/08/20/x.jpg")
	createRecord(t, s, "id1", string(store.StatusComplete), "organized/2026/08/20/x.jpg")

	// The file at the correct location must NOT be flagged.
	result, err := orphans.Scan(s, storagePath)
	if err != nil {
		t.Fatalf("Scan returned error: %v", err)
	}
	if containsPath(candidatePaths(result), p) {
		t.Fatalf("correctly-located file %s was wrongly flagged: %v", p, candidatePaths(result))
	}

	// A spurious file at the doubled-path location (storagePath/organized/
	// organized/...) must be flagged, proving the known set did not match it.
	doubled := writeFile(t, storagePath, "organized/organized/2026/08/20/x.jpg")
	result2, err := orphans.Scan(s, storagePath)
	if err != nil {
		t.Fatalf("second Scan returned error: %v", err)
	}
	if !containsPath(candidatePaths(result2), doubled) {
		t.Fatalf("expected doubled-path file %s to be flagged, got %v", doubled, candidatePaths(result2))
	}
}
