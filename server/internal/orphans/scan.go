// Package orphans provides a pure, fully-parameterized scan/filter/apply
// pipeline for cleaning up orphaned files under the server's organized
// storage tree. An orphan is a file on disk under organized/ with no live
// (complete-status) upload record referencing it. Policy (age guards,
// intervals, circuit breakers) lives in the caller (server/main.go), not
// here — this package stays a library with no baked-in policy.
package orphans

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"time"

	"github.com/moontechs/files-nest/server/internal/store"
)

// Candidate is a file under the organized root that was not matched by any
// complete-status upload record. CTime is populated only for files
// actually flagged as candidates (from ctime(d.Info()) in the WalkDir
// callback), not for every walked file, so matched files cost no extra
// stat. CTime (inode status-change time) is used instead of ModTime
// because filestore.MoveFile now writes client-controlled mtime/atime
// values via os.Chtimes; ctime cannot be set by os.Chtimes, so it remains
// a trustworthy "when did this file land here" signal for the age guard —
// see docs/adr/0006-ctime-based-orphan-age-guard.md.
type Candidate struct {
	Path  string // absolute path under organized/
	CTime time.Time
}

// Result is the outcome of a Scan or Apply pass over the organized tree.
type Result struct {
	Candidates    []Candidate
	Removed       []Candidate
	Errors        []error
	KnownComplete int // count of complete-status records the known-path set was built from
}

// Scan builds the known-path set from complete-status upload records and
// diffs it against the on-disk organized tree rooted at
// filepath.Join(storagePath, "organized").
//
// storagePath is the storage root (the parent of organized/ and db/).
// Record OrganizedPath values are stored relative to storagePath and
// already include the "organized/" prefix, so each known path is
// filepath.Join(storagePath, rec.OrganizedPath) — NOT
// filepath.Join(organizedRoot, rec.OrganizedPath), which would double the
// "organized" segment and orphan every file. See docs/adr/0005 and the
// plan's Context section for the rationale.
//
// The known-set membership check happens before any d.Info() call:
// d.Info() is only invoked for a file not in the known set, i.e. only for
// files already being flagged as candidates. This ordering is what keeps
// matched files free of extra stat cost.
//
// Errors on nested entries, on d.Info() failures, and on ctime(info)
// failures while reading a candidate's CTime are collected into
// Result.Errors and scanning continues (best-effort); a file whose age
// can't be determined is excluded as a candidate — an undetermined age
// must never be treated as "old enough." An error opening the organized
// root itself is returned as the function's error return (fatal).
func Scan(db *store.Store, storagePath string) (Result, error) {
	var result Result

	records, err := db.ListByStatus(store.StatusComplete)
	if err != nil {
		return result, fmt.Errorf("orphans.Scan: list complete uploads: %w", err)
	}
	result.KnownComplete = len(records)

	known := make(map[string]struct{}, len(records))
	for _, rec := range records {
		if rec.OrganizedPath == "" {
			continue
		}
		known[filepath.Join(storagePath, rec.OrganizedPath)] = struct{}{}
	}

	organizedRoot := filepath.Join(storagePath, "organized")

	err = filepath.WalkDir(organizedRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// An error opening the organized root itself (the first callback
			// invocation, from the initial os.Lstat) is fatal.
			if path == organizedRoot {
				return walkErr
			}
			// Errors on nested entries are best-effort: record and continue.
			result.Errors = append(result.Errors, walkErr)
			return nil
		}

		if d.IsDir() {
			return nil
		}

		// Known-set membership first — matched files cost no extra stat.
		if _, ok := known[path]; ok {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("stat %s: %w", path, err))
			return nil
		}

		ct, err := ctime(info)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("ctime %s: %w", path, err))
			return nil
		}

		result.Candidates = append(result.Candidates, Candidate{
			Path:  path,
			CTime: ct,
		})
		return nil
	})
	if err != nil {
		return result, fmt.Errorf("orphans.Scan: walk %s: %w", organizedRoot, err)
	}

	return result, nil
}
