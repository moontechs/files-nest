package filestore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCopyFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source")
	dst := filepath.Join(dir, "destination")
	if err := os.WriteFile(src, []byte("copy me"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(src, dst); err != nil {
		t.Fatalf("copyFile: %v", err)
	}
	data, err := os.ReadFile(dst)
	if err != nil || string(data) != "copy me" {
		t.Fatalf("destination: data=%q err=%v", data, err)
	}
	info, _ := os.Stat(dst)
	if info.Mode().Perm() != 0o640 {
		t.Errorf("mode: got %o, want 640", info.Mode().Perm())
	}
}

func TestCopyFileErrors(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name, src, dst, want string
	}{
		{"open source", filepath.Join(dir, "missing"), filepath.Join(dir, "dst"), "open source"},
		{"create destination", filepath.Join(dir, "source"), filepath.Join(dir, "missing", "dst"), "create destination"},
		{"copy data", filepath.Join(dir, "directory"), filepath.Join(dir, "dst"), "copy data"},
	}
	if err := os.WriteFile(filepath.Join(dir, "source"), []byte("data"), 0o600); err != nil { t.Fatal(err) }
	if err := os.Mkdir(filepath.Join(dir, "directory"), 0o750); err != nil { t.Fatal(err) }
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := copyFile(tc.src, tc.dst)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("copyFile error=%v, want %q", err, tc.want)
			}
		})
	}
}
