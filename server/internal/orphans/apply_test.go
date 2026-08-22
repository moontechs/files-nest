package orphans_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moontechs/files-nest/server/internal/orphans"
)

func TestApply_AllCandidatesRemoved(t *testing.T) {
	dir := t.TempDir()

	candidates := []orphans.Candidate{
		{Path: filepath.Join(dir, "a.jpg")},
		{Path: filepath.Join(dir, "sub", "b.jpg")},
	}
	for _, c := range candidates {
		if err := os.MkdirAll(filepath.Dir(c.Path), 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(c.Path), err)
		}
		if err := os.WriteFile(c.Path, []byte("x"), 0o640); err != nil {
			t.Fatalf("write %s: %v", c.Path, err)
		}
	}

	result := orphans.Apply(candidates)

	if len(result.Errors) != 0 {
		t.Fatalf("expected no errors, got %v", result.Errors)
	}
	if len(result.Removed) != len(candidates) {
		t.Fatalf("expected %d removed, got %d: %+v", len(candidates), len(result.Removed), result.Removed)
	}
	if len(result.Candidates) != len(candidates) {
		t.Fatalf("expected Candidates to preserve the input set (%d), got %d", len(candidates), len(result.Candidates))
	}
	removedPaths := map[string]bool{}
	for _, c := range result.Removed {
		removedPaths[c.Path] = true
	}
	for _, c := range candidates {
		if !removedPaths[c.Path] {
			t.Fatalf("expected %s in Removed, got %+v", c.Path, result.Removed)
		}
		if _, err := os.Stat(c.Path); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be gone, stat err = %v", c.Path, err)
		}
	}
}

func TestApply_MissingFileErrorButOthersRemoved(t *testing.T) {
	dir := t.TempDir()

	gone := filepath.Join(dir, "already-gone.jpg")
	good := filepath.Join(dir, "still-here.jpg")
	if err := os.WriteFile(good, []byte("x"), 0o640); err != nil {
		t.Fatalf("write %s: %v", good, err)
	}

	candidates := []orphans.Candidate{
		{Path: gone}, // file does not exist -> os.Remove fails
		{Path: good},
	}

	result := orphans.Apply(candidates)

	if len(result.Errors) != 1 {
		t.Fatalf("expected exactly 1 error, got %d: %v", len(result.Errors), result.Errors)
	}
	if !strings.Contains(result.Errors[0].Error(), "already-gone.jpg") {
		t.Fatalf("expected error to mention %s, got: %v", gone, result.Errors[0])
	}
	if len(result.Removed) != 1 || result.Removed[0].Path != good {
		t.Fatalf("expected only %s removed, got %+v", good, result.Removed)
	}
	if _, err := os.Stat(good); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be gone, stat err = %v", good, err)
	}
}

func TestApply_EmptyInput(t *testing.T) {
	result := orphans.Apply(nil)
	if len(result.Errors) != 0 || len(result.Removed) != 0 || len(result.Candidates) != 0 {
		t.Fatalf("expected empty result, got %+v", result)
	}
}
