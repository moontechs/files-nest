package statuspage_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/moontechs/files-nest/server/internal/statuspage"
)

const (
	// testVersion is a realistic released version string (no "v" prefix).
	testVersion = "0.3.1"
	// testAddress is a realistic public Host-header value.
	testAddress = "backup.example.com:8080"
	// devVersion is the build-time default used when no version is injected.
	devVersion = "dev"
)

// render writes the status page for data and returns the recorder plus the
// rendered body, failing the test if Render returns an error.
func render(t *testing.T, data statuspage.Data) (*httptest.ResponseRecorder, string) {
	t.Helper()

	rec := httptest.NewRecorder()
	if err := statuspage.Render(rec, data); err != nil {
		t.Fatalf("Render(%+v): %v", data, err)
	}

	return rec, rec.Body.String()
}

func TestRender_StatusOK(t *testing.T) {
	rec, _ := render(t, statuspage.Data{Version: testVersion, Address: testAddress, AuthDisabled: false})

	if rec.Code != http.StatusOK {
		t.Errorf("Render: status = %d, want %d", rec.Code, http.StatusOK)
	}

	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Render: Content-Type = %q, want %q", ct, "text/html; charset=utf-8")
	}
}

func TestRender_ContainsVersionAndAddress(t *testing.T) {
	_, body := render(t, statuspage.Data{Version: testVersion, Address: testAddress, AuthDisabled: false})

	for _, want := range []string{testVersion, testAddress} {
		if !strings.Contains(body, want) {
			t.Errorf("Render: body does not contain %q", want)
		}
	}
}

func TestRender_AuthWarningPresentWhenDisabled(t *testing.T) {
	_, body := render(t, statuspage.Data{Version: devVersion, Address: "127.0.0.1:8080", AuthDisabled: true})

	for _, want := range []string{"Authentication disabled", "BACKUP_USER", "BACKUP_PASS"} {
		if !strings.Contains(body, want) {
			t.Errorf("Render(AuthDisabled=true): body does not contain %q", want)
		}
	}
}

func TestRender_AuthWarningAbsentWhenEnabled(t *testing.T) {
	_, body := render(t, statuspage.Data{Version: devVersion, Address: "127.0.0.1:8080", AuthDisabled: false})

	for _, notWant := range []string{"Authentication disabled", "BACKUP_USER"} {
		if strings.Contains(body, notWant) {
			t.Errorf("Render(AuthDisabled=false): body unexpectedly contains %q", notWant)
		}
	}
}

func TestRender_EmptyVersionAndAddressFallBackToPlaceholders(t *testing.T) {
	// Edge input: an empty Address/Version must render gracefully with
	// placeholders rather than panicking or emitting empty <dd> cells.
	_, body := render(t, statuspage.Data{Version: "", Address: "", AuthDisabled: false})

	for _, want := range []string{devVersion, "unknown"} {
		if !strings.Contains(body, want) {
			t.Errorf("Render(empty inputs): body does not contain fallback %q", want)
		}
	}
}

func TestRender_EmbedsIconDataURIs(t *testing.T) {
	// The template must deliver the favicon/logo inline as data: URIs so the
	// page never depends on separate unauthenticated asset routes.
	_, body := render(t, statuspage.Data{Version: devVersion, Address: "localhost:8080", AuthDisabled: false})

	if !strings.Contains(body, "data:image/png;base64,") {
		t.Error("Render: body does not contain an inline data:image/png base64 URI")
	}
}

func TestRender_TemplateNotText(t *testing.T) {
	// Regression guard: Address is rendered through html/template's escaping,
	// so a Host-header value with HTML must not survive into the body. This is
	// the documented reason the package must never switch to text/template.
	_, body := render(t, statuspage.Data{Version: devVersion, Address: `"><script>alert(1)</script>`, AuthDisabled: false})

	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Error("Render: Address was injected unescaped — html/template escaping regressed")
	}
}
