package orphans

import (
	"testing"
	"time"
)

func TestFilterMinAge(t *testing.T) {
	now := time.Now()
	minAge := 3 * time.Hour

	t.Run("older than minAge is kept", func(t *testing.T) {
		in := []Candidate{{Path: "a", ModTime: now.Add(-4 * time.Hour)}}
		got := FilterMinAge(in, minAge, now)
		if len(got) != 1 || got[0].Path != "a" {
			t.Fatalf("expected 1 kept candidate, got %+v", got)
		}
	})

	t.Run("younger than minAge is dropped", func(t *testing.T) {
		in := []Candidate{{Path: "a", ModTime: now.Add(-2 * time.Hour)}}
		got := FilterMinAge(in, minAge, now)
		if len(got) != 0 {
			t.Fatalf("expected 0 kept candidates, got %+v", got)
		}
	})

	t.Run("exactly minAge old is kept (boundary)", func(t *testing.T) {
		in := []Candidate{{Path: "a", ModTime: now.Add(-minAge)}}
		got := FilterMinAge(in, minAge, now)
		if len(got) != 1 || got[0].Path != "a" {
			t.Fatalf("expected 1 kept candidate (boundary), got %+v", got)
		}
	})

	t.Run("empty input returns empty output", func(t *testing.T) {
		got := FilterMinAge(nil, minAge, now)
		if len(got) != 0 {
			t.Fatalf("expected empty output, got %+v", got)
		}
	})

	t.Run("mixed ages keeps only those old enough", func(t *testing.T) {
		in := []Candidate{
			{Path: "old", ModTime: now.Add(-10 * time.Hour)},
			{Path: "new", ModTime: now.Add(-time.Minute)},
			{Path: "border", ModTime: now.Add(-minAge)},
		}
		got := FilterMinAge(in, minAge, now)
		if len(got) != 2 {
			t.Fatalf("expected 2 kept candidates, got %d", len(got))
		}
		byPath := map[string]bool{}
		for _, c := range got {
			byPath[c.Path] = true
		}
		if !byPath["old"] || !byPath["border"] || byPath["new"] {
			t.Fatalf("unexpected kept set: %+v", got)
		}
	})
}
