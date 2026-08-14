// Package uploadbackend contains internal tests for the tusd recorder adapter
// and the NetworkTimeoutError fix (issue #8).
package uploadbackend

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/exp/slog"

	"github.com/tus/tusd/v2/pkg/filestore"
	"github.com/tus/tusd/v2/pkg/handler"
	"github.com/tus/tusd/v2/pkg/memorylocker"
)

// newTUSHandlerWithLogger builds a TUSHandler with a custom *slog.Logger
// injected into the underlying tusd handler. This allows tests to capture
// log output and verify that NetworkTimeoutError warnings are not emitted.
// It reuses the same store/locker/composer setup as New() but substitutes
// the logger.
func newTUSHandlerWithLogger(dir string, logger *slog.Logger) (*TUSHandler, error) {
	incomingPath := filepath.Join(dir, "incoming")
	if err := os.MkdirAll(incomingPath, 0o750); err != nil {
		return nil, fmt.Errorf("create incoming dir %s: %w", incomingPath, err)
	}

	fs := filestore.New(incomingPath)
	ml := memorylocker.New()

	composer := handler.NewStoreComposer()
	fs.UseIn(composer)
	ml.UseIn(composer)

	unrouted, err := handler.NewUnroutedHandler(handler.Config{
		StoreComposer:           composer,
		BasePath:                "/",
		NotifyCompleteUploads:   false,
		NotifyTerminatedUploads: false,
		NotifyUploadProgress:    false,
		NotifyCreatedUploads:    false,
		DisableDownload:         true,
		DisableTermination:      false,
		Logger:                  logger,
	})
	if err != nil {
		return nil, fmt.Errorf("create tusd handler: %w", err)
	}

	return &TUSHandler{
		store:    fs,
		composer: composer,
		handler:  unrouted,
	}, nil
}

// ---------------------------------------------------------------------------
// Test: tusdRecorder satisfies http.ResponseController deadline interface
// ---------------------------------------------------------------------------

func TestTusdRecorderSetReadDeadline(t *testing.T) {
	rec := newTusdRecorder()

	// http.NewResponseController probes for SetReadDeadline/SetWriteDeadline.
	rc := http.NewResponseController(rec)
	if rc == nil {
		t.Fatal("http.NewResponseController returned nil — *tusdRecorder should satisfy http.ResponseController")
	}

	if err := rc.SetReadDeadline(time.Now()); err != nil {
		t.Errorf("SetReadDeadline: got %v, want nil", err)
	}
}

func TestTusdRecorderSetWriteDeadline(t *testing.T) {
	rec := newTusdRecorder()

	rc := http.NewResponseController(rec)
	if rc == nil {
		t.Fatal("http.NewResponseController returned nil")
	}

	if err := rc.SetWriteDeadline(time.Now()); err != nil {
		t.Errorf("SetWriteDeadline: got %v, want nil", err)
	}
}

func TestTusdRecorderPromotesResponseRecorder(t *testing.T) {
	rec := newTusdRecorder()

	// The embedded *httptest.ResponseRecorder fields must be accessible directly.
	rec.WriteHeader(http.StatusOK)
	if rec.Code != http.StatusOK {
		t.Errorf("Code = %d, want %d", rec.Code, http.StatusOK)
	}

	rec.Header().Set("X-Test", "value")
	if rec.Header().Get("X-Test") != "value" {
		t.Error("Header() should be promoted from embedded ResponseRecorder")
	}

	rec.Body.WriteString("hello")
	if rec.Body.String() != "hello" {
		t.Error("Body should be promoted from embedded ResponseRecorder")
	}
}

// ---------------------------------------------------------------------------
// Test: e2e — ForwardPatch with captured logger shows no NetworkTimeoutError
// ---------------------------------------------------------------------------

func TestForwardPatchNoNetworkTimeoutErrorWarning(t *testing.T) {
	dir := t.TempDir()

	// Build a slog.Logger that writes to a buffer so we can inspect output.
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{
		Level: slog.LevelWarn,
	}))

	h, err := newTUSHandlerWithLogger(dir, logger)
	if err != nil {
		t.Fatalf("newTUSHandlerWithLogger: %v", err)
	}

	// Create an upload
	id, err := h.CreateUpload(context.Background(), "")
	if err != nil {
		t.Fatalf("CreateUpload: %v", err)
	}

	// Write a small chunk to trigger PATCH body reads (this is where
	// SetReadDeadline/SetWriteDeadline is called on every tick).
	payload := bytes.Repeat([]byte("x"), 8192) // multiple read ticks
	_, err = h.ForwardPatch(context.Background(), id, bytes.NewReader(payload), 0, strconv.Itoa(len(payload)))
	if err != nil {
		t.Fatalf("ForwardPatch: %v", err)
	}

	logOutput := logBuf.String()

	// We must NOT see NetworkTimeoutError in the logs.
	if strings.Contains(logOutput, "NetworkTimeoutError") {
		t.Errorf("log output contains NetworkTimeoutError:\n%s", logOutput)
	}

	// The patch must have succeeded.
	info, err := h.GetInfo(context.Background(), id)
	if err != nil {
		t.Fatalf("GetInfo: %v", err)
	}
	if info.Offset != int64(len(payload)) {
		t.Errorf("offset = %d, want %d", info.Offset, len(payload))
	}
}
