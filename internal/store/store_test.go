package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestMigrateCreatesFullSchema(t *testing.T) {
	dbFile := filepath.Join(t.TempDir(), "database.sqlite")

	st, err := Open(dbFile)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	version, err := st.Migrate(ctx)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if version != SchemaVersion {
		t.Fatalf("user_version = %d, want %d", version, SchemaVersion)
	}

	names, err := st.TableNames(ctx)
	if err != nil {
		t.Fatalf("tables: %v", err)
	}
	if len(names) != len(expectedTables) {
		t.Fatalf("tables = %v, want %d entries", names, len(expectedTables))
	}

	missing, err := st.MissingTables(ctx)
	if err != nil {
		t.Fatalf("missing tables: %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("missing tables: %v", missing)
	}

	// A second run must be a no-op and must not reset data.
	if _, err := st.DB().ExecContext(ctx,
		"INSERT INTO `Crontabs` (`name`, `command`, `schedule`, `createdAt`, `updatedAt`) VALUES ('t', 'task a.js', '0 0 * * *', '2026-01-01', '2026-01-01')"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	again, err := st.Migrate(ctx)
	if err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if again != SchemaVersion {
		t.Fatalf("second migrate version = %d, want %d", again, SchemaVersion)
	}
	if count, err := st.CountRows(ctx, "Crontabs"); err != nil || count != 1 {
		t.Fatalf("Crontabs rows = %d (err %v), want 1", count, err)
	}
}
