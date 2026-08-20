package api

import (
	"log"
	"net/http"
)

// ConcurrencyLimiter bounds the number of concurrently in-flight HTTP
// requests that pass through its Middleware. It is implemented as a
// buffered-channel semaphore: acquiring a slot is a non-blocking send
// into the channel, so requests over the configured cap are rejected
// immediately rather than queued (see ADR-0003).
//
// The limiter is stateless between requests apart from the channel,
// so a single instance can be shared across the whole router. The cap
// is fixed at construction time and exposed read-only via Cap().
type ConcurrencyLimiter struct {
	slots chan struct{}
}

// NewConcurrencyLimiter returns a limiter allowing at most max
// concurrent in-flight requests. max must be >= 1; the caller
// (main.go) is responsible for validating the configured value
// before constructing the limiter.
func NewConcurrencyLimiter(max int) *ConcurrencyLimiter {
	return &ConcurrencyLimiter{slots: make(chan struct{}, max)}
}

// Cap returns the configured concurrency limit of the limiter. It is
// the single source of truth for the limit's value (obtained from the
// channel capacity rather than duplicated state).
func (l *ConcurrencyLimiter) Cap() int {
	return cap(l.slots)
}

// Middleware wraps next so that at most Cap() requests run
// concurrently. Requests over the cap are rejected immediately with
// 503 Service Unavailable, a Retry-After of 1 second, and a JSON
// error body; next is not invoked for them. On success, the slot is
// released (via defer) when next.ServeHTTP returns, so a panicking
// handler cannot leak a slot.
func (l *ConcurrencyLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case l.slots <- struct{}{}:
			defer func() { <-l.slots }()
		default:
			log.Printf("rejected upload: over concurrency limit (cap=%d)", l.Cap())
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusServiceUnavailable, "too many concurrent uploads")
			return
		}

		next.ServeHTTP(w, r)
	})
}
