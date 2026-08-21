package filestore

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseCreationDate(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  time.Time
		ok    bool
	}{
		{
			name:  "valid RFC3339",
			input: "2024-03-15T10:30:00Z",
			want:  time.Date(2024, 3, 15, 10, 30, 0, 0, time.UTC),
			ok:    true,
		},
		{
			name:  "valid YYYY-MM-DD",
			input: "2024-06-20",
			want:  time.Date(2024, 6, 20, 0, 0, 0, 0, time.UTC),
			ok:    true,
		},
		{
			name:  "RFC3339Nano",
			input: "2024-12-31T23:59:59.123456789Z",
			want:  time.Date(2024, 12, 31, 23, 59, 59, 123456789, time.UTC),
			ok:    true,
		},
		{
			name:  "RFC3339 with numeric zone offset",
			input: "2024-03-15T10:30:00+02:00",
			want:  time.Date(2024, 3, 15, 8, 30, 0, 0, time.UTC),
			ok:    true,
		},
		{
			name:  "empty string",
			input: "",
			ok:    false,
		},
		{
			name:  "garbage string",
			input: "not-a-date",
			ok:    false,
		},
		{
			name:  "whitespace-only string",
			input: "   ",
			ok:    false,
		},
		{
			name:  "RFC3339 without seconds is rejected",
			input: "2024-03-15T10:30Z",
			ok:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseCreationDate(tt.input)
			if ok != tt.ok {
				t.Fatalf("parseCreationDate(%q) ok = %v, want %v", tt.input, ok, tt.ok)
			}
			if tt.ok && !got.Equal(tt.want) {
				t.Errorf("parseCreationDate(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

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
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("Stat destination: %v", err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Errorf("mode: got %o, want 640", info.Mode().Perm())
	}
}

func TestCopyFileErrors(t *testing.T) {
	failure := errors.New("injected failure")
	cases := []struct {
		name string
		ops  fileOps
		want string
	}{
		{name: "open source", ops: fakeFileOps{openErr: failure}, want: "open source"},
		{name: "create destination", ops: fakeFileOps{createErr: failure}, want: "create destination"},
		{name: "copy data", ops: fakeFileOps{reader: errorReader{err: failure}}, want: "copy data"},
		{name: "sync destination", ops: fakeFileOps{writer: &fakeWriter{syncErr: failure}}, want: "sync destination"},
		{name: "close destination", ops: fakeFileOps{writer: &fakeWriter{closeErr: failure}}, want: "close destination"},
		{name: "stat source", ops: fakeFileOps{statErr: failure}, want: "stat source after copy"},
		{name: "chmod destination", ops: fakeFileOps{chmodErr: failure}, want: "chmod destination"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := copyFileWithOps("source", "destination", tc.ops)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("copyFile error=%v, want %q", err, tc.want)
			}
		})
	}
}

type fakeFileOps struct {
	openErr   error
	createErr error
	reader    io.Reader
	writer    *fakeWriter
	statErr   error
	chmodErr  error
}

func (o fakeFileOps) Open(string) (readCloser, error) {
	if o.openErr != nil {
		return nil, o.openErr
	}
	if o.reader == nil {
		o.reader = bytes.NewReader([]byte("data"))
	}
	return io.NopCloser(o.reader), nil
}

func (o fakeFileOps) Create(string) (syncWriteCloser, error) {
	if o.createErr != nil {
		return nil, o.createErr
	}
	if o.writer == nil {
		o.writer = &fakeWriter{}
	}
	return o.writer, nil
}

func (o fakeFileOps) Stat(string) (os.FileInfo, error) {
	if o.statErr != nil {
		return nil, o.statErr
	}
	return fakeFileInfo{}, nil
}

func (o fakeFileOps) Chmod(string, os.FileMode) error { return o.chmodErr }

type fakeWriter struct {
	bytes.Buffer
	syncErr  error
	closeErr error
}

func (w *fakeWriter) Sync() error  { return w.syncErr }
func (w *fakeWriter) Close() error { return w.closeErr }

type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }

type fakeFileInfo struct{}

func (fakeFileInfo) Name() string       { return "source" }
func (fakeFileInfo) Size() int64        { return 0 }
func (fakeFileInfo) Mode() os.FileMode  { return 0o640 }
func (fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (fakeFileInfo) IsDir() bool        { return false }
func (fakeFileInfo) Sys() any           { return nil }
