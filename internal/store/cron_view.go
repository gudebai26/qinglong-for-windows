package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// CrontabView is one row of `CrontabViews` (back/data/cronView.ts).
type CrontabView struct {
	ID             int64
	Name           sql.NullString
	Position       sql.NullFloat64
	IsDisabled     sql.NullInt64
	Filters        sql.NullString
	Sorts          sql.NullString
	FilterRelation sql.NullString
	Type           sql.NullInt64
	CreatedAt      string
	UpdatedAt      string
}

const crontabViewColumns = "id, name, position, isDisabled, filters, sorts, filterRelation, type, createdAt, updatedAt"

func scanCrontabView(scanner rowScanner) (*CrontabView, error) {
	var v CrontabView
	if err := scanner.Scan(
		&v.ID, &v.Name, &v.Position, &v.IsDisabled, &v.Filters, &v.Sorts,
		&v.FilterRelation, &v.Type, &v.CreatedAt, &v.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &v, nil
}

// ListCrontabViews returns every view ordered by position DESC.
func (s *Store) ListCrontabViews(ctx context.Context) ([]CrontabView, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT "+crontabViewColumns+" FROM CrontabViews ORDER BY position DESC")
	if err != nil {
		return nil, fmt.Errorf("list crontab views: %w", err)
	}
	defer rows.Close()

	var items []CrontabView
	for rows.Next() {
		item, err := scanCrontabView(rows)
		if err != nil {
			return nil, fmt.Errorf("scan crontab view: %w", err)
		}
		items = append(items, *item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate crontab views: %w", err)
	}
	return items, nil
}

// CrontabViewByID loads one view, returning nil when absent.
func (s *Store) CrontabViewByID(ctx context.Context, id int64) (*CrontabView, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT "+crontabViewColumns+" FROM CrontabViews WHERE id = ?", id)
	item, err := scanCrontabView(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load crontab view %d: %w", id, err)
	}
	return item, nil
}

// InsertCrontabView writes a new view and fills in ID.
func (s *Store) InsertCrontabView(ctx context.Context, v *CrontabView) error {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO CrontabViews
		 (name, position, isDisabled, filters, sorts, filterRelation, type, createdAt, updatedAt)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		v.Name, v.Position, v.IsDisabled, v.Filters, v.Sorts,
		v.FilterRelation, v.Type, v.CreatedAt, v.UpdatedAt,
	)
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("read inserted crontab view id: %w", err)
	}
	v.ID = id
	return nil
}

// UpdateCrontabView applies a partial update and touches updatedAt.
func (s *Store) UpdateCrontabView(ctx context.Context, id int64, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	sets := make([]string, 0, len(fields)+1)
	values := make([]any, 0, len(fields)+1)
	for key, value := range fields {
		switch key {
		case "name", "position", "isDisabled", "filters", "sorts", "filterRelation", "type":
			sets = append(sets, quoteIdentifier(key)+" = ?")
			values = append(values, value)
		}
	}
	if len(sets) == 0 {
		return nil
	}
	sets = append(sets, quoteIdentifier("updatedAt")+" = ?")
	values = append(values, SequelizeTime(time.Now()))
	values = append(values, id)
	_, err := s.db.ExecContext(ctx,
		"UPDATE CrontabViews SET "+strings.Join(sets, ", ")+" WHERE id = ?", values...)
	return err
}

// UpdateCrontabViewsDisabled sets isDisabled for a list of views.
func (s *Store) UpdateCrontabViewsDisabled(ctx context.Context, ids []int64, disabled int) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, 0, len(ids)+2)
	args = append(args, disabled, SequelizeTime(time.Now()))
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}
	_, err := s.db.ExecContext(ctx,
		"UPDATE CrontabViews SET isDisabled = ?, updatedAt = ? WHERE id IN ("+
			strings.Join(placeholders, ",")+")", args...)
	return err
}

// DeleteCrontabViews removes views by id.
func (s *Store) DeleteCrontabViews(ctx context.Context, ids []int64) error {
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
		"DELETE FROM CrontabViews WHERE id IN ("+strings.Join(placeholders, ",")+")", args...)
	return err
}
