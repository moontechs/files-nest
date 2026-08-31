// Package api provides HTTP handlers, middleware, and shared utilities
// for the iCloud Backup server API.
package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/moontechs/files-nest/server/internal/filestore"
	"github.com/moontechs/files-nest/server/internal/store"
	"github.com/moontechs/files-nest/server/internal/uploadbackend"
)

const (
	// maxCreateBodyBytes caps the POST /uploads request body (256 KB is
	// generous for upload metadata).
	maxCreateBodyBytes = 256 << 10
	// maxStatusBodyBytes caps the PATCH /uploads/:id/status request body.
	maxStatusBodyBytes = 16 << 10
)

// Sentinel errors for request validation, wrapped at the call site to add
// per-request detail rather than constructing dynamic errors inline.
var (
	errDateRangeOrder   = errors.New("'to' must be after 'from'")
	errInvalidTimestamp = errors.New("invalid timestamp")
)

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

// Handler holds the dependencies needed by all HTTP handlers:
// the BadgerDB store, the embedded tusd upload backend, per-upload locks,
// and the storage path root for organizing completed files.
type Handler struct {
	store       uploadStore
	backend     uploadBackend
	locks       *UploadLocker
	storagePath string
	mover       *filestore.Mover
}

// uploadStore is the portion of the persistence layer used by Handler.
// Keeping the contract at its consumer allows handler tests to exercise
// otherwise unreachable cleanup-error paths without a live Badger failure.
type uploadStore interface {
	DeleteCompletionIntent(string) error
	GetCompletionIntent(string) (*store.CompletionIntent, error)
	GetUpload(string) (*store.Upload, error)
	ListByDateRange(time.Time, time.Time, store.Status, int, string) ([]*store.Upload, string, error)
	PutUploadIfAbsent(*store.Upload) (*store.Upload, bool, error)
	ReRegister(string, string) (*store.Upload, error)
	SaveCompletionIntent(*store.CompletionIntent) error
	UpdateComplete(string, string) (*store.Upload, error)
	UpdateStatus(string, store.Status) (*store.Upload, error)
}

// uploadBackend is the portion of the tusd backend used by Handler.
type uploadBackend interface {
	CreateUpload(context.Context, string) (string, error)
	FilePath(context.Context, string) (string, error)
	ForwardPatch(context.Context, string, io.Reader, int64, string) (int64, error)
	GetInfo(context.Context, string) (*uploadbackend.UploadInfo, error)
	GetOffset(context.Context, string) (int64, error)
	IsComplete(context.Context, string) (bool, error)
	TerminateOrCleanup(context.Context, string) error
}

var (
	_ uploadStore   = (*store.Store)(nil)
	_ uploadBackend = (*uploadbackend.TUSHandler)(nil)
)

// NewHandler creates a Handler wired to the given store, backend, and
// storage path. The UploadLocker is initialized as a zero value (ready
// to use).
func NewHandler(st *store.Store, bk *uploadbackend.TUSHandler, storagePath string) *Handler {
	return &Handler{
		store:       st,
		backend:     bk,
		locks:       &UploadLocker{},
		storagePath: storagePath,
		mover:       filestore.New(storagePath),
	}
}

// ---------------------------------------------------------------------------
// Request / Response types
// ---------------------------------------------------------------------------

// createUploadRequest is the JSON body accepted by POST /uploads.
// Both camelCase and snake_case field names are accepted for the optional
// fields. Required fields are always expected — the handler returns 400
// if any are missing.
type createUploadRequest struct {
	LocalIdentifier  string          `json:"local_identifier"`
	LocalIdentifierC string          `json:"localIdentifier"`
	Filename         string          `json:"filename"`
	CreationDate     string          `json:"creation_date"`
	CreationDateC    string          `json:"creationDate"`
	BundleID         string          `json:"bundle_id"`
	BundleIDC        string          `json:"bundleId"`
	Metadata         json.RawMessage `json:"metadata"`
}

// normalize returns the canonical values from a createUploadRequest,
// preferring snake_case over camelCase for each field.
func (r *createUploadRequest) normalize() createUploadRequest {
	loc := r.LocalIdentifier
	if loc == "" {
		loc = r.LocalIdentifierC
	}

	date := r.CreationDate
	if date == "" {
		date = r.CreationDateC
	}

	bundle := r.BundleID
	if bundle == "" {
		bundle = r.BundleIDC
	}

	return createUploadRequest{
		LocalIdentifier: loc,
		Filename:        r.Filename,
		CreationDate:    date,
		BundleID:        bundle,
		Metadata:        r.Metadata,
	}
}

// CreateUploadResponse is the JSON body returned by a successful
// POST /uploads.
type CreateUploadResponse struct {
	ID              string `json:"id"`
	LocalIdentifier string `json:"local_identifier"`
	Status          string `json:"status"`
	BackendID       string `json:"backend_id"`
	UploadURL       string `json:"upload_url"`
}

// ListUploadsResponse is the JSON envelope returned by GET /uploads.
type ListUploadsResponse struct {
	Items      []*store.Upload `json:"items"`
	NextCursor string          `json:"next_cursor"`
}

// errorResponse is a standard JSON error body.
type errorResponse struct {
	Error string `json:"error"`
}

// ---------------------------------------------------------------------------
// writeJSON helper
// ---------------------------------------------------------------------------

// writeJSON marshals v as JSON and writes it to w with the given status code.
// If marshalling fails it logs the error and returns 500 internally — the
// caller does not need to handle marshal errors.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	err := json.NewEncoder(w).Encode(v)
	if err != nil {
		log.Printf("ERROR writeJSON encode error: %v", err)
	}
}

// writeError writes a JSON error response with the given status code and
// message. This is the canonical way to return non-200 responses from
// API handlers.
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, errorResponse{Error: message})
}

// ---------------------------------------------------------------------------
// POST /uploads
// ---------------------------------------------------------------------------

// HandleCreateUpload handles POST /uploads. It parses the request body,
// validates required fields, creates a tusd upload, and persists the upload
// record in BadgerDB. If a record already exists for the same
// localIdentifier, the tusd upload is terminated and the existing record
// is returned.
func (h *Handler) HandleCreateUpload(w http.ResponseWriter, r *http.Request) {
	req, status, msg := decodeCreateUploadRequest(w, r)
	if status != 0 {
		writeError(w, status, msg)

		return
	}

	// Sanitize the filename.
	safeFilename := SanitizeFilename(req.Filename)
	if safeFilename == "" {
		writeError(w, http.StatusBadRequest, "invalid filename")

		return
	}

	// Derive the deterministic safe server ID.
	id := SafeID(req.LocalIdentifier)

	// Serialize concurrent create/re-register attempts for the same upload.
	// Without this, two concurrent POST /uploads for the same localIdentifier
	// could both create tusd uploads and race on the re-register path below.
	h.locks.Lock(id)
	defer h.locks.Unlock(id)

	// Build the Upload-Metadata header value for tusd.
	tusdMeta := "filename " + base64.StdEncoding.EncodeToString([]byte(safeFilename))

	// Create the tusd upload.
	backendID, err := h.backend.CreateUpload(r.Context(), tusdMeta)
	if err != nil {
		log.Printf("ERROR tusd CreateUpload failed for %s: %v", req.LocalIdentifier, err)
		writeError(w, http.StatusInternalServerError, "failed to create upload")

		return
	}

	// Build the upload record.
	now := time.Now().UTC().Format(time.RFC3339)
	upload := &store.Upload{
		ID:              id,
		LocalIdentifier: req.LocalIdentifier,
		Status:          store.StatusUploading,
		BackendID:       backendID,
		Filename:        safeFilename,
		BundleID:        req.BundleID,
		CreationDate:    req.CreationDate,
		CreatedAt:       now,
		UpdatedAt:       now,
		Metadata:        req.Metadata,
	}

	// Attempt to persist the record. If an existing record already exists
	// for this localIdentifier, branch on its status.
	existing, created, err := h.store.PutUploadIfAbsent(upload)
	if err != nil {
		// Unknown DB error — terminate the tusd upload before returning.
		log.Printf("ERROR PutUploadIfAbsent failed for %s: %v", req.LocalIdentifier, err)

		termErr := h.backend.TerminateOrCleanup(r.Context(), backendID)
		if termErr != nil {
			log.Printf("ERROR failed to terminate tusd upload %s after DB error: %v", backendID, termErr)
		}

		writeError(w, http.StatusInternalServerError, "failed to save upload")

		return
	}

	if !created {
		h.finalizeExistingUpload(w, r, existing, backendID)

		return
	}

	// Return the newly-created record.
	writeJSON(w, http.StatusCreated, uploadToResponse(upload))
}

// decodeCreateUploadRequest parses and validates the POST /uploads request
// body. On success it returns the normalized request with status 0. On
// failure it returns the HTTP status code and message for the caller to write.
func decodeCreateUploadRequest(w http.ResponseWriter, r *http.Request) (createUploadRequest, int, string) {
	// Limit body size to prevent abuse (256 KB is generous for metadata).
	r.Body = http.MaxBytesReader(w, r.Body, maxCreateBodyBytes)

	var req createUploadRequest

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	err := dec.Decode(&req)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			return req, http.StatusRequestEntityTooLarge, "request body too large"
		}

		return req, http.StatusBadRequest, "invalid request body: " + err.Error()
	}

	// Reject trailing data after the JSON value (prevents confusing
	// "{"local_identifier":"x","filename":"y","creation_date":"z"}extra"
	// from being silently accepted).
	var trailing json.RawMessage

	err = dec.Decode(&trailing)
	if !errors.Is(err, io.EOF) {
		return req, http.StatusBadRequest, "request body contains trailing data"
	}

	req = req.normalize()

	// Validate required fields.
	if req.LocalIdentifier == "" {
		return req, http.StatusBadRequest, "local_identifier is required"
	}

	if req.Filename == "" {
		return req, http.StatusBadRequest, "filename is required"
	}

	if req.CreationDate == "" {
		return req, http.StatusBadRequest, "creation_date is required"
	}

	// Validate the creation_date format. This is the primary defense against
	// path traversal via the organized-tree path builder: an untrusted client
	// could otherwise submit a crafted date (e.g. "../../tmp") that lands the
	// completed file outside the storage root. Only RFC3339 / RFC3339Nano /
	// YYYY-MM-DD values are accepted; everything else is rejected with 400.
	// SafePathSegment in filestore is kept as defense-in-depth for any record
	// that reaches the path builder through a non-API path.
	_, err = parseRFC3339(req.CreationDate)
	if err != nil {
		return req, http.StatusBadRequest, "creation_date must be an RFC3339 timestamp (e.g. 2024-03-15T10:30:00Z)"
	}

	return req, 0, ""
}

// StoragePath returns the storage root path used by the handler for
// organizing completed files. Exported for test access.
func (h *Handler) StoragePath() string {
	return h.storagePath
}

// uploadToResponse converts a store.Upload to a CreateUploadResponse for
// the POST /uploads endpoint.
func uploadToResponse(upload *store.Upload) CreateUploadResponse {
	return CreateUploadResponse{
		ID:              upload.ID,
		LocalIdentifier: upload.LocalIdentifier,
		Status:          string(upload.Status),
		BackendID:       upload.BackendID,
		UploadURL:       "/uploads/" + upload.ID + "/data",
	}
}

// existingToResponse is identical to uploadToResponse but used when
// returning an existing (conflicting) record so the client can branch
// on the current status.
func existingToResponse(upload *store.Upload) CreateUploadResponse {
	return uploadToResponse(upload)
}

// ---------------------------------------------------------------------------
// GET /uploads
// ---------------------------------------------------------------------------

// HandleListUploads handles GET /uploads. It accepts query parameters:
//   - from:   RFC3339 start date (inclusive), defaults to "1970-01-01"
//   - to:     RFC3339 end date (inclusive), defaults to "2999-12-31"
//   - status: optional status filter (uploading, complete, etc.)
//   - limit:  max results per page, defaults to 500, clamped to [1, 1000]
//   - cursor: base64-encoded pagination cursor from a previous response
//
// Returns a JSON object with "items" (array of Upload) and "next_cursor"
// (empty string when all results have been returned).
func (h *Handler) HandleListUploads(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	// Parse date range.
	from, toTime, err := parseDateRange(query.Get("from"), query.Get("to"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())

		return
	}

	status := store.Status(query.Get("status"))

	limit, _ := strconv.Atoi(query.Get("limit"))
	cursor := query.Get("cursor")

	uploads, nextCursor, err := h.store.ListByDateRange(from, toTime, status, limit, cursor)
	if err != nil {
		if errors.Is(err, store.ErrInvalidCursor) {
			writeError(w, http.StatusBadRequest, "invalid cursor")

			return
		}

		log.Printf("ERROR ListByDateRange failed: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to list uploads")

		return
	}

	// Return empty array (not null) when there are no results.
	items := uploads
	if items == nil {
		items = []*store.Upload{}
	}

	writeJSON(w, http.StatusOK, ListUploadsResponse{
		Items:      items,
		NextCursor: nextCursor,
	})
}

// parseDateRange parses the optional from/to query parameters as RFC3339
// timestamps. If either is empty, a sensible default is used (far past
// for from, far future for to).
func parseDateRange(fromStr, toStr string) (time.Time, time.Time, error) {
	var (
		from time.Time
		err  error
	)

	if fromStr != "" {
		from, err = parseRFC3339(fromStr)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
	} else {
		from = time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)
	}

	var toTime time.Time
	if toStr != "" {
		toTime, err = parseRFC3339(toStr)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
	} else {
		toTime = time.Date(2999, 12, 31, 23, 59, 59, 0, time.UTC)
	}

	if toTime.Before(from) {
		return time.Time{}, time.Time{}, errDateRangeOrder
	}

	return from, toTime, nil
}

// parseRFC3339 parses a timestamp string in RFC3339 or RFC3339Nano format.
func parseRFC3339(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339, value)
	if err == nil {
		return parsed, nil
	}

	parsed, err = time.Parse(time.RFC3339Nano, value)
	if err == nil {
		return parsed, nil
	}
	// Also accept a date-only format (YYYY-MM-DD) for convenience.
	parsed, err = time.Parse("2006-01-02", value)
	if err == nil {
		return parsed, nil
	}

	return time.Time{}, fmt.Errorf(
		"%w: %s (expected RFC3339 format, e.g. 2024-03-15T10:30:00Z)", errInvalidTimestamp, value)
}

// ---------------------------------------------------------------------------
// GET /uploads/:id
// ---------------------------------------------------------------------------

// HandleGetUpload handles GET /uploads/:id. It returns the full upload
// record as JSON, or 404 if the record does not exist.
func (h *Handler) HandleGetUpload(w http.ResponseWriter, r *http.Request) {
	id := extractID(r)
	// The id is validated below to a fixed-length base64url string (no
	// control characters), but strip newlines first as cheap defense-in-depth
	// against log injection before the value reaches any log call.
	id = strings.ReplaceAll(id, "\n", "")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing upload id")

		return
	}

	// Validate the safe ID format before attempting a DB lookup.
	err := ValidateSafeID(id)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid upload id: "+err.Error())

		return
	}

	upload, err := h.store.GetUpload(id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "upload not found")

			return
		}

		log.Printf("ERROR GetUpload failed for %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, "failed to get upload")

		return
	}

	writeJSON(w, http.StatusOK, upload)
}

// ---------------------------------------------------------------------------
// HEAD /uploads/:id/data
// ---------------------------------------------------------------------------

// HandleHeadUploadData handles HEAD /uploads/:id/data. It looks up the upload
// record by its safe ID, fetches the current upload offset from the tusd
// backend, and returns TUS protocol headers (Upload-Offset, Tus-Resumable).
//
// If the backend reports the upload as not found (ErrNotFound), the upload
// record is updated to backend_lost and a 409 Conflict is returned.
func (h *Handler) HandleHeadUploadData(w http.ResponseWriter, r *http.Request) {
	id := extractID(r)
	// The id is validated below to a fixed-length base64url string (no
	// control characters), but strip newlines first as cheap defense-in-depth
	// against log injection before the value reaches any log call.
	id = strings.ReplaceAll(id, "\n", "")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing upload id")

		return
	}

	err := ValidateSafeID(id)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid upload id: "+err.Error())

		return
	}

	// Acquire the per-upload lock so HEAD cannot race with a concurrent
	// PATCH /status (completion) or DELETE. Without this, a completion that
	// terminates the tusd backend between HEAD's status read and HEAD's
	// GetOffset call would cause HEAD to observe ErrNotFound and flip an
	// already-complete record to backend_lost.
	h.locks.Lock(id)
	defer h.locks.Unlock(id)

	upload, err := h.store.GetUpload(id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "upload not found")

			return
		}

		log.Printf("ERROR GetUpload failed for %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, "failed to get upload")

		return
	}

	// Guard against non-uploading records before touching the tusd backend.
	// A completed upload has already had its tusd backend terminated, so a
	// GetOffset call here would return ErrNotFound and incorrectly flip the
	// record to backend_lost (corrupting a successful completion). A deleted
	// record is treated as not found. backend_lost / completing records are
	// not valid HEAD targets either.
	switch upload.Status {
	case store.StatusUploading:
		// OK — proceed to fetch the offset.
	case store.StatusDeleted:
		writeError(w, http.StatusNotFound, "upload not found")

		return
	case store.StatusComplete:
		writeError(w, http.StatusConflict, "upload already completed")

		return
	case store.StatusBackendLost:
		writeError(w, http.StatusConflict, "backend_lost")

		return
	case store.StatusCompleting:
		writeError(w, http.StatusConflict, "upload not in uploading state")

		return
	default:
		writeError(w, http.StatusConflict, "upload not in uploading state")

		return
	}

	info, err := h.backend.GetInfo(r.Context(), upload.BackendID)
	if err != nil {
		if errors.Is(err, uploadbackend.ErrNotFound) {
			h.handleBackendLost(w, r, upload)

			return
		}

		log.Printf("ERROR GetInfo failed for backend %s: %v", upload.BackendID, err)
		writeError(w, http.StatusInternalServerError, "failed to get upload info")

		return
	}

	w.Header().Set("Upload-Offset", strconv.FormatInt(info.Offset, 10))

	if info.SizeIsDeferred {
		w.Header().Set("Upload-Defer-Length", "1")
	} else {
		w.Header().Set("Upload-Length", strconv.FormatInt(info.Size, 10))
	}

	w.Header().Set("Tus-Resumable", "1.0.0")
	w.WriteHeader(http.StatusOK)
}

// ---------------------------------------------------------------------------
// PATCH /uploads/:id/data
// ---------------------------------------------------------------------------

// HandlePatchUploadData handles PATCH /uploads/:id/data. It forwards the
// request body and standard TUS headers (Upload-Offset, Upload-Length,
// Content-Type) to the embedded tusd backend for data transfer.
//
// The per-upload lock is acquired to prevent concurrent PATCH /data,
// PATCH /status, and DELETE operations on the same upload.
//
// If the upload record shows a completed or deleted status, a 409 Conflict
// is returned. If the backend reports the upload as not found, the record
// is updated to backend_lost and a 409 Conflict is returned.
func (h *Handler) HandlePatchUploadData(w http.ResponseWriter, r *http.Request) {
	id := extractID(r)
	// The id is validated below to a fixed-length base64url string (no
	// control characters), but strip newlines first as cheap defense-in-depth
	// against log injection before the value reaches any log call.
	id = strings.ReplaceAll(id, "\n", "")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing upload id")

		return
	}

	err := ValidateSafeID(id)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid upload id: "+err.Error())

		return
	}

	h.locks.Lock(id)
	defer h.locks.Unlock(id)

	upload, err := h.store.GetUpload(id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "upload not found")

			return
		}

		log.Printf("ERROR GetUpload failed for %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, "failed to get upload")

		return
	}

	// Reject operations on non-uploading records.
	if rejectUploadNotUploading(w, upload.Status) {
		return
	}

	// Validate Content-Type per the TUS protocol.
	if r.Header.Get("Content-Type") != "application/offset+octet-stream" {
		writeError(w, http.StatusBadRequest, "Content-Type must be application/offset+octet-stream")

		return
	}

	// Parse the Upload-Offset header from the request.
	offsetStr := r.Header.Get("Upload-Offset")
	if offsetStr == "" {
		writeError(w, http.StatusBadRequest, "Upload-Offset header is required")

		return
	}

	requestOffset, err := strconv.ParseInt(offsetStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid Upload-Offset: "+err.Error())

		return
	}

	h.forwardUploadData(w, r, upload, requestOffset)
}

// ---------------------------------------------------------------------------
// PATCH /uploads/:id/status
// ---------------------------------------------------------------------------

// HandlePatchUploadStatus handles PATCH /uploads/:id/status. It only accepts
// {"status": "complete"} to mark an upload as complete. Before marking
// complete, it verifies the tusd backend reports the upload as fully uploaded
// (known length, offset == length), then moves the file from the incoming
// directory to the organized tree, updates the DB record, and cleans up
// the tusd sidecar.
func (h *Handler) HandlePatchUploadStatus(w http.ResponseWriter, r *http.Request) {
	id := extractID(r)
	// The id is validated below to a fixed-length base64url string (no
	// control characters), but strip newlines first as cheap defense-in-depth
	// against log injection before the value reaches any log call.
	id = strings.ReplaceAll(id, "\n", "")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing upload id")

		return
	}

	err := ValidateSafeID(id)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid upload id: "+err.Error())

		return
	}

	h.locks.Lock(id)
	defer h.locks.Unlock(id)

	upload, err := h.store.GetUpload(id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "upload not found")

			return
		}

		log.Printf("ERROR GetUpload failed for %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, "failed to get upload")

		return
	}

	// Only allow completion from the uploading state.
	if rejectUploadCompletionStatus(w, upload.Status) {
		return
	}

	// Parse the request body: only {"status": "complete"} is accepted.
	if !decodeStatusRequest(w, r) {
		return
	}

	// Verify the tusd backend reports the upload as fully uploaded.
	complete, err := h.backend.IsComplete(r.Context(), upload.BackendID)
	if err != nil {
		if errors.Is(err, uploadbackend.ErrNotFound) {
			h.handleBackendLost(w, r, upload)

			return
		}

		log.Printf("ERROR IsComplete failed for backend %s: %v", upload.BackendID, err)
		writeError(w, http.StatusInternalServerError, "failed to check upload completion")

		return
	}

	if !complete {
		writeError(w, http.StatusConflict, "upload_incomplete")

		return
	}

	// Get the source file path from the tusd backend.
	srcPath, err := h.backend.FilePath(r.Context(), upload.BackendID)
	if err != nil {
		if errors.Is(err, uploadbackend.ErrNotFound) {
			h.handleBackendLost(w, r, upload)

			return
		}

		log.Printf("ERROR FilePath failed for backend %s: %v", upload.BackendID, err)
		writeError(w, http.StatusInternalServerError, "failed to get file path")

		return
	}

	// Compose the destination organized path and move the file (see
	// moveCompletedFile for the TOCTOU and retry-safety rationale).
	plan, ok := h.moveCompletedFile(w, r, upload, srcPath)
	if !ok {
		return
	}

	h.finalizeCompletion(w, r, upload, plan.Rel)
}

// ---------------------------------------------------------------------------
// DELETE /uploads/:id
// ---------------------------------------------------------------------------

// HandleDeleteUpload handles DELETE /uploads/:id. It terminates the upload
// in the tusd backend (ignoring ErrNotFound since the backend may already
// be gone), removes the organized file from disk (if the upload was
// completed), and then updates the record status to deleted.
//
// File-removal errors are logged but do not abort the operation — the DB
// record is still marked deleted even if file cleanup fails.
func (h *Handler) HandleDeleteUpload(w http.ResponseWriter, r *http.Request) {
	id := extractID(r)
	// The id is validated below to a fixed-length base64url string (no
	// control characters), but strip newlines first as cheap defense-in-depth
	// against log injection before the value reaches any log call.
	id = strings.ReplaceAll(id, "\n", "")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing upload id")

		return
	}

	err := ValidateSafeID(id)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid upload id: "+err.Error())

		return
	}

	h.locks.Lock(id)
	defer h.locks.Unlock(id)

	upload, err := h.store.GetUpload(id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "upload not found")

			return
		}

		log.Printf("ERROR GetUpload failed for %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, "failed to get upload")

		return
	}

	// If already deleted, return success immediately.
	if upload.Status == store.StatusDeleted {
		w.WriteHeader(http.StatusNoContent)

		return
	}

	// Terminate the tusd backend upload. Ignore ErrNotFound — the backend
	// may already be gone (e.g. after a completed upload was moved or a
	// previous partial cleanup).
	termErr := h.backend.TerminateOrCleanup(r.Context(), upload.BackendID)
	if termErr != nil && !errors.Is(termErr, uploadbackend.ErrNotFound) {
		log.Printf("ERROR failed to terminate tusd upload %s during delete: %v", upload.BackendID, termErr)
	}

	// Remove the organized file from disk if the upload was completed.
	// Errors are logged but do not abort the DELETE — the DB record
	// is still marked deleted even if file cleanup fails.
	if upload.OrganizedPath != "" {
		removeErr := h.mover.RemoveOrganizedFile(upload.OrganizedPath)
		if removeErr != nil {
			log.Printf("ERROR failed to remove organized file %s for upload %s: %v", upload.OrganizedPath, upload.ID, removeErr)
		}
	}

	// Update the DB record status to deleted.
	_, err = h.store.UpdateStatus(upload.ID, store.StatusDeleted)
	if err != nil {
		log.Printf("ERROR failed to update status to deleted for %s: %v", upload.ID, err)
		writeError(w, http.StatusInternalServerError, "failed to delete upload")

		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// handleBackendLost updates the upload record status to backend_lost and
// writes a 409 Conflict response. It is called when the tusd backend
// reports that an upload no longer exists (ErrNotFound).
//
// It only transitions the record to backend_lost from a non-terminal state
// (uploading or completing). Terminal states (complete, deleted, or already
// backend_lost) are left untouched: HEAD /data does not acquire the per-upload
// lock and does not reject terminal statuses before contacting the backend,
// so without this guard a late HEAD on an upload that has since been completed
// (and whose tusd backend was cleaned up) would clobber the complete status
// and corrupt the record. Re-reading the current record avoids acting on the
// potentially stale snapshot held by the caller.
func (h *Handler) handleBackendLost(w http.ResponseWriter, _ *http.Request, upload *store.Upload) {
	current, err := h.store.GetUpload(upload.ID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			log.Printf("ERROR failed to re-read upload %s before backend_lost: %v", upload.ID, err)
		}
	} else {
		switch current.Status {
		case store.StatusUploading, store.StatusCompleting:
			_, err = h.store.UpdateStatus(upload.ID, store.StatusBackendLost)
			if err != nil {
				log.Printf("ERROR failed to set backend_lost for %s: %v", upload.ID, err)
			}
		case store.StatusComplete, store.StatusDeleted, store.StatusBackendLost:
			// Preserve terminal state.
		default:
			// Unknown status: preserve current state.
		}
	}

	writeError(w, http.StatusConflict, "backend_lost")
}

// finalizeExistingUpload handles the idempotent path when POST /uploads finds
// an existing record for the same local identifier. If the existing record's
// backend is gone (backend_lost) or deleted, the newly-created tusd backend
// is re-registered on the existing record. Otherwise the existing record is
// returned and the redundant tusd upload is cleaned up.
func (h *Handler) finalizeExistingUpload(
	w http.ResponseWriter, r *http.Request, existing *store.Upload, backendID string,
) {
	switch existing.Status {
	case store.StatusBackendLost, store.StatusDeleted:
		reReg, err := h.store.ReRegister(existing.ID, backendID)
		if err != nil {
			log.Printf("ERROR ReRegister failed for %s: %v", existing.ID, err)

			termErr := h.backend.TerminateOrCleanup(r.Context(), backendID)
			if termErr != nil {
				log.Printf("ERROR failed to terminate tusd upload %s after re-register failure: %v", backendID, termErr)
			}

			writeError(w, http.StatusInternalServerError, "failed to re-register upload")

			return
		}

		writeJSON(w, http.StatusCreated, uploadToResponse(reReg))

		return
	case store.StatusUploading, store.StatusCompleting, store.StatusComplete:
		// In progress or complete: return the existing record idempotently.
	default:
		// Unknown status: treat like an in-progress upload.
	}

	termErr := h.backend.TerminateOrCleanup(r.Context(), backendID)
	if termErr != nil {
		log.Printf("ERROR failed to terminate redundant tusd upload %s: %v", backendID, termErr)
	}

	writeJSON(w, http.StatusOK, existingToResponse(existing))
}

// moveCompletedFile moves a fully-uploaded file from the tusd incoming dir to
// the organized tree and returns the final organized-path plan.
//
// PlanDestination always appends a stable _<upload.ID> suffix, so two distinct
// live records can never compute the same destination path — each record's ID
// is unique, and the store layer already deduplicates records by local
// identifier (PutUploadIfAbsent) before a second record could exist. The plan
// + intent + rename sequence still runs under the Mover's organized-tree mutex
// so the safety-net stat in PlanDestination and the rename stay atomic with
// respect to other concurrent completion and recovery moves touching the same
// tree.
//
// Retry safety: if a previous completion attempt for this upload already
// persisted a completion intent, reuse its destination paths verbatim. The
// destination the planner computes is now fully deterministic — the
// unconditional _<upload.ID> suffix depends on no disk state and no backend_id
// (the tusd backend ID, which changes across backend_lost re-registration) —
// so recomputing it on a retry always reproduces the same path. Intent reuse
// is therefore kept as a deliberate simplicity/consistency choice, avoiding a
// redundant re-plan (and its safety-net stat) on every retry, rather than as
// a correctness requirement for a specific failure mode; it also lets
// MoveFile's idempotency (src missing + dst present → success) take effect
// unchanged when a prior attempt moved the file but failed before the DB
// update.
//
// The completion intent is persisted (via saveIntent) BEFORE the rename, still
// under the mutex, so a crash between the intent write and the move remains
// recoverable on the next startup.
//
// The second return value is false when an error response has already been
// written to w and the caller should stop.
func (h *Handler) moveCompletedFile(
	w http.ResponseWriter, _ *http.Request, upload *store.Upload, srcPath string,
) (filestore.PlanDestResult, bool) {
	saveIntent := func(plan filestore.PlanDestResult) error {
		intent := &store.CompletionIntent{
			ID:           upload.ID,
			BackendID:    upload.BackendID,
			Src:          srcPath,
			Dst:          plan.Abs,
			DstRel:       plan.Rel,
			CreationDate: plan.DateUsed,
			CreatedAt:    time.Now().UTC().Format(time.RFC3339),
		}

		return h.store.SaveCompletionIntent(intent)
	}

	var plan filestore.PlanDestResult

	existing, err := h.store.GetCompletionIntent(upload.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		log.Printf("ERROR failed to read existing completion intent for %s: %v", upload.ID, err)
		writeError(w, http.StatusInternalServerError, "failed to prepare completion")

		return plan, false
	}

	if existing != nil {
		existingPlan := filestore.PlanDestResult{Abs: existing.Dst, Rel: existing.DstRel, DateUsed: existing.CreationDate}

		moveErr := h.mover.MoveToPlaned(srcPath, existingPlan, saveIntent)
		if moveErr != nil {
			log.Printf("ERROR completion move failed for %s: %v", upload.ID, moveErr)
			writeError(w, http.StatusInternalServerError, "failed to move file")

			return plan, false
		}

		return existingPlan, true
	}

	planned, err := h.mover.PlanAndMove(
		srcPath, upload.CreationDate, upload.CreatedAt, upload.Filename, upload.ID, saveIntent)
	if err != nil {
		log.Printf("ERROR completion move failed for %s: %v", upload.ID, err)
		writeError(w, http.StatusInternalServerError, "failed to move file")

		return plan, false
	}

	return planned, true
}

// finalizeCompletion marks the upload complete in the DB with its organized
// path, deletes the now-redundant completion intent, and best-effort cleans
// up the tusd sidecar before sending 204 No Content.
func (h *Handler) finalizeCompletion(w http.ResponseWriter, r *http.Request, upload *store.Upload, relPath string) {
	_, err := h.store.UpdateComplete(upload.ID, relPath)
	if err != nil {
		log.Printf("ERROR failed to update status to complete for %s: %v", upload.ID, err)
		writeError(w, http.StatusInternalServerError, "failed to complete upload")

		return
	}

	// Delete the completion intent now that the DB is consistent.
	delErr := h.store.DeleteCompletionIntent(upload.ID)
	if delErr != nil {
		log.Printf("ERROR failed to delete completion intent for %s: %v", upload.ID, delErr)
	}

	// Best-effort cleanup of the tusd sidecar (.info file).
	termErr := h.backend.TerminateOrCleanup(r.Context(), upload.BackendID)
	if termErr != nil && !errors.Is(termErr, uploadbackend.ErrNotFound) {
		log.Printf("ERROR failed to terminate tusd upload %s after completion: %v", upload.BackendID, termErr)
	}

	w.WriteHeader(http.StatusNoContent)
}

// forwardUploadData verifies the client's offset matches the backend's current
// offset and forwards the PATCH body to the tusd backend, writing the TUS
// headers and 204 No Content on success.
func (h *Handler) forwardUploadData(
	w http.ResponseWriter, r *http.Request, upload *store.Upload, requestOffset int64,
) {
	// Get the current offset from the backend to verify it matches.
	currentOffset, err := h.backend.GetOffset(r.Context(), upload.BackendID)
	if err != nil {
		if errors.Is(err, uploadbackend.ErrNotFound) {
			h.handleBackendLost(w, r, upload)

			return
		}

		log.Printf("ERROR GetOffset failed for backend %s: %v", upload.BackendID, err)
		writeError(w, http.StatusInternalServerError, "failed to get upload offset")

		return
	}

	if requestOffset != currentOffset {
		writeError(w, http.StatusConflict,
			fmt.Sprintf("offset mismatch: client=%d, server=%d", requestOffset, currentOffset))

		return
	}

	// Forward Upload-Length if present (declares final size for deferred-length uploads).
	uploadLength := r.Header.Get("Upload-Length")

	// Forward the PATCH to the embedded tusd backend.
	newOffset, err := h.backend.ForwardPatch(r.Context(), upload.BackendID, r.Body, currentOffset, uploadLength)
	if err != nil {
		if errors.Is(err, uploadbackend.ErrNotFound) {
			h.handleBackendLost(w, r, upload)

			return
		}

		if errors.Is(err, uploadbackend.ErrInvalidOffset) {
			writeError(w, http.StatusConflict, "offset mismatch")

			return
		}

		log.Printf("ERROR ForwardPatch failed for backend %s: %v", upload.BackendID, err)
		writeError(w, http.StatusInternalServerError, "failed to write upload data")

		return
	}

	w.Header().Set("Upload-Offset", strconv.FormatInt(newOffset, 10))
	w.Header().Set("Tus-Resumable", "1.0.0")
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// extractID extracts the ":id" path parameter from the request.
// The router is configured with Go 1.22+ ServeMux patterns (e.g.
// /uploads/{id}/data), so the ID is always available via r.PathValue.
func extractID(r *http.Request) string {
	return r.PathValue("id")
}

// rejectUploadNotUploading writes a 409 Conflict and returns true when the
// upload status is not StatusUploading (the only state that may receive data).
func rejectUploadNotUploading(w http.ResponseWriter, status store.Status) bool {
	if status == store.StatusUploading {
		return false
	}

	if status == store.StatusComplete || status == store.StatusDeleted {
		writeError(w, http.StatusConflict, "upload already completed or deleted")

		return true
	}

	writeError(w, http.StatusConflict, "upload not in uploading state")

	return true
}

// rejectUploadCompletionStatus writes a 409 Conflict and returns true when the
// upload status is not StatusUploading (the only state from which completion
// is allowed).
func rejectUploadCompletionStatus(w http.ResponseWriter, status store.Status) bool {
	switch status {
	case store.StatusUploading:
		return false
	case store.StatusComplete:
		writeError(w, http.StatusConflict, "upload already completed")
	case store.StatusDeleted:
		writeError(w, http.StatusConflict, "upload already deleted")
	case store.StatusBackendLost:
		writeError(w, http.StatusConflict, "backend_lost")
	case store.StatusCompleting:
		writeError(w, http.StatusConflict, "upload not in uploading state")
	default:
		writeError(w, http.StatusConflict, "upload not in uploading state")
	}

	return true
}

// decodeStatusRequest parses the PATCH /uploads/:id/status request body,
// accepting only {"status": "complete"}. It writes the error response and
// returns false on any validation failure.
func decodeStatusRequest(w http.ResponseWriter, r *http.Request) bool {
	// Limit body size to prevent abuse (16 KB is generous for a single-field
	// JSON body). An unbounded body stream could be used as a DoS vector.
	r.Body = http.MaxBytesReader(w, r.Body, maxStatusBodyBytes)

	var req struct {
		Status string `json:"status"`
	}

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	err := dec.Decode(&req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")

		return false
	}

	// Reject trailing data after the JSON value.
	var trailing json.RawMessage

	err = dec.Decode(&trailing)
	if !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "request body contains trailing data")

		return false
	}

	if req.Status != "complete" {
		writeError(w, http.StatusBadRequest, "status must be 'complete'")

		return false
	}

	return true
}
