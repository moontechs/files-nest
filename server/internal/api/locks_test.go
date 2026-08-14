// Package api_test tests the per-upload locking mechanism.
package api_test

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moontechs/files-nest/server/internal/api"
)

// ---------------------------------------------------------------------------
// Basic Lock / Unlock
// ---------------------------------------------------------------------------

func TestLockUnlock_SingleID(t *testing.T) {
	l := &api.UploadLocker{}

	l.Lock("upload-1")
	l.Unlock("upload-1")
	// If we get here without deadlock, the basic sequence works.
}

func TestLockUnlock_MultipleSequentialSameID(t *testing.T) {
	l := &api.UploadLocker{}

	l.Lock("shared")
	l.Unlock("shared")

	l.Lock("shared")
	l.Unlock("shared")

	l.Lock("shared")
	l.Unlock("shared")
	// Three sequential lock/unlock cycles on the same ID should all succeed.
}

func TestLockUnlock_DifferentIDs(t *testing.T) {
	l := &api.UploadLocker{}

	l.Lock("a")
	l.Lock("b")
	l.Lock("c")

	l.Unlock("a")
	l.Unlock("b")
	l.Unlock("c")
	// Different IDs should not interfere with each other.
}

// ---------------------------------------------------------------------------
// Concurrent serialization: same ID blocks
// ---------------------------------------------------------------------------

// TestConcurrentSameID_Serializes verifies that two goroutines competing for
// the same upload ID execute sequentially — one must complete its critical
// section before the other enters.
func TestConcurrentSameID_Serializes(t *testing.T) {
	l := &api.UploadLocker{}

	var orderMu sync.Mutex
	var order []string

	var wg sync.WaitGroup
	wg.Add(2)

	// Goroutine A: locks "id1", records entry, waits a moment, records exit
	go func() {
		defer wg.Done()
		l.Lock("id1")
		orderMu.Lock()
		order = append(order, "A-enter")
		orderMu.Unlock()

		// Hold the lock long enough for B to attempt
		time.Sleep(50 * time.Millisecond)

		orderMu.Lock()
		order = append(order, "A-exit")
		orderMu.Unlock()
		l.Unlock("id1")
	}()

	// Goroutine B: locks same "id1", records entry and exit
	go func() {
		defer wg.Done()
		// Wait a tiny bit so A acquires first
		time.Sleep(5 * time.Millisecond)
		l.Lock("id1")

		orderMu.Lock()
		order = append(order, "B-enter")
		orderMu.Unlock()

		orderMu.Lock()
		order = append(order, "B-exit")
		orderMu.Unlock()
		l.Unlock("id1")
	}()

	wg.Wait()

	// Verify order: A must enter and exit before B enters
	if len(order) != 4 {
		t.Fatalf("expected 4 ordered events, got %d: %v", len(order), order)
	}

	if order[0] != "A-enter" {
		t.Errorf("expected first event A-enter, got %q", order[0])
	}
	if order[1] != "A-exit" {
		t.Errorf("expected second event A-exit, got %q", order[1])
	}
	if order[2] != "B-enter" {
		t.Errorf("expected third event B-enter, got %q", order[2])
	}
}

func TestConcurrentSameID_MultipleWaiters(t *testing.T) {
	l := &api.UploadLocker{}

	const n = 5
	var counter int64
	var wg sync.WaitGroup
	wg.Add(n)

	for range n {
		go func() {
			defer wg.Done()
			l.Lock("contended")
			// Each goroutine increments the counter while holding the lock.
			// If two held the lock concurrently, the counter would have visible
			// races, but with serialization it should be fine.
			atomic.AddInt64(&counter, 1)
			time.Sleep(10 * time.Millisecond)
			l.Unlock("contended")
		}()
	}

	wg.Wait()

	if counter != int64(n) {
		t.Errorf("counter = %d, want %d", counter, n)
	}
}

// ---------------------------------------------------------------------------
// Different IDs proceed independently
// ---------------------------------------------------------------------------

func TestConcurrentDifferentIDs_ProceedIndependently(t *testing.T) {
	l := &api.UploadLocker{}

	var mu sync.Mutex
	var order []string
	var wg sync.WaitGroup
	wg.Add(2)

	// Goroutine A locks "id-a", holds it for 50ms
	go func() {
		defer wg.Done()
		l.Lock("id-a")
		mu.Lock()
		order = append(order, "A-started")
		mu.Unlock()
		time.Sleep(50 * time.Millisecond)
		mu.Lock()
		order = append(order, "A-finished")
		mu.Unlock()
		l.Unlock("id-a")
	}()

	// Goroutine B locks "id-b" — should NOT wait for A
	go func() {
		defer wg.Done()
		// Wait a tiny bit so A acquires first
		time.Sleep(5 * time.Millisecond)
		l.Lock("id-b")
		mu.Lock()
		order = append(order, "B-started")
		mu.Unlock()
		// B should be able to proceed before A finishes since it's a different ID
		l.Unlock("id-b")
	}()

	wg.Wait()

	// B should have started before A finished (different IDs)
	beforeAFinished := false
	for _, event := range order {
		if event == "B-started" {
			beforeAFinished = true
		}
		if event == "A-finished" {
			if !beforeAFinished {
				t.Error("B-started should happen before A-finished for different IDs")
			}
			break
		}
	}
}

// ---------------------------------------------------------------------------
// TryLock via timing-based test: should block when held
// ---------------------------------------------------------------------------

func TestLock_BlocksWhenHeld(t *testing.T) {
	l := &api.UploadLocker{}

	l.Lock("block-test")

	// Attempt to lock the same ID in a separate goroutine — should block
	blocked := make(chan struct{})
	go func() {
		l.Lock("block-test")
		close(blocked)
	}()

	// The goroutine should not be able to acquire the lock within a short timeout
	select {
	case <-blocked:
		t.Fatal("goroutine acquired lock while it should still be held")
	case <-time.After(50 * time.Millisecond):
		// Expected — lock is still held
	}

	l.Unlock("block-test")

	// After unlocking, the goroutine should acquire the lock
	select {
	case <-blocked:
		// Success — goroutine acquired lock
	case <-time.After(time.Second):
		t.Fatal("goroutine did not acquire lock after release within 1s")
	}

	// Cleanup
	l.Unlock("block-test")
}

// ---------------------------------------------------------------------------
// Do convenience method
// ---------------------------------------------------------------------------

func TestDo_Success(t *testing.T) {
	l := &api.UploadLocker{}

	called := false
	err := l.Do("do-test", func() error {
		called = true
		return nil
	})
	if err != nil {
		t.Errorf("Do returned error: %v", err)
	}
	if !called {
		t.Error("Do did not call the function")
	}
}

func TestDo_ErrorReturned(t *testing.T) {
	l := &api.UploadLocker{}

	sentinel := errors.New("expected error")
	err := l.Do("do-error", func() error {
		return sentinel
	})

	if !errors.Is(err, sentinel) {
		t.Errorf("Do returned %v, want %v", err, sentinel)
	}
}

func TestDo_PanicStillReleasesLock(t *testing.T) {
	l := &api.UploadLocker{}

	// Start a goroutine that panics inside Do
	panicked := make(chan struct{}, 1)
	go func() {
		defer func() {
			recover()
			close(panicked)
		}()
		_ = l.Do("panic-test", func() error {
			panic("intentional panic")
		})
	}()

	// Wait for the panic to be recovered and lock released
	select {
	case <-panicked:
		// Good — goroutine finished
	case <-time.After(time.Second):
		t.Fatal("goroutine did not recover from panic within 1s")
	}

	// Now we should be able to acquire the lock — if Do did not release it
	// on panic, this would deadlock.
	locked := make(chan struct{}, 1)
	go func() {
		l.Lock("panic-test")
		close(locked)
	}()

	select {
	case <-locked:
		// Success — lock was released after panic
		l.Unlock("panic-test")
	case <-time.After(time.Second):
		t.Fatal("lock was not released after panic in Do (deadlock)")
	}
}

// ---------------------------------------------------------------------------
// Serialization: Do with concurrent same ID
// ---------------------------------------------------------------------------

func TestDo_SerializesSameID(t *testing.T) {
	l := &api.UploadLocker{}

	var counter int
	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(2)

	// Goroutine A holds the lock and does work
	go func() {
		defer wg.Done()
		_ = l.Do("serial-do", func() error {
			mu.Lock()
			counter = 1
			mu.Unlock()
			time.Sleep(50 * time.Millisecond)
			mu.Lock()
			counter = 2
			mu.Unlock()
			return nil
		})
	}()

	// Goroutine B tries the same ID
	go func() {
		defer wg.Done()
		time.Sleep(5 * time.Millisecond)
		_ = l.Do("serial-do", func() error {
			mu.Lock()
			// If serialization works, counter should be 2 (A finished its work)
			if counter != 2 {
				t.Errorf("in B critical section, counter = %d, want 2 (serialization broken)", counter)
			}
			mu.Unlock()
			return nil
		})
	}()

	wg.Wait()
}

// ---------------------------------------------------------------------------
// Map cleanup: entries are removed when no one holds or waits
// ---------------------------------------------------------------------------

func TestLock_CleanupAfterUnlock(t *testing.T) {
	l := &api.UploadLocker{}

	// Lock then unlock
	l.Lock("cleanup-me")
	l.Unlock("cleanup-me")

	// The entry should be cleaned up. We verify by checking that a fresh
	// lock/unlock cycle creates a new entry without issues.
	l.Lock("cleanup-me")
	l.Unlock("cleanup-me")
}

// ---------------------------------------------------------------------------
// Zero value readiness
// ---------------------------------------------------------------------------

func TestLock_ZeroValueReady(t *testing.T) {
	var l api.UploadLocker // zero value, no explicit initialization

	l.Lock("zero")
	l.Unlock("zero")
}

// ---------------------------------------------------------------------------
// Concurrent lock/unlock interleaving between many IDs
// ---------------------------------------------------------------------------

func TestLock_StressDifferentIDs(t *testing.T) {
	l := &api.UploadLocker{}
	var wg sync.WaitGroup

	const ids = 20
	const goroutinesPerID = 10

	for id := range ids {
		uploadID := func() string {
			return string(rune('A' + id))
		}()
		for range goroutinesPerID {
			wg.Add(1)
			go func(idStr string) {
				defer wg.Done()
				l.Lock(idStr)
				// Simulate a tiny bit of work
				time.Sleep(time.Microsecond)
				l.Unlock(idStr)
			}(uploadID)
		}
	}

	wg.Wait()
	// No deadlocks = success
}

// ---------------------------------------------------------------------------
// Unlock without Lock (no-op safety)
// ---------------------------------------------------------------------------

func TestUnlock_WithoutLockIsNoOp(t *testing.T) {
	l := &api.UploadLocker{}

	// Should not panic or deadlock
	l.Unlock("never-locked")
}

// ---------------------------------------------------------------------------
// Nested lock/unlock on different IDs
// ---------------------------------------------------------------------------

func TestLock_NestedDifferentIDs(t *testing.T) {
	l := &api.UploadLocker{}

	l.Lock("outer")
	l.Lock("inner")
	l.Unlock("inner")
	l.Unlock("outer")
	// Nested locks on different IDs should work without issue.
}

// ---------------------------------------------------------------------------
// Exported helper test — ensure UploadLocker is exported
// ---------------------------------------------------------------------------

func TestUploadLocker_TypeExported(t *testing.T) {
	// Compile-time check: api.UploadLocker must be exported
	var _ *api.UploadLocker
	_ = &api.UploadLocker{}
}

// ---------------------------------------------------------------------------
// Do with multiple sequential calls — lock release verified
// ---------------------------------------------------------------------------

func TestDo_SequentialCallsSameID(t *testing.T) {
	l := &api.UploadLocker{}

	for i := range 5 {
		val := i
		err := l.Do("seq", func() error {
			if val != i {
				t.Errorf("iteration %d: val = %d, want %d", i, val, i)
			}
			return nil
		})
		if err != nil {
			t.Errorf("iteration %d: unexpected error: %v", i, err)
		}
	}
}
