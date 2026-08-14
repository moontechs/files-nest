package store_test

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/moontechs/files-nest/server/internal/store"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// testIntent creates a minimal CompletionIntent for use in tests.
func testIntent(id string) *store.CompletionIntent {
	return &store.CompletionIntent{
		ID:        id,
		BackendID: "tusd-" + id,
		Src:       "/tmp/incoming/" + id,
		Dst:       "/tmp/organized/2024/03/15/IMG_" + id + ".jpg",
		DstRel:    "organized/2024/03/15/IMG_" + id + ".jpg",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
}

// ---------------------------------------------------------------------------
// SaveCompletionIntent
// ---------------------------------------------------------------------------

func TestCompletionIntent_SaveAndGet(t *testing.T) {
	s := openTestStore(t)
	id := "test-intent-1"

	intent := &store.CompletionIntent{
		ID:        id,
		BackendID: "tusd-backend-123",
		Src:       "/tmp/incoming/abc123",
		Dst:       "/tmp/organized/2024/03/15/IMG_1234.jpg",
		DstRel:    "organized/2024/03/15/IMG_1234.jpg",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}

	if err := s.SaveCompletionIntent(intent); err != nil {
		t.Fatalf("SaveCompletionIntent failed: %v", err)
	}

	got, err := s.GetCompletionIntent(id)
	if err != nil {
		t.Fatalf("GetCompletionIntent failed: %v", err)
	}
	if got == nil {
		t.Fatal("GetCompletionIntent returned nil")
	}
	if got.ID != intent.ID {
		t.Errorf("ID: got %q, want %q", got.ID, intent.ID)
	}
	if got.BackendID != intent.BackendID {
		t.Errorf("BackendID: got %q, want %q", got.BackendID, intent.BackendID)
	}
	if got.Src != intent.Src {
		t.Errorf("Src: got %q, want %q", got.Src, intent.Src)
	}
	if got.Dst != intent.Dst {
		t.Errorf("Dst: got %q, want %q", got.Dst, intent.Dst)
	}
	if got.DstRel != intent.DstRel {
		t.Errorf("DstRel: got %q, want %q", got.DstRel, intent.DstRel)
	}
	if got.CreatedAt != intent.CreatedAt {
		t.Errorf("CreatedAt: got %q, want %q", got.CreatedAt, intent.CreatedAt)
	}
}

func TestCompletionIntent_SaveAndGet_AllFields(t *testing.T) {
	s := openTestStore(t)

	// Test with various field values — IDs with special chars, deep paths, etc.
	tests := []struct {
		name   string
		intent *store.CompletionIntent
	}{
		{
			name:   "simple id and path",
			intent: testIntent("intent-001"),
		},
		{
			name: "id with slashes-like pattern",
			intent: &store.CompletionIntent{
				ID:        "asset-ABC123/L0/000",
				BackendID: "tusd-asset-ABC123",
				Src:       "/storage/incoming/tusd-asset-ABC123",
				Dst:       "/storage/organized/2024/06/15/IMG_9876.jpg",
				DstRel:    "organized/2024/06/15/IMG_9876.jpg",
				CreatedAt: "2024-06-15T10:30:00Z",
			},
		},
		{
			name: "deep nested paths",
			intent: &store.CompletionIntent{
				ID:        "deep-path-intent",
				BackendID: "tusd-deep-123",
				Src:       "/a/very/deep/nested/path/incoming/file.dat",
				Dst:       "/another/very/deep/nested/path/organized/2024/12/31/file.dat",
				DstRel:    "organized/2024/12/31/file.dat",
				CreatedAt: "2024-12-31T23:59:59Z",
			},
		},
		{
			name: "empty optional fields",
			intent: &store.CompletionIntent{
				ID:        "minimal-intent",
				BackendID: "tusd-minimal",
				Src:       "/tmp/src",
				Dst:       "/tmp/dst",
				DstRel:    "",
				CreatedAt: "2024-01-01T00:00:00Z",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := s.SaveCompletionIntent(tt.intent); err != nil {
				t.Fatalf("SaveCompletionIntent failed: %v", err)
			}

			got, err := s.GetCompletionIntent(tt.intent.ID)
			if err != nil {
				t.Fatalf("GetCompletionIntent failed: %v", err)
			}
			if got == nil {
				t.Fatal("GetCompletionIntent returned nil")
			}

			// Verify all fields match
			if got.ID != tt.intent.ID {
				t.Errorf("ID: got %q, want %q", got.ID, tt.intent.ID)
			}
			if got.BackendID != tt.intent.BackendID {
				t.Errorf("BackendID: got %q, want %q", got.BackendID, tt.intent.BackendID)
			}
			if got.Src != tt.intent.Src {
				t.Errorf("Src: got %q, want %q", got.Src, tt.intent.Src)
			}
			if got.Dst != tt.intent.Dst {
				t.Errorf("Dst: got %q, want %q", got.Dst, tt.intent.Dst)
			}
			if got.DstRel != tt.intent.DstRel {
				t.Errorf("DstRel: got %q, want %q", got.DstRel, tt.intent.DstRel)
			}
			if got.CreatedAt != tt.intent.CreatedAt {
				t.Errorf("CreatedAt: got %q, want %q", got.CreatedAt, tt.intent.CreatedAt)
			}
		})
	}
}

func TestCompletionIntent_SaveAndGet_MultipleIntents(t *testing.T) {
	s := openTestStore(t)

	// Save multiple intents and verify each is independently retrievable
	intents := make([]*store.CompletionIntent, 5)
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("multi-intent-%d", i)
		intent := testIntent(id)
		intents[i] = intent
		if err := s.SaveCompletionIntent(intent); err != nil {
			t.Fatalf("SaveCompletionIntent %d failed: %v", i, err)
		}
	}

	// Verify each intent is independently retrievable with correct fields
	for i, want := range intents {
		got, err := s.GetCompletionIntent(want.ID)
		if err != nil {
			t.Fatalf("GetCompletionIntent %d (%q) failed: %v", i, want.ID, err)
		}
		if got == nil {
			t.Fatalf("GetCompletionIntent %d (%q) returned nil", i, want.ID)
		}
		if got.ID != want.ID {
			t.Errorf("intent %d ID: got %q, want %q", i, got.ID, want.ID)
		}
		if got.Src != want.Src {
			t.Errorf("intent %d Src: got %q, want %q", i, got.Src, want.Src)
		}
	}
}

func TestCompletionIntent_Overwrite(t *testing.T) {
	s := openTestStore(t)
	id := "overwrite-intent"

	// Save initial intent
	intent1 := &store.CompletionIntent{
		ID:        id,
		BackendID: "tusd-first",
		Src:       "/tmp/incoming/first",
		Dst:       "/tmp/organized/first.jpg",
		DstRel:    "organized/first.jpg",
		CreatedAt: "2024-01-01T00:00:00Z",
	}
	if err := s.SaveCompletionIntent(intent1); err != nil {
		t.Fatalf("first SaveCompletionIntent failed: %v", err)
	}

	// Overwrite with a different intent (same ID)
	intent2 := &store.CompletionIntent{
		ID:        id,
		BackendID: "tusd-second",
		Src:       "/tmp/incoming/second",
		Dst:       "/tmp/organized/second.jpg",
		DstRel:    "organized/second.jpg",
		CreatedAt: "2024-02-02T00:00:00Z",
	}
	if err := s.SaveCompletionIntent(intent2); err != nil {
		t.Fatalf("second SaveCompletionIntent failed: %v", err)
	}

	// Verify it was overwritten (not appended/duplicated)
	got, err := s.GetCompletionIntent(id)
	if err != nil {
		t.Fatalf("GetCompletionIntent failed: %v", err)
	}
	if got == nil {
		t.Fatal("GetCompletionIntent returned nil")
	}
	if got.BackendID != intent2.BackendID {
		t.Errorf("BackendID: got %q, want %q (should be overwritten)", got.BackendID, intent2.BackendID)
	}
	if got.Src != intent2.Src {
		t.Errorf("Src: got %q, want %q (should be overwritten)", got.Src, intent2.Src)
	}

	// List should return exactly 1 intent
	intents, err := s.ListCompletionIntents()
	if err != nil {
		t.Fatalf("ListCompletionIntents failed: %v", err)
	}
	if len(intents) != 1 {
		t.Errorf("expected 1 intent after overwrite, got %d", len(intents))
	}
}

// ---------------------------------------------------------------------------
// GetCompletionIntent — not found
// ---------------------------------------------------------------------------

func TestCompletionIntent_GetNotFound(t *testing.T) {
	s := openTestStore(t)

	got, err := s.GetCompletionIntent("nonexistent")
	if err != nil {
		t.Fatalf("GetCompletionIntent failed: %v", err)
	}
	if got != nil {
		t.Fatal("expected nil for nonexistent completion intent")
	}
}

func TestCompletionIntent_GetNotFound_AfterDelete(t *testing.T) {
	s := openTestStore(t)
	id := "get-not-found-after-delete"

	intent := testIntent(id)
	if err := s.SaveCompletionIntent(intent); err != nil {
		t.Fatalf("SaveCompletionIntent failed: %v", err)
	}

	if err := s.DeleteCompletionIntent(id); err != nil {
		t.Fatalf("DeleteCompletionIntent failed: %v", err)
	}

	got, err := s.GetCompletionIntent(id)
	if err != nil {
		t.Fatalf("GetCompletionIntent after delete failed: %v", err)
	}
	if got != nil {
		t.Fatal("expected nil after delete")
	}
}

func TestCompletionIntent_GetNotFound_EmptyID(t *testing.T) {
	s := openTestStore(t)

	got, err := s.GetCompletionIntent("")
	if err != nil {
		t.Fatalf("GetCompletionIntent with empty ID failed: %v", err)
	}
	if got != nil {
		t.Fatal("expected nil for empty ID")
	}
}

// ---------------------------------------------------------------------------
// DeleteCompletionIntent
// ---------------------------------------------------------------------------

func TestCompletionIntent_Delete(t *testing.T) {
	s := openTestStore(t)
	id := "test-intent-del"

	intent := &store.CompletionIntent{
		ID:        id,
		BackendID: "tusd-backend-456",
		Src:       "/tmp/incoming/def456",
		Dst:       "/tmp/organized/2024/04/20/IMG_5678.jpg",
		DstRel:    "organized/2024/04/20/IMG_5678.jpg",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}

	if err := s.SaveCompletionIntent(intent); err != nil {
		t.Fatalf("SaveCompletionIntent failed: %v", err)
	}

	if err := s.DeleteCompletionIntent(id); err != nil {
		t.Fatalf("DeleteCompletionIntent failed: %v", err)
	}

	got, err := s.GetCompletionIntent(id)
	if err != nil {
		t.Fatalf("GetCompletionIntent after delete failed: %v", err)
	}
	if got != nil {
		t.Fatal("expected nil after delete")
	}
}

func TestCompletionIntent_Delete_NotFoundIsNoOp(t *testing.T) {
	s := openTestStore(t)

	// Deleting a nonexistent intent should not error — it's already absent.
	err := s.DeleteCompletionIntent("nonexistent-intent")
	if err != nil {
		t.Fatalf("DeleteCompletionIntent on nonexistent should not error, got: %v", err)
	}
}

func TestCompletionIntent_Delete_DoesNotAffectOtherIntents(t *testing.T) {
	s := openTestStore(t)

	// Save three intents
	ids := []string{"keep-a", "delete-b", "keep-c"}
	for _, id := range ids {
		intent := testIntent(id)
		if err := s.SaveCompletionIntent(intent); err != nil {
			t.Fatalf("SaveCompletionIntent %s failed: %v", id, err)
		}
	}

	// Delete the middle one
	if err := s.DeleteCompletionIntent("delete-b"); err != nil {
		t.Fatalf("DeleteCompletionIntent failed: %v", err)
	}

	// First and third should still exist
	for _, id := range []string{"keep-a", "keep-c"} {
		got, err := s.GetCompletionIntent(id)
		if err != nil {
			t.Fatalf("GetCompletionIntent %s failed: %v", id, err)
		}
		if got == nil {
			t.Errorf("intent %s should still exist after deleting other", id)
		}
	}

	// Deleted should be gone
	got, err := s.GetCompletionIntent("delete-b")
	if err != nil {
		t.Fatalf("GetCompletionIntent delete-b failed: %v", err)
	}
	if got != nil {
		t.Error("intent delete-b should be gone")
	}

	// Verify list returns exactly 2
	intents, err := s.ListCompletionIntents()
	if err != nil {
		t.Fatalf("ListCompletionIntents failed: %v", err)
	}
	if len(intents) != 2 {
		t.Errorf("expected 2 intents, got %d", len(intents))
	}
}

// ---------------------------------------------------------------------------
// ListCompletionIntents
// ---------------------------------------------------------------------------

func TestCompletionIntent_List(t *testing.T) {
	s := openTestStore(t)

	// Save multiple intents
	ids := []string{"intent-a", "intent-b", "intent-c"}
	for _, id := range ids {
		intent := &store.CompletionIntent{
			ID:        id,
			BackendID: "tusd-" + id,
			Src:       "/tmp/incoming/" + id,
			Dst:       "/tmp/organized/" + id + "/file.jpg",
			DstRel:    "organized/" + id + "/file.jpg",
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
		}
		if err := s.SaveCompletionIntent(intent); err != nil {
			t.Fatalf("SaveCompletionIntent %s failed: %v", id, err)
		}
	}

	intents, err := s.ListCompletionIntents()
	if err != nil {
		t.Fatalf("ListCompletionIntents failed: %v", err)
	}
	if len(intents) != 3 {
		t.Errorf("expected 3 intents, got %d", len(intents))
	}

	// Verify all IDs are present
	gotIDs := make(map[string]bool)
	for _, intent := range intents {
		gotIDs[intent.ID] = true
	}
	for _, id := range ids {
		if !gotIDs[id] {
			t.Errorf("intent %q not found in list", id)
		}
	}
}

func TestCompletionIntent_ListEmpty(t *testing.T) {
	s := openTestStore(t)

	intents, err := s.ListCompletionIntents()
	if err != nil {
		t.Fatalf("ListCompletionIntents on empty store failed: %v", err)
	}
	if len(intents) != 0 {
		t.Errorf("expected 0 intents, got %d", len(intents))
	}
}

func TestCompletionIntent_List_AfterDeletes(t *testing.T) {
	s := openTestStore(t)

	// Save 5 intents, delete 3
	ids := []string{"list-del-a", "list-del-b", "list-del-c", "list-del-d", "list-del-e"}
	for _, id := range ids {
		if err := s.SaveCompletionIntent(testIntent(id)); err != nil {
			t.Fatalf("SaveCompletionIntent %s failed: %v", id, err)
		}
	}

	// Delete three
	for _, id := range []string{"list-del-b", "list-del-d", "list-del-e"} {
		if err := s.DeleteCompletionIntent(id); err != nil {
			t.Fatalf("DeleteCompletionIntent %s failed: %v", id, err)
		}
	}

	// List should return 2
	intents, err := s.ListCompletionIntents()
	if err != nil {
		t.Fatalf("ListCompletionIntents failed: %v", err)
	}
	if len(intents) != 2 {
		t.Errorf("expected 2 intents after deletes, got %d", len(intents))
	}

	// Verify the remaining IDs
	remaining := map[string]bool{"list-del-a": true, "list-del-c": true}
	for _, intent := range intents {
		if !remaining[intent.ID] {
			t.Errorf("unexpected intent %q in list after deletes", intent.ID)
		}
		delete(remaining, intent.ID)
	}
	if len(remaining) > 0 {
		for id := range remaining {
			t.Errorf("expected intent %q to be in list but was not", id)
		}
	}
}

func TestCompletionIntent_List_LargeCount(t *testing.T) {
	s := openTestStore(t)

	// Save 100 intents
	const n = 100
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("list-large-%d", i)
		if err := s.SaveCompletionIntent(testIntent(id)); err != nil {
			t.Fatalf("SaveCompletionIntent %s failed: %v", id, err)
		}
	}

	intents, err := s.ListCompletionIntents()
	if err != nil {
		t.Fatalf("ListCompletionIntents failed: %v", err)
	}
	if len(intents) != n {
		t.Errorf("expected %d intents, got %d", n, len(intents))
	}

	// Verify all IDs are unique
	seen := make(map[string]bool)
	for _, intent := range intents {
		if seen[intent.ID] {
			t.Errorf("duplicate intent ID %q in list", intent.ID)
		}
		seen[intent.ID] = true
	}
}

// ---------------------------------------------------------------------------
// Lifecycle: save → list → delete → list → not found
// ---------------------------------------------------------------------------

func TestCompletionIntent_FullLifecycle(t *testing.T) {
	s := openTestStore(t)
	id := "lifecycle-intent"

	// Step 1: Save
	intent := testIntent(id)
	if err := s.SaveCompletionIntent(intent); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Step 2: Get by ID
	got, err := s.GetCompletionIntent(id)
	if err != nil {
		t.Fatalf("Get after save failed: %v", err)
	}
	if got == nil {
		t.Fatal("Get after save returned nil")
	}
	if got.BackendID != intent.BackendID {
		t.Errorf("BackendID: got %q, want %q", got.BackendID, intent.BackendID)
	}

	// Step 3: List includes it
	intents, err := s.ListCompletionIntents()
	if err != nil {
		t.Fatalf("List after save failed: %v", err)
	}
	if len(intents) != 1 {
		t.Errorf("expected 1 intent in list, got %d", len(intents))
	}

	// Step 4: Delete
	if err := s.DeleteCompletionIntent(id); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// Step 5: Get returns nil
	got, err = s.GetCompletionIntent(id)
	if err != nil {
		t.Fatalf("Get after delete failed: %v", err)
	}
	if got != nil {
		t.Fatal("Get after delete should return nil")
	}

	// Step 6: List is empty
	intents, err = s.ListCompletionIntents()
	if err != nil {
		t.Fatalf("List after delete failed: %v", err)
	}
	if len(intents) != 0 {
		t.Errorf("expected 0 intents after delete, got %d", len(intents))
	}

	// Step 7: Delete again is safe
	if err := s.DeleteCompletionIntent(id); err != nil {
		t.Fatalf("Second delete should not error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Store isolation: each store has its own intents
// ---------------------------------------------------------------------------

func TestCompletionIntent_StoreIsolation(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	s1, err := store.Open(filepath.Join(dir1, "db"))
	if err != nil {
		t.Fatalf("Open s1 failed: %v", err)
	}
	defer s1.Close()

	s2, err := store.Open(filepath.Join(dir2, "db"))
	if err != nil {
		t.Fatalf("Open s2 failed: %v", err)
	}
	defer s2.Close()

	// Save in s1
	intent1 := testIntent("store-1-intent")
	if err := s1.SaveCompletionIntent(intent1); err != nil {
		t.Fatalf("s1 SaveCompletionIntent failed: %v", err)
	}

	// Save in s2
	intent2 := testIntent("store-2-intent")
	if err := s2.SaveCompletionIntent(intent2); err != nil {
		t.Fatalf("s2 SaveCompletionIntent failed: %v", err)
	}

	// s1 should have 1
	intents1, err := s1.ListCompletionIntents()
	if err != nil {
		t.Fatalf("s1 ListCompletionIntents failed: %v", err)
	}
	if len(intents1) != 1 || intents1[0].ID != "store-1-intent" {
		t.Errorf("s1 expected [store-1-intent], got %v", intents1)
	}

	// s2 should have 1
	intents2, err := s2.ListCompletionIntents()
	if err != nil {
		t.Fatalf("s2 ListCompletionIntents failed: %v", err)
	}
	if len(intents2) != 1 || intents2[0].ID != "store-2-intent" {
		t.Errorf("s2 expected [store-2-intent], got %v", intents2)
	}
}

// ---------------------------------------------------------------------------
// Concurrent access smoke test
// ---------------------------------------------------------------------------

func TestCompletionIntent_ConcurrentSaveAndList(t *testing.T) {
	s := openTestStore(t)

	const n = 20
	errCh := make(chan error, n*2)

	// Concurrently save intents
	for i := 0; i < n; i++ {
		i := i
		go func() {
			id := fmt.Sprintf("concurrent-intent-%d", i)
			errCh <- s.SaveCompletionIntent(testIntent(id))
		}()
	}

	// Collect save results
	for i := 0; i < n; i++ {
		if err := <-errCh; err != nil {
			t.Errorf("concurrent save %d: %v", i, err)
		}
	}

	// Concurrently read all
	for i := 0; i < n; i++ {
		i := i
		go func() {
			id := fmt.Sprintf("concurrent-intent-%d", i)
			_, err := s.GetCompletionIntent(id)
			errCh <- err
		}()
	}

	for i := 0; i < n; i++ {
		if err := <-errCh; err != nil {
			t.Errorf("concurrent get %d: %v", i, err)
		}
	}

	// Verify count
	intents, err := s.ListCompletionIntents()
	if err != nil {
		t.Fatalf("ListCompletionIntents failed: %v", err)
	}
	if len(intents) != n {
		t.Errorf("expected %d intents, got %d", n, len(intents))
	}
}

func TestCompletionIntent_ConcurrentDeleteOnSameIntent(t *testing.T) {
	s := openTestStore(t)
	id := "concurrent-delete-intent"

	// Save one intent
	if err := s.SaveCompletionIntent(testIntent(id)); err != nil {
		t.Fatalf("SaveCompletionIntent failed: %v", err)
	}

	errCh := make(chan error, 5)

	// 5 concurrent deletes — only one should succeed in effect, but none should error
	for i := 0; i < 5; i++ {
		go func() {
			errCh <- s.DeleteCompletionIntent(id)
		}()
	}

	for i := 0; i < 5; i++ {
		if err := <-errCh; err != nil {
			t.Errorf("concurrent delete %d: %v", i, err)
		}
	}

	// Intent should be gone
	got, err := s.GetCompletionIntent(id)
	if err != nil {
		t.Fatalf("GetCompletionIntent after concurrent deletes: %v", err)
	}
	if got != nil {
		t.Error("intent should be gone after concurrent deletes")
	}
}

// ---------------------------------------------------------------------------
// Edge cases
// ---------------------------------------------------------------------------

func TestCompletionIntent_ZeroValue(t *testing.T) {
	s := openTestStore(t)

	// A completely zero-value CompletionIntent — should be storable
	zero := &store.CompletionIntent{}
	if err := s.SaveCompletionIntent(zero); err != nil {
		t.Fatalf("SaveCompletionIntent with zero value failed: %v", err)
	}

	// Should be retrievable (empty ID means key is "completion/")
	got, err := s.GetCompletionIntent("")
	if err != nil {
		t.Fatalf("GetCompletionIntent with empty ID failed: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil for zero-value intent")
	}
	if got.ID != "" {
		t.Errorf("ID: got %q, want empty", got.ID)
	}
	if got.BackendID != "" {
		t.Errorf("BackendID: got %q, want empty", got.BackendID)
	}
}

func TestCompletionIntent_BackendIDWithSpecialChars(t *testing.T) {
	s := openTestStore(t)

	intent := &store.CompletionIntent{
		ID:        "special-chars-intent",
		BackendID: "tusd/abcdef/1234/5678?query=test",
		Src:       "/tmp/incoming/tusd_abcdef",
		Dst:       "/tmp/organized/special/file (1).jpg",
		DstRel:    "organized/special/file (1).jpg",
		CreatedAt: "2024-07-01T12:00:00Z",
	}

	if err := s.SaveCompletionIntent(intent); err != nil {
		t.Fatalf("SaveCompletionIntent with special chars failed: %v", err)
	}

	got, err := s.GetCompletionIntent(intent.ID)
	if err != nil {
		t.Fatalf("GetCompletionIntent failed: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil")
	}
	if got.BackendID != intent.BackendID {
		t.Errorf("BackendID: got %q, want %q", got.BackendID, intent.BackendID)
	}
	if got.Dst != intent.Dst {
		t.Errorf("Dst: got %q, want %q", got.Dst, intent.Dst)
	}
}

func TestCompletionIntent_ListOnlyReturnsIntents(t *testing.T) {
	s := openTestStore(t)

	// Save some uploads (should NOT appear in completion intents list)
	u := testUpload("asset-not-intent", nil)
	if err := s.CreateUpload(u); err != nil {
		t.Fatalf("CreateUpload failed: %v", err)
	}

	// Save a completion intent
	intent := testIntent("real-intent")
	if err := s.SaveCompletionIntent(intent); err != nil {
		t.Fatalf("SaveCompletionIntent failed: %v", err)
	}

	// List should only contain the intent, not the upload
	intents, err := s.ListCompletionIntents()
	if err != nil {
		t.Fatalf("ListCompletionIntents failed: %v", err)
	}
	if len(intents) != 1 {
		t.Errorf("expected 1 intent, got %d", len(intents))
	}
	if len(intents) > 0 && intents[0].ID != "real-intent" {
		t.Errorf("expected intent ID 'real-intent', got %q", intents[0].ID)
	}
}
