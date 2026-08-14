package uploadbackend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/tus/tusd/v2/pkg/filestore"
	"github.com/tus/tusd/v2/pkg/handler"
	"github.com/tus/tusd/v2/pkg/memorylocker"
)

// dirPerm is the permission mode used when creating the incoming/ directory.
const dirPerm = 0o750

// Internal sentinel errors for tusd adapter operations. These are wrapped at
// the call site with contextual details rather than constructed dynamically.
var (
	errMissingLocation     = errors.New("tusd: missing Location header in CreateUpload response")
	errExtractUploadID     = errors.New("tusd: could not extract upload ID from Location")
	errMissingOffset       = errors.New("tusd: missing Upload-Offset in PATCH response")
	errNoStorageInfo       = errors.New("tusd: no storage info available for upload")
	errMissingStorageKey   = errors.New("tusd: missing StorageKeyPath for upload")
	errTusdConflict        = errors.New("tusd conflict")
	errTusdNotImplemented  = errors.New("tusd not implemented")
	errTusdVersionMismatch = errors.New("tusd version mismatch")
	errUnsupportedVersion  = errors.New("tusd: unsupported version")
	errTusdGeneric         = errors.New("tusd error")
	errTusdHTTP            = errors.New("tusd: HTTP error")
)

// tusdRecorder wraps httptest.ResponseRecorder to satisfy the deadline-setting
// interface http.ResponseController probes for. tusd calls SetReadDeadline/
// SetWriteDeadline on every body-read tick; ResponseRecorder doesn't implement
// them, so tusd logs a NetworkTimeoutError WARN on every tick. There is no real
// deadline to enforce for an in-process call, so these are no-ops.
type tusdRecorder struct {
	*httptest.ResponseRecorder
}

func newTusdRecorder() *tusdRecorder {
	return &tusdRecorder{ResponseRecorder: httptest.NewRecorder()}
}

func (r *tusdRecorder) SetReadDeadline(time.Time) error  { return nil }
func (r *tusdRecorder) SetWriteDeadline(time.Time) error { return nil }

// UploadInfo contains full information about an upload from the backend.
// This is a project-owned type that isolates callers from tusd's handler.FileInfo.
type UploadInfo struct {
	// ID is the tusd upload identifier (backend_id).
	ID string
	// Size is the total file size in bytes. Zero if SizeIsDeferred is true.
	Size int64
	// SizeIsDeferred indicates that the total file size was not yet declared.
	SizeIsDeferred bool
	// Offset is the number of bytes written so far.
	Offset int64
	// Metadata contains the Upload-Metadata key/value pairs.
	Metadata map[string]string
	// Storage contains backend-specific storage information, such as
	// the binary file path ("Path") and info file path ("InfoPath").
	Storage map[string]string
}

// TUSHandler wraps an embedded tusd v2 UnroutedHandler and exposes a narrow
// API for upload creation, data transfer, status queries, and cleanup.
// No tusd types are exposed outside this package.
type TUSHandler struct {
	store    filestore.FileStore
	composer *handler.StoreComposer
	handler  *handler.UnroutedHandler
}

// New creates a new TUSHandler with storage rooted at the given path.
// The incoming/ subdirectory holds the tusd binary files and .info sidecars.
// The directory is created if it does not exist.
func New(storagePath string) (*TUSHandler, error) {
	incomingPath := filepath.Join(storagePath, "incoming")

	err := os.MkdirAll(incomingPath, dirPerm)
	if err != nil {
		return nil, fmt.Errorf("create incoming dir %s: %w", incomingPath, err)
	}

	fileStore := filestore.New(incomingPath)
	ml := memorylocker.New()

	composer := handler.NewStoreComposer()
	fileStore.UseIn(composer)
	ml.UseIn(composer)

	unrouted, err := handler.NewUnroutedHandler(handler.Config{
		StoreComposer: composer,
		BasePath:      "/",
		// Disable notifications — we handle completion ourselves.
		NotifyCompleteUploads:   false,
		NotifyTerminatedUploads: false,
		NotifyUploadProgress:    false,
		NotifyCreatedUploads:    false,
		// Disable download (not needed for upload-only system).
		DisableDownload: true,
		// Enable termination (DELETE).
		DisableTermination: false,
	})
	if err != nil {
		return nil, fmt.Errorf("create tusd handler: %w", err)
	}

	return &TUSHandler{
		store:    fileStore,
		composer: composer,
		handler:  unrouted,
	}, nil
}

// ---------------------------------------------------------------------------
// CreateUpload
// ---------------------------------------------------------------------------

// CreateUpload creates a new upload with Upload-Defer-Length: 1 (file size
// is not yet known). The metadata parameter is the raw Upload-Metadata header
// value, or empty to omit. Returns the tusd upload ID (backend_id).
func (h *TUSHandler) CreateUpload(ctx context.Context, metadata string) (string, error) {
	rec := newTusdRecorder()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "/", nil)
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Defer-Length", "1")

	if metadata != "" {
		req.Header.Set("Upload-Metadata", metadata)
	}

	h.handler.PostFile(rec, req)

	if rec.Code != http.StatusCreated {
		return "", extractTusdError(rec.ResponseRecorder)
	}

	location := rec.Header().Get("Location")
	if location == "" {
		return "", errMissingLocation
	}

	// Extract the upload ID from the Location URL.
	// The URL is like "http://.../<id>" or "/<id>".
	id := filepath.Base(location)
	if id == "" || id == "." || id == "/" {
		return "", fmt.Errorf("%w: %q", errExtractUploadID, location)
	}

	return id, nil
}

// ---------------------------------------------------------------------------
// GetOffset
// ---------------------------------------------------------------------------

// GetOffset returns the current upload offset (bytes written) for the given
// backend ID. Returns ErrNotFound if the upload does not exist.
func (h *TUSHandler) GetOffset(ctx context.Context, backendID string) (int64, error) {
	info, err := h.GetInfo(ctx, backendID)
	if err != nil {
		return 0, err
	}

	return info.Offset, nil
}

// ---------------------------------------------------------------------------
// ForwardPatch
// ---------------------------------------------------------------------------

// ForwardPatch streams data from body to the tusd upload at the given offset.
// If uploadLength is non-empty, it declares the final upload length (used for
// deferred-length uploads to finalize the size). Returns the new offset after
// the chunk is written, or an error.
func (h *TUSHandler) ForwardPatch(
	ctx context.Context, backendID string, body io.Reader, offset int64, uploadLength string,
) (int64, error) {
	rec := newTusdRecorder()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPatch, "/"+backendID, body)
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Offset", strconv.FormatInt(offset, 10))
	req.Header.Set("Content-Type", "application/offset+octet-stream")

	if uploadLength != "" {
		req.Header.Set("Upload-Length", uploadLength)
	}

	h.handler.PatchFile(rec, req)

	if rec.Code != http.StatusNoContent {
		return 0, extractTusdError(rec.ResponseRecorder)
	}

	offsetStr := rec.Header().Get("Upload-Offset")
	if offsetStr == "" {
		return 0, errMissingOffset
	}

	newOff, err := strconv.ParseInt(offsetStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("tusd: invalid Upload-Offset %q: %w", offsetStr, err)
	}

	return newOff, nil
}

// ---------------------------------------------------------------------------
// GetInfo
// ---------------------------------------------------------------------------

// GetInfo returns full upload information from the tusd data store.
// Returns ErrNotFound if the upload does not exist.
func (h *TUSHandler) GetInfo(ctx context.Context, backendID string) (*UploadInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	upload, err := h.composer.Core.GetUpload(ctx, backendID)
	if err != nil {
		return nil, normalizeError(err)
	}

	info, err := upload.GetInfo(ctx)
	if err != nil {
		return nil, normalizeError(err)
	}

	return &UploadInfo{
		ID:             info.ID,
		Size:           info.Size,
		SizeIsDeferred: info.SizeIsDeferred,
		Offset:         info.Offset,
		Metadata:       info.MetaData,
		Storage:        info.Storage,
	}, nil
}

// ---------------------------------------------------------------------------
// IsComplete
// ---------------------------------------------------------------------------

// IsComplete checks whether the upload has a known (non-deferred) length and
// offset equals length. Returns ErrNotFound if the upload does not exist.
func (h *TUSHandler) IsComplete(ctx context.Context, backendID string) (bool, error) {
	info, err := h.GetInfo(ctx, backendID)
	if err != nil {
		return false, err
	}

	return !info.SizeIsDeferred && info.Offset == info.Size, nil
}

// ---------------------------------------------------------------------------
// FilePath
// ---------------------------------------------------------------------------

// FilePath returns the absolute path to the upload's binary file on disk.
// Returns ErrNotFound if the upload does not exist.
func (h *TUSHandler) FilePath(ctx context.Context, backendID string) (string, error) {
	info, err := h.GetInfo(ctx, backendID)
	if err != nil {
		return "", err
	}

	if info.Storage == nil {
		return "", fmt.Errorf("%w: %s", errNoStorageInfo, backendID)
	}

	path, ok := info.Storage[filestore.StorageKeyPath]
	if !ok || path == "" {
		return "", fmt.Errorf("%w: %s", errMissingStorageKey, backendID)
	}

	return path, nil
}

// ---------------------------------------------------------------------------
// TerminateOrCleanup
// ---------------------------------------------------------------------------

// TerminateOrCleanup terminates the upload in tusd, removing both the binary
// file and the .info sidecar from disk. If the binary file has already been
// moved (e.g. during the completion flow), it cleans up any remaining .info
// file directly and returns ErrNotFound to indicate no tusd state remains.
//
// Callers should ignore ErrNotFound and treat it as success (nothing to
// clean up), but can use it to branch on whether tusd state existed.
func (h *TUSHandler) TerminateOrCleanup(ctx context.Context, backendID string) error {
	// First, try proper termination through the tusd handler.
	rec := newTusdRecorder()
	req, _ := http.NewRequestWithContext(ctx, http.MethodDelete, "/"+backendID, nil)
	req.Header.Set("Tus-Resumable", "1.0.0")

	h.handler.DelFile(rec, req)

	switch rec.Code {
	case http.StatusNoContent:
		return nil // Fully terminated (binary + .info both removed)
	case http.StatusNotFound:
		// The binary file was already moved or never existed.
		// Try to clean up any remaining .info sidecar.
		infoPath := filepath.Join(h.store.Path, backendID+".info")

		err := os.Remove(infoPath)
		if err != nil {
			if os.IsNotExist(err) {
				return ErrNotFound
			}

			log.Printf("tusd: failed to remove info sidecar %s: %v", infoPath, err)
		}

		return ErrNotFound
	default:
		return extractTusdError(rec.ResponseRecorder)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// normalizeError converts a tusd error to our package-level sentinel errors.
func normalizeError(err error) error {
	if err == nil {
		return nil
	}

	if errors.Is(err, handler.ErrNotFound) {
		return ErrNotFound
	}
	// Wrap non-not-found errors to prevent tusd types from leaking.
	return fmt.Errorf("tusd: %w", err)
}

// extractTusdError reads error information from a tusd response recorder.
// It returns the appropriate sentinel error for known status codes, or a
// generic error for unknown failures.
func extractTusdError(rec *httptest.ResponseRecorder) error {
	body := strings.TrimSpace(rec.Body.String())

	switch rec.Code {
	case http.StatusNotFound:
		return ErrNotFound
	case http.StatusConflict:
		// Mismatched offset or similar conflict.
		if body != "" {
			return fmt.Errorf("%w: %s", errTusdConflict, body)
		}

		return ErrInvalidOffset
	case http.StatusLocked:
		return ErrLocked
	case http.StatusNotImplemented:
		if body != "" {
			return fmt.Errorf("%w: %s", errTusdNotImplemented, body)
		}

		return ErrUploadRejected
	case http.StatusPreconditionFailed:
		if body != "" {
			return fmt.Errorf("%w: %s", errTusdVersionMismatch, body)
		}

		return errUnsupportedVersion
	}

	// Generic error with body text if available.
	if body != "" {
		return fmt.Errorf("%w: %s", errTusdGeneric, body)
	}

	return fmt.Errorf("%w: %d", errTusdHTTP, rec.Code)
}
