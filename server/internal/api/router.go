// Package api provides HTTP handlers, middleware, and shared utilities
// for the iCloud Backup server API.
package api

import (
	"log"
	"net/http"
	"time"
)

// NewRouter creates an http.Handler that routes all API endpoints to the
// appropriate handlers with BasicAuth middleware applied to every route.
//
// Routes are registered using Go 1.22+ ServeMux patterns which support
// method-based matching ({METHOD /path}) and path parameters ({id}).
// The handlers extract the "id" parameter via r.PathValue("id").
//
// All routes except /health require authentication. The health endpoint
// is intentionally kept outside auth so monitoring tools can check
// liveness without credentials.
func NewRouter(h *Handler, authCfg AuthConfig) http.Handler {
	mux := http.NewServeMux()

	// Unauthenticated health check endpoint.
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	// Auth middleware wrapping all API routes.
	auth := AuthMiddleware(authCfg)

	// Upload management endpoints.
	mux.Handle("POST /uploads", auth(http.HandlerFunc(h.HandleCreateUpload)))
	mux.Handle("GET /uploads", auth(http.HandlerFunc(h.HandleListUploads)))
	mux.Handle("GET /uploads/{id}", auth(http.HandlerFunc(h.HandleGetUpload)))

	// TUS protocol data endpoints.
	mux.Handle("HEAD /uploads/{id}/data", auth(http.HandlerFunc(h.HandleHeadUploadData)))
	mux.Handle("PATCH /uploads/{id}/data", auth(http.HandlerFunc(h.HandlePatchUploadData)))

	// Status transition and deletion.
	mux.Handle("PATCH /uploads/{id}/status", auth(http.HandlerFunc(h.HandlePatchUploadStatus)))
	mux.Handle("DELETE /uploads/{id}", auth(http.HandlerFunc(h.HandleDeleteUpload)))

	// Wrap with a panic recovery middleware to prevent a single panicking
	// handler from crashing the entire server.
	return recoveryMiddleware(requestLogMiddleware(mux))
}

// ---------------------------------------------------------------------------
// Middleware
// ---------------------------------------------------------------------------

// recoveryMiddleware recovers from panics in downstream handlers, logs the
// stack trace, and returns a 500 Internal Server Error. Without this, a
// single panic would crash the entire server process.
func recoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("panic recovered: %v (path=%s method=%s)", rec, r.URL.Path, r.Method)
				// Emit a JSON body with a JSON content type so clients can parse
				// it consistently. http.Error would label this text/plain.
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Content-Type-Options", "nosniff")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":"internal server error"}`))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// requestLogMiddleware logs every HTTP request with its method, path,
// response status, and duration. This is useful for debugging and
// production observability.
func requestLogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Wrap the ResponseWriter to capture the status code.
		lrw := &loggingResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(lrw, r)

		duration := time.Since(start)
		log.Printf("%s %s %d %s", r.Method, r.URL.Path, lrw.statusCode, duration)
	})
}

// loggingResponseWriter wraps http.ResponseWriter to capture the status code
// written by downstream handlers. This enables the request logging middleware
// to include the response status code in its log output.
type loggingResponseWriter struct {
	http.ResponseWriter
	statusCode int
}

// WriteHeader captures the status code before delegating to the wrapped
// ResponseWriter. It is called by http.FileServer and all standard
// response-writing paths.
func (lrw *loggingResponseWriter) WriteHeader(code int) {
	lrw.statusCode = code
	lrw.ResponseWriter.WriteHeader(code)
}
