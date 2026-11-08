package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Crontab is one row of the `Crontabs` table. Nullable columns keep their
// database representation so the API can tell NULL from a zero value, which
// the Node panel serializes as `null` instead of `0`/`""`.
type Crontab struct {
	ID        int64
	Name      sql.NullString
	Command   string
	Schedule  sql.NullString
	Timestamp sql.NullString
	Saved     bool
	Status    sql.NullInt64

	IsSystem   sql.NullInt64
	Pid        sql.NullInt64
	IsDisabled sql.NullInt64
	IsPinned   sql.NullInt64

	LogPath     sql.NullString
	QueuedToken sql.NullString

	Labels sql.NullString

	LastRunningTime   sql.NullInt64
	LastExecutionTime sql.NullInt64
	SubID             sql.NullInt64

	ExtraSchedules sql.NullString

	TaskBefore sql.NullString
	TaskAfter  sql.NullString
	LogName    sql.NullString

	AllowMultipleInstances sql.NullInt64
	WorkDir                sql.NullString

	CreatedAt string
	UpdatedAt string
}

// CrontabColumns is the canonical column order used by every SELECT. It matches
// the physical table order so Sequelize's plain-object key order is preserved.
const CrontabColumns = "id, name, command, schedule, timestamp, saved, status, isSystem, pid, isDisabled, isPinned, log_path, queued_token, labels, last_running_time, last_execution_time, sub_id, extra_schedules, task_before, task_after, log_name, allow_multiple_instances, work_dir, createdAt, updatedAt"

// OrderTerm is one ORDER BY entry. Field is validated against the crontab
// column whitelist before it reaches SQL.
type OrderTerm struct {
	Field string
	Desc  bool
}

var crontabSortable = map[string]struct{}{
	"id": {}, "name": {}, "command": {}, "schedule": {}, "timestamp": {},
	"saved": {}, "status": {}, "isSystem": {}, "pid": {}, "isDisabled": {},
	"isPinned": {}, "log_path": {}, "queued_token": {}, "labels": {},
	"last_running_time": {}, "last_execution_time": {}, "sub_id": {},
	"extra_schedules": {}, "task_before": {}, "task_after": {}, "log_name": {},
	"allow_multiple_instances": {}, "work_dir": {}, "createdAt": {}, "updatedAt": {},
}

// DefaultCrontabOrder mirrors back/services/cron.ts:548-553. It is also the
// order used by setCrontab(), so crontab.list depends on it byte for byte.
var DefaultCrontabOrder = []OrderTerm{
	{Field: "isPinned", Desc: true},
	{Field: "isDisabled"},
	{Field: "status"},
	{Field: "createdAt", Desc: true},
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanCrontab(scanner rowScanner) (*Crontab, error) {
	var c Crontab
	var saved sql.NullInt64
	err := scanner.Scan(
		&c.ID, &c.Name, &c.Command, &c.Schedule, &c.Timestamp, &saved, &c.Status,
		&c.IsSystem, &c.Pid, &c.IsDisabled, &c.IsPinned, &c.LogPath, &c.QueuedToken,
		&c.Labels, &c.LastRunningTime, &c.LastExecutionTime, &c.SubID,
		&c.ExtraSchedules, &c.TaskBefore, &c.TaskAfter, &c.LogName,
		&c.AllowMultipleInstances, &c.WorkDir, &c.CreatedAt, &c.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	c.Saved = saved.Valid && saved.Int64 != 0
	return &c, nil
}

func orderClause(terms []OrderTerm) string {
	if len(terms) == 0 {
		return ""
	}
	parts := make([]string, 0, len(terms))
	for _, term := range terms {
		if _, ok := crontabSortable[term.Field]; !ok {
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

// ListCrontabs returns rows matching where (already parameterized), plus the
// total count for pagination. limit <= 0 means no LIMIT, mirroring the Node
// behaviour where page=0/size=0 returns every row.
func (s *Store) ListCrontabs(ctx context.Context, where string, args []any, order []OrderTerm, limit, offset int) ([]Crontab, int64, error) {
	query := "SELECT " + CrontabColumns + " FROM Crontabs"
	if strings.TrimSpace(where) != "" {
		query += " WHERE " + where
	}
	query += orderClause(order)
	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d OFFSET %d", limit, offset)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list crontabs: %w", err)
	}
	defer rows.Close()

	var items []Crontab
	for rows.Next() {
		item, err := scanCrontab(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan crontab: %w", err)
		}
		items = append(items, *item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate crontabs: %w", err)
	}

	countQuery := "SELECT COUNT(*) FROM Crontabs"
	if strings.TrimSpace(where) != "" {
		countQuery += " WHERE " + where
	}
	var total int64
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count crontabs: %w", err)
	}
	return items, total, nil
}

// AllCrontabs returns every row in setCrontab order.
func (s *Store) AllCrontabs(ctx context.Context) ([]Crontab, error) {
	items, _, err := s.ListCrontabs(ctx, "", nil, DefaultCrontabOrder, 0, 0)
	return items, err
}

// CrontabByID loads one row, returning nil when it does not exist.
func (s *Store) CrontabByID(ctx context.Context, id int64) (*Crontab, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT "+CrontabColumns+" FROM Crontabs WHERE id = ?", id)
	item, err := scanCrontab(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load crontab %d: %w", id, err)
	}
	return item, nil
}

// CrontabByLogPath mirrors CronService.find({log_path}).
func (s *Store) CrontabByLogPath(ctx context.Context, logPath string) (*Crontab, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT "+CrontabColumns+" FROM Crontabs WHERE log_path = ?", logPath)
	item, err := scanCrontab(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load crontab by log_path: %w", err)
	}
	return item, nil
}

// CrontabByCommandSchedule is used by the system-crontab importer.
func (s *Store) CrontabByCommandSchedule(ctx context.Context, command, schedule string) (*Crontab, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT "+CrontabColumns+" FROM Crontabs WHERE command = ? AND schedule = ?",
		command, schedule)
	item, err := scanCrontab(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load crontab by command: %w", err)
	}
	return item, nil
}

// InsertCrontab writes a new row and fills in ID.
func (s *Store) InsertCrontab(ctx context.Context, c *Crontab) error {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO Crontabs
		 (name, command, schedule, timestamp, saved, status, isSystem, pid, isDisabled, isPinned,
		  log_path, queued_token, labels, last_running_time, last_execution_time, sub_id,
		  extra_schedules, task_before, task_after, log_name, allow_multiple_instances, work_dir,
		  createdAt, updatedAt)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.Name, c.Command, c.Schedule, c.Timestamp, boolToInt(c.Saved), c.Status,
		c.IsSystem, c.Pid, c.IsDisabled, c.IsPinned, c.LogPath, c.QueuedToken,
		c.Labels, c.LastRunningTime, c.LastExecutionTime, c.SubID,
		c.ExtraSchedules, c.TaskBefore, c.TaskAfter, c.LogName,
		c.AllowMultipleInstances, c.WorkDir, c.CreatedAt, c.UpdatedAt,
	)
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("read inserted crontab id: %w", err)
	}
	c.ID = id
	return nil
}

var crontabUpdatable = map[string]struct{}{
	"name": {}, "command": {}, "schedule": {}, "timestamp": {}, "saved": {},
	"status": {}, "isSystem": {}, "pid": {}, "isDisabled": {}, "isPinned": {},
	"log_path": {}, "queued_token": {}, "labels": {}, "last_running_time": {},
	"last_execution_time": {}, "sub_id": {}, "extra_schedules": {},
	"task_before": {}, "task_after": {}, "log_name": {},
	"allow_multiple_instances": {}, "work_dir": {},
}

// UpdateCrontab applies a partial update and always touches updatedAt, like
// Sequelize's Model.update.
func (s *Store) UpdateCrontab(ctx context.Context, id int64, fields map[string]any) error {
	_, err := s.updateCrontabsWhere(ctx, "id = ?", []any{id}, fields)
	return err
}

// UpdateCrontabs applies the same partial update to a list of ids.
func (s *Store) UpdateCrontabs(ctx context.Context, ids []int64, fields map[string]any) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	return s.updateCrontabsWhere(ctx, "id IN ("+strings.Join(placeholders, ",")+")", args, fields)
}

// UpdateAllCrontabs applies a partial update to every row (setCrontab's
// `saved = true` sweep).
func (s *Store) UpdateAllCrontabs(ctx context.Context, fields map[string]any) error {
	_, err := s.updateCrontabsWhere(ctx, "", nil, fields)
	return err
}

func (s *Store) updateCrontabsWhere(ctx context.Context, where string, args []any, fields map[string]any) (int64, error) {
	if len(fields) == 0 {
		return 0, nil
	}
	sets := make([]string, 0, len(fields)+1)
	values := make([]any, 0, len(fields)+1)
	for key, value := range fields {
		if _, ok := crontabUpdatable[key]; !ok {
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

	query := "UPDATE Crontabs SET " + strings.Join(sets, ", ")
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

// DeleteCrontabs removes rows by id.
func (s *Store) DeleteCrontabs(ctx context.Context, ids []int64) error {
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
		"DELETE FROM Crontabs WHERE id IN ("+strings.Join(placeholders, ",")+")", args...)
	return err
}

// CrontabLabels decodes the JSON column. NULL stays nil, which serializes as
// JSON null just like Sequelize.
func CrontabLabels(raw sql.NullString) any {
	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return nil
	}
	var value any
	if err := json.Unmarshal([]byte(raw.String), &value); err != nil {
		return raw.String
	}
	return value
}

// CrontabExtraSchedules decodes the object-array column.
func CrontabExtraSchedules(raw sql.NullString) any {
	return CrontabLabels(raw)
}

// ISO8601 converts a Sequelize DATETIME string into the ISO form Express emits
// for a JS Date ("2026-11-07T05:06:49.870Z").
func ISO8601(raw string) *string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	for _, layout := range []string{
		"2006-01-02 15:04:05.000 -07:00",
		"2006-01-02 15:04:05 -07:00",
		"2006-01-02 15:04:05.999999999 -07:00",
		time.RFC3339Nano,
	} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			value := parsed.UTC().Format("2006-01-02T15:04:05.000Z")
			return &value
		}
	}
	value := raw
	return &value
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
