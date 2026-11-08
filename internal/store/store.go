package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// SchemaVersion is stored in PRAGMA user_version. Node does not read it, so
// bumping it never affects the old panel; it only records how far the Go
// migrations have run.
const SchemaVersion = 1

// Store owns the SQLite handle for data/db/database.sqlite.
type Store struct {
	db   *sql.DB
	path string
}

// Open connects to the existing database file. The file is created when the
// panel runs for the very first time, exactly like Sequelize would.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create db directory: %w", err)
	}

	// busy_timeout keeps concurrent writers from failing with "database is
	// locked"; foreign keys stay off because Sequelize never enabled them.
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(0)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// A single connection serializes writes and keeps PRAGMA state stable.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	return &Store{db: db, path: path}, nil
}

// Path returns the database file location.
func (s *Store) Path() string { return s.path }

// DB exposes the handle for DAOs added in later milestones.
func (s *Store) DB() *sql.DB { return s.db }

// Close releases the database handle.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Migrate creates any missing table and records the schema version in
// PRAGMA user_version. On an existing Node database every statement is a
// no-op and only user_version moves from 0 to SchemaVersion.
func (s *Store) Migrate(ctx context.Context) (int, error) {
	current, err := s.UserVersion(ctx)
	if err != nil {
		return 0, err
	}
	if current >= SchemaVersion {
		return current, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return current, fmt.Errorf("begin migration: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after commit is a no-op

	for _, statement := range schemaStatements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return current, fmt.Errorf("apply schema: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", SchemaVersion)); err != nil {
		return current, fmt.Errorf("set user_version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return current, fmt.Errorf("commit migration: %w", err)
	}
	return SchemaVersion, nil
}

// UserVersion reads PRAGMA user_version.
func (s *Store) UserVersion(ctx context.Context) (int, error) {
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return 0, fmt.Errorf("read user_version: %w", err)
	}
	return version, nil
}

// TableNames lists the user tables present in the database.
func (s *Store) TableNames(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan table name: %w", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tables: %w", err)
	}
	sort.Strings(names)
	return names, nil
}

// MissingTables reports expected tables that are absent, so a partially
// migrated database fails loudly instead of returning confusing SQL errors.
func (s *Store) MissingTables(ctx context.Context) ([]string, error) {
	names, err := s.TableNames(ctx)
	if err != nil {
		return nil, err
	}
	present := make(map[string]struct{}, len(names))
	for _, name := range names {
		present[name] = struct{}{}
	}
	var missing []string
	for _, name := range expectedTables {
		if _, ok := present[name]; !ok {
			missing = append(missing, name)
		}
	}
	return missing, nil
}

// CountRows returns the number of rows in a table; used for startup logging.
func (s *Store) CountRows(ctx context.Context, table string) (int64, error) {
	var count int64
	query := fmt.Sprintf("SELECT COUNT(*) FROM %s", quoteIdentifier(table))
	if err := s.db.QueryRowContext(ctx, query).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func quoteIdentifier(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}
