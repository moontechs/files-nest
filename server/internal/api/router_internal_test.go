package api

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func captureRouterLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(previous) })
	return &buf
}

func TestRequestLogMiddlewareLevels(t *testing.T) {
	tests := []struct {
		name   string
		status int
		level  string
	}{
		{name: "success", status: http.StatusOK, level: "DEBUG "},
		{name: "client error", status: http.StatusNotFound, level: "WARN "},
		{name: "server error", status: http.StatusInternalServerError, level: "ERROR "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := captureRouterLogs(t)
			handler := requestLogMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
			}))
			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			handler.ServeHTTP(httptest.NewRecorder(), req)

			if got := logs.String(); !strings.Contains(got, tt.level) {
				t.Errorf("log = %q, want level prefix %q", got, tt.level)
			}
		})
	}
}

func TestRecoveryMiddlewareLogsError(t *testing.T) {
	logs := captureRouterLogs(t)
	handler := recoveryMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/panic", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if got := logs.String(); !strings.Contains(got, "ERROR panic recovered: boom") {
		t.Errorf("log = %q, want ERROR panic prefix", got)
	}
}
