package uploadbackend

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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

type firstReadError struct{}

func (firstReadError) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestForwardPatchFinalLengthRetry(t *testing.T) {
	h, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := context.Background()
	id, err := h.CreateUpload(ctx, "")
	if err != nil {
		t.Fatalf("CreateUpload: %v", err)
	}

	const finalSize = 11
	_, err = h.ForwardPatch(ctx, id, firstReadError{}, 0, strconv.Itoa(finalSize))
	if err == nil {
		t.Fatal("ForwardPatch with failing reader succeeded")
	}

	info, err := h.GetInfo(ctx, id)
	if err != nil {
		t.Fatalf("GetInfo after failed patch: %v", err)
	}
	if info.SizeIsDeferred || info.Size != finalSize || info.Offset != 0 {
		t.Fatalf("info after failed patch = %+v, want declared size %d and offset 0", info, finalSize)
	}

	_, err = h.ForwardPatch(ctx, id, bytes.NewReader([]byte("hello world")), 0, strconv.Itoa(finalSize))
	if err != nil {
		t.Fatalf("retry ForwardPatch: %v", err)
	}
	info, err = h.GetInfo(ctx, id)
	if err != nil {
		t.Fatalf("GetInfo after retry: %v", err)
	}
	if info.Offset != finalSize {
		t.Errorf("offset after retry = %d, want %d", info.Offset, finalSize)
	}
}

func TestForwardPatchFinalLengthMismatch(t *testing.T) {
	h, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := context.Background()
	id, err := h.CreateUpload(ctx, "")
	if err != nil {
		t.Fatalf("CreateUpload: %v", err)
	}
	_, _ = h.ForwardPatch(ctx, id, firstReadError{}, 0, "11")

	_, err = h.ForwardPatch(ctx, id, bytes.NewReader([]byte("hello")), 0, "12")
	var clientErr *ClientError
	if !errors.As(err, &clientErr) || clientErr.Status != http.StatusConflict {
		t.Fatalf("error = %v, want ClientError 409", err)
	}
	info, err := h.GetInfo(ctx, id)
	if err != nil {
		t.Fatalf("GetInfo: %v", err)
	}
	if info.Offset != 0 {
		t.Errorf("offset after mismatch = %d, want 0", info.Offset)
	}
}

func TestForwardPatchFirstFinalLengthDeclaration(t *testing.T) {
	h, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	id, err := h.CreateUpload(context.Background(), "")
	if err != nil {
		t.Fatalf("CreateUpload: %v", err)
	}
	if _, err := h.ForwardPatch(context.Background(), id, bytes.NewReader([]byte("hello")), 0, "5"); err != nil {
		t.Fatalf("first final declaration: %v", err)
	}
}

func TestExtractTusdError(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		sentinel error
		body     string
	}{
		{
			name:     "not implemented",
			status:   http.StatusNotImplemented,
			sentinel: errTusdNotImplemented,
			body:     "feature is unavailable",
		},
		{
			name:     "precondition failed",
			status:   http.StatusPreconditionFailed,
			sentinel: errTusdVersionMismatch,
			body:     "unsupported tus version",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			rec.WriteHeader(tt.status)
			_, _ = rec.Body.WriteString(tt.body)

			err := extractTusdError(rec)
			if !errors.Is(err, tt.sentinel) {
				t.Fatalf("extractTusdError() error = %v, want wrapping %v", err, tt.sentinel)
			}
			if !strings.Contains(err.Error(), tt.body) {
				t.Errorf("error = %q, want body text %q", err, tt.body)
			}
		})
	}
}

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
