package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// CrontabStat is one aggregated row of `CrontabStats`.
type CrontabStat struct {
	Date         string
	RefID        int64
	RunCount     int64
	SuccessCount int64
	FailCount    int64
	TotalTime    int64
	MaxTime      int64
}

// CronBrief is the subset of a task the dashboard joins names from.
type CronBrief struct {
	ID      int64
	Name    sql.NullString
	Command string
}

// CronStatBrief is a task plus the counters the label chart needs.
type CronStatBrief struct {
	ID         int64
	Labels     sql.NullString
	RunCount   int64
	SuccessCnt int64
	TotalTime  int64
}

// CountCrons counts all tasks and the enabled/disabled split.
func (s *Store) CountCrons(ctx context.Context) (total, enabled, disabled int64, err error) {
	err = s.DB().QueryRowContext(ctx,
		"SELECT COUNT(*), COALESCE(SUM(CASE WHEN isDisabled = 0 THEN 1 ELSE 0 END), 0), "+
			"COALESCE(SUM(CASE WHEN isDisabled = 1 THEN 1 ELSE 0 END), 0) FROM Crontabs").
		Scan(&total, &enabled, &disabled)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("count crontabs: %w", err)
	}
	return total, enabled, disabled, nil
}

// StatOverview sums today's counters. The SUM of an empty set is NULL, which
// the caller treats as zero.
func (s *Store) StatOverview(ctx context.Context, date string) (CrontabStat, error) {
	var runs, success, fail, totalTime sql.NullInt64
	err := s.DB().QueryRowContext(ctx,
		"SELECT SUM(run_count), SUM(success_count), SUM(fail_count), SUM(total_time) FROM CrontabStats WHERE date = ?",
		date).Scan(&runs, &success, &fail, &totalTime)
	if err != nil {
		return CrontabStat{}, fmt.Errorf("sum crontab stats: %w", err)
	}
	return CrontabStat{
		RunCount:     runs.Int64,
		SuccessCount: success.Int64,
		FailCount:    fail.Int64,
		TotalTime:    totalTime.Int64,
	}, nil
}

// StatByRef groups one day's counters per task. column is success_count or
// fail_count; rows without any result are skipped, like `[Op.gt]: 0`.
func (s *Store) StatByRef(ctx context.Context, date, column string) ([]CrontabStat, error) {
	if column != "success_count" && column != "fail_count" {
		return nil, fmt.Errorf("unsupported stat column %q", column)
	}
	query := "SELECT ref_id, SUM(" + column + ") FROM CrontabStats WHERE date = ? AND " + column +
		" > 0 GROUP BY ref_id ORDER BY SUM(" + column + ") DESC, ref_id ASC"
	rows, err := s.DB().QueryContext(ctx, query, date)
	if err != nil {
		return nil, fmt.Errorf("group crontab stats: %w", err)
	}
	defer rows.Close()

	var out []CrontabStat
	for rows.Next() {
		var item CrontabStat
		if err := rows.Scan(&item.RefID, &item.RunCount); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// CronBriefs loads the id/name/command triples for a set of ids.
func (s *Store) CronBriefs(ctx context.Context, ids []int64) (map[int64]CronBrief, error) {
	out := map[int64]CronBrief{}
	if len(ids) == 0 {
		return out, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.DB().QueryContext(ctx,
		"SELECT id, name, COALESCE(command, '') FROM Crontabs WHERE id IN ("+placeholders+")", args...)
	if err != nil {
		return nil, fmt.Errorf("load cron briefs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var brief CronBrief
		if err := rows.Scan(&brief.ID, &brief.Name, &brief.Command); err != nil {
			return nil, err
		}
		out[brief.ID] = brief
	}
	return out, rows.Err()
}

// StatTrend groups the given dates by day.
func (s *Store) StatTrend(ctx context.Context, dates []string) ([]CrontabStat, error) {
	if len(dates) == 0 {
		return nil, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(dates)), ",")
	args := make([]any, 0, len(dates))
	for _, date := range dates {
		args = append(args, date)
	}
	rows, err := s.DB().QueryContext(ctx,
		"SELECT date, SUM(run_count), SUM(success_count), SUM(fail_count) FROM CrontabStats "+
			"WHERE date IN ("+placeholders+") GROUP BY date ORDER BY date ASC", args...)
	if err != nil {
		return nil, fmt.Errorf("trend crontab stats: %w", err)
	}
	defer rows.Close()

	var out []CrontabStat
	for rows.Next() {
		var item CrontabStat
		if err := rows.Scan(&item.Date, &item.RunCount, &item.SuccessCount, &item.FailCount); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// StatTopByTime returns the five slowest tasks of a day.
func (s *Store) StatTopByTime(ctx context.Context, date string, limit int) ([]CrontabStat, error) {
	rows, err := s.DB().QueryContext(ctx,
		"SELECT ref_id, SUM(total_time), SUM(run_count), MAX(max_time) FROM CrontabStats "+
			"WHERE date = ? AND run_count > 0 GROUP BY ref_id ORDER BY SUM(total_time) DESC LIMIT ?",
		date, limit)
	if err != nil {
		return nil, fmt.Errorf("top time: %w", err)
	}
	defer rows.Close()

	var out []CrontabStat
	for rows.Next() {
		var item CrontabStat
		if err := rows.Scan(&item.RefID, &item.TotalTime, &item.RunCount, &item.MaxTime); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// StatTopByCount returns the five most frequently run tasks of a day.
func (s *Store) StatTopByCount(ctx context.Context, date string, limit int) ([]CrontabStat, error) {
	rows, err := s.DB().QueryContext(ctx,
		"SELECT ref_id, SUM(run_count), SUM(total_time), SUM(success_count) FROM CrontabStats "+
			"WHERE date = ? AND run_count > 0 GROUP BY ref_id ORDER BY SUM(run_count) DESC LIMIT ?",
		date, limit)
	if err != nil {
		return nil, fmt.Errorf("top count: %w", err)
	}
	defer rows.Close()

	var out []CrontabStat
	for rows.Next() {
		var item CrontabStat
		if err := rows.Scan(&item.RefID, &item.RunCount, &item.TotalTime, &item.SuccessCount); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// RunningInstanceRow is the running-instance shape the runtime chart needs.
type RunningInstanceRow struct {
	ID        int64
	CronID    int64
	Pid       sql.NullInt64
	LogPath   sql.NullString
	StartedAt int64
}

// RunningInstancesByStatus lists instances with this status.
func (s *Store) RunningInstancesByStatus(ctx context.Context, status int64) ([]RunningInstanceRow, error) {
	rows, err := s.DB().QueryContext(ctx,
		"SELECT id, cron_id, pid, log_path, started_at FROM RunningInstances WHERE status = ?", status)
	if err != nil {
		return nil, fmt.Errorf("list running instances: %w", err)
	}
	defer rows.Close()

	var out []RunningInstanceRow
	for rows.Next() {
		var item RunningInstanceRow
		if err := rows.Scan(&item.ID, &item.CronID, &item.Pid, &item.LogPath, &item.StartedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// CronIDsByStatus lists the tasks currently in one status (3 = queued).
func (s *Store) CronIDsByStatus(ctx context.Context, status int64) ([]int64, error) {
	rows, err := s.DB().QueryContext(ctx, "SELECT id FROM Crontabs WHERE status = ?", status)
	if err != nil {
		return nil, fmt.Errorf("list crontabs by status: %w", err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// IdleCrons lists enabled, idle tasks whose last run is older than cutoff.
func (s *Store) IdleCrons(ctx context.Context, cutoff int64, limit int) ([]CronBrief, error) {
	rows, err := s.DB().QueryContext(ctx,
		"SELECT id, name, COALESCE(command, ''), last_execution_time FROM Crontabs "+
			"WHERE isDisabled = 0 AND status = 1 AND last_execution_time < ? "+
			"ORDER BY last_execution_time ASC LIMIT ?", cutoff, limit)
	if err != nil {
		return nil, fmt.Errorf("list idle crontabs: %w", err)
	}
	defer rows.Close()

	var out []CronBrief
	for rows.Next() {
		var brief CronBrief
		var last sql.NullInt64
		if err := rows.Scan(&brief.ID, &brief.Name, &brief.Command, &last); err != nil {
			return nil, err
		}
		out = append(out, brief)
	}
	return out, rows.Err()
}

// LastExecution returns the last execution time of a task, used by the idle
// chart.
func (s *Store) LastExecution(ctx context.Context, id int64) (int64, error) {
	var value sql.NullInt64
	err := s.DB().QueryRowContext(ctx, "SELECT last_execution_time FROM Crontabs WHERE id = ?", id).Scan(&value)
	if err != nil {
		return 0, fmt.Errorf("read last execution: %w", err)
	}
	return value.Int64, nil
}

// LabelSource pairs an enabled task with today's counters for the labels chart.
func (s *Store) LabelSource(ctx context.Context, date string) ([]CronStatBrief, error) {
	rows, err := s.DB().QueryContext(ctx,
		"SELECT c.id, c.labels, COALESCE(s.run_count, 0), COALESCE(s.success_count, 0), COALESCE(s.total_time, 0) "+
			"FROM Crontabs c LEFT JOIN CrontabStats s ON s.ref_id = c.id AND s.date = ? "+
			"WHERE c.isDisabled = 0 ORDER BY c.id", date)
	if err != nil {
		return nil, fmt.Errorf("load label sources: %w", err)
	}
	defer rows.Close()

	var out []CronStatBrief
	for rows.Next() {
		var item CronStatBrief
		if err := rows.Scan(&item.ID, &item.Labels, &item.RunCount, &item.SuccessCnt, &item.TotalTime); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// CountRetainedRunningInstances counts finished instances older than the
// cutoff, matching retention.ts runningInstanceWhere.
func (s *Store) CountRetainedRunningInstances(ctx context.Context, cutoff int64) (int64, error) {
	var count int64
	err := s.DB().QueryRowContext(ctx,
		"SELECT COUNT(*) FROM RunningInstances WHERE status <> 0 AND "+
			"(finished_at < ? OR (finished_at IS NULL AND started_at < ?))", cutoff, cutoff).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count retained instances: %w", err)
	}
	return count, nil
}

// DeleteRetainedRunningInstances removes the rows counted above.
func (s *Store) DeleteRetainedRunningInstances(ctx context.Context, cutoff int64) (int64, error) {
	result, err := s.DB().ExecContext(ctx,
		"DELETE FROM RunningInstances WHERE status <> 0 AND "+
			"(finished_at < ? OR (finished_at IS NULL AND started_at < ?))", cutoff, cutoff)
	if err != nil {
		return 0, fmt.Errorf("delete retained instances: %w", err)
	}
	return result.RowsAffected()
}

// CountRetainedCronStats counts daily buckets older than the cutoff date.
func (s *Store) CountRetainedCronStats(ctx context.Context, beforeDate string) (int64, error) {
	var count int64
	err := s.DB().QueryRowContext(ctx,
		"SELECT COUNT(*) FROM CrontabStats WHERE date < ?", beforeDate).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count retained stats: %w", err)
	}
	return count, nil
}

// DeleteRetainedCronStats removes the buckets counted above.
func (s *Store) DeleteRetainedCronStats(ctx context.Context, beforeDate string) (int64, error) {
	result, err := s.DB().ExecContext(ctx, "DELETE FROM CrontabStats WHERE date < ?", beforeDate)
	if err != nil {
		return 0, fmt.Errorf("delete retained stats: %w", err)
	}
	return result.RowsAffected()
}

// Vacuum compacts the database after rows were deleted.
func (s *Store) Vacuum(ctx context.Context) error {
	if _, err := s.DB().ExecContext(ctx, "VACUUM"); err != nil {
		return fmt.Errorf("vacuum: %w", err)
	}
	return nil
}

// RunningLogPaths returns the log_path of every running instance, which the
// log purge must not delete.
func (s *Store) RunningLogPaths(ctx context.Context) (map[string]struct{}, error) {
	rows, err := s.DB().QueryContext(ctx,
		"SELECT log_path FROM RunningInstances WHERE status = 0 AND log_path IS NOT NULL")
	if err != nil {
		return nil, fmt.Errorf("list running log paths: %w", err)
	}
	defer rows.Close()
	out := map[string]struct{}{}
	for rows.Next() {
		var value sql.NullString
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		if value.Valid && value.String != "" {
			out[value.String] = struct{}{}
		}
	}
	return out, rows.Err()
}
