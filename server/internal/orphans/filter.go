package orphans

import "time"

// FilterMinAge drops any candidate whose ModTime is younger than minAge,
// keeping a candidate that is exactly minAge old (now.Sub(c.ModTime) >=
// minAge). It is a pure function with no I/O.
//
// It acts as a race guard: an in-flight upload's moveCompletedFile can
// write a file into organized/ moments before its DB record commits as
// complete, which Scan would otherwise flag as an orphan. Requiring the
// file to have sat untouched for at least minAge materially narrows that
// window.
//
// now is captured once by the caller after Scan returns — not
// per-candidate — so on a slow scan a candidate whose ModTime was read
// early is compared against a slightly later now than one read near the
// end, making the filter marginally more conservative (never less safe).
func FilterMinAge(candidates []Candidate, minAge time.Duration, now time.Time) []Candidate {
	if len(candidates) == 0 {
		return nil
	}
	kept := make([]Candidate, 0, len(candidates))
	for _, c := range candidates {
		if now.Sub(c.ModTime) >= minAge {
			kept = append(kept, c)
		}
	}
	return kept
}
