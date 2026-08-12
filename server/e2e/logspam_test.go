//go:build e2e

// Package e2e provides black-box end-to-end tests for the iCloud Backup
// server. This file adds the missing black-box regression test for issue #8:
// the embedded tusd handler used to log `NetworkTimeoutError ... error="feature
// not supported"` WARN spam on every read tick of a chunked PATCH. The fix
// (a no-op-deadline recorder adapter) is exercised here over real HTTP,
// through Caddy, into the containerized server, with the real logger that
// main.go configures — the exact path the original bug report came from.
//
// The test is opt-in like the on-disk introspection in storage_test.go: it
// runs only when COMPOSE_PROJECT_NAME (or E2E_COMPOSE_PROJECT) is set and the
// docker CLI is available, so the suite still works against a remote
// SERVER_URL with no local compose stack to inspect.
package e2e

import (
	"bytes"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestLogSpam_NoNetworkTimeoutErrorDuringChunkedPatch reproduces issue #8 at
// the black-box layer: one chunked PATCH over real HTTP, then reads the
// server container's logs and asserts the real tusd logger both emitted its
// usual per-chunk Info line (positive control) and emitted no
// NetworkTimeoutError WARN (the actual regression).
func TestLogSpam_NoNetworkTimeoutErrorDuringChunkedPatch(t *testing.T) {
	storage := requireDockerStorage(t)

	localID := MakeLocalIdentifier(t, t.Name())
	cr := CreateTestUpload(t, localID, "IMG_log_spam_check.jpg")
	require.Equal(t, "uploading", cr.Status)

	// 256KB payload: tusd's writeChunk reads body data in ~32KB ticks
	// (io.Copy's default buffer in the filestore backend), so this drives
	// ~8 deadline-setting ticks (×2 deadline calls each) through the real
	// logger — comfortably enough to reproduce issue #8 pre-fix.
	payload := make([]byte, 256*1024)
	for i := range payload {
		payload[i] = byte((i*31 + 7) % 256)
	}

	length := int64(len(payload))
	patchResp, status, err := PatchUploadData(cr.ID, bytes.NewReader(payload), 0,
		strconv.FormatInt(length, 10))
	require.NoError(t, err, "PATCH /uploads/{id}/data should not error")
	require.Equal(t, http.StatusNoContent, status,
		"PATCH /uploads/{id}/data should return 204")
	require.Equal(t, length, patchResp.UploadOffset,
		"Upload-Offset must equal data length after the full write")

	// Read the full server container log history.
	logs := storage.logs(t)

	// Positive control: tusd logs ChunkWriteStart (Info) on every writeChunk
	// call. Its absence would mean the logger output is not reaching
	// `docker compose logs` for this run, making the negative assertion below
	// pass vacuously.
	require.True(t, strings.Contains(logs, "ChunkWriteStart"),
		"server container logs should contain tusd ChunkWriteStart (logger output not reaching docker compose logs?)")

	// Negative control: scan line by line so a failure reports only the
	// offending lines (plus a count), not a potentially multi-megabyte dump.
	var matches []string
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, "NetworkTimeoutError") {
			matches = append(matches, line)
		}
	}
	require.Empty(t, matches,
		"server container logs contain %d line(s) matching NetworkTimeoutError:\n%s",
		len(matches), strings.Join(matches, "\n"))
}
