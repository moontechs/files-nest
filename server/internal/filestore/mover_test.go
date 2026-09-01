// Package filestore_test tests file storage and organization operations
// for the iCloud Backup server.
package filestore_test

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moontechs/files-nest/server/internal/filestore"
)

var errInjectedMoverTestFailure = errors.New("injected test failure")

const (
	testCreationDate = "2024-03-15T10:30:00Z"
	testAlsoGarbage  = "also-garbage"
)

func TestPlanAndMoveBeforeMoveCallback(t *testing.T) {
	t.Run("nil error proceeds", func(t *testing.T) {
		m := openTestMover(t)
		src := filepath.Join(m.StoragePath(), "incoming", "callback-ok")
		writeFile(t, src, []byte("content"))
		called := false
		plan, err := m.PlanAndMove(src, "2024-01-02T00:00:00Z", "", "file.txt", "id",
			func(got filestore.PlanDestResult) error {
				called = got.Rel == "organized/2024/01/02/file_id.txt"
				return nil
			})
		if err != nil || !called {
			t.Fatalf("PlanAndMove: plan=%+v err=%v callback=%v", plan, err, called)
		}
		assertPathExists(t, plan.Abs)
	})

	t.Run("error prevents move", func(t *testing.T) {
		m := openTestMover(t)
		src := filepath.Join(m.StoragePath(), "incoming", "callback-error")
		writeFile(t, src, []byte("content"))
		want := fmt.Errorf("persist intent: %w", errInjectedMoverTestFailure)
		plan, err := m.PlanAndMove(src, "2024-01-02T00:00:00Z", "", "file.txt", "id",
			func(filestore.PlanDestResult) error { return want })
		if !errors.Is(err, want) || plan.Abs == "" {
			t.Fatalf("PlanAndMove: plan=%+v err=%v", plan, err)
		}
		assertPathExists(t, src)
		assertPathNotExists(t, plan.Abs)
	})
}

func TestPlanAndMoveMoveError(t *testing.T) {
	m := openTestMover(t)
	_, err := m.PlanAndMove(filepath.Join(m.StoragePath(), "missing"), "2024-01-02T00:00:00Z", "", "file.txt", "id", nil)
	if err == nil {
		t.Fatal("PlanAndMove should return an error when the source is missing")
	}
}

func TestMoveToPlanedBeforeMoveCallback(t *testing.T) {
	m := openTestMover(t)
	src := filepath.Join(m.StoragePath(), "incoming", "retry")
	writeFile(t, src, []byte("content"))
	plan := filestore.PlanDestResult{
		Abs:      filepath.Join(m.StoragePath(), "organized/2024/01/02/retry.txt"),
		Rel:      "organized/2024/01/02/retry.txt",
		DateUsed: "",
	}

	called := false
	err := m.MoveToPlaned(src, plan, func(got filestore.PlanDestResult) error {
		called = got == plan
		return nil
	})
	if err != nil || !called {
		t.Fatalf("MoveToPlaned success: err=%v callback=%v", err, called)
	}

	src = filepath.Join(m.StoragePath(), "incoming", "retry-error")
	writeFile(t, src, []byte("content"))
	want := fmt.Errorf("refresh intent: %w", errInjectedMoverTestFailure)
	err = m.MoveToPlaned(src, plan, func(filestore.PlanDestResult) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("MoveToPlaned error: got %v, want %v", err, want)
	}
	assertPathExists(t, src)
}

const (
	relMar2024IMG1234 = "organized/2024/03/15/IMG_1234.jpg"
	img0001Name       = "IMG_0001.jpg"

	// maxFilenameSegmentLen mirrors filestore's unexported cap on the final
	// organized filename component (the common NAME_MAX filesystem limit);
	// the truncation tests below assert against this shared value.
	maxFilenameSegmentLen = 255

	// suffixFilenameTestID is a 43-char identifier matching the length of a
	// production SafeID (SafeIDEncodedLen), as used for the always-applied
	// organized-filename suffix.
	suffixFilenameTestID = "QEzizTsZbhLknu3BxIqchpZg6BiVPEM7p8HYKhmIpCc"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// openTestMover creates a Mover backed by a temp directory.
func openTestMover(t *testing.T) *filestore.Mover {
	t.Helper()
	dir := t.TempDir()
	return filestore.New(dir)
}

// writeFile writes content to a file at the given path.
func writeFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("MkdirAll %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}

// assertPathExists checks that a file or directory exists.
func assertPathExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Errorf("path %s should exist", path)
	}
}

// assertPathNotExists checks that a file or directory does not exist.
func assertPathNotExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		t.Errorf("path %s should not exist", path)
	} else if !os.IsNotExist(err) {
		t.Errorf("unexpected error checking %s: %v", path, err)
	}
}

// assertMtimeMatches asserts the file at path has an mtime equal to the
// given creation date string, within filesystem timestamp resolution.
func assertMtimeMatches(t *testing.T, path, creationDate string) {
	t.Helper()
	want, err := time.Parse(time.RFC3339, creationDate)
	if err != nil {
		t.Fatalf("bad creationDate in test %q: %v", creationDate, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat %s: %v", path, err)
	}
	if !info.ModTime().Truncate(time.Second).Equal(want) {
		t.Errorf("mtime of %s: got %v, want %v", path, info.ModTime(), want)
	}
}

// assertMtimeRecent asserts the file at path has an mtime close to now,
// i.e. Chtimes was not applied and the file kept its upload-time mtime.
func assertMtimeRecent(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat %s: %v", path, err)
	}
	if since := time.Since(info.ModTime()); since > 5*time.Second {
		t.Errorf("mtime of %s: %v is %v in the past; expected upload-time mtime", path, info.ModTime(), since)
	}
}

// ---------------------------------------------------------------------------
// StoragePath
// ---------------------------------------------------------------------------

func TestStoragePath_MatchesConstructor(t *testing.T) {
	m := openTestMover(t)

	if m.StoragePath() == "" {
		t.Error("StoragePath should not be empty")
	}

	// StoragePath should return the path passed to New.
	expected := m.StoragePath()
	// Verify planned destinations are rooted under it: the absolute path of
	// an organized file must live inside the storage root.
	plan := m.PlanDestination("2024-01-01T00:00:00Z", "", "test.txt", "tusd-storage")
	if !strings.HasPrefix(plan.Abs, expected) {
		t.Errorf("abs path %q should start with storage path %q", plan.Abs, expected)
	}
}

// ---------------------------------------------------------------------------
// Mover instantiation
// ---------------------------------------------------------------------------

func TestNew_CreatesNonNil(t *testing.T) {
	m := filestore.New("/tmp/test-storage-12345")
	if m == nil {
		t.Fatal("New returned nil")
	}
	if m.StoragePath() != "/tmp/test-storage-12345" {
		t.Errorf("StoragePath: got %q, want %q", m.StoragePath(), "/tmp/test-storage-12345")
	}
}

// ---------------------------------------------------------------------------
// RemoveOrganizedFile
// ---------------------------------------------------------------------------

func TestRemoveOrganizedFile_Success(t *testing.T) {
	m := openTestMover(t)

	// Create a file in the organized tree.
	relPath := "organized/2024/03/15/test_image.jpg"
	absPath := filepath.Join(m.StoragePath(), relPath)
	writeFile(t, absPath, []byte("organized file content"))

	// Remove it.
	if err := m.RemoveOrganizedFile(relPath); err != nil {
		t.Fatalf("RemoveOrganizedFile failed: %v", err)
	}

	// File should be gone.
	assertPathNotExists(t, absPath)
}

func TestRemoveOrganizedFile_Idempotent(t *testing.T) {
	m := openTestMover(t)

	relPath := "organized/2024/06/20/photo.jpg"
	absPath := filepath.Join(m.StoragePath(), relPath)
	writeFile(t, absPath, []byte("content"))

	// First removal succeeds.
	if err := m.RemoveOrganizedFile(relPath); err != nil {
		t.Fatalf("first RemoveOrganizedFile failed: %v", err)
	}

	// Second removal (file already gone) must also return nil.
	if err := m.RemoveOrganizedFile(relPath); err != nil {
		t.Errorf("second RemoveOrganizedFile should return nil (idempotent), got: %v", err)
	}

	assertPathNotExists(t, absPath)
}

func TestRemoveOrganizedFile_NonExistentFile(t *testing.T) {
	m := openTestMover(t)

	// A path that never existed.
	relPath := "organized/2024/01/01/never_existed.txt"

	// Should return nil (not an error).
	if err := m.RemoveOrganizedFile(relPath); err != nil {
		t.Errorf("RemoveOrganizedFile for non-existent file should return nil, got: %v", err)
	}
}

func TestRemoveOrganizedFile_EmptyPath(t *testing.T) {
	m := openTestMover(t)

	// Empty path is a clean no-op for uploads that were never completed.
	if err := m.RemoveOrganizedFile(""); err != nil {
		t.Errorf("RemoveOrganizedFile with empty path should return nil, got: %v", err)
	}
}

func TestRemoveOrganizedFile_PathTraversal(t *testing.T) {
	m := openTestMover(t)

	// Create a file just outside the storage root to verify it is NOT removed.
	outsidePath := filepath.Join(filepath.Dir(m.StoragePath()), "escaped_file.txt")
	writeFile(t, outsidePath, []byte("should not be removed"))
	t.Cleanup(func() { _ = os.Remove(outsidePath) })

	// Attempt traversal with ".." segments.
	err := m.RemoveOrganizedFile("../../escaped_file.txt")
	if err == nil {
		t.Error("expected error for path traversal, got nil")
	} else {
		t.Logf("got expected error: %v", err)
	}

	// The outside file should still exist.
	assertPathExists(t, outsidePath)
}

func TestRemoveOrganizedFile_DeepPathTraversal(t *testing.T) {
	m := openTestMover(t)

	outsidePath := filepath.Join(filepath.Dir(m.StoragePath()), "deep_escape.txt")
	writeFile(t, outsidePath, []byte("should not be removed"))
	t.Cleanup(func() { _ = os.Remove(outsidePath) })

	// Deeper traversal.
	err := m.RemoveOrganizedFile("organized/2024/../../../deep_escape.txt")
	if err == nil {
		t.Error("expected error for deep path traversal, got nil")
	} else {
		t.Logf("got expected error: %v", err)
	}

	assertPathExists(t, outsidePath)
}

func TestRemoveOrganizedFile_PermissionDenied(t *testing.T) {
	m := openTestMover(t)

	// Create a file in a subdirectory.
	relPath := "organized/2024/08/01/protected_file.txt"
	absPath := filepath.Join(m.StoragePath(), relPath)
	writeFile(t, absPath, []byte("protected content"))

	// Make the parent directory read-only so the file cannot be removed.
	parentDir := filepath.Dir(absPath)
	origMode, err := os.Stat(parentDir)
	if err != nil {
		t.Fatalf("Stat parent dir: %v", err)
	}
	//nolint:gosec // permission-denial test intentionally changes directory mode
	if err := os.Chmod(parentDir, 0o555); err != nil {
		t.Fatalf("Chmod parent dir to 0555: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(parentDir, origMode.Mode()) })

	// Removing the file should fail because the directory is read-only.
	err = m.RemoveOrganizedFile(relPath)
	if err == nil {
		t.Error("expected error for permission denied, got nil")
	} else {
		t.Logf("got expected permission error: %v", err)
	}

	// File should still exist.
	assertPathExists(t, absPath)

	// Restore permissions so cleanup can remove the temp dir.
	//nolint:gosec // permission-denial test intentionally restores directory mode
	_ = os.Chmod(parentDir, 0o755)
}

// ---------------------------------------------------------------------------
// PlanDestination
// ---------------------------------------------------------------------------

func TestPlanDestination_BasicRFC3339(t *testing.T) {
	m := openTestMover(t)

	plan := m.PlanDestination(testCreationDate, "", "IMG_1234.jpg", "tusd-abc")
	wantRel := "organized/2024/03/15/IMG_1234_tusd-abc.jpg"
	if plan.Rel != wantRel {
		t.Errorf("rel: got %q, want %q", plan.Rel, wantRel)
	}
	expectedAbs := filepath.Join(m.StoragePath(), plan.Rel)
	if plan.Abs != expectedAbs {
		t.Errorf("abs: got %q, want %q", plan.Abs, expectedAbs)
	}
}

func TestPlanDestination_DateOnly(t *testing.T) {
	m := openTestMover(t)

	plan := m.PlanDestination("2024-06-20", "", "IMG_5678.jpg", "tusd-def")
	wantRel := "organized/2024/06/20/IMG_5678_tusd-def.jpg"
	if plan.Rel != wantRel {
		t.Errorf("rel: got %q, want %q", plan.Rel, wantRel)
	}
	expectedAbs := filepath.Join(m.StoragePath(), plan.Rel)
	if plan.Abs != expectedAbs {
		t.Errorf("abs: got %q, want %q", plan.Abs, expectedAbs)
	}
}

func TestPlanDestination_CreatedAtFallbackWhenEmpty(t *testing.T) {
	m := openTestMover(t)

	// When creationDate is empty, createdAt is used as fallback.
	plan := m.PlanDestination("", "2024-07-04T12:00:00Z", "IMG_video.mp4", "tusd-fallback")
	wantRel := "organized/2024/07/04/IMG_video_tusd-fallback.mp4"
	if plan.Rel != wantRel {
		t.Errorf("rel: got %q, want %q", plan.Rel, wantRel)
	}
	expectedAbs := filepath.Join(m.StoragePath(), plan.Rel)
	if plan.Abs != expectedAbs {
		t.Errorf("abs: got %q, want %q", plan.Abs, expectedAbs)
	}
}

func TestPlanDestination_CreatedAtFallbackWhenUnparseable(t *testing.T) {
	m := openTestMover(t)

	// When creationDate is unparseable, createdAt is used as fallback.
	plan := m.PlanDestination("bad-date", "2024-07-04", "photo.jpg", "tusd-fallback2")
	wantRel := "organized/2024/07/04/photo_tusd-fallback2.jpg"
	if plan.Rel != wantRel {
		t.Errorf("rel: got %q, want %q", plan.Rel, wantRel)
	}
	expectedAbs := filepath.Join(m.StoragePath(), plan.Rel)
	if plan.Abs != expectedAbs {
		t.Errorf("abs: got %q, want %q", plan.Abs, expectedAbs)
	}
}

func TestPlanDestination_PreexistingPlainFileUnaffected(t *testing.T) {
	m := openTestMover(t)

	// A file already sitting at the plain (unsuffixed) path — e.g. content
	// organized before always-suffixing shipped — is left untouched: the
	// always-applied suffix means this record's destination never targets it.
	existingAbs := filepath.Join(m.StoragePath(), relMar2024IMG1234)
	writeFile(t, existingAbs, []byte("existing"))

	// The plan is fully deterministic: <stem>_<id><ext>, regardless of
	// what exists on disk.
	plan := m.PlanDestination(testCreationDate, "", "IMG_1234.jpg", "tusd-collision")
	expectedRel := "organized/2024/03/15/IMG_1234_tusd-collision.jpg"
	if plan.Rel != expectedRel {
		t.Errorf("rel: got %q, want %q", plan.Rel, expectedRel)
	}
	expectedAbs := filepath.Join(m.StoragePath(), expectedRel)
	if plan.Abs != expectedAbs {
		t.Errorf("abs: got %q, want %q", plan.Abs, expectedAbs)
	}

	// Existing file should remain untouched.
	assertPathExists(t, existingAbs)
}

func TestPlanDestination_BothDatesEmpty(t *testing.T) {
	m := openTestMover(t)

	plan := m.PlanDestination("", "", "file.txt", "tusd-empty")
	wantRel := "organized/unknown/unknown/unknown/file_tusd-empty.txt"
	if plan.Rel != wantRel {
		t.Errorf("rel: got %q, want %q", plan.Rel, wantRel)
	}
	expectedAbs := filepath.Join(m.StoragePath(), plan.Rel)
	if plan.Abs != expectedAbs {
		t.Errorf("abs: got %q, want %q", plan.Abs, expectedAbs)
	}
}

func TestPlanDestination_PreexistingPlainFileContentPreserved(t *testing.T) {
	m := openTestMover(t)

	// Create existing file at the plain path.
	existingAbs := filepath.Join(m.StoragePath(), "organized/2024/01/01/same_name.txt")
	writeFile(t, existingAbs, []byte("do not overwrite"))

	// The always-applied suffix means the plan never targets the existing
	// plain file, so it is not overwritten.
	plan := m.PlanDestination("2024-01-01T00:00:00Z", "", "same_name.txt", "tusd-preserve")
	expectedRel := "organized/2024/01/01/same_name_tusd-preserve.txt"
	if plan.Rel != expectedRel {
		t.Errorf("rel: got %q, want %q", plan.Rel, expectedRel)
	}
	expectedAbs := filepath.Join(m.StoragePath(), expectedRel)
	if plan.Abs != expectedAbs {
		t.Errorf("abs: got %q, want %q", plan.Abs, expectedAbs)
	}

	// The original file must still be at its original path.
	assertPathExists(t, existingAbs)
	//nolint:gosec // test-only read/write of a temp-dir file, not attacker-controlled
	got, err := os.ReadFile(existingAbs)
	if err != nil {
		t.Fatalf("ReadFile existing: %v", err)
	}
	if string(got) != "do not overwrite" {
		t.Errorf("existing file content changed: got %q, want %q", string(got), "do not overwrite")
	}
}

func TestPlanDestination_SuffixNoExtension(t *testing.T) {
	m := openTestMover(t)

	// Create existing file without extension.
	existingAbs := filepath.Join(m.StoragePath(), "organized/2024/02/02/README")
	writeFile(t, existingAbs, []byte("readme"))

	plan := m.PlanDestination("2024-02-02T00:00:00Z", "", "README", "tusd-noext")
	expectedRel := "organized/2024/02/02/README_tusd-noext"
	if plan.Rel != expectedRel {
		t.Errorf("rel: got %q, want %q", plan.Rel, expectedRel)
	}
	expectedAbs := filepath.Join(m.StoragePath(), expectedRel)
	if plan.Abs != expectedAbs {
		t.Errorf("abs: got %q, want %q", plan.Abs, expectedAbs)
	}

	assertPathExists(t, existingAbs)
}

func TestPlanDestination_SuffixMultipleDots(t *testing.T) {
	m := openTestMover(t)

	// Create existing file with multiple dots.
	existingAbs := filepath.Join(m.StoragePath(), "organized/2024/08/01/archive.tar.gz")
	writeFile(t, existingAbs, []byte("existing archive"))

	plan := m.PlanDestination("2024-08-01T00:00:00Z", "", "archive.tar.gz", "tusd-multidot")
	// filepath.Ext returns ".gz" for "archive.tar.gz", so the result is "archive.tar_tusd-multidot.gz".
	expectedRel := "organized/2024/08/01/archive.tar_tusd-multidot.gz"
	if plan.Rel != expectedRel {
		t.Errorf("rel: got %q, want %q", plan.Rel, expectedRel)
	}
	expectedAbs := filepath.Join(m.StoragePath(), expectedRel)
	if plan.Abs != expectedAbs {
		t.Errorf("abs: got %q, want %q", plan.Abs, expectedAbs)
	}

	assertPathExists(t, existingAbs)
}

// ---------------------------------------------------------------------------
// MoveFile (standalone) — basic file move
// ---------------------------------------------------------------------------

func TestMoveFileStandalone_Basic(t *testing.T) {
	dir := t.TempDir()

	src := filepath.Join(dir, "source.txt")
	dst := filepath.Join(dir, "dest", "file.txt")
	content := []byte("standalone move content")
	writeFile(t, src, content)

	if err := filestore.MoveFile(src, dst, ""); err != nil {
		t.Fatalf("MoveFile failed: %v", err)
	}

	// Source should be gone.
	assertPathNotExists(t, src)

	// Destination should exist with content.
	assertPathExists(t, dst)
	//nolint:gosec // test-only read/write of a temp-dir file, not attacker-controlled
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("content: got %q, want %q", string(got), string(content))
	}
}

func TestMoveFileStandalone_CreatesDestDir(t *testing.T) {
	dir := t.TempDir()

	src := filepath.Join(dir, "a", "source.bin")
	dst := filepath.Join(dir, "deeply", "nested", "dir", "output.bin")
	writeFile(t, src, []byte("data"))

	if err := filestore.MoveFile(src, dst, ""); err != nil {
		t.Fatalf("MoveFile failed: %v", err)
	}

	assertPathNotExists(t, src)
	assertPathExists(t, dst)
}

func TestMoveFileStandalone_SameDirectoryMove(t *testing.T) {
	dir := t.TempDir()

	src := filepath.Join(dir, "rename_me.txt")
	dst := filepath.Join(dir, "renamed.txt")
	writeFile(t, src, []byte("rename content"))

	if err := filestore.MoveFile(src, dst, ""); err != nil {
		t.Fatalf("MoveFile failed: %v", err)
	}

	assertPathNotExists(t, src)
	assertPathExists(t, dst)
	//nolint:gosec // test-only read/write of a temp-dir file, not attacker-controlled
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "rename content" {
		t.Errorf("content: got %q, want %q", string(got), "rename content")
	}
}

// ---------------------------------------------------------------------------
// MoveFile (standalone) — idempotent recovery
// ---------------------------------------------------------------------------

func TestMoveFileStandalone_IdempotentSrcMissingDstExists(t *testing.T) {
	dir := t.TempDir()

	src := filepath.Join(dir, "gone.txt")
	dst := filepath.Join(dir, "already_moved.txt")
	writeFile(t, dst, []byte("already at destination"))

	// Source doesn't exist, destination exists — MoveFile should treat as already moved.
	if err := filestore.MoveFile(src, dst, ""); err != nil {
		t.Errorf("MoveFile should return nil for already-moved file, got: %v", err)
	}

	// Destination should still exist with original content.
	assertPathExists(t, dst)
	//nolint:gosec // test-only read/write of a temp-dir file, not attacker-controlled
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "already at destination" {
		t.Errorf("content: got %q, want %q", string(got), "already at destination")
	}
}

func TestMoveFileStandalone_IdempotentSrcMissingDstExistsLarge(t *testing.T) {
	dir := t.TempDir()

	src := filepath.Join(dir, "large_gone.bin")
	dst := filepath.Join(dir, "large_dst.bin")

	// Create a 1 MB destination file.
	size := 1 << 20
	content := make([]byte, size)
	for i := range content {
		content[i] = byte(i % 256)
	}
	if err := os.WriteFile(dst, content, 0o600); err != nil {
		t.Fatalf("WriteFile dst: %v", err)
	}

	// Source missing, destination exists — idempotent.
	if err := filestore.MoveFile(src, dst, ""); err != nil {
		t.Errorf("MoveFile should be idempotent when dst exists, got: %v", err)
	}

	// Verify content intact.
	//nolint:gosec // test-only read/write of a temp-dir file, not attacker-controlled
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(got) != size {
		t.Errorf("size: got %d, want %d", len(got), size)
	}
	for i := range got {
		if got[i] != content[i] {
			t.Errorf("byte %d: got %d, want %d", i, got[i], content[i])
			break
		}
	}
}

func TestMoveFileStandalone_BothMissingError(t *testing.T) {
	dir := t.TempDir()

	src := filepath.Join(dir, "nonexistent.txt")
	dst := filepath.Join(dir, "nowhere.txt")

	err := filestore.MoveFile(src, dst, "")
	if err == nil {
		t.Fatal("expected error when both source and destination are missing")
	}
}

func TestMoveFileStandalone_BothMissingError_DeepPaths(t *testing.T) {
	dir := t.TempDir()

	src := filepath.Join(dir, "a", "b", "c", "missing.txt")
	dst := filepath.Join(dir, "x", "y", "z", "nowhere.txt")

	err := filestore.MoveFile(src, dst, "")
	if err == nil {
		t.Fatal("expected error when both source and destination are missing with deep paths")
	}
}

func TestMoveFileStandalone_SrcMissingDstMissingButDstParentCreated(t *testing.T) {
	dir := t.TempDir()

	src := filepath.Join(dir, "missing_src.txt")
	dst := filepath.Join(dir, "parent", "missing_dst.txt")

	// Parent directory does not exist, source missing — should error.
	err := filestore.MoveFile(src, dst, "")
	if err == nil {
		t.Fatal("expected error when src missing and parent dir doesn't exist")
	}
}

// ---------------------------------------------------------------------------
// MoveFile (standalone) — Chtimes timestamp application (Task 4)
// ---------------------------------------------------------------------------

func TestMoveFileStandalone_ValidCreationDateSetsMtime(t *testing.T) {
	dir := t.TempDir()

	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "dst.txt")
	writeFile(t, src, []byte("content"))

	creationDate := testCreationDate
	if err := filestore.MoveFile(src, dst, creationDate); err != nil {
		t.Fatalf("MoveFile failed: %v", err)
	}

	assertPathNotExists(t, src)
	assertMtimeMatches(t, dst, creationDate)
}

func TestMoveFileStandalone_DateOnlyCreationDateSetsMtimeUTC(t *testing.T) {
	dir := t.TempDir()

	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "dst.txt")
	writeFile(t, src, []byte("content"))

	// Date-only creation dates parse to UTC midnight (pre-existing path
	// behavior, now visible on disk via Chtimes).
	if err := filestore.MoveFile(src, dst, "2024-06-20"); err != nil {
		t.Fatalf("MoveFile failed: %v", err)
	}

	want := time.Date(2024, 6, 20, 0, 0, 0, 0, time.UTC)
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("Stat dst: %v", err)
	}
	if !info.ModTime().Truncate(time.Second).Equal(want) {
		t.Errorf("mtime: got %v, want %v", info.ModTime(), want)
	}
}

func TestMoveFileStandalone_EmptyCreationDateKeepsUploadTime(t *testing.T) {
	dir := t.TempDir()

	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "dst.txt")
	writeFile(t, src, []byte("content"))

	// Empty creationDate: move succeeds, Chtimes skipped, upload-time mtime kept.
	if err := filestore.MoveFile(src, dst, ""); err != nil {
		t.Fatalf("MoveFile failed: %v", err)
	}

	assertPathNotExists(t, src)
	assertMtimeRecent(t, dst)
}

func TestMoveFileStandalone_GarbageCreationDateKeepsUploadTime(t *testing.T) {
	dir := t.TempDir()

	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "dst.txt")
	writeFile(t, src, []byte("content"))

	// Unparseable creationDate: same as empty — move succeeds, no Chtimes.
	if err := filestore.MoveFile(src, dst, "not-a-date"); err != nil {
		t.Fatalf("MoveFile failed: %v", err)
	}

	assertPathNotExists(t, src)
	assertMtimeRecent(t, dst)
}

func TestMoveFileStandalone_OutOfRangeCreationDateKeepsUploadTime(t *testing.T) {
	// Parseable but implausible dates (EXIF clock corruption: epoch output
	// from a dead RTC battery, far-future timestamps) must be clamped out —
	// Chtimes skipped, upload-time mtime kept. This proves the sanity-range
	// clamp, not just the parser.
	for _, tt := range []struct {
		name string
		date string
	}{
		{name: "epoch", date: "1970-01-01T00:00:00Z"},
		{name: "just before sane minimum", date: "1989-12-31T23:59:59Z"},
		{name: "far future", date: "9999-01-01T00:00:00Z"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()

			src := filepath.Join(dir, "src.txt")
			dst := filepath.Join(dir, "dst.txt")
			writeFile(t, src, []byte("content"))

			if err := filestore.MoveFile(src, dst, tt.date); err != nil {
				t.Fatalf("MoveFile failed: %v", err)
			}

			assertPathNotExists(t, src)
			assertMtimeRecent(t, dst)
		})
	}
}

// ---------------------------------------------------------------------------
// PlanDestination + MoveFile integration: PlanDestination then standalone MoveFile
// ---------------------------------------------------------------------------

func TestPlanDestinationThenMoveFile(t *testing.T) {
	m := openTestMover(t)

	// Create source file.
	src := filepath.Join(m.StoragePath(), "incoming", "tusd-integration")
	content := []byte("integration test content")
	writeFile(t, src, content)

	// Plan destination.
	plan := m.PlanDestination("2024-09-15T10:30:00Z", "", "IMG_final.jpg", "tusd-integration")

	// Move file using standalone MoveFile, passing the planned DateUsed so
	// the moved file's timestamps match the resolved date.
	if err := filestore.MoveFile(src, plan.Abs, plan.DateUsed); err != nil {
		t.Fatalf("MoveFile failed: %v", err)
	}

	// Source should be gone.
	assertPathNotExists(t, src)

	// Destination should exist with content.
	assertPathExists(t, plan.Abs)
	got, err := os.ReadFile(plan.Abs)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("content: got %q, want %q", string(got), string(content))
	}

	// Rel path should match.
	expectedRel := "organized/2024/09/15/IMG_final_tusd-integration.jpg"
	if plan.Rel != expectedRel {
		t.Errorf("rel: got %q, want %q", plan.Rel, expectedRel)
	}

	// The moved file's mtime should reflect the resolved date (DateUsed),
	// proving the DateUsed -> MoveFile -> Chtimes wiring end to end.
	assertMtimeMatches(t, plan.Abs, plan.DateUsed)
}

func TestPlanDestinationThenMoveFile_PreexistingPlainFile(t *testing.T) {
	m := openTestMover(t)

	// Create an existing file at the plain path.
	existingAbs := filepath.Join(m.StoragePath(), "organized/2024/10/01/photo.jpg")
	writeFile(t, existingAbs, []byte("existing photo"))

	// Plan destination for a new file with the same name.
	src := filepath.Join(m.StoragePath(), "incoming", "tusd-collision-int")
	writeFile(t, src, []byte("new photo"))

	plan := m.PlanDestination("2024-10-01T12:00:00Z", "", "photo.jpg", "tusd-collision-int")

	// The planned destination always carries the id suffix; the pre-existing
	// plain file is never targeted and therefore survives.
	expectedRel := "organized/2024/10/01/photo_tusd-collision-int.jpg"
	if plan.Rel != expectedRel {
		t.Errorf("rel: got %q, want %q", plan.Rel, expectedRel)
	}

	// Move the file, passing the planned DateUsed for timestamp application.
	if err := filestore.MoveFile(src, plan.Abs, plan.DateUsed); err != nil {
		t.Fatalf("MoveFile failed: %v", err)
	}

	// Source should be gone.
	assertPathNotExists(t, src)

	// Both existing and new files should exist.
	assertPathExists(t, existingAbs)
	assertPathExists(t, plan.Abs)

	// Verify content of both.
	//nolint:gosec // test-only read/write of a temp-dir file, not attacker-controlled
	gotExisting, _ := os.ReadFile(existingAbs)
	if string(gotExisting) != "existing photo" {
		t.Errorf("existing content: got %q, want %q", string(gotExisting), "existing photo")
	}
	gotNew, _ := os.ReadFile(plan.Abs)
	if string(gotNew) != "new photo" {
		t.Errorf("new content: got %q, want %q", string(gotNew), "new photo")
	}
}

// ---------------------------------------------------------------------------
// Edge cases for PlanDestination
// ---------------------------------------------------------------------------

func TestPlanDestination_RFC3339Nano(t *testing.T) {
	m := openTestMover(t)

	plan := m.PlanDestination("2024-12-31T23:59:59.123456789Z", "", "nano.jpg", "tusd-nano")
	wantRel := "organized/2024/12/31/nano_tusd-nano.jpg"
	if plan.Rel != wantRel {
		t.Errorf("rel: got %q, want %q", plan.Rel, wantRel)
	}
	expectedAbs := filepath.Join(m.StoragePath(), plan.Rel)
	if plan.Abs != expectedAbs {
		t.Errorf("abs: got %q, want %q", plan.Abs, expectedAbs)
	}
}

func TestPlanDestination_UnparseableDateBecomesSegment(t *testing.T) {
	m := openTestMover(t)

	plan := m.PlanDestination("not-a-date", "", "file.txt", "tusd-unparseable")
	// The raw string is used as the year segment.
	wantRel := "organized/not-a-date/unknown/unknown/file_tusd-unparseable.txt"
	if plan.Rel != wantRel {
		t.Errorf("rel: got %q, want %q", plan.Rel, wantRel)
	}
	expectedAbs := filepath.Join(m.StoragePath(), plan.Rel)
	if plan.Abs != expectedAbs {
		t.Errorf("abs: got %q, want %q", plan.Abs, expectedAbs)
	}
}

func TestPlanDestination_CreationDatePreferredOverCreatedAt(t *testing.T) {
	m := openTestMover(t)

	// Both dates are valid; creationDate should be preferred.
	plan := m.PlanDestination("2024-01-15T10:00:00Z", "2024-06-20T12:00:00Z", "preferred.jpg", "tusd-pref")
	wantRel := "organized/2024/01/15/preferred_tusd-pref.jpg"
	if plan.Rel != wantRel {
		t.Errorf("rel: got %q, want %q (creationDate should be preferred)", plan.Rel, wantRel)
	}
	expectedAbs := filepath.Join(m.StoragePath(), plan.Rel)
	if plan.Abs != expectedAbs {
		t.Errorf("abs: got %q, want %q", plan.Abs, expectedAbs)
	}
}

func TestPlanDestination_CreationDateNotParseableCreatedAtPreferred(t *testing.T) {
	m := openTestMover(t)

	// creationDate is unparseable, createdAt is valid - should use createdAt.
	plan := m.PlanDestination("garbage-date", "2024-11-05T08:30:00Z", "fallback_test.jpg", "tusd-fb")
	wantRel := "organized/2024/11/05/fallback_test_tusd-fb.jpg"
	if plan.Rel != wantRel {
		t.Errorf("rel: got %q, want %q", plan.Rel, wantRel)
	}
	expectedAbs := filepath.Join(m.StoragePath(), plan.Rel)
	if plan.Abs != expectedAbs {
		t.Errorf("abs: got %q, want %q", plan.Abs, expectedAbs)
	}
}

func TestPlanDestination_EmptyFilename(t *testing.T) {
	m := openTestMover(t)

	plan := m.PlanDestination(testCreationDate, "", "", "tusd-empty-file")
	// filepath.Join removes empty components.
	if plan.Rel != "organized/2024/03/15" {
		t.Errorf("rel: got %q, want %q", plan.Rel, "organized/2024/03/15")
	}
	expectedAbs := filepath.Join(m.StoragePath(), plan.Rel)
	if plan.Abs != expectedAbs {
		t.Errorf("abs: got %q, want %q", plan.Abs, expectedAbs)
	}
}

func TestPlanDestination_DateUsed(t *testing.T) {
	m := openTestMover(t)

	tests := []struct {
		desc         string
		creationDate string
		createdAt    string
		wantDateUsed string
	}{
		{
			desc:         "valid creationDate is used verbatim",
			creationDate: testCreationDate,
			createdAt:    "",
			wantDateUsed: testCreationDate,
		},
		{
			desc:         "valid date-only creationDate is used verbatim",
			creationDate: "2024-06-20",
			createdAt:    "",
			wantDateUsed: "2024-06-20",
		},
		{
			desc:         "creationDate preferred over createdAt when both valid",
			creationDate: "2024-01-15T10:00:00Z",
			createdAt:    "2024-06-20T12:00:00Z",
			wantDateUsed: "2024-01-15T10:00:00Z",
		},
		{
			desc:         "falls back to createdAt when creationDate empty",
			creationDate: "",
			createdAt:    "2024-07-04T12:00:00Z",
			wantDateUsed: "2024-07-04T12:00:00Z",
		},
		{
			desc:         "falls back to createdAt when creationDate unparseable",
			creationDate: "not-a-date",
			createdAt:    "2024-07-04",
			wantDateUsed: "2024-07-04",
		},
		{
			desc:         "raw unparseable creationDate kept when createdAt also unparseable",
			creationDate: "garbage-date",
			createdAt:    testAlsoGarbage,
			wantDateUsed: "garbage-date",
		},
		{
			desc:         "raw unparseable createdAt kept when creationDate empty",
			creationDate: "",
			createdAt:    testAlsoGarbage,
			wantDateUsed: testAlsoGarbage,
		},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			plan := m.PlanDestination(tt.creationDate, tt.createdAt, "file.txt", "tusd-dateused")
			if plan.DateUsed != tt.wantDateUsed {
				t.Errorf("DateUsed: got %q, want %q", plan.DateUsed, tt.wantDateUsed)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// PlanDestination — filename length limits
// ---------------------------------------------------------------------------

func TestPlanDestination_LongFilenameStemTruncatedSuffixAndExtensionIntact(t *testing.T) {
	m := openTestMover(t)

	// A 255-byte filename (the sanitizer's upper bound): 251 'a' bytes plus
	// ".jpg". Adding "_" + 43-char id would exceed the filesystem component
	// limit, so only the stem is truncated to keep the full name within it.
	id := suffixFilenameTestID
	filename := strings.Repeat("a", maxFilenameSegmentLen-len(".jpg")) + ".jpg"
	if len(filename) != maxFilenameSegmentLen {
		t.Fatalf("test setup: wanted %d-byte filename, got %d", maxFilenameSegmentLen, len(filename))
	}

	plan := m.PlanDestination(testCreationDate, "", filename, id)

	got := filepath.Base(plan.Rel)
	if len(got) > maxFilenameSegmentLen {
		t.Errorf("final filename %d bytes exceeds filesystem limit %d", len(got), maxFilenameSegmentLen)
	}

	// The _<id> suffix and the extension always survive intact; only the
	// stem is cut, at exactly the point that leaves the full name at the
	// filesystem limit.
	wantStemLen := maxFilenameSegmentLen - 1 - len(id) - len(".jpg")
	want := filename[:wantStemLen] + "_" + id + ".jpg"
	if got != want {
		t.Errorf("final filename: got %q, want %q", got, want)
	}
	if !strings.HasSuffix(got, "_"+id+".jpg") {
		t.Errorf("suffix and extension must survive intact, got %q", got)
	}
}

func TestPlanDestination_LongFilenameWithinLimitKeepsWholeStem(t *testing.T) {
	m := openTestMover(t)

	// A realistically long name that still fits once the suffix is added:
	// no truncation happens and the whole stem survives.
	id := suffixFilenameTestID
	stem := "IMG_" + strings.Repeat("b", 100)
	filename := stem + ".jpg"

	plan := m.PlanDestination(testCreationDate, "", filename, id)

	want := stem + "_" + id + ".jpg"
	if got := filepath.Base(plan.Rel); got != want {
		t.Errorf("final filename: got %q, want %q", got, want)
	}
	if len(filepath.Base(plan.Rel)) > maxFilenameSegmentLen {
		t.Error("final filename exceeds filesystem limit")
	}
}

// ---------------------------------------------------------------------------
// PlanDestination — pre-existing foreign file safety net
// ---------------------------------------------------------------------------

func TestPlanDestination_PreexistingFileAtDestinationWarns(t *testing.T) {
	m := openTestMover(t)

	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)

	// A foreign file already occupying the exact deterministic destination.
	filename := "IMG_1234_" + suffixFilenameTestID + ".jpg"
	existingAbs := filepath.Join(m.StoragePath(), "organized/2024/03/15", filename)
	writeFile(t, existingAbs, []byte("foreign"))

	plan := m.PlanDestination(testCreationDate, "", "IMG_1234.jpg", suffixFilenameTestID)

	// The WARN stat is a safety net only: the planned paths are unchanged.
	wantRel := "organized/2024/03/15/" + filename
	if plan.Rel != wantRel {
		t.Errorf("paths must be unchanged by the safety-net stat, got %q, want %q", plan.Rel, wantRel)
	}
	if !strings.Contains(buf.String(), "WARN") {
		t.Errorf("expected WARN log about the pre-existing file, got: %q", buf.String())
	}
}

func TestPlanAndMoveOverwritesForeignFileAtDestination(t *testing.T) {
	m := openTestMover(t)

	// A foreign file already sitting at the deterministic destination path.
	planned := m.PlanDestination(testCreationDate, "", "IMG_1234.jpg", suffixFilenameTestID)
	writeFile(t, planned.Abs, []byte("foreign content"))

	// Moving a real upload to the same path silently overwrites it (the WARN
	// safety net has already logged the unexpected pre-existing file).
	src := filepath.Join(m.StoragePath(), "incoming", "tusd-overwrite")
	writeFile(t, src, []byte("real upload"))

	result, err := m.PlanAndMove(src, testCreationDate, "", "IMG_1234.jpg", suffixFilenameTestID, nil)
	if err != nil {
		t.Fatalf("PlanAndMove failed: %v", err)
	}
	if result.Abs != planned.Abs {
		t.Errorf("destination: got %q, want %q", result.Abs, planned.Abs)
	}
	data, err := os.ReadFile(planned.Abs)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "real upload" {
		t.Errorf("content: got %q, want the moved upload's content", string(data))
	}
	assertPathNotExists(t, src)
}

// ---------------------------------------------------------------------------
// PlanDestination — absolute path construction
// ---------------------------------------------------------------------------

func TestPlanDestination_AbsolutePathRooted(t *testing.T) {
	m := openTestMover(t)

	plan := m.PlanDestination("2024-04-01T00:00:00Z", "", "rooted.txt", "tusd-rooted")

	t.Logf("m.StoragePath() = %q", m.StoragePath())
	t.Logf("abs = %q", plan.Abs)

	if !strings.HasPrefix(plan.Abs, m.StoragePath()) {
		t.Errorf("abs %q should start with storage path %q", plan.Abs, m.StoragePath())
	}
}

func TestPlanDestination_DifferentDates(t *testing.T) {
	m := openTestMover(t)

	tests := []struct {
		date     string
		filename string
		wantRel  string
		desc     string
	}{
		{"2024-01-01T00:00:00Z", img0001Name, "organized/2024/01/01/IMG_0001_tusd-test.jpg", "january start"},
		{"2024-12-31T23:59:59Z", "IMG_9999.jpg", "organized/2024/12/31/IMG_9999_tusd-test.jpg", "december end"},
		{"2025-06-15T12:00:00Z", "VID_2025.mp4", "organized/2025/06/15/VID_2025_tusd-test.mp4", "june mid-year"},
		{"2024-02-29T10:30:00Z", "leap_day.txt", "organized/2024/02/29/leap_day_tusd-test.txt", "leap day"},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			plan := m.PlanDestination(tt.date, "", tt.filename, "tusd-test")
			if plan.Rel != tt.wantRel {
				t.Errorf("rel: got %q, want %q", plan.Rel, tt.wantRel)
			}
		})
	}
}
