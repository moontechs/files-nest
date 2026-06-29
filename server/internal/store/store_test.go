package store_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/moontechs/files-nest/server/internal/store"
)

func TestOpen_CreatesDBDirectory(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "testdb")

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer s.Close()

	// Verify the directory was created
	info, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("expected db directory to exist: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("expected db path to be a directory")
	}
}

func TestOpen_SecondOpenOnSamePathSucceeds(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "testdb")

	s1, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("first Open failed: %v", err)
	}
	s1.Close()

	s2, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("second Open on same path failed: %v", err)
	}
	s2.Close()
}

func TestOpen_ReturnsStoreOnSuccess(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "testdb")

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer s.Close()

	if s.DB() == nil {
		t.Fatal("expected non-nil DB from Store")
	}
}

func TestClose_Idempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "testdb")

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("first Close failed: %v", err)
	}

	// Closing an already-closed store should be safe (badger handles this)
	if err := s.Close(); err != nil {
		t.Fatalf("second Close should not panic but got: %v", err)
	}
}
