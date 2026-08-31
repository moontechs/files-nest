package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/moontechs/files-nest/server/internal/api"
)

// TestConcurrencyLimiter_SingleRequestSucceeds verifies that a request
// through an otherwise-idle limiter is admitted and next is invoked.
func TestConcurrencyLimiter_SingleRequestSucceeds(t *testing.T) {
	l := api.NewConcurrencyLimiter(1)

	called := make(chan struct{}, 1)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called <- struct{}{}
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPatch, "/uploads/abc/data", nil)
	rec := httptest.NewRecorder()
	l.Middleware(next).ServeHTTP(rec, req)

	select {
	case <-called:
	default:
		t.Fatal("next was not called")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

// TestConcurrencyLimiter_CapacityEnforced verifies that with capacity N,
// N concurrent requests all reach next while the (N+1)th is rejected
// with 503 and a Retry-After header. Held requests are forced to overlap
// by blocking inside next until released.
func TestConcurrencyLimiter_CapacityEnforced(t *testing.T) {
	const capN = 3
	l := api.NewConcurrencyLimiter(capN)

	entered := make(chan struct{}, capN)
	release := make(chan struct{})
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		entered <- struct{}{}
		<-release // keep the slot occupied until released
		w.WriteHeader(http.StatusOK)
	})
	h := l.Middleware(next)

	var wg sync.WaitGroup
	for range capN {
		wg.Go(func() {
			req := httptest.NewRequestWithContext(context.Background(), http.MethodPatch, "/uploads/abc/data", nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Errorf("held request status = %d, want 200", rec.Code)
			}
		})
	}

	// Wait until all N requests are inside next (all slots occupied).
	for range capN {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for held requests to enter next")
		}
	}

	// The (N+1)th request must be rejected immediately.
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPatch, "/uploads/abc/data", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("over-cap status = %d, want 503", rec.Code)
	}
	if ra := rec.Header().Get("Retry-After"); ra != "1" {
		t.Errorf("Retry-After = %q, want \"1\"", ra)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want %q", ct, "application/json")
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("over-cap body is not JSON: %v", err)
	}
	if body["error"] != "too many concurrent uploads" {
		t.Errorf("over-cap error = %q, want %q", body["error"], "too many concurrent uploads")
	}

	// Release the held requests; they should all complete successfully.
	close(release)
	wg.Wait()
}

// TestConcurrencyLimiter_SlotReleased verifies that after a held request
// completes and releases its slot, a subsequent request is admitted.
func TestConcurrencyLimiter_SlotReleased(t *testing.T) {
	l := api.NewConcurrencyLimiter(1)

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	done := make(chan struct{})
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		entered <- struct{}{}
		<-release // occupy the slot until released
		w.WriteHeader(http.StatusOK)
	})
	h := l.Middleware(next)

	// Occupy the single slot.
	go func() {
		defer close(done)
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPatch, "/uploads/abc/data", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the held request to enter next")
	}

	// A second request while the slot is held must be rejected.
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPatch, "/uploads/abc/data", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("over-cap status = %d, want 503", rec.Code)
	}
	if ra := rec.Header().Get("Retry-After"); ra != "1" {
		t.Errorf("Retry-After = %q, want \"1\"", ra)
	}

	// Release the slot and wait for the holding request to finish.
	close(release)
	<-done

	// A subsequent request must now succeed.
	req2 := httptest.NewRequestWithContext(context.Background(), http.MethodPatch, "/uploads/abc/data", nil)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("post-release status = %d, want 200", rec2.Code)
	}
}
