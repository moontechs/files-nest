// Package api provides HTTP handlers, middleware, and shared utilities
// for the iCloud Backup server API.
package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/moontechs/files-nest/server/internal/filestore"
	"github.com/moontechs/files-nest/server/internal/store"
	"github.com/moontechs/files-nest/server/internal/uploadbackend"
)

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

// Handler holds the dependencies needed by all HTTP handlers:
// the BadgerDB store, the embedded tusd upload backend, per-upload locks,
// and the storage path root for organizing completed files.
type Handler struct {
	store       *store.Store
	backend     *uploadbackend.TUSHandler
	locks       *UploadLocker
	storagePath string
	mover       *filestore.Mover
}

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
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("writeJSON encode error: %v", err)
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
	// Limit body size to prevent abuse (256 KB is generous for metadata).
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)

	var req createUploadRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	// Reject trailing data after the JSON value (prevents confusing
	// "{"local_identifier":"x","filename":"y","creation_date":"z"}extra"
	// from being silently accepted).
	var trailing json.RawMessage
	if err := dec.Decode(&trailing); err != io.EOF {
		writeError(w, http.StatusBadRequest, "request body contains trailing data")
		return
	}

	req = req.normalize()

	// Validate required fields.
	if req.LocalIdentifier == "" {
		writeError(w, http.StatusBadRequest, "local_identifier is required")
		return
	}
	if req.Filename == "" {
		writeError(w, http.StatusBadRequest, "filename is required")
		return
	}
	if req.CreationDate == "" {
		writeError(w, http.StatusBadRequest, "creation_date is required")
		return
	}
	// Validate the creation_date format. This is the primary defense against
	// path traversal via the organized-tree path builder: an untrusted client
	// could otherwise submit a crafted date (e.g. "../../tmp") that lands the
	// completed file outside the storage root. Only RFC3339 / RFC3339Nano /
	// YYYY-MM-DD values are accepted; everything else is rejected with 400.
	// SafePathSegment in filestore is kept as defense-in-depth for any record
	// that reaches the path builder through a non-API path.
	if _, err := parseRFC3339(req.CreationDate); err != nil {
		writeError(w, http.StatusBadRequest, "creation_date must be an RFC3339 timestamp (e.g. 2024-03-15T10:30:00Z)")
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
	backendID, err := h.backend.CreateUpload(tusdMeta)
	if err != nil {
		log.Printf("tusd CreateUpload failed for %s: %v", req.LocalIdentifier, err)
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
		log.Printf("PutUploadIfAbsent failed for %s: %v", req.LocalIdentifier, err)
		if termErr := h.backend.TerminateOrCleanup(backendID); termErr != nil {
			log.Printf("failed to terminate tusd upload %s after DB error: %v", backendID, termErr)
		}
		writeError(w, http.StatusInternalServerError, "failed to save upload")
		return
	}

	if !created {
		// A record already exists for this localIdentifier. If its backend
		// is gone (backend_lost) or it was deleted, the client needs a fresh
		// upload: re-register the newly-created tusd backend on the existing
		// record and reset its status to uploading. Otherwise the upload is
		// still in progress (or already complete) — return the existing record
		// idempotently and clean up the redundant tusd upload.
		switch existing.Status {
		case store.StatusBackendLost, store.StatusDeleted:
			reReg, err := h.store.ReRegister(existing.ID, backendID)
			if err != nil {
				log.Printf("ReRegister failed for %s: %v", existing.ID, err)
				if termErr := h.backend.TerminateOrCleanup(backendID); termErr != nil {
					log.Printf("failed to terminate tusd upload %s after re-register failure: %v", backendID, termErr)
				}
				writeError(w, http.StatusInternalServerError, "failed to re-register upload")
				return
			}
			writeJSON(w, http.StatusCreated, uploadToResponse(reReg))
			return
		default:
			if termErr := h.backend.TerminateOrCleanup(backendID); termErr != nil {
				log.Printf("failed to terminate redundant tusd upload %s: %v", backendID, termErr)
			}
			writeJSON(w, http.StatusOK, existingToResponse(existing))
			return
		}
	}

	// Return the newly-created record.
	writeJSON(w, http.StatusCreated, uploadToResponse(upload))
}

// StoragePath returns the storage root path used by the handler for
// organizing completed files. Exported for test access.
func (h *Handler) StoragePath() string {
	return h.storagePath
}

// uploadToResponse converts a store.Upload to a CreateUploadResponse for
// the POST /uploads endpoint.
func uploadToResponse(u *store.Upload) CreateUploadResponse {
	return CreateUploadResponse{
		ID:              u.ID,
		LocalIdentifier: u.LocalIdentifier,
		Status:          string(u.Status),
		BackendID:       u.BackendID,
		UploadURL:       "/uploads/" + u.ID + "/data",
	}
}

// existingToResponse is identical to uploadToResponse but used when
// returning an existing (conflicting) record so the client can branch
// on the current status.
func existingToResponse(u *store.Upload) CreateUploadResponse {
	return uploadToResponse(u)
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
	q := r.URL.Query()

	// Parse date range.
	from, to, err := parseDateRange(q.Get("from"), q.Get("to"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	status := store.Status(q.Get("status"))

	limit, _ := strconv.Atoi(q.Get("limit"))
	cursor := q.Get("cursor")

	uploads, nextCursor, err := h.store.ListByDateRange(from, to, status, limit, cursor)
	if err != nil {
		if errors.Is(err, store.ErrInvalidCursor) {
			writeError(w, http.StatusBadRequest, "invalid cursor")
			return
		}
		log.Printf("ListByDateRange failed: %v", err)
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
	var from time.Time
	var err error

	if fromStr != "" {
		from, err = parseRFC3339(fromStr)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
	} else {
		from = time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)
	}

	var to time.Time
	if toStr != "" {
		to, err = parseRFC3339(toStr)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
	} else {
		to = time.Date(2999, 12, 31, 23, 59, 59, 0, time.UTC)
	}

	if to.Before(from) {
		return time.Time{}, time.Time{}, errors.New("'to' must be after 'from'")
	}

	return from, to, nil
}

// parseRFC3339 parses a timestamp string in RFC3339 or RFC3339Nano format.
func parseRFC3339(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	// Also accept a date-only format (YYYY-MM-DD) for convenience.
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}
	return time.Time{}, errors.New("invalid timestamp: " + s + " (expected RFC3339 format, e.g. 2024-03-15T10:30:00Z)")
}

// ---------------------------------------------------------------------------
// GET /uploads/:id
// ---------------------------------------------------------------------------

// HandleGetUpload handles GET /uploads/:id. It returns the full upload
// record as JSON, or 404 if the record does not exist.
func (h *Handler) HandleGetUpload(w http.ResponseWriter, r *http.Request) {
	id := extractID(r)
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing upload id")
		return
	}

	// Validate the safe ID format before attempting a DB lookup.
	if err := ValidateSafeID(id); err != nil {
		writeError(w, http.StatusBadRequest, "invalid upload id: "+err.Error())
		return
	}

	upload, err := h.store.GetUpload(id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "upload not found")
			return
		}
		log.Printf("GetUpload failed for %s: %v", id, err)
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
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing upload id")
		return
	}

	if err := ValidateSafeID(id); err != nil {
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
		log.Printf("GetUpload failed for %s: %v", id, err)
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
	default:
		writeError(w, http.StatusConflict, "upload not in uploading state")
		return
	}

	info, err := h.backend.GetInfo(upload.BackendID)
	if err != nil {
		if errors.Is(err, uploadbackend.ErrNotFound) {
			h.handleBackendLost(w, r, upload)
			return
		}
		log.Printf("GetInfo failed for backend %s: %v", upload.BackendID, err)
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
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing upload id")
		return
	}

	if err := ValidateSafeID(id); err != nil {
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
		log.Printf("GetUpload failed for %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, "failed to get upload")
		return
	}

	// Reject operations on non-uploading records.
	switch upload.Status {
	case store.StatusUploading:
		// OK — proceed.
	case store.StatusComplete, store.StatusDeleted:
		writeError(w, http.StatusConflict, "upload already completed or deleted")
		return
	default:
		writeError(w, http.StatusConflict, "upload not in uploading state")
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

	// Get the current offset from the backend to verify it matches.
	currentOffset, err := h.backend.GetOffset(upload.BackendID)
	if err != nil {
		if errors.Is(err, uploadbackend.ErrNotFound) {
			h.handleBackendLost(w, r, upload)
			return
		}
		log.Printf("GetOffset failed for backend %s: %v", upload.BackendID, err)
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
	newOffset, err := h.backend.ForwardPatch(upload.BackendID, r.Body, currentOffset, uploadLength)
	if err != nil {
		if errors.Is(err, uploadbackend.ErrNotFound) {
			h.handleBackendLost(w, r, upload)
			return
		}
		if errors.Is(err, uploadbackend.ErrInvalidOffset) {
			writeError(w, http.StatusConflict, "offset mismatch")
			return
		}
		log.Printf("ForwardPatch failed for backend %s: %v", upload.BackendID, err)
		writeError(w, http.StatusInternalServerError, "failed to write upload data")
		return
	}

	w.Header().Set("Upload-Offset", strconv.FormatInt(newOffset, 10))
	w.Header().Set("Tus-Resumable", "1.0.0")
	w.WriteHeader(http.StatusNoContent)
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
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing upload id")
		return
	}

	if err := ValidateSafeID(id); err != nil {
		writeError(w, http.StatusBadRequest, "invalid upload id: "+err.Error())
		return
	}

	// Limit body size to prevent abuse (16 KB is generous for a single-field
	// JSON body). An unbounded body stream could be used as a DoS vector.
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)

	h.locks.Lock(id)
	defer h.locks.Unlock(id)

	upload, err := h.store.GetUpload(id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "upload not found")
			return
		}
		log.Printf("GetUpload failed for %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, "failed to get upload")
		return
	}

	// Only allow completion from the uploading state.
	if upload.Status != store.StatusUploading {
		switch upload.Status {
		case store.StatusComplete:
			writeError(w, http.StatusConflict, "upload already completed")
		case store.StatusDeleted:
			writeError(w, http.StatusConflict, "upload already deleted")
		case store.StatusBackendLost:
			writeError(w, http.StatusConflict, "backend_lost")
		default:
			writeError(w, http.StatusConflict, "upload not in uploading state")
		}
		return
	}

	// Parse the request body: only {"status": "complete"} is accepted.
	var req struct {
		Status string `json:"status"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Reject trailing data after the JSON value.
	var trailing json.RawMessage
	if err := dec.Decode(&trailing); err != io.EOF {
		writeError(w, http.StatusBadRequest, "request body contains trailing data")
		return
	}

	if req.Status != "complete" {
		writeError(w, http.StatusBadRequest, "status must be 'complete'")
		return
	}

	// Verify the tusd backend reports the upload as fully uploaded.
	complete, err := h.backend.IsComplete(upload.BackendID)
	if err != nil {
		if errors.Is(err, uploadbackend.ErrNotFound) {
			h.handleBackendLost(w, r, upload)
			return
		}
		log.Printf("IsComplete failed for backend %s: %v", upload.BackendID, err)
		writeError(w, http.StatusInternalServerError, "failed to check upload completion")
		return
	}
	if !complete {
		writeError(w, http.StatusConflict, "upload_incomplete")
		return
	}

	// Get the source file path from the tusd backend.
	srcPath, err := h.backend.FilePath(upload.BackendID)
	if err != nil {
		if errors.Is(err, uploadbackend.ErrNotFound) {
			h.handleBackendLost(w, r, upload)
			return
		}
		log.Printf("FilePath failed for backend %s: %v", upload.BackendID, err)
		writeError(w, http.StatusInternalServerError, "failed to get file path")
		return
	}

	// Compose the destination organized path and move the file.
	//
	// The collision check inside PlanDestination (os.Stat) and the rename
	// inside MoveFile must be atomic with respect to other concurrent
	// completions for a different upload that shares the same creation date
	// and filename (e.g. two libraries both exporting IMG_1234.jpg on the
	// same day). Without serialization each completion could see no file at
	// the computed path, compute identical destinations, and the second
	// rename would silently overwrite the first upload's data. The Mover's
	// organized-tree mutex (held across plan + intent + rename) closes that
	// TOCTOU window.
	//
	// Retry safety: if a previous completion attempt for this upload already
	// persisted a completion intent, reuse its destination paths verbatim. A
	// prior attempt may have succeeded in moving the file but failed before
	// the DB update; recomputing the destination now would see the already-
	// moved file as a collision and suffix it with the backend_id, producing
	// a NEW path that does not match where the file actually lives — orphaning
	// the data and breaking recovery. Reusing the intent's paths keeps retries
	// consistent with the original attempt and lets MoveFile's idempotency
	// (src missing + dst present → success) take effect.
	//
	// The completion intent is persisted (via saveIntent) BEFORE the rename,
	// still under the mutex, so a crash between the intent write and the move
	// remains recoverable on the next startup.
	saveIntent := func(plan filestore.PlanDestResult) error {
		intent := &store.CompletionIntent{
			ID:        upload.ID,
			BackendID: upload.BackendID,
			Src:       srcPath,
			Dst:       plan.Abs,
			DstRel:    plan.Rel,
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
		}
		return h.store.SaveCompletionIntent(intent)
	}

	var plan filestore.PlanDestResult
	if existing, err := h.store.GetCompletionIntent(upload.ID); err != nil {
		log.Printf("failed to read existing completion intent for %s: %v", upload.ID, err)
		writeError(w, http.StatusInternalServerError, "failed to prepare completion")
		return
	} else if existing != nil {
		existingPlan := filestore.PlanDestResult{Abs: existing.Dst, Rel: existing.DstRel}
		if err := h.mover.MoveToPlaned(srcPath, existingPlan, saveIntent); err != nil {
			log.Printf("completion move failed for %s: %v", upload.ID, err)
			writeError(w, http.StatusInternalServerError, "failed to move file")
			return
		}
		plan = existingPlan
	} else {
		p, err := h.mover.PlanAndMove(srcPath, upload.CreationDate, upload.CreatedAt, upload.Filename, upload.BackendID, saveIntent)
		if err != nil {
			log.Printf("completion move failed for %s: %v", upload.ID, err)
			writeError(w, http.StatusInternalServerError, "failed to move file")
			return
		}
		plan = p
	}

	relPath := plan.Rel

	// Update the DB record to complete with the organized path.
	if _, err := h.store.UpdateComplete(upload.ID, relPath); err != nil {
		log.Printf("failed to update status to complete for %s: %v", upload.ID, err)
		writeError(w, http.StatusInternalServerError, "failed to complete upload")
		return
	}

	// Delete the completion intent now that the DB is consistent.
	if delErr := h.store.DeleteCompletionIntent(upload.ID); delErr != nil {
		log.Printf("failed to delete completion intent for %s: %v", upload.ID, delErr)
	}

	// Best-effort cleanup of the tusd sidecar (.info file).
	if termErr := h.backend.TerminateOrCleanup(upload.BackendID); termErr != nil && !errors.Is(termErr, uploadbackend.ErrNotFound) {
		log.Printf("failed to terminate tusd upload %s after completion: %v", upload.BackendID, termErr)
	}

	w.WriteHeader(http.StatusNoContent)
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
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing upload id")
		return
	}

	if err := ValidateSafeID(id); err != nil {
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
		log.Printf("GetUpload failed for %s: %v", id, err)
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
	if termErr := h.backend.TerminateOrCleanup(upload.BackendID); termErr != nil && !errors.Is(termErr, uploadbackend.ErrNotFound) {
		log.Printf("failed to terminate tusd upload %s during delete: %v", upload.BackendID, termErr)
	}

	// Remove the organized file from disk if the upload was completed.
	// Errors are logged but do not abort the DELETE — the DB record
	// is still marked deleted even if file cleanup fails.
	if upload.OrganizedPath != "" {
		if removeErr := h.mover.RemoveOrganizedFile(upload.OrganizedPath); removeErr != nil {
			log.Printf("failed to remove organized file %s for upload %s: %v", upload.OrganizedPath, upload.ID, removeErr)
		}
	}

	// Update the DB record status to deleted.
	if _, err := h.store.UpdateStatus(upload.ID, store.StatusDeleted); err != nil {
		log.Printf("failed to update status to deleted for %s: %v", upload.ID, err)
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
			log.Printf("failed to re-read upload %s before backend_lost: %v", upload.ID, err)
		}
	} else {
		switch current.Status {
		case store.StatusUploading, store.StatusCompleting:
			if _, err := h.store.UpdateStatus(upload.ID, store.StatusBackendLost); err != nil {
				log.Printf("failed to set backend_lost for %s: %v", upload.ID, err)
			}
		default:
			// complete, deleted, or already backend_lost — preserve terminal state.
		}
	}
	writeError(w, http.StatusConflict, "backend_lost")
}

// ---------------------------------------------------------------------------
// extractID extracts the ":id" path parameter from the request.
// The router is configured with Go 1.22+ ServeMux patterns (e.g.
// /uploads/{id}/data), so the ID is always available via r.PathValue.
func extractID(r *http.Request) string {
	return r.PathValue("id")
}
