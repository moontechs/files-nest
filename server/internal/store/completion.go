package store

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/dgraph-io/badger/v4"
)

// ---------------------------------------------------------------------------
// CompletionIntent
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// SaveCompletionIntent
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

// ---------------------------------------------------------------------------
// GetCompletionIntent
// ---------------------------------------------------------------------------

// GetCompletionIntent retrieves a completion intent by upload ID.
// Returns nil, nil if not found.
func (s *Store) GetCompletionIntent(id string) (*CompletionIntent, error) {
	var (
		intent CompletionIntent
		found  bool
	)

	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(completionIntentKey(id))
		if err != nil {
			if errors.Is(err, badger.ErrKeyNotFound) {
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

// ---------------------------------------------------------------------------
// DeleteCompletionIntent
// ---------------------------------------------------------------------------

// DeleteCompletionIntent removes a completion intent from the database.
func (s *Store) DeleteCompletionIntent(id string) error {
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Delete(completionIntentKey(id))
	})
}

// ---------------------------------------------------------------------------
// ListCompletionIntents
// ---------------------------------------------------------------------------

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

			err := it.Item().Value(func(val []byte) error {
				return json.Unmarshal(val, &intent)
			})
			if err != nil {
				return fmt.Errorf("unmarshal completion intent: %w", err)
			}

			intents = append(intents, &intent)
		}

		return nil
	})

	return intents, err
}
