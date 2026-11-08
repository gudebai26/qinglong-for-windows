package store

import (
	"context"
	"fmt"
	"time"
)

// RecordCrontabStat increments the daily counters of one task. It is the Go
// equivalent of POST /open/dashboard/record (back/api/dashboard.ts): one row
// per task and day, totals in milliseconds, max_time keeps the slowest run.
func (s *Store) RecordCrontabStat(ctx context.Context, refID, code, elapsedSeconds int64) error {
	now := time.Now()
	date := now.Format("2006-01-02")
	elapsedMs := elapsedSeconds * 1000

	success, failure := int64(0), int64(1)
	if code == 0 {
		success, failure = 1, 0
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO CrontabStats
			(ref_id, date, run_count, success_count, fail_count, total_time, max_time, createdAt, updatedAt)
		VALUES (?, ?, 1, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(ref_id, date) DO UPDATE SET
			run_count = run_count + 1,
			success_count = success_count + excluded.success_count,
			fail_count = fail_count + excluded.fail_count,
			total_time = total_time + excluded.total_time,
			max_time = MAX(max_time, excluded.max_time),
			updatedAt = excluded.updatedAt`,
		refID, date, success, failure, elapsedMs, elapsedMs,
		SequelizeTime(now), SequelizeTime(now))
	if err != nil {
		return fmt.Errorf("record crontab stat: %w", err)
	}
	return nil
}
