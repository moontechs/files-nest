// Package store provides a BadgerDB-backed persistence layer for upload records.
package store

import (
	"fmt"

	"github.com/dgraph-io/badger/v4"
)

// Store wraps a BadgerDB instance and provides upload record operations.
type Store struct {
	db *badger.DB
}

// Open opens or creates a BadgerDB database at the given path.
// The directory is created if it does not exist.
func Open(path string) (*Store, error) {
	opts := badger.DefaultOptions(path).
		WithLogger(nil) // silence the default logger; use our own if needed

	db, err := badger.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("store open: %w", err)
	}

	return &Store{db: db}, nil
}

// Close gracefully shuts down the underlying BadgerDB database.
func (s *Store) Close() error {
	return s.db.Close()
}

// DB returns the underlying BadgerDB instance for low-level operations.
func (s *Store) DB() *badger.DB {
	return s.db
}
