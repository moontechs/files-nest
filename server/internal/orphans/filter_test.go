package orphans_test

import (
	"testing"
	"time"

	"github.com/moontechs/files-nest/server/internal/orphans"
)

func TestFilterMinAge(t *testing.T) {
	now := time.Now()
	minAge := 3 * time.Hour

	t.Run("older than minAge is kept", func(t *testing.T) {
		in := []orphans.Candidate{{Path: "a", CTime: now.Add(-4 * time.Hour)}}
		got := orphans.FilterMinAge(in, minAge, now)
		if len(got) != 1 || got[0].Path != "a" {
			t.Fatalf("expected 1 kept candidate, got %+v", got)
		}
	})

	t.Run("younger than minAge is dropped", func(t *testing.T) {
		in := []orphans.Candidate{{Path: "a", CTime: now.Add(-2 * time.Hour)}}
		got := orphans.FilterMinAge(in, minAge, now)
		if len(got) != 0 {
			t.Fatalf("expected 0 kept candidates, got %+v", got)
		}
	})

	t.Run("exactly minAge old is kept (boundary)", func(t *testing.T) {
		in := []orphans.Candidate{{Path: "a", CTime: now.Add(-minAge)}}
		got := orphans.FilterMinAge(in, minAge, now)
		if len(got) != 1 || got[0].Path != "a" {
			t.Fatalf("expected 1 kept candidate (boundary), got %+v", got)
		}
	})

	t.Run("empty input returns empty output", func(t *testing.T) {
		got := orphans.FilterMinAge(nil, minAge, now)
		if len(got) != 0 {
			t.Fatalf("expected empty output, got %+v", got)
		}
	})

	// TestFilterMinAgeFreshCTimeOldMtime is the filter-side half of the
	// plan's guard proof, paired with ctime_test.go's
	// TestCtimeUnaffectedByChtimes: a file whose mtime would be years old
	// under the pre-Chtimes scheme — e.g. a 2015 backed-up photo that
	// MoveFile just wrote with a client-supplied 2015 creation_date — must
	// still be dropped while its ctime is young. The old mtime-based guard
	// would have passed it as "old enough" the instant it landed on disk;
	// the ctime-based guard only considers it a candidate once it has sat
	// untouched for at least minAge.
	t.Run("fresh ctime with conceptually ancient mtime is dropped", func(t *testing.T) {
		// CTime is "just now" — the file just landed on disk. Conceptually
		// its mtime is 2015 (a backdated client-supplied creation_date), but
		// Candidate carries no mtime field at all: under the new scheme mtime
		// is client-controlled and therefore meaningless for the age guard.
		in := []orphans.Candidate{{Path: "backed-up-2015-photo", CTime: now.Add(-time.Second)}}
		got := orphans.FilterMinAge(in, minAge, now)
		if len(got) != 0 {
			t.Fatalf("expected fresh-CTime candidate to be dropped (too young by ctime), got %+v", got)
		}
	})

	t.Run("mixed ages keeps only those old enough", func(t *testing.T) {
		in := []orphans.Candidate{
			{Path: "old", CTime: now.Add(-10 * time.Hour)},
			{Path: "new", CTime: now.Add(-time.Minute)},
			{Path: "border", CTime: now.Add(-minAge)},
		}
		got := orphans.FilterMinAge(in, minAge, now)
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
