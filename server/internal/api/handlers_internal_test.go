package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moontechs/files-nest/server/internal/store"
	"github.com/moontechs/files-nest/server/internal/uploadbackend"
)

var errInjectedTestFailure = errors.New("injected test failure")

func TestHandleCreateUpload_LogsCleanupFailureAfterStoreFailure(t *testing.T) {
	h, st := newInternalTestHandler(t)
	storeFailure := fmt.Errorf("store failure: %w", errInjectedTestFailure)
	cleanupFailure := fmt.Errorf("cleanup failure: %w", errInjectedTestFailure)
	h.store = failingStore{Store: st, putErr: storeFailure, reRegisterErr: nil}
	backend, ok := h.backend.(*uploadbackend.TUSHandler)
	if !ok {
		t.Fatalf("h.backend is not *uploadbackend.TUSHandler: %T", h.backend)
	}
	h.backend = failingBackend{
		TUSHandler:   backend,
		terminateErr: cleanupFailure,
	}

	logs, restoreLogs := captureLogs(t)
	defer restoreLogs()
	rec := createUploadRequestForTest(h, "STORE-FAIL/L0/000")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
	if !strings.Contains(logs.String(), "failed to terminate tusd upload") {
		t.Fatalf("cleanup failure was not logged: %s", logs.String())
	}
}

func TestHandleCreateUpload_LogsCleanupFailureAfterReRegisterFailure(t *testing.T) {
	h, st := newInternalTestHandler(t)
	const localID = "REREGISTER-FAIL/L0/000"
	if rec := createUploadRequestForTest(h, localID); rec.Code != http.StatusCreated {
		t.Fatalf("initial create status = %d: %s", rec.Code, rec.Body.String())
	}

	uploads, _, err := st.ListByDateRange(time.Time{}, time.Now(), "", 10, "")
	if err != nil || len(uploads) != 1 {
		t.Fatalf("ListByDateRange: uploads=%d err=%v", len(uploads), err)
	}
	if _, err := st.UpdateStatus(uploads[0].ID, store.StatusBackendLost); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}

	cleanupFailure := fmt.Errorf("cleanup failure: %w", errInjectedTestFailure)
	reRegisterFailure := fmt.Errorf("re-register failure: %w", errInjectedTestFailure)
	h.store = failingStore{Store: st, putErr: nil, reRegisterErr: reRegisterFailure}
	backend, ok := h.backend.(*uploadbackend.TUSHandler)
	if !ok {
		t.Fatalf("h.backend is not *uploadbackend.TUSHandler: %T", h.backend)
	}
	h.backend = failingBackend{
		TUSHandler:   backend,
		terminateErr: cleanupFailure,
	}
	logs, restoreLogs := captureLogs(t)
	defer restoreLogs()
	rec := createUploadRequestForTest(h, localID)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
	if !strings.Contains(logs.String(), "failed to terminate tusd upload") {
		t.Fatalf("cleanup failure was not logged: %s", logs.String())
	}
}

func newInternalTestHandler(t *testing.T) (*Handler, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	storagePath := t.TempDir()
	bk, err := uploadbackend.New(storagePath)
	if err != nil {
		t.Fatalf("uploadbackend.New: %v", err)
	}
	return NewHandler(st, bk, storagePath), st
}

func createUploadRequestForTest(h *Handler, localID string) *httptest.ResponseRecorder {
	body := `{"local_identifier":"` + localID +
		`","filename":"test.jpg","creation_date":"2024-03-15T00:00:00Z"}`
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/uploads", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.HandleCreateUpload(rec, req)
	return rec
}

func captureLogs(t *testing.T) (*bytes.Buffer, func()) {
	t.Helper()
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	return &logs, func() { log.SetOutput(previous) }
}

type failingStore struct {
	*store.Store

	putErr        error
	reRegisterErr error
}

func (s failingStore) PutUploadIfAbsent(upload *store.Upload) (*store.Upload, bool, error) {
	if s.putErr != nil {
		return nil, false, s.putErr
	}
	existing, created, err := s.Store.PutUploadIfAbsent(upload)
	if err != nil {
		return nil, false, fmt.Errorf("put upload if absent: %w", err)
	}
	return existing, created, nil
}

func (s failingStore) ReRegister(id, backendID string) (*store.Upload, error) {
	if s.reRegisterErr != nil {
		return nil, s.reRegisterErr
	}
	upload, err := s.Store.ReRegister(id, backendID)
	if err != nil {
		return nil, fmt.Errorf("re-register: %w", err)
	}
	return upload, nil
}

type failingBackend struct {
	*uploadbackend.TUSHandler

	terminateErr error
}

func (b failingBackend) TerminateOrCleanup(context.Context, string) error { return b.terminateErr }
