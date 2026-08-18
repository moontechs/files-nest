package store

import "encoding/base64"

// IndexEntry represents a single key-value pair in a BadgerDB index.
// Index entries are written and deleted within the same transaction as
// the main upload record, ensuring index consistency.
type IndexEntry struct {
	Key   []byte
	Value []byte
}

// index defines the interface for a single index dimension over Upload records.
// Each implementation maps a specific aspect of an upload to index keys.
//
// For a Create operation, only WriteEntries is called.
// For an UpdateStatus operation, DeleteEntries is called with the old record
//
//	state, then WriteEntries is called with the new record state.
//
// For a Delete operation, only DeleteEntries is called.
type index interface {
	// WriteEntries returns index entries that should exist for the given upload.
	WriteEntries(upload *Upload) []IndexEntry

	// DeleteEntries returns index entries that should be removed for the given
	// upload. For most indexes this is identical to WriteEntries, but enables
	// correct handling of status changes where only the status key differs.
	DeleteEntries(upload *Upload) []IndexEntry
}

// indexRegistry manages the set of active indexes.
type indexRegistry struct {
	indexes []index
}

// newIndexRegistry creates a registry with all standard indexes.
func newIndexRegistry() *indexRegistry {
	return &indexRegistry{
		indexes: []index{
			&dateIndex{},
			&statusIndex{},
			&localIndex{},
			&backendIndex{},
		},
	}
}

// writeEntries collects all index entries to create for the given upload.
func (r *indexRegistry) writeEntries(upload *Upload) []IndexEntry {
	entries := make([]IndexEntry, 0, len(r.indexes))
	for _, idx := range r.indexes {
		entries = append(entries, idx.WriteEntries(upload)...)
	}

	return entries
}

// deleteEntries collects all index entries to remove for the given upload.
func (r *indexRegistry) deleteEntries(upload *Upload) []IndexEntry {
	entries := make([]IndexEntry, 0, len(r.indexes))
	for _, idx := range r.indexes {
		entries = append(entries, idx.DeleteEntries(upload)...)
	}

	return entries
}

// ---------------------------------------------------------------------------
// DateIndex — enables date-range queries for GET /uploads
// Key:   idx/date/<YYYY-MM-DD>/<id>
// Value: RFC3339 creation date string
// ---------------------------------------------------------------------------

type dateIndex struct{}

func (d *dateIndex) WriteEntries(upload *Upload) []IndexEntry {
	date := upload.CreationDate
	if len(date) >= 10 {
		date = date[:10]
	}

	key := []byte("idx/date/" + date + "/" + upload.ID)

	return []IndexEntry{{Key: key, Value: []byte(upload.CreationDate)}}
}

func (d *dateIndex) DeleteEntries(upload *Upload) []IndexEntry {
	return d.WriteEntries(upload)
}

// ---------------------------------------------------------------------------
// StatusIndex — enables status-based lookups (e.g. resume scan)
// Key:   idx/status/<status>/<id>
// Value: backend_id (for resume without an extra lookup)
// ---------------------------------------------------------------------------

type statusIndex struct{}

func (s *statusIndex) WriteEntries(upload *Upload) []IndexEntry {
	key := []byte("idx/status/" + string(upload.Status) + "/" + upload.ID)

	return []IndexEntry{{Key: key, Value: []byte(upload.BackendID)}}
}

func (s *statusIndex) DeleteEntries(upload *Upload) []IndexEntry {
	return s.WriteEntries(upload)
}

// ---------------------------------------------------------------------------
// LocalIndex — maps a photo library localIdentifier to a safe upload ID
// Key:   idx/local/<base64url(local_identifier)>
// Value: safe upload ID
// ---------------------------------------------------------------------------

type localIndex struct{}

func (l *localIndex) WriteEntries(upload *Upload) []IndexEntry {
	enc := base64.RawURLEncoding.EncodeToString([]byte(upload.LocalIdentifier))
	key := []byte("idx/local/" + enc)

	return []IndexEntry{{Key: key, Value: []byte(upload.ID)}}
}

func (l *localIndex) DeleteEntries(upload *Upload) []IndexEntry {
	return l.WriteEntries(upload)
}

// ---------------------------------------------------------------------------
// BackendIndex — maps a tusd backend ID to a safe upload ID
// Key:   idx/backend/<backend_id>
// Value: safe upload ID
// ---------------------------------------------------------------------------

type backendIndex struct{}

func (b *backendIndex) WriteEntries(upload *Upload) []IndexEntry {
	key := []byte("idx/backend/" + upload.BackendID)

	return []IndexEntry{{Key: key, Value: []byte(upload.ID)}}
}

func (b *backendIndex) DeleteEntries(upload *Upload) []IndexEntry {
	return b.WriteEntries(upload)
}

// ---------------------------------------------------------------------------
// Key helpers (used by store methods directly)
// ---------------------------------------------------------------------------

// recordKey returns the BadgerDB key for an upload record.
func recordKey(id string) []byte {
	return []byte("uploads/" + id)
}

// completionIntentKey returns the BadgerDB key for a completion intent.
func completionIntentKey(id string) []byte {
	return []byte("completion/" + id)
}

// localIndexKey returns the BadgerDB key for the local identifier index,
// used to look up whether an upload already exists for a localIdentifier.
func localIndexKey(localIdentifier string) []byte {
	enc := base64.RawURLEncoding.EncodeToString([]byte(localIdentifier))

	return []byte("idx/local/" + enc)
}

// backendIndexKey returns the BadgerDB key for the backend ID index.
func backendIndexKey(backendID string) []byte {
	return []byte("idx/backend/" + backendID)
}

// statusIndexPrefix returns the key prefix for scanning all entries with
// the given status.
func statusIndexPrefix(status Status) []byte {
	return []byte("idx/status/" + string(status) + "/")
}

// dateIndexPrefix returns the key prefix for all date index entries.
func dateIndexPrefix() []byte {
	return []byte("idx/date/")
}
