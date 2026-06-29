package api_test

import (
	"strings"
	"testing"

	"github.com/moontechs/files-nest/server/internal/api"
)

// rawLocalID is a realistic PhotoKit localIdentifier, which may contain '/'
// characters that are unsafe for URL/FS paths.
const rawLocalID = "ABCD1234-1234-1234-1234-123456789012/L0/040"

func TestSafeID_Deterministic(t *testing.T) {
	id1 := api.SafeID(rawLocalID)
	id2 := api.SafeID(rawLocalID)
	if id1 != id2 {
		t.Errorf("SafeID not deterministic: %q != %q", id1, id2)
	}
}

func TestSafeID_DifferentInputsProduceDifferentIDs(t *testing.T) {
	id1 := api.SafeID("asset-001")
	id2 := api.SafeID("asset-002")
	if id1 == id2 {
		t.Error("SafeID produced identical output for different inputs")
	}
}

func TestSafeID_EmptyInput(t *testing.T) {
	id := api.SafeID("")
	if id == "" {
		t.Error("SafeID should not return empty string even for empty input")
	}
}

func TestSafeID_NoPathSeparators(t *testing.T) {
	id := api.SafeID(rawLocalID)
	for _, c := range id {
		if c == '/' {
			t.Errorf("SafeID contains path separator '/' in %q", id)
		}
	}
}

func TestSafeID_FixedLength(t *testing.T) {
	id := api.SafeID(rawLocalID)
	if len(id) != api.SafeIDEncodedLen {
		t.Errorf("SafeID length = %d, want %d", len(id), api.SafeIDEncodedLen)
	}
}

func TestSafeID_AllASCII(t *testing.T) {
	id := api.SafeID(rawLocalID)
	for _, c := range id {
		if c > 127 {
			t.Errorf("SafeID contains non-ASCII character %q (%U)", c, c)
		}
	}
}

func TestValidateSafeID_Valid(t *testing.T) {
	id := api.SafeID(rawLocalID)
	if err := api.ValidateSafeID(id); err != nil {
		t.Errorf("expected valid safe ID, got: %v", err)
	}
}

func TestValidateSafeID_Empty(t *testing.T) {
	if err := api.ValidateSafeID(""); err == nil {
		t.Error("expected error for empty safe ID")
	}
}

func TestValidateSafeID_InvalidBase64(t *testing.T) {
	if err := api.ValidateSafeID("!!!not-valid-base64!!!"); err == nil {
		t.Error("expected error for invalid base64 input")
	}
}

func TestValidateSafeID_WrongLength(t *testing.T) {
	id := api.SafeID(rawLocalID)
	// Truncate to produce a wrong-length ID
	if len(id) < 3 {
		t.Fatal("test precondition failed: ID too short")
	}
	truncated := id[:len(id)-3]
	if err := api.ValidateSafeID(truncated); err == nil {
		t.Error("expected error for truncated safe ID")
	}
}

func TestLocalIdentifierIndexKey_Deterministic(t *testing.T) {
	k1 := api.LocalIdentifierIndexKey(rawLocalID)
	k2 := api.LocalIdentifierIndexKey(rawLocalID)
	if k1 != k2 {
		t.Errorf("LocalIdentifierIndexKey not deterministic: %q != %q", k1, k2)
	}
}

func TestLocalIdentifierIndexKey_NoPathSeparators(t *testing.T) {
	key := api.LocalIdentifierIndexKey(rawLocalID)
	for _, c := range key {
		if c == '/' {
			t.Errorf("LocalIdentifierIndexKey contains '/' in %q", key)
		}
	}
}

func TestLocalIdentifierIndexKey_EmptyInput(t *testing.T) {
	key := api.LocalIdentifierIndexKey("")
	if key != "" {
		t.Errorf("LocalIdentifierIndexKey should return empty for empty input, got %q", key)
	}
}

func TestLocalIdentifierIndexKey_DifferentInputs(t *testing.T) {
	k1 := api.LocalIdentifierIndexKey("asset-001")
	k2 := api.LocalIdentifierIndexKey("asset-002")
	if k1 == k2 {
		t.Error("LocalIdentifierIndexKey produced same output for different inputs")
	}
}

// --------------------------------------------------------------------------
// SanitizeFilename
// --------------------------------------------------------------------------

func TestSanitizeFilename_Normal(t *testing.T) {
	result := api.SanitizeFilename("IMG_1234.jpg")
	if result != "IMG_1234.jpg" {
		t.Errorf("expected %q, got %q", "IMG_1234.jpg", result)
	}
}

func TestSanitizeFilename_StripsDirectory(t *testing.T) {
	result := api.SanitizeFilename("path/to/sneaky/file.jpg")
	if result != "file.jpg" {
		t.Errorf("expected %q, got %q", "file.jpg", result)
	}
}

func TestSanitizeFilename_StripsLeadingSlash(t *testing.T) {
	result := api.SanitizeFilename("/etc/passwd")
	if result != "passwd" {
		t.Errorf("expected %q, got %q", "passwd", result)
	}
}

func TestSanitizeFilename_EmptyReturnsEmpty(t *testing.T) {
	result := api.SanitizeFilename("")
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}

func TestSanitizeFilename_DotReturnsEmpty(t *testing.T) {
	result := api.SanitizeFilename(".")
	if result != "" {
		t.Errorf("expected empty string for '.', got %q", result)
	}
}

func TestSanitizeFilename_DotDotReturnsEmpty(t *testing.T) {
	result := api.SanitizeFilename("..")
	if result != "" {
		t.Errorf("expected empty string for '..', got %q", result)
	}
}

func TestSanitizeFilename_DotDotInPath(t *testing.T) {
	// filepath.Base("../../etc/passwd") → "passwd", so traversal
	// components are stripped by Base. The result is valid.
	result := api.SanitizeFilename("../../etc/passwd")
	if result != "passwd" {
		t.Errorf("expected %q, got %q", "passwd", result)
	}
}

func TestSanitizeFilename_HiddenFileRejected(t *testing.T) {
	result := api.SanitizeFilename(".hidden")
	if result != "" {
		t.Errorf("expected empty string for hidden file, got %q", result)
	}
}

func TestSanitizeFilename_HiddenInDirectory(t *testing.T) {
	// filepath.Base("dir/.hidden") → ".hidden" → hidden → rejected
	result := api.SanitizeFilename("dir/.hidden")
	if result != "" {
		t.Errorf("expected empty string for hidden file in dir, got %q", result)
	}
}

func TestSanitizeFilename_WithSpaces(t *testing.T) {
	result := api.SanitizeFilename("My Photo.jpg")
	if result != "My Photo.jpg" {
		t.Errorf("expected %q, got %q", "My Photo.jpg", result)
	}
}

func TestSanitizeFilename_UnicodeFilename(t *testing.T) {
	result := api.SanitizeFilename("照片.jpg")
	if result != "照片.jpg" {
		t.Errorf("expected %q, got %q", "照片.jpg", result)
	}
}

func TestSanitizeFilename_TooLongName(t *testing.T) {
	longName := string(make([]byte, 300)) + ".jpg"
	result := api.SanitizeFilename(longName)
	if result != "" {
		t.Errorf("expected empty string for >255 byte name, got %q", result)
	}
}

func TestSanitizeFilename_Exactly255Bytes(t *testing.T) {
	// Build a 255-byte filename using printable 'a' characters
	shortName := strings.Repeat("a", 251) + ".jpg"
	if len(shortName) != 255 {
		t.Skipf("test setup: wanted 255 bytes, got %d", len(shortName))
	}
	result := api.SanitizeFilename(shortName)
	if result != shortName {
		t.Errorf("expected 255-char name preserved, got empty or truncated")
	}
}

func TestSanitizeFilename_BackslashRejected(t *testing.T) {
	result := api.SanitizeFilename("bad\\name.jpg")
	if result != "" {
		t.Errorf("expected empty string for backslash in name, got %q", result)
	}
}

func TestSanitizeFilename_NullByteRejected(t *testing.T) {
	result := api.SanitizeFilename("bad\x00name.jpg")
	if result != "" {
		t.Errorf("expected empty string for null byte in name, got %q", result)
	}
}

func TestSanitizeFilename_ControlCharRejected(t *testing.T) {
	result := api.SanitizeFilename("bad\x01name.jpg")
	if result != "" {
		t.Errorf("expected empty string for control char in name, got %q", result)
	}
}

func TestSanitizeFilename_DotAtEnd(t *testing.T) {
	result := api.SanitizeFilename("filename.")
	if result != "filename." {
		t.Errorf("expected %q, got %q", "filename.", result)
	}
}

func TestSanitizeFilename_NoExtension(t *testing.T) {
	result := api.SanitizeFilename("README")
	if result != "README" {
		t.Errorf("expected %q, got %q", "README", result)
	}
}
