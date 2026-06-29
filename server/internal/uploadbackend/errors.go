// Package uploadbackend provides a narrow adapter around tusd to isolate the
// rest of the codebase from tusd API changes. It defines the core interfaces
// and error types used by the file upload system.
package uploadbackend

import "errors"

// Sentinel errors returned by this package. Callers should use errors.Is to
// compare against these sentinels rather than depending on tusd error types.
var (
	// ErrNotFound is returned when the requested upload does not exist in the
	// backend. It is normalized from tusd's handler.ErrNotFound so that callers
	// do not need to import tusd types directly.
	ErrNotFound = errors.New("upload not found")

	// ErrLocked is returned when the upload is currently locked by another
	// request and the lock could not be acquired.
	ErrLocked = errors.New("upload locked")

	// ErrInvalidOffset is returned when the Upload-Offset header in a PATCH
	// request does not match the server's current offset.
	ErrInvalidOffset = errors.New("invalid offset")

	// ErrUploadRejected is returned when the upload creation is rejected
	// by the server (e.g. by a pre-create hook callback).
	ErrUploadRejected = errors.New("upload rejected by server")
)
