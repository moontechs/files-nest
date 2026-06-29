// Package api provides HTTP handlers, middleware, and shared utilities
// for the iCloud Backup server API.
package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net/http"
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
