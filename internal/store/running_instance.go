package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// RunningInstance is one row of `RunningInstances` (back/data/runningInstance.ts).
type RunningInstance struct {
	ID         int64
	CronID     int64
	Pid        sql.NullInt64
	LogPath    sql.NullString
	StartedAt  int64
	FinishedAt sql.NullInt64
	Status     int64
	ExitCode   sql.NullInt64
	CreatedAt  string
	UpdatedAt  string
}

const runningInstanceColumns = "id, cron_id, pid, log_path, started_at, finished_at, status, exit_code, createdAt, updatedAt"

func scanRunningInstance(scanner rowScanner) (*RunningInstance, error) {
	var i RunningInstance
	if err := scanner.Scan(
		&i.ID, &i.CronID, &i.Pid, &i.LogPath, &i.StartedAt, &i.FinishedAt,
		&i.Status, &i.ExitCode, &i.CreatedAt, &i.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &i, nil
}

// ListRunningInstances returns a task's instances, newest first.
func (s *Store) ListRunningInstances(ctx context.Context, cronID int64) ([]RunningInstance, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT "+runningInstanceColumns+" FROM RunningInstances WHERE cron_id = ? ORDER BY started_at DESC",
		cronID)
	if err != nil {
		return nil, fmt.Errorf("list running instances: %w", err)
	}
	defer rows.Close()

	var items []RunningInstance
	for rows.Next() {
		item, err := scanRunningInstance(rows)
		if err != nil {
			return nil, fmt.Errorf("scan running instance: %w", err)
		}
		items = append(items, *item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate running instances: %w", err)
	}
	return items, nil
}

// RunningInstanceByID loads one instance, returning nil when absent.
func (s *Store) RunningInstanceByID(ctx context.Context, id int64) (*RunningInstance, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT "+runningInstanceColumns+" FROM RunningInstances WHERE id = ?", id)
	item, err := scanRunningInstance(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load running instance %d: %w", id, err)
	}
	return item, nil
}

// RunningInstancesByCron returns all instances of a task, newest first.
func (s *Store) RunningInstancesByCron(ctx context.Context, cronID int64) ([]RunningInstance, error) {
	return s.ListRunningInstances(ctx, cronID)
}

// RunningInstanceByLogPath finds a running instance for /api/logs/detail.
func (s *Store) RunningInstanceByLogPath(ctx context.Context, logPath string) (*RunningInstance, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT "+runningInstanceColumns+
			" FROM RunningInstances WHERE log_path = ? AND status = 0 LIMIT 1", logPath)
	item, err := scanRunningInstance(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load running instance by log_path: %w", err)
	}
	return item, nil
}

// InsertRunningInstance writes a new instance row.
func (s *Store) InsertRunningInstance(ctx context.Context, i *RunningInstance) error {
	now := SequelizeTime(time.Now())
	if i.CreatedAt == "" {
		i.CreatedAt = now
	}
	if i.UpdatedAt == "" {
		i.UpdatedAt = now
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO RunningInstances
		 (cron_id, pid, log_path, started_at, finished_at, status, exit_code, createdAt, updatedAt)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		i.CronID, i.Pid, i.LogPath, i.StartedAt, i.FinishedAt, i.Status, i.ExitCode,
		i.CreatedAt, i.UpdatedAt,
	)
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("read inserted instance id: %w", err)
	}
	i.ID = id
	return nil
}

// UpdateRunningInstance applies a partial update and touches updatedAt.
func (s *Store) UpdateRunningInstance(ctx context.Context, id int64, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	sets := make([]string, 0, len(fields)+1)
	values := make([]any, 0, len(fields)+1)
	for key, value := range fields {
		switch key {
		case "pid", "log_path", "finished_at", "status", "exit_code":
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
		"UPDATE RunningInstances SET "+strings.Join(sets, ", ")+" WHERE id = ?", values...)
	return err
}

// FinishRunningInstances marks the matching running rows as finished.
func (s *Store) FinishRunningInstances(ctx context.Context, cronID int64, pid int64, status int64, exitCode any) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE RunningInstances
		 SET finished_at = ?, status = ?, exit_code = ?, updatedAt = ?
		 WHERE cron_id = ? AND pid = ? AND status = 0`,
		time.Now().Unix(), status, exitCode, SequelizeTime(time.Now()), cronID, pid)
	return err
}

// CountRunningInstances counts rows still running for a task.
func (s *Store) CountRunningInstances(ctx context.Context, cronID int64) (int64, error) {
	var count int64
	err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM RunningInstances WHERE cron_id = ? AND status = 0", cronID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count running instances: %w", err)
	}
	return count, nil
}
