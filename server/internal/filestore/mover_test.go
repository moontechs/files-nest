// Package filestore_test tests file storage and organization operations
// for the iCloud Backup server.
package filestore_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moontechs/files-nest/server/internal/filestore"
)

func TestPlanAndMoveBeforeMoveCallback(t *testing.T) {
	t.Run("nil error proceeds", func(t *testing.T) {
		m := openTestMover(t)
		src := filepath.Join(m.StoragePath(), "incoming", "callback-ok")
		writeFile(t, src, []byte("content"))
		called := false
		plan, err := m.PlanAndMove(src, "2024-01-02T00:00:00Z", "", "file.txt", "id", func(got filestore.PlanDestResult) error {
			called = got.Rel == "organized/2024/01/02/file.txt"
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
		want := errors.New("persist intent")
		plan, err := m.PlanAndMove(src, "2024-01-02T00:00:00Z", "", "file.txt", "id", func(filestore.PlanDestResult) error { return want })
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
	plan := filestore.PlanDestResult{Rel: "organized/2024/01/02/retry.txt", Abs: filepath.Join(m.StoragePath(), "organized/2024/01/02/retry.txt")}

	called := false
	if err := m.MoveToPlaned(src, plan, func(got filestore.PlanDestResult) error { called = got == plan; return nil }); err != nil || !called {
		t.Fatalf("MoveToPlaned success: err=%v callback=%v", err, called)
	}

	src = filepath.Join(m.StoragePath(), "incoming", "retry-error")
	writeFile(t, src, []byte("content"))
	want := errors.New("refresh intent")
	err := m.MoveToPlaned(src, plan, func(filestore.PlanDestResult) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("MoveToPlaned error: got %v, want %v", err, want)
	}
	assertPathExists(t, src)
}

const (
	relMar2024IMG1234 = "organized/2024/03/15/IMG_1234.jpg"
	relDec2024IMG9999 = "organized/2024/12/31/IMG_9999.jpg"
	img0001Name       = "IMG_0001.jpg"
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

// ---------------------------------------------------------------------------
// OrganizedPath
// ---------------------------------------------------------------------------

func TestOrganizedPath_ParsesRFC3339(t *testing.T) {
	m := openTestMover(t)

	rel, abs := m.OrganizedPath("2024-03-15T10:30:00Z", "IMG_1234.jpg")

	expectedRel := relMar2024IMG1234
	if rel != expectedRel {
		t.Errorf("rel: got %q, want %q", rel, expectedRel)
	}

	expectedAbs := filepath.Join(m.StoragePath(), expectedRel)
	if abs != expectedAbs {
		t.Errorf("abs: got %q, want %q", abs, expectedAbs)
	}
}

func TestOrganizedPath_ParsesDateOnly(t *testing.T) {
	m := openTestMover(t)

	rel, abs := m.OrganizedPath("2024-06-20", "IMG_5678.jpg")

	expectedRel := "organized/2024/06/20/IMG_5678.jpg"
	if rel != expectedRel {
		t.Errorf("rel: got %q, want %q", rel, expectedRel)
	}

	expectedAbs := filepath.Join(m.StoragePath(), expectedRel)
	if abs != expectedAbs {
		t.Errorf("abs: got %q, want %q", abs, expectedAbs)
	}
}

func TestOrganizedPath_UnparseableDateFallsBack(t *testing.T) {
	m := openTestMover(t)

	rel, abs := m.OrganizedPath("not-a-date", "file.txt")

	expectedRel := "organized/not-a-date/unknown/unknown/file.txt"
	if rel != expectedRel {
		t.Errorf("rel: got %q, want %q", rel, expectedRel)
	}

	expectedAbs := filepath.Join(m.StoragePath(), expectedRel)
	if abs != expectedAbs {
		t.Errorf("abs: got %q, want %q", abs, expectedAbs)
	}
}

func TestOrganizedPath_EmptyDate(t *testing.T) {
	m := openTestMover(t)

	rel, abs := m.OrganizedPath("", img0001Name)

	// filepath.Join collapses empty segments, so the result has no double slash.
	expectedRel := "organized/unknown/unknown/IMG_0001.jpg"
	if rel != expectedRel {
		t.Errorf("rel: got %q, want %q", rel, expectedRel)
	}

	expectedAbs := filepath.Join(m.StoragePath(), expectedRel)
	if abs != expectedAbs {
		t.Errorf("abs: got %q, want %q", abs, expectedAbs)
	}
}

func TestOrganizedPath_RFC3339Nano(t *testing.T) {
	m := openTestMover(t)

	rel, abs := m.OrganizedPath("2024-12-31T23:59:59.123456789Z", "IMG_9999.jpg")

	expectedRel := relDec2024IMG9999
	if rel != expectedRel {
		t.Errorf("rel: got %q, want %q", rel, expectedRel)
	}

	expectedAbs := filepath.Join(m.StoragePath(), expectedRel)
	if abs != expectedAbs {
		t.Errorf("abs: got %q, want %q", abs, expectedAbs)
	}
}

func TestOrganizedPath_DifferentDates(t *testing.T) {
	m := openTestMover(t)

	tests := []struct {
		date     string
		filename string
		wantRel  string
		desc     string
	}{
		{
			date:     "2024-01-01T00:00:00Z",
			filename: img0001Name,
			wantRel:  "organized/2024/01/01/IMG_0001.jpg",
			desc:     "january start",
		},
		{
			date:     "2024-12-31T23:59:59Z",
			filename: "IMG_9999.jpg",
			wantRel:  relDec2024IMG9999,
			desc:     "december end",
		},
		{
			date:     "2025-06-15T12:00:00Z",
			filename: "VID_2025.mp4",
			wantRel:  "organized/2025/06/15/VID_2025.mp4",
			desc:     "june mid-year",
		},
		{
			date:     "2024-02-29T10:30:00Z",
			filename: "leap_day.txt",
			wantRel:  "organized/2024/02/29/leap_day.txt",
			desc:     "leap day",
		},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			rel, _ := m.OrganizedPath(tt.date, tt.filename)
			if rel != tt.wantRel {
				t.Errorf("rel: got %q, want %q", rel, tt.wantRel)
			}
		})
	}
}

func TestOrganizedPath_StoragePathPrefix(t *testing.T) {
	// Verify the absolute path is rooted at the mover's storage path.
	m := openTestMover(t)

	_, abs := m.OrganizedPath("2024-03-15T10:30:00Z", "test.jpg")

	if !strings.HasPrefix(abs, m.StoragePath()) {
		t.Errorf("abs %q should have prefix %q", abs, m.StoragePath())
	}
}

// ---------------------------------------------------------------------------
// MoveFile — basic success
// ---------------------------------------------------------------------------

func TestMoveFile_Success(t *testing.T) {
	m := openTestMover(t)

	// Create a source file in an incoming-like directory.
	srcDir := filepath.Join(m.StoragePath(), "incoming")
	srcPath := filepath.Join(srcDir, "tusd-abc123")
	content := []byte("test file content for mover")
	writeFile(t, srcPath, content)

	// Move it.
	result, err := m.MoveFile(srcPath, "2024-03-15T10:30:00Z", "IMG_1234.jpg", "tusd-abc123")
	if err != nil {
		t.Fatalf("MoveFile failed: %v", err)
	}

	// Check result paths.
	if result.Src != srcPath {
		t.Errorf("result.Src: got %q, want %q", result.Src, srcPath)
	}
	if result.DstRel != relMar2024IMG1234 {
		t.Errorf("result.DstRel: got %q, want %q", result.DstRel, relMar2024IMG1234)
	}
	expectedAbs := filepath.Join(m.StoragePath(), result.DstRel)
	if result.Dst != expectedAbs {
		t.Errorf("result.Dst: got %q, want %q", result.Dst, expectedAbs)
	}
	if result.Deduplicated {
		t.Error("result.Deduplicated should be false for fresh destination")
	}

	// Source file should no longer exist at the original path.
	assertPathNotExists(t, srcPath)

	// Destination file should exist with original content.
	assertPathExists(t, result.Dst)
	gotContent, err := os.ReadFile(result.Dst)
	if err != nil {
		t.Fatalf("ReadFile destination: %v", err)
	}
	if string(gotContent) != string(content) {
		t.Errorf("content at destination: got %q, want %q", string(gotContent), string(content))
	}
}

func TestMoveFile_CreatesDestinationDirectory(t *testing.T) {
	m := openTestMover(t)

	// Create source file.
	srcPath := filepath.Join(m.StoragePath(), "incoming", "tusd-def456")
	writeFile(t, srcPath, []byte("content"))

	// Move to a deep path — directories should be created automatically.
	result, err := m.MoveFile(srcPath, "2024-06-20T14:30:00Z", "IMG_5678.jpg", "tusd-def456")
	if err != nil {
		t.Fatalf("MoveFile failed: %v", err)
	}

	// Destination directory should exist.
	destDir := filepath.Dir(result.Dst)
	if info, err := os.Stat(destDir); err != nil {
		t.Errorf("destination directory %s should exist: %v", destDir, err)
	} else if !info.IsDir() {
		t.Errorf("destination %s is not a directory", destDir)
	}

	// File should exist at destination.
	assertPathExists(t, result.Dst)
}

// ---------------------------------------------------------------------------
// MoveFile — deduplication
// ---------------------------------------------------------------------------

func TestMoveFile_DeduplicatesWhenDestinationExists(t *testing.T) {
	m := openTestMover(t)

	// Create an existing file at the computed destination path.
	existingRel := relMar2024IMG1234
	existingAbs := filepath.Join(m.StoragePath(), existingRel)
	writeFile(t, existingAbs, []byte("existing content"))

	// Create source file to move.
	srcPath := filepath.Join(m.StoragePath(), "incoming", "tusd-dup001")
	writeFile(t, srcPath, []byte("new content"))

	// Move — should deduplicate because destination exists.
	result, err := m.MoveFile(srcPath, "2024-03-15T10:30:00Z", "IMG_1234.jpg", "tusd-dup001")
	if err != nil {
		t.Fatalf("MoveFile failed: %v", err)
	}

	if !result.Deduplicated {
		t.Error("expected Deduplicated=true when destination already exists")
	}

	// The destination filename should include the backendID.
	expectedFilename := "IMG_1234_tusd-dup001.jpg"
	if filepath.Base(result.Dst) != expectedFilename {
		t.Errorf("destination filename: got %q, want %q", filepath.Base(result.Dst), expectedFilename)
	}

	// The relative path should reflect the deduplicated filename.
	expectedRel := "organized/2024/03/15/IMG_1234_tusd-dup001.jpg"
	if result.DstRel != expectedRel {
		t.Errorf("result.DstRel: got %q, want %q", result.DstRel, expectedRel)
	}

	// Original existing file should still be present.
	assertPathExists(t, existingAbs)
	//nolint:gosec // test-only read/write of a temp-dir file, not attacker-controlled
	gotExisting, err := os.ReadFile(existingAbs)
	if err != nil {
		t.Fatalf("ReadFile existing: %v", err)
	}
	if string(gotExisting) != "existing content" {
		t.Errorf("existing file content changed: got %q, want %q", string(gotExisting), "existing content")
	}

	// New file should be present at deduplicated path with correct content.
	assertPathExists(t, result.Dst)
	gotNew, err := os.ReadFile(result.Dst)
	if err != nil {
		t.Fatalf("ReadFile deduplicated: %v", err)
	}
	if string(gotNew) != "new content" {
		t.Errorf("deduplicated file content: got %q, want %q", string(gotNew), "new content")
	}
}

func TestMoveFile_MultipleDeduplications(t *testing.T) {
	m := openTestMover(t)
	backendID1 := "tusd-first"
	backendID2 := "tusd-second"

	// Move first file — no dedup needed.
	src1 := filepath.Join(m.StoragePath(), "incoming", backendID1)
	writeFile(t, src1, []byte("first"))
	result1, err := m.MoveFile(src1, "2024-03-15T10:30:00Z", "IMG_1234.jpg", backendID1)
	if err != nil {
		t.Fatalf("first MoveFile failed: %v", err)
	}
	if result1.Deduplicated {
		t.Error("first move should not be deduplicated")
	}
	if result1.DstRel != relMar2024IMG1234 {
		t.Errorf("first DstRel: got %q, want %q", result1.DstRel, relMar2024IMG1234)
	}

	// Move second file — same date and filename, should deduplicate.
	src2 := filepath.Join(m.StoragePath(), "incoming", backendID2)
	writeFile(t, src2, []byte("second"))
	result2, err := m.MoveFile(src2, "2024-03-15T10:30:00Z", "IMG_1234.jpg", backendID2)
	if err != nil {
		t.Fatalf("second MoveFile failed: %v", err)
	}
	if !result2.Deduplicated {
		t.Error("second move should be deduplicated")
	}
	expectedRel2 := "organized/2024/03/15/IMG_1234_tusd-second.jpg"
	if result2.DstRel != expectedRel2 {
		t.Errorf("second DstRel: got %q, want %q", result2.DstRel, expectedRel2)
	}

	// Move third file — same date and filename again, should also deduplicate.
	// Note: the deduplication is only against the *computed* path
	// (organized/2024/03/15/IMG_1234.jpg), not against previously
	// deduplicated names. This means the third file will also get the
	// backendID suffix because the base path still exists (occupied by
	// the first file).
	backendID3 := "tusd-third"
	src3 := filepath.Join(m.StoragePath(), "incoming", backendID3)
	writeFile(t, src3, []byte("third"))
	result3, err := m.MoveFile(src3, "2024-03-15T10:30:00Z", "IMG_1234.jpg", backendID3)
	if err != nil {
		t.Fatalf("third MoveFile failed: %v", err)
	}
	if !result3.Deduplicated {
		t.Error("third move should be deduplicated")
	}
	expectedRel3 := "organized/2024/03/15/IMG_1234_tusd-third.jpg"
	if result3.DstRel != expectedRel3 {
		t.Errorf("third DstRel: got %q, want %q", result3.DstRel, expectedRel3)
	}

	// All three files should exist and have correct content.
	for _, tc := range []struct {
		path string
		want string
	}{
		{result1.Dst, "first"},
		{result2.Dst, "second"},
		{result3.Dst, "third"},
	} {
		got, err := os.ReadFile(tc.path)
		if err != nil {
			t.Errorf("ReadFile %s: %v", tc.path, err)
			continue
		}
		if string(got) != tc.want {
			t.Errorf("content at %s: got %q, want %q", tc.path, string(got), tc.want)
		}
	}
}

func TestMoveFile_DeduplicationWithExtension(t *testing.T) {
	m := openTestMover(t)
	backendID := "tusd-ext-001"

	// Create existing file.
	existingAbs := filepath.Join(m.StoragePath(), "organized/2024/06/15/video.mp4")
	writeFile(t, existingAbs, []byte("existing"))

	// Move a file with the same name.
	src := filepath.Join(m.StoragePath(), "incoming", backendID)
	writeFile(t, src, []byte("new video"))
	result, err := m.MoveFile(src, "2024-06-15T12:00:00Z", "video.mp4", backendID)
	if err != nil {
		t.Fatalf("MoveFile failed: %v", err)
	}

	if !result.Deduplicated {
		t.Error("expected deduplication for same filename")
	}
	expectedRel := "organized/2024/06/15/video_tusd-ext-001.mp4"
	if result.DstRel != expectedRel {
		t.Errorf("DstRel: got %q, want %q", result.DstRel, expectedRel)
	}
	if filepath.Ext(result.Dst) != ".mp4" {
		t.Errorf("extension should be preserved, got %q", filepath.Ext(result.Dst))
	}
}

func TestMoveFile_DeduplicationNoExtension(t *testing.T) {
	m := openTestMover(t)
	backendID := "tusd-noext"

	// Create existing file without extension.
	existingAbs := filepath.Join(m.StoragePath(), "organized/2024/01/01/README")
	writeFile(t, existingAbs, []byte("existing"))

	// Move a file with the same name (no extension).
	src := filepath.Join(m.StoragePath(), "incoming", backendID)
	writeFile(t, src, []byte("new"))
	result, err := m.MoveFile(src, "2024-01-01T00:00:00Z", "README", backendID)
	if err != nil {
		t.Fatalf("MoveFile failed: %v", err)
	}

	if !result.Deduplicated {
		t.Error("expected deduplication")
	}
	// For a file without extension, ext is empty, so the filename
	// becomes "README_tusd-noext".
	expectedFilename := "README_tusd-noext"
	if filepath.Base(result.Dst) != expectedFilename {
		t.Errorf("filename: got %q, want %q", filepath.Base(result.Dst), expectedFilename)
	}
}

func TestMoveFile_DeduplicationWithMultipleDots(t *testing.T) {
	m := openTestMover(t)
	backendID := "tusd-multidot"

	// Create existing file.
	existingAbs := filepath.Join(m.StoragePath(), "organized/2024/08/01/archive.tar.gz")
	writeFile(t, existingAbs, []byte("existing"))

	// Move file with same name but multiple dots.
	src := filepath.Join(m.StoragePath(), "incoming", backendID)
	writeFile(t, src, []byte("new archive"))
	result, err := m.MoveFile(src, "2024-08-01T00:00:00Z", "archive.tar.gz", backendID)
	if err != nil {
		t.Fatalf("MoveFile failed: %v", err)
	}

	if !result.Deduplicated {
		t.Error("expected deduplication")
	}
	// filepath.Ext returns ".gz" for "archive.tar.gz", so the result
	// is "archive.tar_tusd-multidot.gz".
	expectedFilename := "archive.tar_tusd-multidot.gz"
	if filepath.Base(result.Dst) != expectedFilename {
		t.Errorf("filename: got %q, want %q", filepath.Base(result.Dst), expectedFilename)
	}
}

// ---------------------------------------------------------------------------
// MoveFile — edge cases
// ---------------------------------------------------------------------------

func TestMoveFile_SourceNotFound(t *testing.T) {
	m := openTestMover(t)

	srcPath := filepath.Join(m.StoragePath(), "incoming", "nonexistent")
	_, err := m.MoveFile(srcPath, "2024-03-15T10:30:00Z", "nope.jpg", "tusd-nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent source file")
	}
}

func TestMoveFile_SameDateDifferentFilenames(t *testing.T) {
	m := openTestMover(t)

	// Move multiple files with different filenames on the same date.
	files := []struct {
		srcName   string
		filename  string
		content   string
		backendID string
	}{
		{"tusd-a", img0001Name, "first photo", "tusd-a"},
		{"tusd-b", "IMG_0002.jpg", "second photo", "tusd-b"},
		{"tusd-c", "IMG_0003.jpg", "third photo", "tusd-c"},
	}

	for _, f := range files {
		src := filepath.Join(m.StoragePath(), "incoming", f.srcName)
		writeFile(t, src, []byte(f.content))

		result, err := m.MoveFile(src, "2024-03-15T10:30:00Z", f.filename, f.backendID)
		if err != nil {
			t.Fatalf("MoveFile %s failed: %v", f.srcName, err)
		}
		if result.Deduplicated {
			t.Errorf("MoveFile %s should not deduplicate (different filename)", f.srcName)
		}

		// Verify content.
		got, err := os.ReadFile(result.Dst)
		if err != nil {
			t.Fatalf("ReadFile %s: %v", result.Dst, err)
		}
		if string(got) != f.content {
			t.Errorf("content at %s: got %q, want %q", result.Dst, string(got), f.content)
		}
	}
}

func TestMoveFile_DifferentDatesSameFilename(t *testing.T) {
	m := openTestMover(t)

	// Move files with the same filename but different dates — should not deduplicate
	// because they end up in different directories.
	dates := []struct {
		date        string
		expectedRel string
		backendID   string
	}{
		{"2024-01-15T10:00:00Z", "organized/2024/01/15/IMG_0001.jpg", "tusd-jan"},
		{"2024-02-20T11:00:00Z", "organized/2024/02/20/IMG_0001.jpg", "tusd-feb"},
		{"2024-03-25T12:00:00Z", "organized/2024/03/25/IMG_0001.jpg", "tusd-mar"},
	}

	for _, d := range dates {
		src := filepath.Join(m.StoragePath(), "incoming", d.backendID)
		writeFile(t, src, []byte(d.backendID))

		result, err := m.MoveFile(src, d.date, img0001Name, d.backendID)
		if err != nil {
			t.Fatalf("MoveFile for %s failed: %v", d.date, err)
		}
		if result.Deduplicated {
			t.Errorf("expected no dedup for different dates, got dedup on %s", d.date)
		}
		if result.DstRel != d.expectedRel {
			t.Errorf("DstRel: got %q, want %q", result.DstRel, d.expectedRel)
		}
	}
}

// ---------------------------------------------------------------------------
// MoveFile — large content
// ---------------------------------------------------------------------------

func TestMoveFile_LargeFile(t *testing.T) {
	m := openTestMover(t)

	// Create a 1 MB source file.
	size := 1 << 20 // 1 MB
	content := make([]byte, size)
	for i := range content {
		content[i] = byte(i % 256)
	}

	srcPath := filepath.Join(m.StoragePath(), "incoming", "tusd-large")
	writeFile(t, srcPath, content)

	result, err := m.MoveFile(srcPath, "2024-06-15T10:30:00Z", "large_file.bin", "tusd-large")
	if err != nil {
		t.Fatalf("MoveFile failed: %v", err)
	}

	gotContent, err := os.ReadFile(result.Dst)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(gotContent) != size {
		t.Errorf("content size: got %d, want %d", len(gotContent), size)
	}
	for i := range gotContent {
		if gotContent[i] != content[i] {
			t.Errorf("byte %d: got %d, want %d", i, gotContent[i], content[i])
			break
		}
	}
}

// ---------------------------------------------------------------------------
// MoveFile — source path cleanup
// ---------------------------------------------------------------------------

func TestMoveFile_SourceDirectoryRemains(t *testing.T) {
	// Verify that the source *directory* (parent of the moved file) is
	// not deleted — only the file itself is removed.
	m := openTestMover(t)

	srcDir := filepath.Join(m.StoragePath(), "incoming")
	srcPath := filepath.Join(srcDir, "tusd-cleanup")
	writeFile(t, srcPath, []byte("content"))

	result, err := m.MoveFile(srcPath, "2024-03-15T10:30:00Z", "IMG_cleanup.jpg", "tusd-cleanup")
	if err != nil {
		t.Fatalf("MoveFile failed: %v", err)
	}

	// Source file is gone.
	assertPathNotExists(t, result.Src)

	// Source directory still exists.
	assertPathExists(t, srcDir)
}

// ---------------------------------------------------------------------------
// MoveFile — concurrent safety (smoke)
// ---------------------------------------------------------------------------

func TestMoveFile_ConcurrentDifferentPaths(t *testing.T) {
	m := openTestMover(t)

	const n = 10
	errCh := make(chan error, n)

	for i := range n {
		go func() {
			srcName := "tusd-concurrent-" + itoa(i)
			srcPath := filepath.Join(m.StoragePath(), "incoming", srcName)
			writeFile(t, srcPath, []byte(srcName))

			// Each goroutine moves to a unique path (different filename or date).
			filename := "IMG_" + itoa(i) + ".jpg"
			_, err := m.MoveFile(srcPath, "2024-03-15T10:30:00Z", filename, srcName)
			errCh <- err
		}()
	}

	for i := range n {
		if err := <-errCh; err != nil {
			t.Errorf("concurrent move %d: %v", i, err)
		}
	}

	// Verify all files were moved.
	for i := range n {
		dstPath := filepath.Join(m.StoragePath(), "organized/2024/03/15/IMG_"+itoa(i)+".jpg")
		assertPathExists(t, dstPath)
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
	// Verify by checking OrganizedPath is relative to it.
	_, abs := m.OrganizedPath("2024-01-01T00:00:00Z", "test.txt")
	if !strings.HasPrefix(abs, expected) {
		t.Errorf("abs path %q should start with storage path %q", abs, expected)
	}
}

// ---------------------------------------------------------------------------
// Helpers (internal)
// ---------------------------------------------------------------------------

// itoa is a simple int-to-string for the concurrent test.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
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
// Tests for edge cases in time parsing
// ---------------------------------------------------------------------------

func TestOrganizedPath_DateFallback(t *testing.T) {
	m := openTestMover(t)

	// A completely random string should fall through.
	rel, abs := m.OrganizedPath("random-garbage", "file.txt")
	expectedRel := "organized/random-garbage/unknown/unknown/file.txt"
	if rel != expectedRel {
		t.Errorf("rel: got %q, want %q", rel, expectedRel)
	}
	if !strings.HasPrefix(abs, m.StoragePath()) {
		t.Errorf("abs should be under storage path")
	}
}

// ---------------------------------------------------------------------------
// MoveFile with nested source paths
// ---------------------------------------------------------------------------

func TestMoveFile_DeepSourcePath(t *testing.T) {
	m := openTestMover(t)

	// Source file in a deep directory within incoming.
	srcPath := filepath.Join(m.StoragePath(), "incoming", "sub", "nested", "tusd-deep")
	writeFile(t, srcPath, []byte("deep path content"))

	result, err := m.MoveFile(srcPath, "2024-07-04T00:00:00Z", "deep_file.txt", "tusd-deep")
	if err != nil {
		t.Fatalf("MoveFile failed: %v", err)
	}

	// File should be moved, source gone.
	assertPathNotExists(t, srcPath)
	assertPathExists(t, result.Dst)

	got, err := os.ReadFile(result.Dst)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "deep path content" {
		t.Errorf("content: got %q, want %q", string(got), "deep path content")
	}
}

// ---------------------------------------------------------------------------
// MoveFile — idempotency property: moving the same source twice fails
// ---------------------------------------------------------------------------

func TestMoveFile_SourceGoneSecondFails(t *testing.T) {
	m := openTestMover(t)

	srcPath := filepath.Join(m.StoragePath(), "incoming", "tusd-once")
	writeFile(t, srcPath, []byte("only move once"))

	// First move succeeds.
	_, err := m.MoveFile(srcPath, "2024-03-15T10:30:00Z", "once.jpg", "tusd-once")
	if err != nil {
		t.Fatalf("first MoveFile failed: %v", err)
	}

	// Second move with same source fails (source is gone).
	_, err = m.MoveFile(srcPath, "2024-03-15T10:30:00Z", "again.jpg", "tusd-once-gone")
	if err == nil {
		t.Fatal("expected error when source file is already moved")
	}
}

// ---------------------------------------------------------------------------
// OrganizedPath — empty filename
// ---------------------------------------------------------------------------

func TestOrganizedPath_EmptyFilename(t *testing.T) {
	m := openTestMover(t)

	rel, abs := m.OrganizedPath("2024-03-15T10:30:00Z", "")
	// filepath.Join removes empty components, so the filename part is omitted.
	expectedRel := "organized/2024/03/15"
	if rel != expectedRel {
		t.Errorf("rel: got %q, want %q", rel, expectedRel)
	}
	expectedAbs := filepath.Join(m.StoragePath(), expectedRel)
	if abs != expectedAbs {
		t.Errorf("abs: got %q, want %q", abs, expectedAbs)
	}
}

// ---------------------------------------------------------------------------
// MoveFile — Content integrity across different times
// ---------------------------------------------------------------------------

func TestMoveFile_TimeBasedDirsAreDistinct(t *testing.T) {
	m := openTestMover(t)
	backendID := "tusd-time-distinct"

	// Move to January.
	janSrc := filepath.Join(m.StoragePath(), "incoming", backendID)
	writeFile(t, janSrc, []byte("january content"))

	janResult, err := m.MoveFile(janSrc, "2024-01-15T10:00:00Z", "photo.jpg", backendID)
	if err != nil {
		t.Fatalf("MoveFile January failed: %v", err)
	}
	if janResult.Deduplicated {
		t.Error("January move should not be deduplicated")
	}
	if janResult.DstRel != "organized/2024/01/15/photo.jpg" {
		t.Errorf("January DstRel: got %q, want %q", janResult.DstRel, "organized/2024/01/15/photo.jpg")
	}

	// Move to February with same filename — should NOT deduplicate because
	// the directory is different.
	febSrc := filepath.Join(m.StoragePath(), "incoming", backendID+"-feb")
	writeFile(t, febSrc, []byte("february content"))

	febResult, err := m.MoveFile(febSrc, "2024-02-20T11:00:00Z", "photo.jpg", backendID+"-feb")
	if err != nil {
		t.Fatalf("MoveFile February failed: %v", err)
	}
	if febResult.Deduplicated {
		t.Error("February move should not deduplicate (different directory)")
	}
	if febResult.DstRel != "organized/2024/02/20/photo.jpg" {
		t.Errorf("February DstRel: got %q, want %q", febResult.DstRel, "organized/2024/02/20/photo.jpg")
	}

	// Verify both files exist with correct content.
	janContent, err := os.ReadFile(janResult.Dst)
	if err != nil {
		t.Fatalf("ReadFile January: %v", err)
	}
	if string(janContent) != "january content" {
		t.Errorf("January content: got %q, want %q", string(janContent), "january content")
	}

	febContent, err := os.ReadFile(febResult.Dst)
	if err != nil {
		t.Fatalf("ReadFile February: %v", err)
	}
	if string(febContent) != "february content" {
		t.Errorf("February content: got %q, want %q", string(febContent), "february content")
	}
}

// ---------------------------------------------------------------------------
// MoveFile — verify Result fields
// ---------------------------------------------------------------------------

func TestMoveFile_ResultFields(t *testing.T) {
	m := openTestMover(t)

	srcPath := filepath.Join(m.StoragePath(), "incoming", "tusd-result-check")
	content := []byte("check result fields")
	writeFile(t, srcPath, content)

	result, err := m.MoveFile(srcPath, "2024-03-15T10:30:00Z", "IMG_result.jpg", "tusd-result-check")
	if err != nil {
		t.Fatalf("MoveFile failed: %v", err)
	}

	// Src should be the exact path we passed in.
	if result.Src != srcPath {
		t.Errorf("Src: got %q, want %q", result.Src, srcPath)
	}

	// DstRel should be a clean relative path starting with "organized/".
	if !strings.HasPrefix(result.DstRel, "organized/") {
		t.Errorf("DstRel should start with organized/, got %q", result.DstRel)
	}

	// Dst should be rooted at the storage path.
	if !strings.HasPrefix(result.Dst, m.StoragePath()) {
		t.Errorf("Dst %q should be under storage path %q", result.Dst, m.StoragePath())
	}

	// Dst should be the absolute version of DstRel.
	expectedDst := filepath.Join(m.StoragePath(), result.DstRel)
	if result.Dst != expectedDst {
		t.Errorf("Dst: got %q, want %q", result.Dst, expectedDst)
	}

	// Deduplicated should be false (fresh destination).
	if result.Deduplicated {
		t.Error("Deduplicated should be false")
	}

	// File should exist at Dst.
	assertPathExists(t, result.Dst)

	// Content should match.
	gotContent, err := os.ReadFile(result.Dst)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(gotContent) != string(content) {
		t.Errorf("content: got %q, want %q", string(gotContent), string(content))
	}
}

// ---------------------------------------------------------------------------
// MoveFile — destination path structure validation
// ---------------------------------------------------------------------------

func TestMoveFile_OrganizedDirStructure(t *testing.T) {
	m := openTestMover(t)

	srcPath := filepath.Join(m.StoragePath(), "incoming", "tusd-structure")
	writeFile(t, srcPath, []byte("struct test"))

	creationDate := "2024-11-05T08:30:00Z"
	_, err := m.MoveFile(srcPath, creationDate, "IMG_structure.jpg", "tusd-structure")
	if err != nil {
		t.Fatalf("MoveFile failed: %v", err)
	}

	// Verify the directory structure exists.
	organizedBase := filepath.Join(m.StoragePath(), "organized")
	yearDir := filepath.Join(organizedBase, "2024")
	monthDir := filepath.Join(yearDir, "11")
	dayDir := filepath.Join(monthDir, "05")
	filePath := filepath.Join(dayDir, "IMG_structure.jpg")

	for _, p := range []string{organizedBase, yearDir, monthDir, dayDir, filePath} {
		assertPathExists(t, p)
	}

	// Verify that organizedBase, yearDir, and monthDir are directories.
	for _, p := range []string{organizedBase, yearDir, monthDir, dayDir} {
		info, err := os.Stat(p)
		if err != nil {
			t.Errorf("Stat %s: %v", p, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%s is not a directory", p)
		}
	}

	// Verify the file is a regular file (not a directory).
	info, err := os.Stat(filePath)
	if err != nil {
		t.Fatalf("Stat file: %v", err)
	}
	if info.IsDir() {
		t.Errorf("file %s should be a regular file, not a directory", filePath)
	}
}

// ---------------------------------------------------------------------------
// MoveFile — same file moved with different backend IDs on same date
// (legitimate deduplication scenario)
// ---------------------------------------------------------------------------

func TestMoveFile_SameDateFilenameDifferentBackendIDs(t *testing.T) {
	m := openTestMover(t)

	baseRel := "organized/2024/06/15/IMG_1000.jpg"

	// First move — no dedup.
	src1 := filepath.Join(m.StoragePath(), "incoming", "tusd-1000-a")
	writeFile(t, src1, []byte("file a"))
	r1, err := m.MoveFile(src1, "2024-06-15T12:00:00Z", "IMG_1000.jpg", "tusd-1000-a")
	if err != nil {
		t.Fatalf("first MoveFile: %v", err)
	}
	if r1.Deduplicated {
		t.Error("first move should not be deduplicated")
	}
	if r1.DstRel != baseRel {
		t.Errorf("first DstRel: got %q, want %q", r1.DstRel, baseRel)
	}

	// Second move — same date and filename, different backend ID -> dedup.
	src2 := filepath.Join(m.StoragePath(), "incoming", "tusd-1000-b")
	writeFile(t, src2, []byte("file b"))
	r2, err := m.MoveFile(src2, "2024-06-15T12:00:00Z", "IMG_1000.jpg", "tusd-1000-b")
	if err != nil {
		t.Fatalf("second MoveFile: %v", err)
	}
	if !r2.Deduplicated {
		t.Error("second move should be deduplicated")
	}
	expectedRel2 := "organized/2024/06/15/IMG_1000_tusd-1000-b.jpg"
	if r2.DstRel != expectedRel2 {
		t.Errorf("second DstRel: got %q, want %q", r2.DstRel, expectedRel2)
	}

	// Both files should exist.
	assertPathExists(t, r1.Dst)
	assertPathExists(t, r2.Dst)

	// Verify content.
	got1, _ := os.ReadFile(r1.Dst)
	got2, _ := os.ReadFile(r2.Dst)
	if string(got1) != "file a" {
		t.Errorf("file a content: got %q", string(got1))
	}
	if string(got2) != "file b" {
		t.Errorf("file b content: got %q", string(got2))
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

	plan := m.PlanDestination("2024-03-15T10:30:00Z", "", "IMG_1234.jpg", "tusd-abc")
	if plan.Rel != relMar2024IMG1234 {
		t.Errorf("rel: got %q, want %q", plan.Rel, relMar2024IMG1234)
	}
	expectedAbs := filepath.Join(m.StoragePath(), plan.Rel)
	if plan.Abs != expectedAbs {
		t.Errorf("abs: got %q, want %q", plan.Abs, expectedAbs)
	}
}

func TestPlanDestination_DateOnly(t *testing.T) {
	m := openTestMover(t)

	plan := m.PlanDestination("2024-06-20", "", "IMG_5678.jpg", "tusd-def")
	if plan.Rel != "organized/2024/06/20/IMG_5678.jpg" {
		t.Errorf("rel: got %q, want %q", plan.Rel, "organized/2024/06/20/IMG_5678.jpg")
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
	if plan.Rel != "organized/2024/07/04/IMG_video.mp4" {
		t.Errorf("rel: got %q, want %q", plan.Rel, "organized/2024/07/04/IMG_video.mp4")
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
	if plan.Rel != "organized/2024/07/04/photo.jpg" {
		t.Errorf("rel: got %q, want %q", plan.Rel, "organized/2024/07/04/photo.jpg")
	}
	expectedAbs := filepath.Join(m.StoragePath(), plan.Rel)
	if plan.Abs != expectedAbs {
		t.Errorf("abs: got %q, want %q", plan.Abs, expectedAbs)
	}
}

func TestPlanDestination_CollisionInsertsBackendID(t *testing.T) {
	m := openTestMover(t)

	// Create an existing file at the computed path.
	existingAbs := filepath.Join(m.StoragePath(), relMar2024IMG1234)
	writeFile(t, existingAbs, []byte("existing"))

	// PlanDestination should detect the collision and insert backendID.
	plan := m.PlanDestination("2024-03-15T10:30:00Z", "", "IMG_1234.jpg", "tusd-collision")
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
	if plan.Rel != "organized/unknown/unknown/unknown/file.txt" {
		t.Errorf("rel: got %q, want %q", plan.Rel, "organized/unknown/unknown/unknown/file.txt")
	}
	expectedAbs := filepath.Join(m.StoragePath(), plan.Rel)
	if plan.Abs != expectedAbs {
		t.Errorf("abs: got %q, want %q", plan.Abs, expectedAbs)
	}
}

func TestPlanDestination_PreservesExistingOnCollision(t *testing.T) {
	m := openTestMover(t)

	// Create existing file.
	existingAbs := filepath.Join(m.StoragePath(), "organized/2024/01/01/same_name.txt")
	writeFile(t, existingAbs, []byte("do not overwrite"))

	// PlanDestination should not overwrite the existing file.
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

func TestPlanDestination_CollisionWithNoExtension(t *testing.T) {
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

func TestPlanDestination_MultipleDotsExtension(t *testing.T) {
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

	if err := filestore.MoveFile(src, dst); err != nil {
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

	if err := filestore.MoveFile(src, dst); err != nil {
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

	if err := filestore.MoveFile(src, dst); err != nil {
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
	if err := filestore.MoveFile(src, dst); err != nil {
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
	if err := filestore.MoveFile(src, dst); err != nil {
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

	err := filestore.MoveFile(src, dst)
	if err == nil {
		t.Fatal("expected error when both source and destination are missing")
	}
}

func TestMoveFileStandalone_BothMissingError_DeepPaths(t *testing.T) {
	dir := t.TempDir()

	src := filepath.Join(dir, "a", "b", "c", "missing.txt")
	dst := filepath.Join(dir, "x", "y", "z", "nowhere.txt")

	err := filestore.MoveFile(src, dst)
	if err == nil {
		t.Fatal("expected error when both source and destination are missing with deep paths")
	}
}

func TestMoveFileStandalone_SrcMissingDstMissingButDstParentCreated(t *testing.T) {
	dir := t.TempDir()

	src := filepath.Join(dir, "missing_src.txt")
	dst := filepath.Join(dir, "parent", "missing_dst.txt")

	// Parent directory does not exist, source missing — should error.
	err := filestore.MoveFile(src, dst)
	if err == nil {
		t.Fatal("expected error when src missing and parent dir doesn't exist")
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

	// Move file using standalone MoveFile.
	if err := filestore.MoveFile(src, plan.Abs); err != nil {
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
	expectedRel := "organized/2024/09/15/IMG_final.jpg"
	if plan.Rel != expectedRel {
		t.Errorf("rel: got %q, want %q", plan.Rel, expectedRel)
	}
}

func TestPlanDestinationWithCollisionThenMoveFile(t *testing.T) {
	m := openTestMover(t)

	// Create an existing file at the computed destination.
	existingAbs := filepath.Join(m.StoragePath(), "organized/2024/10/01/photo.jpg")
	writeFile(t, existingAbs, []byte("existing photo"))

	// Plan destination for a new file with the same name.
	src := filepath.Join(m.StoragePath(), "incoming", "tusd-collision-int")
	writeFile(t, src, []byte("new photo"))

	plan := m.PlanDestination("2024-10-01T12:00:00Z", "", "photo.jpg", "tusd-collision-int")

	// The planned destination should have the backendID suffix due to collision.
	expectedRel := "organized/2024/10/01/photo_tusd-collision-int.jpg"
	if plan.Rel != expectedRel {
		t.Errorf("rel: got %q, want %q", plan.Rel, expectedRel)
	}

	// Move the file.
	if err := filestore.MoveFile(src, plan.Abs); err != nil {
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
	if plan.Rel != "organized/2024/12/31/nano.jpg" {
		t.Errorf("rel: got %q, want %q", plan.Rel, "organized/2024/12/31/nano.jpg")
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
	if plan.Rel != "organized/not-a-date/unknown/unknown/file.txt" {
		t.Errorf("rel: got %q, want %q", plan.Rel, "organized/not-a-date/unknown/unknown/file.txt")
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
	if plan.Rel != "organized/2024/01/15/preferred.jpg" {
		t.Errorf("rel: got %q, want %q (creationDate should be preferred)", plan.Rel, "organized/2024/01/15/preferred.jpg")
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
	if plan.Rel != "organized/2024/11/05/fallback_test.jpg" {
		t.Errorf("rel: got %q, want %q", plan.Rel, "organized/2024/11/05/fallback_test.jpg")
	}
	expectedAbs := filepath.Join(m.StoragePath(), plan.Rel)
	if plan.Abs != expectedAbs {
		t.Errorf("abs: got %q, want %q", plan.Abs, expectedAbs)
	}
}

func TestPlanDestination_EmptyFilename(t *testing.T) {
	m := openTestMover(t)

	plan := m.PlanDestination("2024-03-15T10:30:00Z", "", "", "tusd-empty-file")
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
			creationDate: "2024-03-15T10:30:00Z",
			wantDateUsed: "2024-03-15T10:30:00Z",
		},
		{
			desc:         "valid date-only creationDate is used verbatim",
			creationDate: "2024-06-20",
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
			createdAt:    "also-garbage",
			wantDateUsed: "garbage-date",
		},
		{
			desc:         "raw unparseable createdAt kept when creationDate empty",
			createdAt:    "also-garbage",
			wantDateUsed: "also-garbage",
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
// Verify PlanDestination correctly uses OrganizedPath logic internally
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
		{"2024-01-01T00:00:00Z", img0001Name, "organized/2024/01/01/IMG_0001.jpg", "january start"},
		{"2024-12-31T23:59:59Z", "IMG_9999.jpg", relDec2024IMG9999, "december end"},
		{"2025-06-15T12:00:00Z", "VID_2025.mp4", "organized/2025/06/15/VID_2025.mp4", "june mid-year"},
		{"2024-02-29T10:30:00Z", "leap_day.txt", "organized/2024/02/29/leap_day.txt", "leap day"},
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
