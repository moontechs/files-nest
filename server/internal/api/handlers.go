// Package api provides HTTP handlers, middleware, and shared utilities
// for the iCloud Backup server API.
package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
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

	// Sanitize the filename.
	safeFilename := SanitizeFilename(req.Filename)
	if safeFilename == "" {
		writeError(w, http.StatusBadRequest, "invalid filename")
		return
	}

	// Derive the deterministic safe server ID.
	id := SafeID(req.LocalIdentifier)

	// Build the Upload-Metadata header value for tusd.
	tusdMeta := "filename " + base64.RawURLEncoding.EncodeToString([]byte(safeFilename))

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
	// for this localIdentifier, terminate the tusd upload and return the
	// existing record.
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

	// If a record already existed, terminate the newly-created tusd upload
	// and return the existing record.
	if !created {
		if termErr := h.backend.TerminateOrCleanup(backendID); termErr != nil {
			log.Printf("failed to terminate redundant tusd upload %s: %v", backendID, termErr)
		}
		writeJSON(w, http.StatusOK, existingToResponse(existing))
		return
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

	// Treat deleted records as not found.
	if upload.Status == store.StatusDeleted {
		writeError(w, http.StatusNotFound, "upload not found")
		return
	}

	offset, err := h.backend.GetOffset(upload.BackendID)
	if err != nil {
		if errors.Is(err, uploadbackend.ErrNotFound) {
			h.handleBackendLost(w, r, upload)
			return
		}
		log.Printf("GetOffset failed for backend %s: %v", upload.BackendID, err)
		writeError(w, http.StatusInternalServerError, "failed to get upload offset")
		return
	}

	w.Header().Set("Upload-Offset", strconv.FormatInt(offset, 10))
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

	// Compose the destination organized path.
	// Use creation_date, falling back to created_at if empty.
	creationDate := upload.CreationDate
	if creationDate == "" {
		creationDate = upload.CreatedAt
	}
	relPath, dstPath := organizedPath(h.storagePath, creationDate, upload.Filename)

	// If the destination already exists, insert the backend_id before the
	// extension to create a unique path (e.g. IMG_0001_<backend_id>.jpg).
	if _, err := os.Stat(dstPath); err == nil {
		ext := filepath.Ext(dstPath)
		base := strings.TrimSuffix(dstPath, ext)
		dstPath = base + "_" + upload.BackendID + ext
	}

	// Save a completion intent for crash recovery. If the server crashes
	// between the file move and the DB update, the intent is recovered on
	// the next startup.
	intent := &store.CompletionIntent{
		ID:        upload.ID,
		BackendID: upload.BackendID,
		Src:       srcPath,
		Dst:       dstPath,
		DstRel:    relPath,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := h.store.SaveCompletionIntent(intent); err != nil {
		log.Printf("failed to save completion intent for %s: %v", upload.ID, err)
		writeError(w, http.StatusInternalServerError, "failed to prepare completion")
		return
	}

	// Ensure the destination directory exists.
	if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
		log.Printf("failed to create directory %s: %v", filepath.Dir(dstPath), err)
		writeError(w, http.StatusInternalServerError, "failed to create destination directory")
		return
	}

	// Move the completed file from incoming to organized.
	if err := os.Rename(srcPath, dstPath); err != nil {
		log.Printf("failed to move file %s -> %s: %v", srcPath, dstPath, err)
		writeError(w, http.StatusInternalServerError, "failed to move file")
		return
	}

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
// be gone), then updates the record status to deleted.
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
func (h *Handler) handleBackendLost(w http.ResponseWriter, _ *http.Request, upload *store.Upload) {
	if _, err := h.store.UpdateStatus(upload.ID, store.StatusBackendLost); err != nil {
		log.Printf("failed to set backend_lost for %s: %v", upload.ID, err)
	}
	writeError(w, http.StatusConflict, "backend_lost")
}

// organizedPath computes the relative and absolute organized file paths
// from the storage root, creation date, and filename.
//
// The relative path uses the form: organized/YYYY/MM/DD/<filename>.
func organizedPath(storagePath, creationDate, filename string) (rel, abs string) {
	// Parse the creation date to extract year, month, day.
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
		// Fallback: use the raw date string as a single segment.
		year = creationDate
		month = "unknown"
		day = "unknown"
	}

	rel = filepath.Join("organized", year, month, day, filename)
	abs = filepath.Join(storagePath, rel)
	return
}

// ---------------------------------------------------------------------------
// extractID extracts the ":id" path parameter from the request.
// It supports Go 1.22+ ServeMux patterns (r.PathValue) and falls back to
// extracting from the URL path as a last resort.
func extractID(r *http.Request) string {
	// Go 1.22+ ServeMux: GET /uploads/{id}
	if id := r.PathValue("id"); id != "" {
		return id
	}
	// Fallback: extract the last non-empty segment of the URL path.
	// This handles the case where the handler is mounted at /uploads/
	// and the path is /uploads/<id>.
	parts := strings.Split(strings.TrimRight(r.URL.Path, "/"), "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return ""
}
