// Package keyv reads and writes data/db/keyv.sqlite, the Keyv store the Node
// panel uses for authInfo, apps and lang.
//
// The on-disk format is Keyv's: one `keyv` table with a VARCHAR key and a TEXT
// value holding {"value":...,"expires":null}. Keys carry the default "keyv:"
// namespace prefix (keyv:authInfo, keyv:apps, keyv:lang). Writing anything else
// makes the Node panel lose the admin account, so this package is the single
// place allowed to touch the file.
package keyv

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Namespace mirrors Keyv's default namespace.
const Namespace = "keyv:"

// Well known keys.
const (
	KeyAuthInfo = Namespace + "authInfo"
	KeyApps     = Namespace + "apps"
	KeyLang     = Namespace + "lang"
)

// ErrNotFound is returned when a key is absent or expired.
var ErrNotFound = errors.New("keyv: key not found")

// Store owns the keyv.sqlite handle.
type Store struct {
	db   *sql.DB
	path string
}

// entry is Keyv's serialized envelope.
type entry struct {
	Value   json.RawMessage `json:"value"`
	Expires *int64          `json:"expires"`
}

// Open connects to keyv.sqlite and creates the table when the file is new,
// matching @keyv/sqlite's own CREATE TABLE statement.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create keyv directory: %w", err)
	}
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open keyv: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping keyv: %w", err)
	}
	if _, err := db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS keyv(key VARCHAR(255) PRIMARY KEY, value TEXT )`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create keyv table: %w", err)
	}
	return &Store{db: db, path: path}, nil
}

// Path returns the sqlite file location.
func (s *Store) Path() string { return s.path }

// Close releases the handle.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Get returns the raw JSON value stored under key.
func (s *Store) Get(ctx context.Context, key string) (json.RawMessage, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM keyv WHERE key = ?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("keyv get %s: %w", key, err)
	}
	var envelope entry
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		// A value written by another tool without the envelope is still usable.
		return json.RawMessage(raw), nil
	}
	if envelope.Expires != nil && *envelope.Expires > 0 && *envelope.Expires <= time.Now().UnixMilli() {
		return nil, ErrNotFound
	}
	if len(envelope.Value) == 0 {
		return json.RawMessage("null"), nil
	}
	return envelope.Value, nil
}

// RawEntry returns the stored TEXT of a key without unwrapping the envelope.
// It exists for diagnostics and tests that assert the on-disk format.
func (s *Store) RawEntry(ctx context.Context, key string) (string, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM keyv WHERE key = ?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return raw, nil
}

// Set stores value under key using Keyv's envelope with no expiry.
func (s *Store) Set(ctx context.Context, key string, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("keyv marshal %s: %w", key, err)
	}
	envelope, err := json.Marshal(entry{Value: payload})
	if err != nil {
		return fmt.Errorf("keyv marshal envelope %s: %w", key, err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO keyv (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
		key, string(envelope))
	if err != nil {
		return fmt.Errorf("keyv set %s: %w", key, err)
	}
	return nil
}

// Delete removes a key.
func (s *Store) Delete(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM keyv WHERE key = ?`, key)
	return err
}
