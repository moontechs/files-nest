//go:build e2e

// Package e2e provides black-box end-to-end tests for the iCloud Backup
// server. This file verifies the server-side concurrent-upload limit
// (issue #24): the `MAX_CONCURRENT_UPLOADS` cap (default 4) applied to
// `PATCH /uploads/{id}/data`, over-cap requests rejected with 503 +
// Retry-After, and the `GET /config` discovery endpoint.
//
// Over real HTTP, goroutines launched near-simultaneously do NOT guarantee
// the server sees them concurrently — a fast PATCH can complete before its
// siblings even arrive, accidentally serializing what should be a concurrency
// test. To force genuine overlap we stream each PATCH body from a slow reader
// that dribbles bytes with a per-chunk delay, keeping every admitted request
// in-flight long enough for its peers to be admitted/rejected first.
//
// Each test fires PATCHes against DISTINCT freshly-created upload IDs so the
// concurrency limiter is exercised in isolation, deliberately avoiding the
// unrelated per-upload-ID `UploadLocker` mutex.
package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Slow streaming body — the concurrency-overlap mechanism
// ---------------------------------------------------------------------------

// slowReader is an io.Reader that yields exactly total bytes in chunks,
// sleeping delay between every read. Serving a PATCH body from a slowReader
// stretches the request's in-flight time so concurrent requests genuinely
// overlap at the server rather than racing to completion in series.
type slowReader struct {
	remaining int
	chunk     int
	delay     time.Duration
}

// Read sleeps delay, then returns the next chunk (up to cap(p), bounded by
// the remaining bytes). It returns io.EOF once all bytes have been emitted.
func (r *slowReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, io.EOF
	}
	time.Sleep(r.delay)

	n := r.chunk
	if n > len(p) {
		n = len(p)
	}
	if n > r.remaining {
		n = r.remaining
	}
	for i := 0; i < n; i++ {
		p[i] = byte((i*31 + 7) % 256)
	}
	r.remaining -= n
	return n, nil
}

// overlapBody returns a slowReader sized and throttled to hold a PATCH
// in-flight for roughly ~300ms — long enough for a burst of sibling requests
// to reach the server while admitted slots are still occupied.
//
//	32KB at 1KB per read with 10ms between reads → 32 reads × 10ms ≈ 320ms.
//
// Tune these constants together: shrinking totalBytes or delay shrinks the
// overlap window and risks the burst serializing; growing them wastes CI time
// without making the test more correct.
func overlapBody() io.Reader {
	return &slowReader{
		remaining: 32 * 1024,
		chunk:     1024,
		delay:     10 * time.Millisecond,
	}
}

// ---------------------------------------------------------------------------
// Concurrent PATCH helper
// ---------------------------------------------------------------------------

// concurrentPatchResult captures everything a test needs from one slow PATCH.
type concurrentPatchResult struct {
	id         string
	statusCode int
	retryAfter string
	offset     int64
	err        error
}

// slowPatch performs a single slow-streamed PATCH to /uploads/{id}/data from
// offset 0 with Upload-Length set to totalBytes (so a successful write
// completes the upload). It reports the HTTP status, Retry-After header, and
// final Upload-Offset without failing the test itself.
func slowPatch(id string, totalBytes int, body io.Reader) concurrentPatchResult {
	req, err := http.NewRequest("PATCH", serverURL+"/uploads/"+id+"/data", body)
	if err != nil {
		return concurrentPatchResult{id: id, err: err}
	}
	req.Header.Set("Content-Type", "application/offset+octet-stream")
	req.Header.Set("Upload-Offset", "0")
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Length", strconv.Itoa(totalBytes))
	if backupUser != "" || backupPass != "" {
		req.SetBasicAuth(backupUser, backupPass)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return concurrentPatchResult{id: id, err: err}
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	r := concurrentPatchResult{
		id:         id,
		statusCode: resp.StatusCode,
		retryAfter: resp.Header.Get("Retry-After"),
	}
	if off := resp.Header.Get("Upload-Offset"); off != "" {
		r.offset, _ = strconv.ParseInt(off, 10, 64)
	}
	return r
}

// fireSlowPatches creates count distinct uploads, then simultaneously PATCHes
// a slow body (overlapBody) to each and returns one result per upload, ordered
// to match the created IDs. Because every upload ID is distinct, the results
// exercise only the global concurrency limiter.
func fireSlowPatches(t testing.TB, count, totalBytes int) []concurrentPatchResult {
	t.Helper()

	uploads := make([]*CreateUploadResponse, count)
	for i := 0; i < count; i++ {
		cr := CreateTestUpload(t,
			MakeLocalIdentifier(t, fmt.Sprintf("conc-%02d", i)),
			fmt.Sprintf("IMG_conc_%02d.jpg", i))
		uploads[i] = cr
	}

	results := make([]concurrentPatchResult, count)
	var wg sync.WaitGroup
	var mu sync.Mutex

	for i, cr := range uploads {
		i, cr := i, cr
		wg.Add(1)
		go func() {
			defer wg.Done()
			res := slowPatch(cr.ID, totalBytes, overlapBody())
			mu.Lock()
			results[i] = res
			mu.Unlock()
		}()
	}
	wg.Wait()

	return results
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// concurrencyTotal is the byte size used for every slow PATCH body. Each
// admitted write therefore completes and its Upload-Offset equals this value.
const concurrencyTotal = 32 * 1024

// TestConcurrency_SingleUploadBaseline verifies the simplest case: one (slow)
// PATCH /uploads/{id}/data against an otherwise-idle server succeeds with 204
// and the expected Upload-Offset. Everything above builds on this.
func TestConcurrency_SingleUploadBaseline(t *testing.T) {
	cr := CreateTestUpload(t, MakeLocalIdentifier(t, t.Name()), "IMG_conc_baseline.jpg")

	res := slowPatch(cr.ID, concurrencyTotal, overlapBody())
	require.NoError(t, res.err, "single PATCH should not error")
	require.Equal(t, http.StatusNoContent, res.statusCode,
		"single PATCH /uploads/{id}/data should return 204")
	require.Equal(t, int64(concurrencyTotal), res.offset,
		"Upload-Offset must equal the full body length after success")
	require.Empty(t, res.retryAfter,
		"an admitted request must not carry a Retry-After header")
}

// TestConcurrency_ExactlyAtCapSucceeds verifies that exactly the default cap
// (4) of concurrent uploads — fired simultaneously against four distinct
// upload IDs — all succeed. This confirms the boundary case: at-cap requests
// are admitted, not spuriously rejected.
func TestConcurrency_ExactlyAtCapSucceeds(t *testing.T) {
	results := fireSlowPatches(t, 4, concurrencyTotal)
	require.Len(t, results, 4)

	for _, res := range results {
		require.NoError(t, res.err, "upload %s should not error", res.id)
		require.Equal(t, http.StatusNoContent, res.statusCode,
			"PATCH %s at the configured cap should return 204", res.id)
		require.Equal(t, int64(concurrencyTotal), res.offset,
			"PATCH %s should have written the full body", res.id)
	}
}

// TestConcurrency_OverCapRejected verifies that more than the default cap (4)
// of concurrent uploads — six fired simultaneously against distinct IDs —
// results in exactly four successes and two 503 rejections carrying a
// Retry-After header. >4 simultaneously in-flight exceeds the cap of 4.
func TestConcurrency_OverCapRejected(t *testing.T) {
	results := fireSlowPatches(t, 6, concurrencyTotal)
	require.Len(t, results, 6)

	var admitted, rejected int
	for _, res := range results {
		require.NoError(t, res.err, "upload %s should not error", res.id)
		switch res.statusCode {
		case http.StatusNoContent:
			admitted++
			require.Equal(t, int64(concurrencyTotal), res.offset,
				"admitted PATCH %s should have written the full body", res.id)
		case http.StatusServiceUnavailable:
			rejected++
			require.Equal(t, "1", res.retryAfter,
				"rejected PATCH %s must carry Retry-After: 1", res.id)
			require.Zero(t, res.offset,
				"rejected PATCH %s must not have written any data", res.id)
		default:
			t.Fatalf("PATCH %s returned unexpected status %d", res.id, res.statusCode)
		}
	}

	require.Equal(t, 4, admitted,
		"exactly the configured cap (4) concurrent uploads must be admitted")
	require.Equal(t, 2, rejected,
		"requests beyond the configured cap (4) must be rejected with 503")
}

// TestConcurrency_ConfigEndpoint verifies that GET /config (authenticated)
// advertises the server's effective concurrency cap. The e2e Compose stack
// deliberately leaves MAX_CONCURRENT_UPLOADS unset, so the deployed server
// falls back to the documented default of 4.
func TestConcurrency_ConfigEndpoint(t *testing.T) {
	req, err := http.NewRequest("GET", serverURL+"/config", nil)
	require.NoError(t, err, "create GET /config request")
	if backupUser != "" || backupPass != "" {
		req.SetBasicAuth(backupUser, backupPass)
	}

	resp, err := httpClient.Do(req)
	require.NoError(t, err, "GET /config should not error")
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode,
		"GET /config should return 200")
	require.Equal(t, "application/json", resp.Header.Get("Content-Type"),
		"GET /config must return application/json")

	var cfg struct {
		MaxConcurrentUploads int `json:"maxConcurrentUploads"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&cfg),
		"GET /config body should be valid JSON")
	require.Equal(t, 4, cfg.MaxConcurrentUploads,
		"GET /config must advertise the default cap of 4")
}
