// Package statuspage renders the unauthenticated GET / status page: a small,
// static, four-card HTML document showing that the server is running, its
// version and address, and whether HTTP Basic Auth is disabled. It carries no
// upload data — it is a reachability/setup aid, not a client.
package statuspage

import (
	"embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"net/http"
)

// assets bundles the page template and its images into the binary.
// favicon.png is the 32px app icon; logo.png is the 128px one; screenshot.png
// is a cropped macOS menu-bar screenshot showing the client mid-sync.
//
//go:embed status.html favicon.png logo.png screenshot.png
var assets embed.FS

// tmpl is parsed once at package init. ParseFS panics on a malformed template,
// which is the desired fail-fast behavior for an asset compiled into the
// binary — a broken status.html should fail the build's tests, never render
// half a page at runtime.
//
//nolint:gochecknoglobals // parsed templates are immutable shared state, not mutable configuration
var tmpl = template.Must(template.New("status.html").Funcs(template.FuncMap{
	"dataURI": dataURI,
}).ParseFS(assets, "status.html"))

// Data is the view model rendered by status.html. It deliberately carries no
// per-upload or credential data.
type Data struct {
	Version      string // e.g. "0.3.1" or "dev" — no "v" prefix
	Address      string // scheme + r.Host, e.g. "https://backup.example.com" or "http://192.168.1.50:8080"
	AuthDisabled bool
}

// Render writes the status page HTML to w. Any template execution error is
// returned so the caller can log it; the response is already committed by
// then, so the caller cannot turn it into a non-200.
func Render(w http.ResponseWriter, data Data) error {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)

	err := tmpl.Execute(w, data)
	if err != nil {
		return fmt.Errorf("render status page: %w", err)
	}

	return nil
}

// dataURI returns one of the embedded PNGs as a data: URI so the page needs no
// extra routes and makes no external requests. The assets are compiled into
// the binary and never attacker-controlled, so marking the result as a
// template.URL is safe and is required for html/template to permit the data:
// scheme in href/src attributes.
//
//nolint:gosec // G203: value is a data: URI built solely from an embedded, non-user-controlled asset
func dataURI(name string) template.URL {
	png, err := assets.ReadFile(name)
	if err != nil {
		return ""
	}

	const pngMIME = "image/png"

	return template.URL("data:" + pngMIME + ";base64," + base64.StdEncoding.EncodeToString(png))
}
