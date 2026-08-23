package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log"
	"net/http"
)

// contextKey is a private type for context keys to avoid collisions with
// keys defined in other packages. Using a string directly as a context key
// is unsafe because any package could define the same string key.
type contextKey string

// UserKey is the context key used to store the authenticated username in
// request contexts. Retrieve it via AuthUserFromContext.
const UserKey contextKey = "auth_user"

// AuthConfig holds the credentials for HTTP Basic Authentication. When both
// Username and Password are empty strings, authentication is skipped entirely
// — this is useful for local development and testing without credentials.
type AuthConfig struct {
	Username string
	Password string
}

// AuthMiddleware returns an HTTP middleware that enforces Basic Authentication
// for all requests. It extracts the Authorization header, decodes the base64
// credentials, and verifies them against the provided AuthConfig using
// constant-time comparison to resist timing attacks.
//
// If AuthConfig has both empty Username and empty Password, the middleware
// passes all requests through without authentication. This allows the same
// binary to run in both authenticated (production) and unauthenticated
// (development) modes based on environment variables.
//
// On success, the authenticated username is stored in the request context
// under UserKey. Handlers can retrieve it via AuthUserFromContext.
//
// On failure, a 401 Unauthorized response is returned with a JSON body
// containing the error details and a WWW-Authenticate header.
func AuthMiddleware(cfg AuthConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// When no credentials are configured, skip authentication entirely.
			// This supports development mode where credentials are optional.
			if cfg.Username == "" && cfg.Password == "" {
				next.ServeHTTP(w, r)

				return
			}

			user, pass, ok := r.BasicAuth()
			if !ok {
				writeUnauthorized(w, "missing or malformed authorization header")

				return
			}

			// Use constant-time comparison to prevent timing side-channel
			// attacks that could leak credential information character by
			// character.
			userMatch := subtle.ConstantTimeCompare([]byte(user), []byte(cfg.Username))
			passMatch := subtle.ConstantTimeCompare([]byte(pass), []byte(cfg.Password))

			if userMatch != 1 || passMatch != 1 {
				writeUnauthorized(w, "invalid credentials")

				return
			}

			// Store the authenticated username in the request context so
			// downstream handlers can identify who made the request.
			ctx := context.WithValue(r.Context(), UserKey, user)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// AuthUserFromContext returns the authenticated username stored in the
// request context by AuthMiddleware. It returns an empty string if the
// request was not authenticated or if the value was not set.
func AuthUserFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}

	if user, ok := ctx.Value(UserKey).(string); ok {
		return user
	}

	return ""
}

// writeUnauthorized sends a 401 Unauthorized response with a JSON error body
// and a WWW-Authenticate header instructing the client to use Basic
// authentication with the "iCloud Backup Server" realm.
func writeUnauthorized(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("WWW-Authenticate", `Basic realm="iCloud Backup Server", charset="UTF-8"`)
	w.WriteHeader(http.StatusUnauthorized)
	// Encode the error response. Encoding a static map cannot fail for these
	// types, but we still check the error rather than silently ignoring it.
	err := json.NewEncoder(w).Encode(map[string]string{"error": message})
	if err != nil {
		log.Printf("ERROR writeUnauthorized encode error: %v", err)
	}
}
