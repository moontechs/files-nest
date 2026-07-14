// Package filestore provides file storage and organization operations
// for the iCloud Backup server. It handles moving completed files from
// the tusd incoming directory into the date-based organized tree.
package filestore

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Mover organizes completed upload files from the tusd incoming directory
// into the date-based organized tree under the storage root path.
//
// moveMu serializes the plan-then-move sequence for the organized tree.
// PlanDestination detects collisions with os.Stat and MoveFile renames
// atomically; without a lock spanning both, two concurrent completions
// for different uploads that share a creation date + filename could each
// see no file at the computed path, compute identical destinations, and
// the second rename would silently overwrite the first upload's data.
// The mutex closes that TOCTOU window. Moves are fast (in-process rename),
// so global serialization of the move step has no practical throughput
// impact on the upload (PATCH /data) path, which is only same-id locked.
type Mover struct {
	storagePath string
	moveMu      sync.Mutex
}

// New creates a new Mover with the given storage root path.
// The storage root is the top-level directory containing incoming/,
// organized/, and db/ subdirectories.
func New(storagePath string) *Mover {
	return &Mover{storagePath: storagePath}
}

// StoragePath returns the storage root path. Exported for test access.
func (m *Mover) StoragePath() string {
	return m.storagePath
}

// OrganizedPath computes the relative and absolute organized file paths
// from the creation date and filename.
//
// The relative path uses the form: organized/YYYY/MM/DD/<filename>
// where YYYY, MM, DD are extracted from the creation date.
//
// If the creation date cannot be parsed as RFC3339 or YYYY-MM-DD, the
// raw date string is used as a single path segment under organized/ and
// month/day default to "unknown".
func (m *Mover) OrganizedPath(creationDate, filename string) (rel, abs string) {
	var year, month, day string
	if t, err := time.Parse(time.RFC3339, creationDate); err == nil {
		year = t.Format("2006")
		month = t.Format("01")
		day = t.Format("02")
	} else if t, err := time.Parse("2006-01-02", creationDate); err == nil {
		year = t.Format("2006")
		month = t.Format("01")
		day = t.Format("02")
	} else {
		// Fallback: sanitize the raw date string as a single path segment.
		// SafePathSegment rejects traversal characters ('/', '\\', '..') so an
		// unparseable date can never escape the organized root. An empty result
		// (empty or unsafe input) is left empty so filepath.Join collapses it,
		// preserving the organized/unknown/unknown/<file> layout for empty dates.
		year = SafePathSegment(creationDate)
		month = "unknown"
		day = "unknown"
	}

	rel = filepath.Join("organized", year, month, day, filename)
	abs = filepath.Join(m.storagePath, rel)
	return
}

// PlanDestResult holds the planned destination paths for an upload.
type PlanDestResult struct {
	// Abs is the absolute destination path on the filesystem.
	Abs string
	// Rel is the relative destination path for storage in DB records.
	Rel string
}

// RemoveOrganizedFile removes the organized file at the given relative path
// from the storage root. It is safe to call on uploads that were never
// completed (empty organizedPath returns nil immediately).
//
// Path safety: the resolved absolute path is cleaned and verified to be
// contained within the storage root before any file operation, as
// defense-in-depth in case organizedPath is ever fed from a source other
// than Mover's own output.
//
// The mutex m.moveMu is acquired for the duration of the os.Remove call
// to prevent TOCTOU with concurrent PlanDestination / MoveFile operations
// that may stat and rename to the same computed path.
//
// If the file does not exist (os.IsNotExist), nil is returned — the
// operation is idempotent. Any other error is returned to the caller.
func (m *Mover) RemoveOrganizedFile(organizedPath string) error {
	if organizedPath == "" {
		return nil
	}

	absPath := filepath.Join(m.storagePath, organizedPath)
	cleanAbs := filepath.Clean(absPath)
	cleanRoot := filepath.Clean(m.storagePath)

	// Verify the resolved path is still contained within the storage root.
	// Without this check, a relative path containing ".." segments could
	// escape the organized tree even after filepath.Join and Clean.
	if !strings.HasPrefix(cleanAbs, cleanRoot+string(filepath.Separator)) && cleanAbs != cleanRoot {
		return fmt.Errorf("path %q escapes storage root %q", organizedPath, m.storagePath)
	}

	m.moveMu.Lock()
	defer m.moveMu.Unlock()

	if err := os.Remove(cleanAbs); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return nil
}

// PlanDestination computes the destination file paths for an upload,
// returning the absolute destination path and relative organized path.
//
// The organized path uses the form: organized/YYYY/MM/DD/<filename>
// where YYYY, MM, DD are extracted from the creation date. If
// creationDate cannot be parsed as RFC3339 or YYYY-MM-DD, createdAt
// is tried as a fallback. If neither can be parsed, the raw date
// string is used as a single segment under organized/ with month
// and day defaulting to "unknown".
//
// If a file already exists at the computed destination, the backendID
// is inserted before the filename extension to avoid overwriting the
// existing file (e.g. IMG_0001.jpg → IMG_0001_<backendID>.jpg).
func (m *Mover) PlanDestination(creationDate, createdAt, filename, backendID string) PlanDestResult {
	// Determine the best available date for path construction.
	var dateToUse string
	if creationDate != "" && isParseableDate(creationDate) {
		dateToUse = creationDate
	} else if createdAt != "" && isParseableDate(createdAt) {
		dateToUse = createdAt
	} else {
		dateToUse = creationDate
		if dateToUse == "" {
			dateToUse = createdAt
		}
	}

	// Parse date into YYYY/MM/DD.
	var year, month, day string
	t1, err1 := time.Parse(time.RFC3339, dateToUse)
	if err1 == nil {
		year = t1.Format("2006")
		month = t1.Format("01")
		day = t1.Format("02")
	} else {
		t2, err2 := time.Parse("2006-01-02", dateToUse)
		if err2 == nil {
			year = t2.Format("2006")
			month = t2.Format("01")
			day = t2.Format("02")
		} else {
			year = SafePathSegment(dateToUse)
			if year == "" {
				year = "unknown"
			}
			month = "unknown"
			day = "unknown"
		}
	}

	rel := filepath.Join("organized", year, month, day, filename)
	abs := filepath.Join(m.storagePath, rel)

	// Collision: if a file already exists at the computed path, insert
	// backendID before the extension to avoid overwriting.
	if _, err := os.Stat(abs); err == nil {
		ext := filepath.Ext(abs)
		base := strings.TrimSuffix(abs, ext)
		abs = base + "_" + backendID + ext
		rel = filepath.Join(filepath.Dir(rel), filepath.Base(abs))
	}

	return PlanDestResult{Abs: abs, Rel: rel}
}

// MoveResult holds the result of a file move operation.
type MoveResult struct {
	// Src is the original source file path that was moved.
	Src string
	// Dst is the final absolute destination path after the move.
	Dst string
	// DstRel is the relative destination path for storage in DB records.
	DstRel string
	// Deduplicated is true when the destination was modified because a file
	// already existed at the computed path. When true, the filename used has
	// the backendID inserted before the extension (e.g. IMG_0001_<id>.jpg).
	Deduplicated bool
}

// MoveFile moves a file from srcPath to the organized tree. It computes
// the destination path from creationDate and filename, creates the
// destination directory if needed, and handles deduplication when a file
// already exists at the computed path by inserting the backendID before
// the filename extension.
//
// MoveFile uses os.Rename for an atomic move on the same filesystem (the
// common case since incoming/ and organized/ are both under the storage
// root). If the rename fails with EXDEV (cross-device link), it falls
// back to a copy-then-remove sequence.
//
// On success it returns a MoveResult with the final paths. The caller
// should use DstRel (not the computed rel) for persisting in the DB,
// because deduplication may have changed the filename.
func (m *Mover) MoveFile(srcPath, creationDate, filename, backendID string) (*MoveResult, error) {
	m.moveMu.Lock()
	defer m.moveMu.Unlock()
	plan := m.PlanDestination(creationDate, "", filename, backendID)

	// Determine if deduplication occurred by comparing the final path
	// against the base organized path (without collision handling).
	baseRel, _ := m.OrganizedPath(creationDate, filename)
	deduped := plan.Rel != baseRel

	if err := MoveFile(srcPath, plan.Abs); err != nil {
		return nil, err
	}

	return &MoveResult{
		Src:          srcPath,
		Dst:          plan.Abs,
		DstRel:       plan.Rel,
		Deduplicated: deduped,
	}, nil
}

// PlanAndMove plans the destination for a completed upload and moves the
// file there, both under the organized-tree mutex so the collision check
// in PlanDestination and the rename in MoveFile are atomic with respect
// to other concurrent completions. beforeMove (if non-nil) is called
// after planning and before the rename, still holding the mutex; callers
// use it to persist a completion intent for crash recovery so a crash
// between the intent write and the rename remains recoverable.
//
// If beforeMove returns an error, no move is performed and that error is
// returned to the caller.
func (m *Mover) PlanAndMove(src, creationDate, createdAt, filename, backendID string, beforeMove func(PlanDestResult) error) (PlanDestResult, error) {
	m.moveMu.Lock()
	defer m.moveMu.Unlock()
	plan := m.PlanDestination(creationDate, createdAt, filename, backendID)
	if beforeMove != nil {
		if err := beforeMove(plan); err != nil {
			return plan, err
		}
	}
	if err := MoveFile(src, plan.Abs); err != nil {
		return plan, err
	}
	return plan, nil
}

// MoveToPlaned moves src to a pre-determined destination (for example, one
// recovered from an existing completion intent on a retry) under the
// organized-tree mutex. beforeMove (if non-nil) is called after acquiring
// the mutex and before the rename, so callers can refresh a completion
// intent with the same atomicity guarantees as PlanAndMove.
func (m *Mover) MoveToPlaned(src string, plan PlanDestResult, beforeMove func(PlanDestResult) error) error {
	m.moveMu.Lock()
	defer m.moveMu.Unlock()
	if beforeMove != nil {
		if err := beforeMove(plan); err != nil {
			return err
		}
	}
	return MoveFile(src, plan.Abs)
}

// MoveFile moves a file from src to dst atomically using os.Rename.
// It creates the destination directory if needed. On EXDEV (cross-device
// link) it falls back to copy-then-delete.
//
// MoveFile is idempotent for crash recovery: if src does not exist but
// dst does, it returns nil (the file was already moved). If both src
// and dst are missing, it returns a descriptive error.
func MoveFile(src, dst string) error {
	// Idempotent recovery: if src is gone but dst exists, treat as already moved.
	if _, err := os.Stat(src); os.IsNotExist(err) {
		if _, err := os.Stat(dst); err == nil {
			return nil
		}
		return fmt.Errorf("source %s does not exist and destination %s does not exist either", src, dst)
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return fmt.Errorf("create destination directory %s: %w", filepath.Dir(dst), err)
	}

	if err := os.Rename(src, dst); err != nil {
		if errors.Is(err, syscall.EXDEV) {
			if err := copyFile(src, dst); err != nil {
				return fmt.Errorf("copy %s -> %s (cross-device): %w", src, dst, err)
			}
			if err := os.Remove(src); err != nil {
				return fmt.Errorf("remove source %s after cross-device copy: %w", src, err)
			}
		} else {
			return fmt.Errorf("rename %s -> %s: %w", src, dst, err)
		}
	}

	return nil
}

// copyFile copies a file from src to dst, preserving the file mode.
// The destination directory must already exist.
//
// This is the cross-device (EXDEV) fallback for MoveFile. Because the caller
// removes the source immediately after a successful copy, the destination
// must be durably on disk before we return — otherwise a crash after io.Copy
// completes but before the kernel flushes the destination's data would leave
// a partial/empty destination with the source already gone (data loss on
// recovery). We therefore Sync the destination and check the Close error
// (which can surface a deferred write error from the kernel) before
// reporting success.
func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create destination: %w", err)
	}

	// Copy data, flush it to disk, then close while surfacing any deferred
	// write error. A bare `defer dstFile.Close()` would swallow Close errors
	// that indicate the data did not reach stable storage.
	if _, err := io.Copy(dstFile, srcFile); err != nil {
		dstFile.Close()
		return fmt.Errorf("copy data: %w", err)
	}
	if err := dstFile.Sync(); err != nil {
		dstFile.Close()
		return fmt.Errorf("sync destination: %w", err)
	}
	if err := dstFile.Close(); err != nil {
		return fmt.Errorf("close destination: %w", err)
	}

	// Preserve the file mode from the source.
	srcInfo, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("stat source after copy: %w", err)
	}
	if err := os.Chmod(dst, srcInfo.Mode()); err != nil {
		return fmt.Errorf("chmod destination: %w", err)
	}

	return nil
}

// isParseableDate checks whether a string can be parsed as a standard date
// format (RFC3339 or YYYY-MM-DD).
func isParseableDate(s string) bool {
	if s == "" {
		return false
	}
	if _, err := time.Parse(time.RFC3339, s); err == nil {
		return true
	}
	if _, err := time.Parse("2006-01-02", s); err == nil {
		return true
	}
	return false
}

// SafePathSegment converts an arbitrary string into a single path-safe path
// segment. It is used when a creation date cannot be parsed as RFC3339 or
// YYYY-MM-DD and the raw string would otherwise be used directly as a
// directory name under organized/. Without this, a malformed or adversarial
// creation_date containing '/', '\', '..', or control characters could escape
// the intended organized tree (path traversal / injection).
//
// The function:
//   - replaces '/' and '\' with '_',
//   - removes control characters (< 0x20) and NULL,
//   - rejects "." and ".." (collapses to empty),
//   - trims leading/trailing dots and spaces (filesystem-irritating),
//   - truncates to 200 bytes (well under common FS name limits).
//
// It returns empty string if the result is empty or unsafe.
func SafePathSegment(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '/', '\\':
			b.WriteRune('_')
		case '.', ' ':
			// keep; handled by trimming below
			b.WriteRune(r)
		default:
			if r < 32 || r == 0 {
				continue
			}
			b.WriteRune(r)
		}
	}
	out := strings.Trim(b.String(), ". _")
	if out == "" || out == "." || out == ".." {
		return ""
	}
	if len(out) > 200 {
		out = out[:200]
		out = strings.TrimRight(out, ". _")
	}
	return out
}
