package orphans

import "time"

// FilterMinAge drops any candidate whose CTime is younger than minAge,
// keeping a candidate that is exactly minAge old (now.Sub(c.CTime) >=
// minAge). It is a pure function with no I/O.
//
// It acts as a race guard: an in-flight upload's moveCompletedFile can
// write a file into organized/ moments before its DB record commits as
// complete, which Scan would otherwise flag as an orphan. Requiring the
// file to have sat untouched for at least minAge materially narrows that
// window.
//
// The guard keys off ctime (inode status-change time), not mtime or
// atime, because those are client-controlled since filestore.MoveFile
// started calling os.Chtimes with the client-supplied creation_date
// (Task 4): a backed-up photo from 2015 lands on disk today with a 2015
// mtime, which an mtime-based guard would treat as "old enough" the
// instant it appears — reopening the exact race window this filter
// exists to close, for what is the common case (backed-up photos are
// essentially never dated today). ctime cannot be set by os.Chtimes — the
// kernel stamps it on every metadata change, including the Chtimes call
// itself — so it still means "when did this file land here," independent
// of whatever timestamp value was written into it. See
// docs/adr/0006-ctime-based-orphan-age-guard.md for the full rationale.
//
// The two tests that prove this guard holds are ctime_test.go's
// TestCtimeUnaffectedByChtimes (Chtimes cannot move ctime backward) and
// the fresh-CTime/old-mtime case in filter_test.go (a candidate whose
// mtime would be years old under the old scheme is still dropped while
// its ctime is young). Together they show the guard survives
// client-controlled mtime on the filesystem side and on the policy side,
// not just as an assertion in the ADR.
//
// now is captured once by the caller after Scan returns — not
// per-candidate — so on a slow scan a candidate whose CTime was read
// early is compared against a slightly later now than one read near the
// end, making the filter marginally more conservative (never less safe).
func FilterMinAge(candidates []Candidate, minAge time.Duration, now time.Time) []Candidate {
	if len(candidates) == 0 {
		return nil
	}

	kept := make([]Candidate, 0, len(candidates))
	for _, c := range candidates {
		if now.Sub(c.CTime) >= minAge {
			kept = append(kept, c)
		}
	}

	return kept
}
