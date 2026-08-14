package store

import (
	"encoding/base64"
	"encoding/json"
	"errors"
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

// Upload status lifecycle values.
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

// Sentinel errors returned by store operations.
var (
	ErrNotFound      = errors.New("upload not found")
	ErrConflict      = errors.New("upload already exists")
	ErrInvalidCursor = errors.New("invalid cursor")
)

// ---------------------------------------------------------------------------
// CreateUpload
// ---------------------------------------------------------------------------

// CreateUpload creates a new upload record and all its index entries in a
// single BadgerDB transaction. If an upload with the same localIdentifier
// already exists it returns ErrConflict — the caller should handle idempotency
// by reading the existing record and branching on its status.
func (s *Store) CreateUpload(upload *Upload) error {
	err := s.db.Update(func(txn *badger.Txn) error {
		// Check for duplicate local identifier
		key := localIndexKey(upload.LocalIdentifier)

		_, err := txn.Get(key)
		if err == nil {
			return ErrConflict
		} else if !errors.Is(err, badger.ErrKeyNotFound) {
			return fmt.Errorf("check local index: %w", err)
		}

		// Write main record
		recordVal, err := json.Marshal(upload)
		if err != nil {
			return fmt.Errorf("marshal upload: %w", err)
		}

		err = txn.Set(recordKey(upload.ID), recordVal)
		if err != nil {
			return fmt.Errorf("set record: %w", err)
		}

		// Write all index entries
		reg := newIndexRegistry()
		for _, entry := range reg.writeEntries(upload) {
			err := txn.Set(entry.Key, entry.Value)
			if err != nil {
				return fmt.Errorf("set index %s: %w", string(entry.Key), err)
			}
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("create upload: %w", err)
	}

	return nil
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
			if errors.Is(err, badger.ErrKeyNotFound) {
				return ErrNotFound
			}

			return fmt.Errorf("get record: %w", err)
		}

		return item.Value(func(val []byte) error {
			err := json.Unmarshal(val, &upload)
			if err != nil {
				return fmt.Errorf("unmarshal upload: %w", err)
			}

			return nil
		})
	})
	if err != nil {
		return nil, fmt.Errorf("get upload: %w", err)
	}

	return &upload, nil
}

// ---------------------------------------------------------------------------
// Lookup helpers
// ---------------------------------------------------------------------------

// UploadByLocalIdentifier looks up an upload by its original PhotoKit
// localIdentifier. Returns ErrNotFound if no record exists.
func (s *Store) UploadByLocalIdentifier(localIdentifier string) (*Upload, error) {
	key := localIndexKey(localIdentifier)

	var id string

	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(key)
		if err != nil {
			if errors.Is(err, badger.ErrKeyNotFound) {
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
		return nil, fmt.Errorf("lookup upload by local identifier: %w", err)
	}

	if id == "" {
		return nil, ErrNotFound
	}

	return s.GetUpload(id)
}

// UploadByBackendID looks up an upload by its tusd backend ID.
// Returns ErrNotFound if no record exists.
func (s *Store) UploadByBackendID(backendID string) (*Upload, error) {
	key := backendIndexKey(backendID)

	var id string

	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(key)
		if err != nil {
			if errors.Is(err, badger.ErrKeyNotFound) {
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
		return nil, fmt.Errorf("lookup upload by backend id: %w", err)
	}

	if id == "" {
		return nil, ErrNotFound
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
	var (
		existing *Upload
		created  bool
	)

	err := s.db.Update(func(txn *badger.Txn) error {
		rec, found, loadErr := s.loadExistingUpload(txn, upload.LocalIdentifier)
		if loadErr != nil {
			return loadErr
		}

		if found {
			existing = rec

			return nil // return existing, created=false
		}

		// No existing record (or a local index pointing at a missing record) —
		// create a new one.
		writeErr := s.writeNewUpload(txn, upload)
		if writeErr != nil {
			return writeErr
		}

		created = true

		return nil
	})
	if err != nil {
		return nil, false, fmt.Errorf("put upload if absent: %w", err)
	}

	if created {
		return upload, true, nil
	}

	return existing, false, nil
}

// loadExistingUpload resolves the upload record for a local identifier within
// an open transaction. It reports found=false when no record exists, or when
// the local index points at a missing record (an inconsistency that callers
// treat as "create new"). Any other read/unmarshal error is returned.
func (s *Store) loadExistingUpload(txn *badger.Txn, localIdentifier string) (*Upload, bool, error) {
	item, err := txn.Get(localIndexKey(localIdentifier))
	if err != nil {
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil, false, nil
		}

		return nil, false, fmt.Errorf("check local index: %w", err)
	}

	var existing *Upload

	err = item.Value(func(val []byte) error {
		if len(val) == 0 {
			return nil
		}

		id := string(val)

		recordItem, err := txn.Get(recordKey(id))
		if err != nil {
			if errors.Is(err, badger.ErrKeyNotFound) {
				// Index inconsistent: local index points to a missing record.
				// Return not-found so the caller falls through to create-new.
				return nil
			}

			return fmt.Errorf("get existing record for local index: %w", err)
		}

		var rec Upload

		err = recordItem.Value(func(val2 []byte) error {
			return json.Unmarshal(val2, &rec)
		})
		if err != nil {
			return fmt.Errorf("unmarshal existing record: %w", err)
		}

		existing = &rec

		return nil
	})
	if err != nil {
		return nil, false, fmt.Errorf("read existing upload value: %w", err)
	}

	return existing, existing != nil, nil
}

// writeNewUpload persists a new upload record and all of its index entries
// within an open transaction.
func (s *Store) writeNewUpload(txn *badger.Txn, upload *Upload) error {
	recordVal, err := json.Marshal(upload)
	if err != nil {
		return fmt.Errorf("marshal upload: %w", err)
	}

	err = txn.Set(recordKey(upload.ID), recordVal)
	if err != nil {
		return fmt.Errorf("set record: %w", err)
	}

	reg := newIndexRegistry()
	for _, entry := range reg.writeEntries(upload) {
		err := txn.Set(entry.Key, entry.Value)
		if err != nil {
			return fmt.Errorf("set index %s: %w", string(entry.Key), err)
		}
	}

	return nil
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
			if errors.Is(err, badger.ErrKeyNotFound) {
				return ErrNotFound
			}

			return fmt.Errorf("get record for status update: %w", err)
		}

		var old Upload

		err = item.Value(func(val []byte) error {
			return json.Unmarshal(val, &old)
		})
		if err != nil {
			return fmt.Errorf("unmarshal old record: %w", err)
		}

		reg := newIndexRegistry()

		// Delete old status index entry (critical: prevents ghost keys)
		for _, entry := range reg.deleteEntries(&old) {
			err := txn.Delete(entry.Key)
			if err != nil {
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

		err = txn.Set(recordKey(id), newVal)
		if err != nil {
			return fmt.Errorf("set updated record: %w", err)
		}

		// Write new status index entry
		for _, entry := range reg.writeEntries(&old) {
			err := txn.Set(entry.Key, entry.Value)
			if err != nil {
				return fmt.Errorf("set new index %s: %w", string(entry.Key), err)
			}
		}

		updated = &old

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("update upload status: %w", err)
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
			if errors.Is(err, badger.ErrKeyNotFound) {
				return ErrNotFound
			}

			return fmt.Errorf("get record for complete: %w", err)
		}

		var upload Upload

		err = item.Value(func(val []byte) error {
			return json.Unmarshal(val, &upload)
		})
		if err != nil {
			return fmt.Errorf("unmarshal record for complete: %w", err)
		}

		// Delete old status index entry (critical: prevents ghost keys)
		reg := newIndexRegistry()
		for _, entry := range reg.deleteEntries(&upload) {
			err := txn.Delete(entry.Key)
			if err != nil {
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

		err = txn.Set(recordKey(id), newVal)
		if err != nil {
			return fmt.Errorf("set completed record: %w", err)
		}

		// Write new status index entry
		for _, entry := range reg.writeEntries(&upload) {
			err := txn.Set(entry.Key, entry.Value)
			if err != nil {
				return fmt.Errorf("set new index %s: %w", string(entry.Key), err)
			}
		}

		updated = &upload

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("complete upload: %w", err)
	}

	return updated, nil
}

// ---------------------------------------------------------------------------
// ReRegister
// ---------------------------------------------------------------------------

// ReRegister atomically replaces an existing upload's backend_id and resets
// its status to StatusUploading. It is used when a client re-registers an
// upload whose previous backend was lost (status=backend_lost) or that was
// previously deleted (status=deleted) — in both cases the client needs a
// fresh tusd upload rather than the stale record.
//
// The old index entries (including the backend index pointing at the lost
// backend) are removed and the new ones are written in the same transaction,
// so no ghost keys are left behind. Returns ErrNotFound if the record does
// not exist.
func (s *Store) ReRegister(id string, newBackendID string) (*Upload, error) {
	var updated *Upload

	err := s.db.Update(func(txn *badger.Txn) error {
		item, err := txn.Get(recordKey(id))
		if err != nil {
			if errors.Is(err, badger.ErrKeyNotFound) {
				return ErrNotFound
			}

			return fmt.Errorf("get record for re-register: %w", err)
		}

		var upload Upload

		err = item.Value(func(val []byte) error {
			return json.Unmarshal(val, &upload)
		})
		if err != nil {
			return fmt.Errorf("unmarshal record for re-register: %w", err)
		}

		reg := newIndexRegistry()

		// Delete old index entries (backend and status indexes change).
		for _, entry := range reg.deleteEntries(&upload) {
			err := txn.Delete(entry.Key)
			if err != nil {
				return fmt.Errorf("delete old index %s: %w", string(entry.Key), err)
			}
		}

		// Reset to a fresh uploading state with the new backend.
		upload.BackendID = newBackendID
		upload.Status = StatusUploading
		upload.OrganizedPath = "" // clear any stale organized path
		upload.UpdatedAt = time.Now().UTC().Format(time.RFC3339)

		newVal, err := json.Marshal(&upload)
		if err != nil {
			return fmt.Errorf("marshal re-registered record: %w", err)
		}

		err = txn.Set(recordKey(id), newVal)
		if err != nil {
			return fmt.Errorf("set re-registered record: %w", err)
		}

		// Write new index entries for the updated record.
		for _, entry := range reg.writeEntries(&upload) {
			err := txn.Set(entry.Key, entry.Value)
			if err != nil {
				return fmt.Errorf("set new index %s: %w", string(entry.Key), err)
			}
		}

		updated = &upload

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("re-register upload: %w", err)
	}

	return updated, nil
}

// ---------------------------------------------------------------------------
// DeleteUpload
// ---------------------------------------------------------------------------

// DeleteUpload removes an upload record and all its index entries in a single
// transaction. Returns ErrNotFound if the record does not exist.
func (s *Store) DeleteUpload(id string) error {
	err := s.db.Update(func(txn *badger.Txn) error {
		item, err := txn.Get(recordKey(id))
		if err != nil {
			if errors.Is(err, badger.ErrKeyNotFound) {
				return ErrNotFound
			}

			return fmt.Errorf("get record for delete: %w", err)
		}

		var upload Upload

		err = item.Value(func(val []byte) error {
			return json.Unmarshal(val, &upload)
		})
		if err != nil {
			return fmt.Errorf("unmarshal upload for delete: %w", err)
		}

		// Delete all index entries
		reg := newIndexRegistry()
		for _, entry := range reg.deleteEntries(&upload) {
			err := txn.Delete(entry.Key)
			if err != nil {
				return fmt.Errorf("delete index %s: %w", string(entry.Key), err)
			}
		}

		// Delete main record
		err = txn.Delete(recordKey(id))
		if err != nil {
			return fmt.Errorf("delete record: %w", err)
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("delete upload: %w", err)
	}

	return nil
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
		iter := txn.NewIterator(badger.DefaultIteratorOptions)
		defer iter.Close()

		for iter.Seek(prefix); iter.ValidForPrefix(prefix); iter.Next() {
			key := string(iter.Item().Key())
			// key format: "idx/status/<status>/<id>"
			lastSlash := strings.LastIndex(key, "/")
			if lastSlash < 0 {
				continue
			}

			id := key[lastSlash+1:]

			record, err := txn.Get(recordKey(id))
			if err != nil {
				if errors.Is(err, badger.ErrKeyNotFound) {
					continue // skip orphaned index entries
				}

				return fmt.Errorf("load record %s: %w", id, err)
			}

			var upload Upload

			err = record.Value(func(val []byte) error {
				return json.Unmarshal(val, &upload)
			})
			if err != nil {
				return fmt.Errorf("unmarshal record %s: %w", id, err)
			}

			uploads = append(uploads, &upload)
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list uploads by status: %w", err)
	}

	return uploads, nil
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
func (s *Store) ListByDateRange(
	fromTime, toTime time.Time, statusFilter Status, limit int, cursor string,
) ([]*Upload, string, error) {
	if limit <= 0 {
		limit = 500
	}

	if limit > 1000 {
		limit = 1000
	}

	seekKey, cursorKey, err := dateRangeSeekKeys(fromTime, cursor)
	if err != nil {
		return nil, "", err
	}

	var (
		uploads    []*Upload
		nextCursor string
	)

	err = s.db.View(func(txn *badger.Txn) error {
		var scanErr error

		uploads, nextCursor, scanErr = s.scanDateRange(txn, seekKey, cursorKey, fromTime, toTime, statusFilter, limit)

		return scanErr
	})
	if err != nil {
		return nil, "", fmt.Errorf("list uploads by date range: %w", err)
	}

	return uploads, nextCursor, nil
}

// dateRangeSeekKeys computes the iterator seek key and the exact cursor key
// to skip for cursor-based pagination. An empty cursor seeks to the start of
// the from-date; a provided cursor seeks to (and, if still present, skips)
// the last entry already returned.
func dateRangeSeekKeys(from time.Time, cursor string) (string, string, error) {
	seekKey := "idx/date/" + from.Format("2006-01-02")

	if cursor == "" {
		return seekKey, "", nil
	}

	cursorBytes, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", "", fmt.Errorf("%w: %w", ErrInvalidCursor, err)
	}

	seekKey = "idx/date/" + string(cursorBytes)

	return seekKey, seekKey, nil
}

// scanDateRange iterates the date index within [from, to] and returns the
// matching upload records plus the next-page cursor (empty when this page is
// the last). It runs within a read transaction and skips orphaned index
// entries whose record no longer exists.
func (s *Store) scanDateRange(
	txn *badger.Txn, seekKey, cursorKey string, fromTime, toTime time.Time, statusFilter Status, limit int,
) ([]*Upload, string, error) {
	fromStr := fromTime.Format("2006-01-02")
	toStr := toTime.Format("2006-01-02")

	iter := txn.NewIterator(badger.DefaultIteratorOptions)
	defer iter.Close()

	prefix := dateIndexPrefix()

	var (
		uploads    []*Upload
		nextCursor string
	)

	for iter.Seek([]byte(seekKey)); iter.ValidForPrefix(prefix); iter.Next() {
		key := string(iter.Item().Key())

		// Skip the cursor entry itself (already returned in the previous
		// page) — but only when it still exists. If it was deleted, the
		// iterator is now positioned on the next valid entry, which we
		// must return rather than skip.
		if cursorKey != "" && key == cursorKey {
			cursorKey = "" // consume; don't skip subsequent entries

			continue
		}

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

		upload, found, err := s.loadDateRangeRecord(txn, id)
		if err != nil {
			return nil, "", err
		}

		if !found {
			continue // skip orphaned index entries
		}

		// Apply status filter (empty means no filter)
		if statusFilter != "" && upload.Status != statusFilter {
			continue
		}

		// Post-filter by full timestamp — the date index only has day
		// precision (YYYY-MM-DD), so a query like "from=2035-07-15T12:00:00Z"
		// would return all records from July 15 rather than just those after
		// noon. This refinement compares the stored creation_date (which is
		// always an RFC3339 or date-only string) against the parsed from/to
		// time.Time values to enforce the caller's intended time range.
		creationTime, ok := parseCreationTime(upload.CreationDate)
		if !ok {
			continue // skip records with unparseable creation dates
		}

		if creationTime.Before(fromTime) || creationTime.After(toTime) {
			continue
		}

		uploads = append(uploads, upload)

		if len(uploads) >= limit {
			nextCursor = nextPageCursor(iter, prefix, dateStr, id)

			break
		}
	}

	return uploads, nextCursor, nil
}

// loadDateRangeRecord loads a single upload record by ID within a read
// transaction. It reports found=false for orphaned index entries (record
// missing) so the caller can skip them.
func (s *Store) loadDateRangeRecord(txn *badger.Txn, id string) (*Upload, bool, error) {
	item, err := txn.Get(recordKey(id))
	if err != nil {
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil, false, nil
		}

		return nil, false, fmt.Errorf("load record %s: %w", id, err)
	}

	var upload Upload

	err = item.Value(func(val []byte) error {
		return json.Unmarshal(val, &upload)
	})
	if err != nil {
		return nil, false, fmt.Errorf("unmarshal record %s: %w", id, err)
	}

	return &upload, true, nil
}

// nextPageCursor peeks at the next iterator entry and returns a base64 cursor
// for it, or an empty string when there is no next entry. The peek avoids an
// unnecessary trailing round-trip when the last page fills exactly to the
// limit.
func nextPageCursor(iter *badger.Iterator, prefix []byte, dateStr, id string) string {
	iter.Next()

	if !iter.ValidForPrefix(prefix) {
		return ""
	}

	cursorID := dateStr + "/" + id

	return base64.RawURLEncoding.EncodeToString([]byte(cursorID))
}

// parseCreationTime parses a stored creation_date into a time.Time using the
// formats the server accepts (RFC3339, RFC3339Nano, then YYYY-MM-DD). The
// second return value reports whether parsing succeeded.
func parseCreationTime(creationDate string) (time.Time, bool) {
	parsed, err := time.Parse(time.RFC3339, creationDate)
	if err == nil {
		return parsed, true
	}

	parsed, err = time.Parse(time.RFC3339Nano, creationDate)
	if err == nil {
		return parsed, true
	}

	parsed, err = time.Parse("2006-01-02", creationDate)
	if err == nil {
		return parsed, true
	}

	return time.Time{}, false
}
