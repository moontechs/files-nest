// Package filestore provides file storage and organization operations
// for the iCloud Backup server. It handles moving completed files from
// the tusd incoming directory into the date-based organized tree.
package filestore

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// dirPerm is the permission mode used when creating directories in the
// organized storage tree (owner/group read-write-execute, no world access).
const (
	dirPerm           = 0o750
	maxPathSegmentLen = 200
	unknownSegment    = "unknown"

	// maxFilenameSegmentLen caps the final organized filename component at the
	// common filesystem per-component limit (NAME_MAX, 255 bytes). Only the
	// filename stem is ever truncated to fit; the `_<id>` suffix and the
	// extension always survive intact (they identify the record and the type).
	maxFilenameSegmentLen = 255
)

// Sentinel errors for path/file operations, wrapped at the call site with
// contextual details rather than constructed dynamically.
var (
	errPathEscape  = errors.New("path escapes storage root")
	errBothMissing = errors.New("source and destination do not exist")
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
func (m *Mover) OrganizedPath(creationDate, filename string) (string, string) {
	var year, month, day string

	t, ok := parseCreationDate(creationDate)
	if ok {
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
		month = unknownSegment
		day = unknownSegment
	}

	rel := filepath.Join("organized", year, month, day, filename)
	abs := filepath.Join(m.storagePath, rel)

	return rel, abs
}

// PlanDestResult holds the planned destination paths for an upload.
type PlanDestResult struct {
	// Abs is the absolute destination path on the filesystem.
	Abs string
	// Rel is the relative destination path for storage in DB records.
	Rel string
	// DateUsed is the date string that was actually used to build the
	// organized path: creationDate when parseable, else createdAt as a
	// fallback, else the raw (possibly unparseable) input string. Callers
	// use it to apply the same resolved date to the moved file's
	// timestamps instead of re-deriving fallback logic.
	DateUsed string
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
		return fmt.Errorf("%w: path %q escapes storage root %q", errPathEscape, organizedPath, m.storagePath)
	}

	m.moveMu.Lock()
	defer m.moveMu.Unlock()

	err := os.Remove(cleanAbs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}

		return fmt.Errorf("remove organized file: %w", err)
	}

	return nil
}

// PlanDestination computes the destination file paths for an upload,
// returning the absolute destination path and relative organized path.
//
// The organized path uses the form: organized/YYYY/MM/DD/<stem>_<id><ext>
// where YYYY, MM, DD are extracted from the creation date and <id> is the
// caller-supplied per-record identifier. The suffix is applied
// unconditionally — never only on collision — so the destination is fully
// deterministic from (creationDate, filename, id) alone, with no dependence
// on current disk state. If creationDate cannot be parsed as RFC3339 or
// YYYY-MM-DD, createdAt is tried as a fallback. If neither can be parsed,
// the raw date string is used as a single segment under organized/ with
// month and day defaulting to "unknown".
//
// No collision-avoidance fallback exists between two live records: the
// store layer keys records by LocalIdentifier and returns the existing
// record on a repeat (PutUploadIfAbsent), so no two live records can share
// an id in the first place — the SafeID hash's uniqueness alone is not the
// guarantee. The moveMu lock separately forecloses the TOCTOU window
// between planning and the actual move.
//
// A *foreign* file (not produced by this mover) already occupying the
// computed path is a separate, accepted risk: the path is deterministic, so
// the move silently overwrites it. The single os.Stat in this method exists
// only as a detection/logging safety net for that case — it is never a
// naming decision, and the planned paths are returned unchanged either way.
func (m *Mover) PlanDestination(creationDate, createdAt, filename, id string) PlanDestResult {
	// Determine the best available date for path construction.
	dateToUse := creationDate
	if dateToUse == "" || !isParseableDate(dateToUse) {
		if createdAt != "" && isParseableDate(createdAt) {
			dateToUse = createdAt
		} else {
			dateToUse = creationDate
			if dateToUse == "" {
				dateToUse = createdAt
			}
		}
	}

	// Parse date into YYYY/MM/DD.
	year, month, day := datePathSegments(dateToUse)

	// Always append the identifier suffix to the filename component, never
	// only on collision. The suffix and the extension must survive intact
	// (they identify the record and the file type), so only the stem is
	// truncated if the full <stem>_<id><ext> would exceed filesystem name
	// limits; an empty filename has no stem and is left unchanged.
	filename = suffixFilename(filename, id)

	rel := filepath.Join("organized", year, month, day, filename)
	abs := filepath.Join(m.storagePath, rel)

	// Safety net only: a pre-existing file at the deterministic destination
	// is unexpected (foreign file, inconsistent disk state, or a bug
	// elsewhere). This stat never influences the returned paths — the move
	// overwrites — it exists purely so the condition is observable in logs.
	_, statErr := os.Stat(abs)
	if statErr == nil {
		log.Printf("WARN filestore: unexpected file already at organized destination %s; it will be overwritten", abs)
	}

	return PlanDestResult{Abs: abs, Rel: rel, DateUsed: dateToUse}
}

// suffixFilename inserts "_" + id before the extension of a filename,
// truncating the stem (never the suffix or extension) so the full
// <stem>_<id><ext> component stays within maxFilenameSegmentLen bytes.
// An empty filename is returned unchanged (there is no stem to suffix).
func suffixFilename(filename, id string) string {
	if filename == "" {
		return ""
	}

	ext := filepath.Ext(filename)
	stem := strings.TrimSuffix(filename, ext)

	// Bytes available for the stem once the suffix and extension take
	// their share of the per-component limit.
	maxStem := max(maxFilenameSegmentLen-1-len(id)-len(ext), 0)

	if len(stem) > maxStem {
		stem = stem[:maxStem]
	}

	return stem + "_" + id + ext
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

	// Pass the raw creationDate through (not plan.DateUsed): the method's
	// contract is that the caller's creationDate wins for the timestamp,
	// matching the date it used for path construction via PlanDestination.
	err := MoveFile(srcPath, plan.Abs, creationDate)
	if err != nil {
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
func (m *Mover) PlanAndMove(
	src, creationDate, createdAt, filename, backendID string, beforeMove func(PlanDestResult) error,
) (PlanDestResult, error) {
	m.moveMu.Lock()
	defer m.moveMu.Unlock()

	plan := m.PlanDestination(creationDate, createdAt, filename, backendID)
	if beforeMove != nil {
		err := beforeMove(plan)
		if err != nil {
			return plan, err
		}
	}

	err := MoveFile(src, plan.Abs, plan.DateUsed)
	if err != nil {
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
		err := beforeMove(plan)
		if err != nil {
			return err
		}
	}

	return MoveFile(src, plan.Abs, plan.DateUsed)
}

// MoveFile moves a file from src to dst atomically using os.Rename.
// It creates the destination directory if needed. On EXDEV (cross-device
// link) it falls back to copy-then-delete.
//
// creationDate, when parseable and plausible, is applied to the moved
// file's mtime/atime via os.Chtimes so filesystem tools and backups see
// the file's actual capture date instead of the upload time. This is
// best-effort: an unparseable, empty, or implausible date (the common
// EXIF clock-corruption case) leaves the file at upload-time mtime, and
// a Chtimes failure is logged but does not fail the move — the move
// itself already succeeded.
//
// MoveFile is idempotent for crash recovery: if src does not exist but
// dst does, it returns nil (the file was already moved). If both src
// and dst are missing, it returns a descriptive error.
func MoveFile(src, dst, creationDate string) error {
	// Idempotent recovery: if src is gone but dst exists, treat as already moved.
	_, err := os.Stat(src)
	if os.IsNotExist(err) {
		_, err = os.Stat(dst)
		if err == nil {
			return nil
		}

		return fmt.Errorf("%w: source %s does not exist and destination %s does not exist either", errBothMissing, src, dst)
	}

	err = os.MkdirAll(filepath.Dir(dst), dirPerm)
	if err != nil {
		return fmt.Errorf("create destination directory %s: %w", filepath.Dir(dst), err)
	}

	if err := renameOrCopy(src, dst); err != nil {
		return err
	}

	// The Chtimes call below runs while PlanAndMove / MoveToPlaned / the
	// method MoveFile hold m.moveMu, the single mutex serializing all moves
	// (mover.go). That is acceptable because Chtimes is a fast local
	// syscall, but if moves ever target slow storage (e.g. the network-mount
	// case ADR 0006 warns against), this turns into a throughput bottleneck
	// nobody asked for — revisit then.
	applyCreationTimestamp(dst, creationDate)

	return nil
}

// applyCreationTimestamp best-effort sets dst's mtime/atime to the
// client-supplied creation date. Unparseable, empty, or out-of-range
// dates skip Chtimes entirely with no log line — implausible dates are an
// expected/common input for this data source (camera/phone clocks with a
// dead RTC battery routinely produce epoch or far-future EXIF dates), not
// an error. A Chtimes failure on a sane date is logged but not returned:
// the move itself already succeeded, and a missing/wrong timestamp is a
// lesser defect than losing the uploaded file.
func applyCreationTimestamp(dst, creationDate string) {
	if creationDate == "" {
		return
	}

	t, ok := parseCreationDate(creationDate)
	if !ok || !isSaneCreationDate(t, time.Now()) {
		return
	}

	if err := os.Chtimes(dst, t, t); err != nil {
		log.Printf("ERROR filestore: chtimes %s failed: %v", dst, err)
	}
}

// isSaneCreationDate reports whether a parsed creation date is plausible
// enough to burn into permanent filesystem state. Chtimes writes are not
// reversible and leave no "this looked suspicious" record, so dates
// before the sane minimum (a dead RTC battery's epoch output) or more
// than maxSaneCreationDateSkew into the future (client clock skew) are
// clamped out: the file keeps its upload-time mtime instead.
//
//nolint:gochecknoglobals // immutable config thresholds for the sanity clamp, never mutated at runtime
var (
	minSaneCreationDate     = time.Date(1990, 1, 1, 0, 0, 0, 0, time.UTC)
	maxSaneCreationDateSkew = 24 * time.Hour // allow for client clock skew
)

func isSaneCreationDate(t, now time.Time) bool {
	return !t.Before(minSaneCreationDate) && !t.After(now.Add(maxSaneCreationDateSkew))
}

// renameOrCopy renames src to dst, falling back to a copy-then-delete on
// EXDEV (cross-device link). os.Rename fails across filesystem boundaries,
// which happens when the incoming and organized directories live on
// different mounts; the copy fallback preserves behavior in that case.
func renameOrCopy(src, dst string) error {
	err := os.Rename(src, dst)
	if err == nil {
		return nil
	}

	if !errors.Is(err, syscall.EXDEV) {
		return fmt.Errorf("rename %s -> %s: %w", src, dst, err)
	}

	err = copyFile(src, dst)
	if err != nil {
		return fmt.Errorf("copy %s -> %s (cross-device): %w", src, dst, err)
	}

	err = os.Remove(src)
	if err != nil {
		return fmt.Errorf("remove source %s after cross-device copy: %w", src, err)
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
	return copyFileWithOps(src, dst, osFileOps{})
}

type readCloser interface {
	io.Reader
	io.Closer
}

type syncWriteCloser interface {
	io.Writer
	Sync() error
	io.Closer
}

type fileOps interface {
	Open(string) (readCloser, error)
	Create(string) (syncWriteCloser, error)
	Stat(string) (os.FileInfo, error)
	Chmod(string, os.FileMode) error
}

type osFileOps struct{}

func (osFileOps) Open(name string) (readCloser, error) {
	return os.Open(name)
}

func (osFileOps) Create(name string) (syncWriteCloser, error) {
	return os.Create(name)
}

func (osFileOps) Stat(name string) (os.FileInfo, error) {
	return os.Stat(name)
}

func (osFileOps) Chmod(name string, mode os.FileMode) error {
	return os.Chmod(name, mode)
}

func copyFileWithOps(src, dst string, ops fileOps) error {
	//nolint:gosec // src/dst are built from sanitized components (SafeID,
	// SanitizeFilename, SafePathSegment) or server-generated tusd paths, never raw user input
	srcFile, err := ops.Open(src)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer func() { _ = srcFile.Close() }()

	//nolint:gosec // dst is a sanitized organized-tree path computed by PlanDestination
	dstFile, err := ops.Create(dst)
	if err != nil {
		return fmt.Errorf("create destination: %w", err)
	}

	// Copy data, flush it to disk, then close while surfacing any deferred
	// write error. A bare `defer dstFile.Close()` would swallow Close errors
	// that indicate the data did not reach stable storage.
	_, err = io.Copy(dstFile, srcFile)
	if err != nil {
		_ = dstFile.Close()

		return fmt.Errorf("copy data: %w", err)
	}

	err = dstFile.Sync()
	if err != nil {
		_ = dstFile.Close()

		return fmt.Errorf("sync destination: %w", err)
	}

	err = dstFile.Close()
	if err != nil {
		return fmt.Errorf("close destination: %w", err)
	}

	// Preserve the file mode from the source.
	srcInfo, err := ops.Stat(src)
	if err != nil {
		return fmt.Errorf("stat source after copy: %w", err)
	}

	err = ops.Chmod(dst, srcInfo.Mode())
	if err != nil {
		return fmt.Errorf("chmod destination: %w", err)
	}

	return nil
}

// parseCreationDate parses a creation date string, trying RFC3339 first
// (e.g. "2024-03-15T10:30:00Z", including fractional seconds) and falling
// back to the YYYY-MM-DD calendar format. It returns the parsed time and
// whether parsing succeeded.
func parseCreationDate(input string) (time.Time, bool) {
	parsed, err := time.Parse(time.RFC3339, input)
	if err == nil {
		return parsed, true
	}

	parsed, err = time.Parse("2006-01-02", input)
	if err == nil {
		return parsed, true
	}

	return time.Time{}, false
}

// datePathSegments converts a date string into the YYYY/MM/DD path segments
// used by the organized tree. RFC3339 and YYYY-MM-DD values are parsed into
// their calendar components; anything else falls back to a sanitized single
// segment with "unknown" month/day placeholders.
func datePathSegments(dateToUse string) (string, string, string) {
	t, ok := parseCreationDate(dateToUse)
	if ok {
		return t.Format("2006"), t.Format("01"), t.Format("02")
	}

	year := SafePathSegment(dateToUse)
	if year == "" {
		year = unknownSegment
	}

	return year, unknownSegment, unknownSegment
}

// isParseableDate checks whether a string can be parsed as a standard date
// format (RFC3339 or YYYY-MM-DD).
func isParseableDate(value string) bool {
	if value == "" {
		return false
	}

	_, ok := parseCreationDate(value)

	return ok
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
func SafePathSegment(value string) string {
	if value == "" {
		return ""
	}

	var builder strings.Builder

	for _, r := range value {
		switch r {
		case '/', '\\':
			builder.WriteRune('_')
		case '.', ' ':
			// keep; handled by trimming below
			builder.WriteRune(r)
		default:
			if r < 32 || r == 0 {
				continue
			}

			builder.WriteRune(r)
		}
	}

	out := strings.Trim(builder.String(), ". _")
	if out == "" || out == "." || out == ".." {
		return ""
	}

	if len(out) > maxPathSegmentLen {
		out = out[:maxPathSegmentLen]
		out = strings.TrimRight(out, ". _")
	}

	return out
}
