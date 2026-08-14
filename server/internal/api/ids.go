// Package api provides HTTP handlers, middleware, and shared utilities
// for the iCloud Backup server API.
package api

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

const (
	// SafeIDEncodedLen is the expected length of a safe server ID in its
	// base64url-encoded form (SHA-256 hash → 32 bytes → 43 base64 chars).
	SafeIDEncodedLen = 43
)

// SafeID derives a deterministic, path-safe server ID from a PhotoKit
// localIdentifier. The localIdentifier may contain '/' and other characters
// unsafe for URL paths and filesystem paths.
//
// We use SHA-256 + raw URL-safe base64 encoding to produce a compact,
// fixed-length identifier safe for use as a BadgerDB key segment and URL
// path component.
func SafeID(localIdentifier string) string {
	h := sha256.Sum256([]byte(localIdentifier))

	return base64.RawURLEncoding.EncodeToString(h[:])
}

// ValidateSafeID checks whether s is a valid safe server ID produced by
// SafeID. It verifies the length, base64url decoding, and decoded hash size.
func ValidateSafeID(s string) error {
	if s == "" {
		return errors.New("safe id must not be empty")
	}

	if len(s) != SafeIDEncodedLen {
		return fmt.Errorf("safe id has length %d, want %d", len(s), SafeIDEncodedLen)
	}

	decoded, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return fmt.Errorf("safe id decode: %w", err)
	}

	if len(decoded) != sha256.Size {
		return fmt.Errorf("safe id decoded length %d, want %d", len(decoded), sha256.Size)
	}

	return nil
}

// SanitizeFilename makes a filename safe for the organized tree.
// It strips directory components, rejects empty names, rejects path traversal
// (e.g. "..", absolute paths), and preserves only known-safe characters.
// Returns empty string if the filename is unsafe.
func SanitizeFilename(filename string) string {
	// Strip directory components — last path segment only
	filename = filepath.Base(filename)

	// Reject empty or dot-only names
	if filename == "" || filename == "." || filename == ".." {
		return ""
	}

	// Reject hidden files / names starting with dot
	if strings.HasPrefix(filename, ".") {
		return ""
	}

	// Reject names longer than 255 bytes (common FS limit)
	if len(filename) > 255 {
		return ""
	}

	// Reject names containing control characters or characters unsafe
	// in most filesystems (NULL, /, backslash)
	for _, c := range filename {
		if c == 0 || c == '/' || c == '\\' {
			return ""
		}

		if c < 32 {
			return ""
		}
	}

	return filename
}

// LocalIdentifierIndexKey encodes a localIdentifier for use as a BadgerDB
// key segment in the local identifier index. This is a reversible encoding —
// the BadgerDB value stores the safe upload ID so a client can look up an
// upload by its original PhotoKit identifier.
func LocalIdentifierIndexKey(localIdentifier string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(localIdentifier))
}
