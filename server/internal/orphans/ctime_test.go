package orphans

import (
	"os"
	"testing"
	"time"
)

// newCtimeFile creates a real temp file still present on disk and returns
// its path. Callers must not create the file's os.FileInfo themselves —
// the whole point is to exercise ctime against genuine syscall.Stat_t data
// from the host filesystem.
func newCtimeFile(t *testing.T) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "ctime-*")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	name := f.Name()
	if err := f.Close(); err != nil {
		t.Fatalf("close temp file: %v", err)
	}
	return name
}

// recent reports whether ct is within a few seconds of now (allowing for
// wall-clock drift between the stat and the time.Since call).
func recent(ct time.Time) bool {
	since := time.Since(ct)
	return since >= 0 && since < 10*time.Second
}

// TestCtimeWithinSecondsOfNow exercises ctime against a real temp file and
// asserts the returned status-change time is recent and error-free.
func TestCtimeWithinSecondsOfNow(t *testing.T) {
	name := newCtimeFile(t)

	info, err := os.Stat(name)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	ct, err := ctime(info)
	if err != nil {
		t.Fatalf("ctime(info) returned error: %v", err)
	}
	if !recent(ct) {
		t.Fatalf("ctime = %v, want within a few seconds of now", ct)
	}
}

// TestCtimeUnaffectedByChtimes is the regression proof for the whole plan:
// it backdates the file's mtime via os.Chtimes (the exact mechanism
// filestore.MoveFile now uses to write client-controlled timestamps) and
// asserts ctime stays recent. Chtimes can set mtime/atime but never ctime,
// so the orphan-scan age guard built in Task 8 genuinely survives the
// client-controlled-mtime attack this plan introduces.
func TestCtimeUnaffectedByChtimes(t *testing.T) {
	name := newCtimeFile(t)

	old := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(name, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	info, err := os.Stat(name)
	if err != nil {
		t.Fatalf("fresh stat: %v", err)
	}

	// Confirm the backdate actually took hold on mtime, or the test proves
	// nothing.
	if !info.ModTime().Before(time.Now().Add(-20 * time.Hour)) {
		t.Fatalf("mtime should have been backdated to %v, but a fresh stat still reads %v", old, info.ModTime())
	}

	ct, err := ctime(info)
	if err != nil {
		t.Fatalf("ctime(info) returned error: %v", err)
	}
	if !recent(ct) {
		t.Fatalf("ctime moved backward with mtime to %v; want ctime still recent", ct)
	}
}
