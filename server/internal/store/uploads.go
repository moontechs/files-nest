package store

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dgraph-io/badger/v4"
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

// Status represents the lifecycle state of an upload record.
type Status string

const (
	StatusUploading   Status = "uploading"
	StatusCompleting  Status = "completing"
	StatusComplete    Status = "complete"
	StatusDeleted     Status = "deleted"
	StatusBackendLost Status = "backend_lost"
)

// Upload is the primary record stored in BadgerDB.
type Upload struct {
	ID              string          `json:"id"`
	LocalIdentifier string          `json:"local_identifier"`
	Status          Status          `json:"status"`
	BackendID       string          `json:"backend_id"`
	Filename        string          `json:"filename"`
	BundleID        string          `json:"bundle_id,omitempty"`
	CreationDate    string          `json:"creation_date"`
	CreatedAt       string          `json:"created_at"`
	UpdatedAt       string          `json:"updated_at"`
	Metadata        json.RawMessage `json:"metadata,omitempty"`
	OrganizedPath   string          `json:"organized_path,omitempty"`
}

// CompletionIntent is a recovery record persisted before moving a completed
// file from the incoming directory to the organized tree. If the server
// crashes between the file move, the DB update, and the tusd cleanup, the
// completion intent enables the server to recover on startup.
type CompletionIntent struct {
	ID        string `json:"id"`
	BackendID string `json:"backend_id"`
	Src       string `json:"src"`
	Dst       string `json:"dst"`
	DstRel    string `json:"dst_rel"`
	CreatedAt string `json:"created_at"`
}

// Sentinel errors returned by store operations.
var (
	ErrNotFound = fmt.Errorf("upload not found")
	ErrConflict = fmt.Errorf("upload already exists")
)

// ---------------------------------------------------------------------------
// CreateUpload
// ---------------------------------------------------------------------------

// CreateUpload creates a new upload record and all its index entries in a
// single BadgerDB transaction. If an upload with the same localIdentifier
// already exists it returns ErrConflict — the caller should handle idempotency
// by reading the existing record and branching on its status.
func (s *Store) CreateUpload(upload *Upload) error {
	return s.db.Update(func(txn *badger.Txn) error {
		// Check for duplicate local identifier
		key := localIndexKey(upload.LocalIdentifier)
		if _, err := txn.Get(key); err == nil {
			return ErrConflict
		} else if err != badger.ErrKeyNotFound {
			return fmt.Errorf("check local index: %w", err)
		}

		// Write main record
		recordVal, err := json.Marshal(upload)
		if err != nil {
			return fmt.Errorf("marshal upload: %w", err)
		}
		if err := txn.Set(recordKey(upload.ID), recordVal); err != nil {
			return fmt.Errorf("set record: %w", err)
		}

		// Write all index entries
		reg := newIndexRegistry()
		for _, entry := range reg.writeEntries(upload) {
			if err := txn.Set(entry.Key, entry.Value); err != nil {
				return fmt.Errorf("set index %s: %w", string(entry.Key), err)
			}
		}

		return nil
	})
}

// ---------------------------------------------------------------------------
// GetUpload
// ---------------------------------------------------------------------------

// GetUpload retrieves an upload record by its safe server ID.
// Returns ErrNotFound if the record does not exist.
func (s *Store) GetUpload(id string) (*Upload, error) {
	var upload Upload
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(recordKey(id))
		if err != nil {
			if err == badger.ErrKeyNotFound {
				return ErrNotFound
			}
			return fmt.Errorf("get record: %w", err)
		}
		return item.Value(func(val []byte) error {
			if err := json.Unmarshal(val, &upload); err != nil {
				return fmt.Errorf("unmarshal upload: %w", err)
			}
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return &upload, nil
}

// ---------------------------------------------------------------------------
// Lookup helpers
// ---------------------------------------------------------------------------

// UploadByLocalIdentifier looks up an upload by its original PhotoKit
// localIdentifier. Returns nil, nil if no record exists.
func (s *Store) UploadByLocalIdentifier(localIdentifier string) (*Upload, error) {
	key := localIndexKey(localIdentifier)

	var id string
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(key)
		if err != nil {
			if err == badger.ErrKeyNotFound {
				return nil
			}
			return fmt.Errorf("get local index: %w", err)
		}
		return item.Value(func(val []byte) error {
			id = string(val)
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	if id == "" {
		return nil, nil
	}
	return s.GetUpload(id)
}

// UploadByBackendID looks up an upload by its tusd backend ID.
// Returns nil, nil if no record exists.
func (s *Store) UploadByBackendID(backendID string) (*Upload, error) {
	key := backendIndexKey(backendID)

	var id string
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(key)
		if err != nil {
			if err == badger.ErrKeyNotFound {
				return nil
			}
			return fmt.Errorf("get backend index: %w", err)
		}
		return item.Value(func(val []byte) error {
			id = string(val)
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	if id == "" {
		return nil, nil
	}
	return s.GetUpload(id)
}

// ---------------------------------------------------------------------------
// PutUploadIfAbsent
// ---------------------------------------------------------------------------

// PutUploadIfAbsent atomically creates an upload record if no record exists
// for the given localIdentifier. It returns the upload (either newly created
// or the existing record) and a boolean indicating whether a new record was
// created (true) or an existing record was returned (false).
//
// This is the core idempotency primitive: the caller does not need to handle
// ErrConflict — instead it checks the created bool to decide next steps (e.g.
// whether to keep or clean up a newly-created tusd upload).
func (s *Store) PutUploadIfAbsent(upload *Upload) (*Upload, bool, error) {
	var existing *Upload
	var created bool

	err := s.db.Update(func(txn *badger.Txn) error {
		key := localIndexKey(upload.LocalIdentifier)
		item, err := txn.Get(key)
		if err == nil {
			// Record exists for this localIdentifier — return the existing record.
			if err := item.Value(func(val []byte) error {
				if len(val) > 0 {
					id := string(val)
					recordItem, err := txn.Get(recordKey(id))
					if err != nil {
						if err == badger.ErrKeyNotFound {
							// Index inconsistent: local index points to missing record.
							// Fall through to create-new path.
							return nil
						}
						return fmt.Errorf("get existing record for local index: %w", err)
					}
					var rec Upload
					if err := recordItem.Value(func(val2 []byte) error {
						return json.Unmarshal(val2, &rec)
					}); err != nil {
						return fmt.Errorf("unmarshal existing record: %w", err)
					}
					existing = &rec
				}
				return nil
			}); err != nil {
				return err
			}
			if existing != nil {
				return nil // return existing, created=false
			}
			// Index was inconsistent — fall through to create.
		} else if err != badger.ErrKeyNotFound {
			return fmt.Errorf("check local index: %w", err)
		}

		// No existing record — create a new one.
		recordVal, err := json.Marshal(upload)
		if err != nil {
			return fmt.Errorf("marshal upload: %w", err)
		}
		if err := txn.Set(recordKey(upload.ID), recordVal); err != nil {
			return fmt.Errorf("set record: %w", err)
		}

		reg := newIndexRegistry()
		for _, entry := range reg.writeEntries(upload) {
			if err := txn.Set(entry.Key, entry.Value); err != nil {
				return fmt.Errorf("set index %s: %w", string(entry.Key), err)
			}
		}

		created = true
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if created {
		return upload, true, nil
	}
	return existing, false, nil
}

// ---------------------------------------------------------------------------
// UpdateStatus
// ---------------------------------------------------------------------------

// UpdateStatus atomically changes an upload's status in a single transaction.
// It reads the existing record, deletes the old status index entry, writes
// the updated record, and writes the new status index entry. The old status
// index deletion is critical — skipping it leaves a ghost key that corrupts
// every status-based scan.
//
// Returns ErrNotFound if the record does not exist. Returns the updated
// Upload on success.
func (s *Store) UpdateStatus(id string, newStatus Status) (*Upload, error) {
	var updated *Upload

	err := s.db.Update(func(txn *badger.Txn) error {
		item, err := txn.Get(recordKey(id))
		if err != nil {
			if err == badger.ErrKeyNotFound {
				return ErrNotFound
			}
			return fmt.Errorf("get record for status update: %w", err)
		}

		var old Upload
		if err := item.Value(func(val []byte) error {
			return json.Unmarshal(val, &old)
		}); err != nil {
			return fmt.Errorf("unmarshal old record: %w", err)
		}

		reg := newIndexRegistry()

		// Delete old status index entry (critical: prevents ghost keys)
		for _, entry := range reg.deleteEntries(&old) {
			if err := txn.Delete(entry.Key); err != nil {
				return fmt.Errorf("delete old index %s: %w", string(entry.Key), err)
			}
		}

		// Update record
		old.Status = newStatus
		old.UpdatedAt = time.Now().UTC().Format(time.RFC3339)

		newVal, err := json.Marshal(&old)
		if err != nil {
			return fmt.Errorf("marshal updated record: %w", err)
		}
		if err := txn.Set(recordKey(id), newVal); err != nil {
			return fmt.Errorf("set updated record: %w", err)
		}

		// Write new status index entry
		for _, entry := range reg.writeEntries(&old) {
			if err := txn.Set(entry.Key, entry.Value); err != nil {
				return fmt.Errorf("set new index %s: %w", string(entry.Key), err)
			}
		}

		updated = &old
		return nil
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// ---------------------------------------------------------------------------
// UpdateComplete
// ---------------------------------------------------------------------------

// UpdateComplete atomically transitions an upload from any status to
// StatusComplete and sets the organized_path in a single transaction.
// Returns ErrNotFound if the record does not exist.
//
// Unlike UpdateStatus, this function also persists the organized_path field
// in the same atomic write, ensuring the record is self-consistent for crash
// recovery: if the record shows status=complete, the organized_path is always
// set. The completion intent (written before the file move) provides the
// recovery anchor; this function is called after the file has been safely moved.
func (s *Store) UpdateComplete(id string, organizedPath string) (*Upload, error) {
	var updated *Upload

	err := s.db.Update(func(txn *badger.Txn) error {
		item, err := txn.Get(recordKey(id))
		if err != nil {
			if err == badger.ErrKeyNotFound {
				return ErrNotFound
			}
			return fmt.Errorf("get record for complete: %w", err)
		}

		var upload Upload
		if err := item.Value(func(val []byte) error {
			return json.Unmarshal(val, &upload)
		}); err != nil {
			return fmt.Errorf("unmarshal record for complete: %w", err)
		}

		// Delete old status index entry (critical: prevents ghost keys)
		reg := newIndexRegistry()
		for _, entry := range reg.deleteEntries(&upload) {
			if err := txn.Delete(entry.Key); err != nil {
				return fmt.Errorf("delete old index %s: %w", string(entry.Key), err)
			}
		}

		// Update record
		upload.Status = StatusComplete
		upload.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		upload.OrganizedPath = organizedPath

		newVal, err := json.Marshal(&upload)
		if err != nil {
			return fmt.Errorf("marshal completed record: %w", err)
		}
		if err := txn.Set(recordKey(id), newVal); err != nil {
			return fmt.Errorf("set completed record: %w", err)
		}

		// Write new status index entry
		for _, entry := range reg.writeEntries(&upload) {
			if err := txn.Set(entry.Key, entry.Value); err != nil {
				return fmt.Errorf("set new index %s: %w", string(entry.Key), err)
			}
		}

		updated = &upload
		return nil
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// ---------------------------------------------------------------------------
// DeleteUpload
// ---------------------------------------------------------------------------

// DeleteUpload removes an upload record and all its index entries in a single
// transaction. Returns ErrNotFound if the record does not exist.
func (s *Store) DeleteUpload(id string) error {
	return s.db.Update(func(txn *badger.Txn) error {
		item, err := txn.Get(recordKey(id))
		if err != nil {
			if err == badger.ErrKeyNotFound {
				return ErrNotFound
			}
			return fmt.Errorf("get record for delete: %w", err)
		}

		var upload Upload
		if err := item.Value(func(val []byte) error {
			return json.Unmarshal(val, &upload)
		}); err != nil {
			return fmt.Errorf("unmarshal upload for delete: %w", err)
		}

		// Delete all index entries
		reg := newIndexRegistry()
		for _, entry := range reg.deleteEntries(&upload) {
			if err := txn.Delete(entry.Key); err != nil {
				return fmt.Errorf("delete index %s: %w", string(entry.Key), err)
			}
		}

		// Delete main record
		if err := txn.Delete(recordKey(id)); err != nil {
			return fmt.Errorf("delete record: %w", err)
		}

		return nil
	})
}

// ---------------------------------------------------------------------------
// ListByStatus
// ---------------------------------------------------------------------------

// ListByStatus returns all upload records with the given status, loaded
// within a single read transaction. Results are not ordered.
// Orphaned index entries (pointing to deleted records) are silently skipped.
func (s *Store) ListByStatus(status Status) ([]*Upload, error) {
	prefix := statusIndexPrefix(status)
	var uploads []*Upload

	err := s.db.View(func(txn *badger.Txn) error {
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()

		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			key := string(it.Item().Key())
			// key format: "idx/status/<status>/<id>"
			lastSlash := strings.LastIndex(key, "/")
			if lastSlash < 0 {
				continue
			}
			id := key[lastSlash+1:]

			record, err := txn.Get(recordKey(id))
			if err != nil {
				if err == badger.ErrKeyNotFound {
					continue // skip orphaned index entries
				}
				return fmt.Errorf("load record %s: %w", id, err)
			}

			var upload Upload
			if err := record.Value(func(val []byte) error {
				return json.Unmarshal(val, &upload)
			}); err != nil {
				return fmt.Errorf("unmarshal record %s: %w", id, err)
			}
			uploads = append(uploads, &upload)
		}
		return nil
	})
	return uploads, err
}

// ---------------------------------------------------------------------------
// ListByDateRange
// ---------------------------------------------------------------------------

// ListByDateRange returns upload records within a date range, ordered by
// creation date ascending. The cursor parameter enables cursor-based
// pagination and is a base64-encoded "<YYYY-MM-DD>/<id>" string. Pass an
// empty cursor for the first page.
//
// The returned nextCursor is empty when there are no more results.
// The limit is clamped to [1, 1000]; defaults to 500 if <= 0.
func (s *Store) ListByDateRange(from, to time.Time, statusFilter Status, limit int, cursor string) ([]*Upload, string, error) {
	if limit <= 0 {
		limit = 500
	}
	if limit > 1000 {
		limit = 1000
	}

	fromStr := from.Format("2006-01-02")
	toStr := to.Format("2006-01-02")

	// Determine seek key
	var seekKey string
	var skipFirst bool // true when we need to skip the cursor entry itself

	if cursor != "" {
		cursorBytes, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil {
			return nil, "", fmt.Errorf("invalid cursor: %w", err)
		}
		seekKey = "idx/date/" + string(cursorBytes)
		skipFirst = true
	} else {
		seekKey = "idx/date/" + fromStr
		skipFirst = false
	}

	var uploads []*Upload
	var nextCursor string

	err := s.db.View(func(txn *badger.Txn) error {
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()

		prefix := dateIndexPrefix()

		for it.Seek([]byte(seekKey)); it.ValidForPrefix(prefix); it.Next() {
			// Skip the cursor entry (already returned in previous page)
			if skipFirst {
				skipFirst = false
				continue
			}

			key := string(it.Item().Key())
			// key format: "idx/date/<YYYY-MM-DD>/<id>"
			parts := strings.SplitN(key, "/", 4)
			if len(parts) < 4 {
				continue
			}
			dateStr := parts[2]

			// Respect the "to" bound
			if dateStr > toStr {
				break
			}
			if dateStr < fromStr {
				continue
			}

			id := parts[3]

			// Load full record
			item, err := txn.Get(recordKey(id))
			if err != nil {
				if err == badger.ErrKeyNotFound {
					continue // skip orphaned index entries
				}
				return fmt.Errorf("load record %s: %w", id, err)
			}

			var upload Upload
			if err := item.Value(func(val []byte) error {
				return json.Unmarshal(val, &upload)
			}); err != nil {
				return fmt.Errorf("unmarshal record %s: %w", id, err)
			}

			// Apply status filter (empty means no filter)
			if statusFilter != "" && upload.Status != statusFilter {
				continue
			}

			uploads = append(uploads, &upload)

			if len(uploads) >= limit {
				cursorID := dateStr + "/" + id
				nextCursor = base64.RawURLEncoding.EncodeToString([]byte(cursorID))
				break
			}
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}

	return uploads, nextCursor, nil
}

// ---------------------------------------------------------------------------
// CompletionIntent operations
// ---------------------------------------------------------------------------

// SaveCompletionIntent persists a completion intent for crash recovery.
func (s *Store) SaveCompletionIntent(intent *CompletionIntent) error {
	return s.db.Update(func(txn *badger.Txn) error {
		val, err := json.Marshal(intent)
		if err != nil {
			return fmt.Errorf("marshal completion intent: %w", err)
		}
		return txn.Set(completionIntentKey(intent.ID), val)
	})
}

// GetCompletionIntent retrieves a completion intent by upload ID.
// Returns nil, nil if not found.
func (s *Store) GetCompletionIntent(id string) (*CompletionIntent, error) {
	var intent CompletionIntent
	var found bool

	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(completionIntentKey(id))
		if err != nil {
			if err == badger.ErrKeyNotFound {
				return nil
			}
			return fmt.Errorf("get completion intent: %w", err)
		}
		found = true
		return item.Value(func(val []byte) error {
			return json.Unmarshal(val, &intent)
		})
	})
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	return &intent, nil
}

// DeleteCompletionIntent removes a completion intent from the database.
func (s *Store) DeleteCompletionIntent(id string) error {
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Delete(completionIntentKey(id))
	})
}

// ListCompletionIntents returns all pending completion intents. This is used
// during server startup to recover from crashes that occurred between the file
// move, the DB update, and the tusd cleanup.
func (s *Store) ListCompletionIntents() ([]*CompletionIntent, error) {
	prefix := []byte("completion/")
	var intents []*CompletionIntent

	err := s.db.View(func(txn *badger.Txn) error {
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()

		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			var intent CompletionIntent
			if err := it.Item().Value(func(val []byte) error {
				return json.Unmarshal(val, &intent)
			}); err != nil {
				return fmt.Errorf("unmarshal completion intent: %w", err)
			}
			intents = append(intents, &intent)
		}
		return nil
	})
	return intents, err
}
