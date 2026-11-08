package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Env is one row of the `Envs` table. Nullable columns keep their database
// representation so the API can tell NULL from zero, which Node serializes as
// `null` rather than `0`/`""`.
type Env struct {
	ID        int64
	Value     sql.NullString
	Timestamp sql.NullString
	Status    sql.NullInt64
	Position  sql.NullFloat64
	Name      sql.NullString
	Remarks   sql.NullString
	IsPinned  sql.NullInt64
	Labels    sql.NullString

	CreatedAt string
	UpdatedAt string
}

// EnvColumns is the physical column order, which is also the key order
// Sequelize produces for a plain row.
const EnvColumns = "id, value, timestamp, status, position, name, remarks, isPinned, labels, createdAt, updatedAt"

func scanEnv(scanner rowScanner) (*Env, error) {
	var item Env
	if err := scanner.Scan(
		&item.ID, &item.Value, &item.Timestamp, &item.Status, &item.Position,
		&item.Name, &item.Remarks, &item.IsPinned, &item.Labels,
		&item.CreatedAt, &item.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &item, nil
}

// EnvFilter carries the optional search terms of GET /api/envs. The service
// builds the LIKE patterns (raw and percent-encoded) so the search semantics
// stay in one place.
type EnvFilter struct {
	Patterns []string
	// OnlyEnabled restricts the result to rows the task environment uses.
	OnlyEnabled bool
}

// ListEnvs returns rows in the panel order: pinned first, then position, then
// creation time. The order is also the order set_envs() writes to env.sh.
func (s *Store) ListEnvs(ctx context.Context, filter EnvFilter) ([]Env, error) {
	where := []string{}
	args := []any{}
	if len(filter.Patterns) > 0 {
		// {[Op.or]: [{name: {[Op.or]: [raw, encoded]}}, {value: ...}, {remarks: ...}]}
		parts := make([]string, 0, len(filter.Patterns)*3)
		for _, pattern := range filter.Patterns {
			parts = append(parts, "name LIKE ?", "value LIKE ?", "remarks LIKE ?")
			args = append(args, pattern, pattern, pattern)
		}
		where = append(where, "("+strings.Join(parts, " OR ")+")")
	}
	if filter.OnlyEnabled {
		where = append(where, "name IS NOT NULL", "status = 0")
	}
	query := "SELECT " + EnvColumns + " FROM Envs"
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY COALESCE(`isPinned`, 0) DESC, `position` DESC, `createdAt` ASC"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list envs: %w", err)
	}
	defer rows.Close()

	items := []Env{}
	for rows.Next() {
		item, err := scanEnv(rows)
		if err != nil {
			return nil, fmt.Errorf("scan env: %w", err)
		}
		items = append(items, *item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate envs: %w", err)
	}
	return items, nil
}

// EnvByID loads one row, returning nil when it does not exist.
func (s *Store) EnvByID(ctx context.Context, id int64) (*Env, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT "+EnvColumns+" FROM Envs WHERE id = ?", id)
	item, err := scanEnv(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load env %d: %w", id, err)
	}
	return item, nil
}

// EnvsByIDs loads several rows in primary key order.
func (s *Store) EnvsByIDs(ctx context.Context, ids []int64) ([]Env, error) {
	if len(ids) == 0 {
		return []Env{}, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx,
		"SELECT "+EnvColumns+" FROM Envs WHERE id IN ("+strings.Join(placeholders, ",")+")", args...)
	if err != nil {
		return nil, fmt.Errorf("load envs: %w", err)
	}
	defer rows.Close()

	items := []Env{}
	for rows.Next() {
		item, err := scanEnv(rows)
		if err != nil {
			return nil, fmt.Errorf("scan env: %w", err)
		}
		items = append(items, *item)
	}
	return items, rows.Err()
}

// InsertEnv writes a new row and fills in ID.
func (s *Store) InsertEnv(ctx context.Context, item *Env) error {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO Envs (value, timestamp, status, position, name, remarks, isPinned, labels, createdAt, updatedAt)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.Value, item.Timestamp, item.Status, item.Position, item.Name,
		item.Remarks, item.IsPinned, item.Labels, item.CreatedAt, item.UpdatedAt,
	)
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("read inserted env id: %w", err)
	}
	item.ID = id
	return nil
}

var envUpdatable = map[string]struct{}{
	"value": {}, "timestamp": {}, "status": {}, "position": {}, "name": {},
	"remarks": {}, "isPinned": {}, "labels": {},
}

// UpdateEnv applies a partial update and always touches updatedAt.
func (s *Store) UpdateEnv(ctx context.Context, id int64, fields map[string]any) error {
	_, err := s.updateEnvsWhere(ctx, "id = ?", []any{id}, fields)
	return err
}

// UpdateEnvs applies the same partial update to a list of ids.
func (s *Store) UpdateEnvs(ctx context.Context, ids []int64, fields map[string]any) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	_, err := s.updateEnvsWhere(ctx, "id IN ("+strings.Join(placeholders, ",")+")", args, fields)
	return err
}

func (s *Store) updateEnvsWhere(ctx context.Context, where string, args []any, fields map[string]any) (int64, error) {
	if len(fields) == 0 {
		return 0, nil
	}
	sets := make([]string, 0, len(fields)+1)
	values := make([]any, 0, len(fields)+1)
	for key, value := range fields {
		if _, ok := envUpdatable[key]; !ok {
			continue
		}
		sets = append(sets, quoteIdentifier(key)+" = ?")
		values = append(values, value)
	}
	if len(sets) == 0 {
		return 0, nil
	}
	sets = append(sets, quoteIdentifier("updatedAt")+" = ?")
	values = append(values, SequelizeTime(time.Now()))
	values = append(values, args...)

	query := "UPDATE Envs SET " + strings.Join(sets, ", ")
	if where != "" {
		query += " WHERE " + where
	}
	res, err := s.db.ExecContext(ctx, query, values...)
	if err != nil {
		return 0, err
	}
	affected, _ := res.RowsAffected()
	return affected, nil
}

// DeleteEnvs removes rows by id.
func (s *Store) DeleteEnvs(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	_, err := s.db.ExecContext(ctx,
		"DELETE FROM Envs WHERE id IN ("+strings.Join(placeholders, ",")+")", args...)
	return err
}

// EnvLabels decodes the JSON column. NULL stays nil, which serializes as JSON
// null exactly like Sequelize.
func EnvLabels(raw sql.NullString) any {
	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return nil
	}
	var value any
	if err := json.Unmarshal([]byte(raw.String), &value); err != nil {
		return raw.String
	}
	return value
}
