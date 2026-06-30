// Package api provides HTTP handlers, middleware, and shared utilities
// for the iCloud Backup server API.
package api

import "sync"

// UploadLocker provides per-upload in-memory locking to serialize
// concurrent operations on the same upload within the same process.
// It complements BadgerDB's transactional isolation by preventing
// interleaved PATCH /data, PATCH /status, and DELETE operations
// that BadgerDB alone cannot serialize at the application level.
//
// The zero value is ready to use. Call Lock before operating on an
// upload and Unlock when done, typically via defer:
//
//	func (h *Handler) handleSomething(w http.ResponseWriter, r *http.Request) {
//	    id := r.PathValue("id") // Go 1.22+ ServeMux pattern {id}
//	    h.locks.Lock(id)
//	    defer h.locks.Unlock(id)
//	    // ... critical section
//	}
//
// Locks are reference-counted and automatically cleaned up from the
// internal map when no goroutine holds or waits for them.
type UploadLocker struct {
	mu    sync.Mutex
	locks map[string]*uploadLock
}

// uploadLock pairs a sync.Mutex with a reference count. The refCount
// tracks how many goroutines are holding or waiting to hold the inner
// Mutex, so the map entry can be safely deleted when no one needs it.
type uploadLock struct {
	sync.Mutex
	refCount int
}

// Lock acquires an exclusive lock for the given upload ID. It blocks
// until the lock is available. Must be paired with a corresponding
// call to Unlock.
func (l *UploadLocker) Lock(id string) {
	l.mu.Lock()
	if l.locks == nil {
		l.locks = make(map[string]*uploadLock)
	}
	ul, ok := l.locks[id]
	if !ok {
		ul = &uploadLock{}
		l.locks[id] = ul
	}
	ul.refCount++
	l.mu.Unlock()

	ul.Lock()
}

// Unlock releases the lock for the given upload ID. It is safe to
// call even if no Lock was acquired for the ID — in that case it is
// a no-op. Calling Unlock without a matching Lock is a programming
// error that may cause the underlying mutex to panic.
func (l *UploadLocker) Unlock(id string) {
	l.mu.Lock()
	ul, ok := l.locks[id]
	if !ok {
		l.mu.Unlock()
		return
	}
	ul.Unlock()
	ul.refCount--
	if ul.refCount <= 0 {
		delete(l.locks, id)
	}
	l.mu.Unlock()
}

// Do acquires the lock for id, calls fn, and releases the lock when
// fn returns — even if fn panics. It returns the error from fn.
// This is a convenience wrapper around Lock/Unlock for callers that
// want a scoped critical section without an explicit defer.
func (l *UploadLocker) Do(id string, fn func() error) (err error) {
	l.Lock(id)
	defer l.Unlock(id)
	return fn()
}
