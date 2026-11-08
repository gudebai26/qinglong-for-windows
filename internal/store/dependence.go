package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Dependence is one row of the `Dependences` table.
type Dependence struct {
	ID        int64
	Name      sql.NullString
	Type      sql.NullInt64
	Timestamp sql.NullString
	Status    sql.NullInt64
	// Log is the JSON array of output chunks the install produced.
	Log    sql.NullString
	Remark sql.NullString

	CreatedAt string
	UpdatedAt string
}

// DependenceColumns is the physical column order.
const DependenceColumns = "id, name, type, timestamp, status, log, remark, createdAt, updatedAt"

// DependenceDefaultOrder mirrors DependenceService.find: sort terms first, then
// createdAt DESC.
var DependenceDefaultOrder = []OrderTerm{
	{Field: "createdAt", Desc: true},
}

func scanDependence(scanner rowScanner) (*Dependence, error) {
	var item Dependence
	if err := scanner.Scan(
		&item.ID, &item.Name, &item.Type, &item.Timestamp, &item.Status,
		&item.Log, &item.Remark, &item.CreatedAt, &item.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &item, nil
}

// ListDependences returns rows matching where (already parameterized).
func (s *Store) ListDependences(ctx context.Context, where string, args []any, order []OrderTerm) ([]Dependence, error) {
	query := "SELECT " + DependenceColumns + " FROM Dependences"
	if strings.TrimSpace(where) != "" {
		query += " WHERE " + where
	}
	query += dependenceOrderClause(order)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list dependences: %w", err)
	}
	defer rows.Close()

	var items []Dependence
	for rows.Next() {
		item, err := scanDependence(rows)
		if err != nil {
			return nil, fmt.Errorf("scan dependence: %w", err)
		}
		items = append(items, *item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate dependences: %w", err)
	}
	return items, nil
}

var dependenceSortable = map[string]struct{}{
	"id": {}, "name": {}, "type": {}, "timestamp": {}, "status": {},
	"log": {}, "remark": {}, "createdAt": {}, "updatedAt": {},
}

// dependenceOrderClause is orderClause with the dependence column whitelist.
func dependenceOrderClause(terms []OrderTerm) string {
	if len(terms) == 0 {
		return ""
	}
	parts := make([]string, 0, len(terms))
	for _, term := range terms {
		if _, ok := dependenceSortable[term.Field]; !ok {
			continue
		}
		direction := "ASC"
		if term.Desc {
			direction = "DESC"
		}
		parts = append(parts, quoteIdentifier(term.Field)+" "+direction)
	}
	if len(parts) == 0 {
		return ""
	}
	return " ORDER BY " + strings.Join(parts, ", ")
}

// DependenceByID loads one row, returning nil when it does not exist.
func (s *Store) DependenceByID(ctx context.Context, id int64) (*Dependence, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT "+DependenceColumns+" FROM Dependences WHERE id = ?", id)
	item, err := scanDependence(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load dependence %d: %w", id, err)
	}
	return item, nil
}

// InsertDependences writes a batch of new rows in one transaction and fills in
// the ids, mirroring bulkCreate.
func (s *Store) InsertDependences(ctx context.Context, items []*Dependence) error {
	if len(items) == 0 {
		return nil
	}
	now := SequelizeTime(time.Now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin insert dependences: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after commit is a no-op

	for _, item := range items {
		if item.CreatedAt == "" {
			item.CreatedAt = now
		}
		if item.UpdatedAt == "" {
			item.UpdatedAt = now
		}
		res, err := tx.ExecContext(ctx,
			`INSERT INTO Dependences
			 (name, type, timestamp, status, log, remark, createdAt, updatedAt)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			item.Name, item.Type, item.Timestamp, item.Status, item.Log,
			item.Remark, item.CreatedAt, item.UpdatedAt,
		)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("read inserted dependence id: %w", err)
		}
		item.ID = id
	}
	return tx.Commit()
}

var dependenceUpdatable = map[string]struct{}{
	"name": {}, "type": {}, "timestamp": {}, "status": {}, "log": {}, "remark": {},
}

// UpdateDependence applies a partial update and always touches updatedAt.
func (s *Store) UpdateDependence(ctx context.Context, id int64, fields map[string]any) error {
	_, err := s.updateDependencesWhere(ctx, "id = ?", []any{id}, fields)
	return err
}

// UpdateDependences applies the same partial update to a list of ids.
func (s *Store) UpdateDependences(ctx context.Context, ids []int64, fields map[string]any) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	return s.updateDependencesWhere(ctx, "id IN ("+strings.Join(placeholders, ",")+")", args, fields)
}

func (s *Store) updateDependencesWhere(ctx context.Context, where string, args []any, fields map[string]any) (int64, error) {
	if len(fields) == 0 {
		return 0, nil
	}
	sets := make([]string, 0, len(fields)+1)
	values := make([]any, 0, len(fields)+1)
	for key, value := range fields {
		if _, ok := dependenceUpdatable[key]; !ok {
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

	query := "UPDATE Dependences SET " + strings.Join(sets, ", ")
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

// DeleteDependences removes rows by id.
func (s *Store) DeleteDependences(ctx context.Context, ids []int64) error {
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
		"DELETE FROM Dependences WHERE id IN ("+strings.Join(placeholders, ",")+")", args...)
	return err
}

// AppendDependenceLog appends one output chunk to the JSON log array. Node
// pushes onto the array and rewrites the whole column; doing the same keeps
// the stored value byte-compatible (frontend joins the array with ”).
func (s *Store) AppendDependenceLog(ctx context.Context, id int64, chunk string) error {
	item, err := s.DependenceByID(ctx, id)
	if err != nil {
		return err
	}
	if item == nil {
		return nil
	}
	entries := DependenceLog(item.Log)
	entries = append(entries, chunk)
	encoded, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	_, err = s.updateDependencesWhere(ctx, "id = ?", []any{id}, map[string]any{
		"log": string(encoded),
	})
	return err
}

// DependenceLog decodes the JSON column into its string array. A missing or
// malformed value reads as an empty array, which serializes as [] like the
// Sequelize default.
func DependenceLog(raw sql.NullString) []string {
	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return []string{}
	}
	var entries []string
	if err := json.Unmarshal([]byte(raw.String), &entries); err != nil {
		return []string{}
	}
	if entries == nil {
		return []string{}
	}
	return entries
}

// UpdateDependencesByTypeStatus rewrites the status of every row of one runtime
// in one status, which is how dependence-clean flags installed packages as
// failed after their cache was dropped.
func (s *Store) UpdateDependencesByTypeStatus(ctx context.Context, depType, from, to int64) (int64, error) {
	result, err := s.DB().ExecContext(ctx,
		"UPDATE Dependences SET status = ?, updatedAt = ? WHERE type = ? AND status = ?",
		to, SequelizeTime(time.Now()), depType, from)
	if err != nil {
		return 0, fmt.Errorf("update dependences by type: %w", err)
	}
	return result.RowsAffected()
}

// CountDependencesByTypeStatus counts rows of one runtime in one status.
func (s *Store) CountDependencesByTypeStatus(ctx context.Context, depType, status int64) (int64, error) {
	var count int64
	err := s.DB().QueryRowContext(ctx,
		"SELECT COUNT(*) FROM Dependences WHERE type = ? AND status = ?", depType, status).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count dependences: %w", err)
	}
	return count, nil
}
