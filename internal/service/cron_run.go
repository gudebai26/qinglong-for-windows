package service

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"qinglong-go/internal/platform"
	"qinglong-go/internal/runner"
	"qinglong-go/internal/store"
)

// InstanceResponse is the raw:true shape of GET /api/crons/:id/instances. Note
// that createdAt/updatedAt stay in Sequelize's storage format because the Node
// route queries with raw:true.
type InstanceResponse struct {
	ID         int64   `json:"id"`
	CronID     int64   `json:"cron_id"`
	Pid        *int64  `json:"pid"`
	LogPath    *string `json:"log_path"`
	StartedAt  int64   `json:"started_at"`
	FinishedAt *int64  `json:"finished_at"`
	Status     int64   `json:"status"`
	ExitCode   *int64  `json:"exit_code"`
	CreatedAt  string  `json:"createdAt"`
	UpdatedAt  string  `json:"updatedAt"`
}

// Instances implements GET /api/crons/:id/instances.
func (s *CronService) Instances(ctx context.Context, cronID int64) ([]InstanceResponse, error) {
	rows, err := s.store.ListRunningInstances(ctx, cronID)
	if err != nil {
		return nil, err
	}
	result := make([]InstanceResponse, 0, len(rows))
	for i := range rows {
		row := &rows[i]
		result = append(result, InstanceResponse{
			ID:         row.ID,
			CronID:     row.CronID,
			Pid:        nullableInt(row.Pid),
			LogPath:    nullableString(row.LogPath),
			StartedAt:  row.StartedAt,
			FinishedAt: nullableInt(row.FinishedAt),
			Status:     row.Status,
			ExitCode:   nullableInt(row.ExitCode),
			CreatedAt:  row.CreatedAt,
			UpdatedAt:  row.UpdatedAt,
		})
	}
	return result, nil
}

// Run implements PUT /api/crons/run. Manual runs share one queue, scheduled
// triggers use another; both are limited by cronConcurrency.
func (s *CronService) Run(ctx context.Context, ids []int64) error {
	if s.scheduler != nil {
		return s.scheduler.RunManual(ctx, ids)
	}
	if err := s.MarkQueued(ctx, ids); err != nil {
		return err
	}
	for _, id := range ids {
		cronID := id
		go s.RunQueued(context.Background(), cronID)
	}
	return nil
}

// MarkQueued flags the rows and mints one queued_token, exactly like
// CronService.run(ids) in Node.
func (s *CronService) MarkQueued(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := s.store.UpdateCrontabs(ctx, ids, map[string]any{
		"status":       CronStatusQueued,
		"queued_token": uuidV4(),
	})
	return err
}

// RunQueued executes one row that is already marked queued. It is the manual
// path (button, boot tasks) and keeps the queued -> running claim so a stop
// request can cancel the snapshot before the process starts.
func (s *CronService) RunQueued(ctx context.Context, cronID int64) {
	s.runQueued(ctx, cronID, "")
}

func (s *CronService) runQueued(ctx context.Context, cronID int64, expectedToken string) {
	row, err := s.store.CrontabByID(ctx, cronID)
	if err != nil || row == nil {
		return
	}
	if !row.Status.Valid || row.Status.Int64 != CronStatusQueued {
		return
	}
	if expectedToken != "" && stringValue(row.QueuedToken) != expectedToken {
		return
	}
	queuedToken := row.QueuedToken
	queuedLogPath := row.LogPath

	logPath, logFile, err := s.openTaskLog(row)
	if err != nil {
		s.logger.Error("[panel] task log rejected", "cron_id", cronID, "error", err)
		s.releaseQueued(cronID, queuedToken)
		return
	}
	defer logFile.Close()

	if !s.beginStart(cronID) {
		// Another trigger is already starting this task.
		s.releaseQueued(cronID, queuedToken)
		return
	}
	defer s.endStart(cronID)

	startedAt := time.Now()
	execution, err := s.startTask(row, logPath, logFile, true)
	if err != nil {
		s.logger.Error("[panel] start task", "cron_id", cronID, "error", err)
		s.releaseQueued(cronID, queuedToken)
		return
	}
	pid := int64(execution.Pid())
	s.trackRunning(pid)
	defer s.untrackRunning(pid)

	claimed := false
	defer func() {
		if !claimed {
			// The spawn never claimed the queued snapshot: release it so the
			// task does not stay stuck in "queued" after a start failure.
			s.releaseQueued(cronID, queuedToken)
		}
	}()

	nowText := store.SequelizeTime(time.Now())
	res, err := s.store.DB().ExecContext(context.Background(),
		`UPDATE Crontabs
		 SET status = ?, pid = ?, log_path = ?, updatedAt = ?
		 WHERE id = ? AND status = ? AND queued_token IS ? AND log_path IS ?`,
		CronStatusRunning, pid, logPath, nowText, cronID, CronStatusQueued, queuedToken, queuedLogPath)
	if err != nil {
		execution.Abort()
		s.logger.Error("[panel] claim queued task", "cron_id", cronID, "error", err)
		return
	}
	if affected, _ := res.RowsAffected(); affected != 1 {
		execution.Abort()
		return
	}
	claimed = true

	instanceID := s.insertInstance(cronID, pid, logPath, startedAt.Unix())
	result := s.waitTask(execution)
	s.finishQueued(cronID, pid, queuedToken, logPath, startedAt, result, instanceID)
}

// RunScheduled executes a trigger produced by the scheduler. Unlike the manual
// path it claims the running state directly, because there is no queued
// snapshot: the row goes idle -> running -> idle.
func (s *CronService) RunScheduled(ctx context.Context, cronID int64) {
	if err := s.runScheduled(ctx, cronID); err != nil {
		s.logger.Error("[schedule] run task failed", "cron_id", cronID, "error", err)
	}
}

func (s *CronService) runScheduled(ctx context.Context, cronID int64) error {
	if ctx.Err() != nil {
		return nil
	}
	row, err := s.store.CrontabByID(ctx, cronID)
	if err != nil || row == nil {
		return err
	}
	if isDisabled(row) {
		return nil
	}

	skip, err := s.checkSingleInstance(ctx, row)
	if err != nil {
		return err
	}
	if skip {
		return nil
	}

	// The stale recovery above may have rewritten status/pid.
	if row, err = s.store.CrontabByID(ctx, cronID); err != nil {
		return err
	}
	if row == nil || isDisabled(row) {
		return nil
	}

	logPath, logFile, err := s.openTaskLog(row)
	if err != nil {
		return err
	}
	defer logFile.Close()

	if !s.beginStart(cronID) {
		return nil
	}
	defer s.endStart(cronID)

	startedAt := time.Now()
	execution, err := s.startTask(row, logPath, logFile, false)
	if err != nil {
		return err
	}
	pid := int64(execution.Pid())
	s.trackRunning(pid)
	defer s.untrackRunning(pid)

	// A mutation or a competing rule may have started the task between the
	// check above and the spawn; only the winner keeps its process.
	claimed, err := s.claimRunning(cronID, pid, logPath, startedAt.Unix())
	if err != nil {
		execution.Abort()
		return err
	}
	if !claimed {
		execution.Abort()
		s.logger.Info("[schedule] task superseded before startup", "cron_id", cronID, "pid", pid)
		return nil
	}

	instanceID := s.insertInstance(cronID, pid, logPath, startedAt.Unix())
	result := s.waitTask(execution)
	s.finishScheduled(cronID, pid, startedAt, result, instanceID)
	return nil
}

// checkSingleInstance ports the guard at the top of runCron(): a task that does
// not allow parallel runs is skipped while a live instance exists, and a pid
// that is already dead is recovered before the new run starts.
func (s *CronService) checkSingleInstance(ctx context.Context, row *store.Crontab) (bool, error) {
	if allowMultipleInstances(row) {
		return false, nil
	}
	// A start in flight (random delay, spawn) is already a running task even
	// though no pid is stored yet.
	if s.isStarting(row.ID) {
		s.logger.Info("[schedule] skip starting single-instance task", "cron_id", row.ID)
		return true, nil
	}
	if !row.Status.Valid {
		return false, nil
	}
	switch row.Status.Int64 {
	case CronStatusQueued:
		// A queued manual run is an active reservation, even when the stored
		// pid belongs to a previous execution.
		if row.Pid.Valid && row.Pid.Int64 > 0 {
			s.logger.Info("[schedule] skip queued single-instance task",
				"cron_id", row.ID, "pid", row.Pid.Int64)
			return true, nil
		}
	case CronStatusRunning:
		if !row.Pid.Valid || row.Pid.Int64 <= 0 {
			return false, nil
		}
		pid := row.Pid.Int64
		if platform.ProcessAlive(int(pid)) {
			s.logger.Info("[schedule] skip active single-instance task",
				"cron_id", row.ID, "pid", pid)
			return true, nil
		}
		if err := s.recoverStaleRun(ctx, row, pid); err != nil {
			return false, err
		}
		s.logger.Info("[schedule] recovered stale single-instance task",
			"cron_id", row.ID, "pid", pid)
	}
	return false, nil
}

// recoverStaleRun releases a running row whose pid is gone and closes the
// orphaned instances, like the recovery branch in runCron.ts.
func (s *CronService) recoverStaleRun(ctx context.Context, row *store.Crontab, pid int64) error {
	now := time.Now()
	res, err := s.store.DB().ExecContext(ctx,
		`UPDATE Crontabs
		 SET status = ?, pid = NULL, queued_token = NULL, updatedAt = ?
		 WHERE id = ? AND status = ? AND pid = ? AND allow_multiple_instances = 0`,
		CronStatusIdle, store.SequelizeTime(now), row.ID, CronStatusRunning, pid)
	if err != nil {
		return err
	}
	if affected, _ := res.RowsAffected(); affected != 1 {
		// Someone else recovered or replaced the run first.
		return nil
	}
	_, err = s.store.DB().ExecContext(ctx,
		`UPDATE RunningInstances SET status = ?, finished_at = ?, updatedAt = ?
		 WHERE cron_id = ? AND pid = ? AND status = ?`,
		InstanceStopped, now.Unix(), store.SequelizeTime(now), row.ID, pid, InstanceRunning)
	return err
}

// claimRunning writes the running state with a compare-and-set so two rules
// firing at the same second cannot both own a single-instance task.
func (s *CronService) claimRunning(cronID, pid int64, logPath string, startedAt int64) (bool, error) {
	res, err := s.store.DB().ExecContext(context.Background(),
		`UPDATE Crontabs
		 SET status = ?, pid = ?, log_path = ?, last_execution_time = ?, updatedAt = ?
		 WHERE id = ? AND (isDisabled IS NULL OR isDisabled = 0)
		   AND (allow_multiple_instances <> 0 OR status IS NULL OR status IN (?, ?))`,
		CronStatusRunning, pid, logPath, startedAt, store.SequelizeTime(time.Now()),
		cronID, CronStatusIdle, CronStatusDisabled)
	if err != nil {
		return false, err
	}
	affected, _ := res.RowsAffected()
	return affected == 1, nil
}

func (s *CronService) finishScheduled(cronID, pid int64, startedAt time.Time, result runner.Result, instanceID int64) {
	ctx := context.Background()
	finishedAt := time.Now()
	// Only the instance that still owns the row may reset it, so a parallel
	// run (allow_multiple_instances=1) is not marked idle by a sibling.
	if _, err := s.store.DB().ExecContext(ctx,
		`UPDATE Crontabs
		 SET status = ?, pid = NULL, queued_token = NULL, last_execution_time = ?,
		     last_running_time = ?, updatedAt = ?
		 WHERE id = ? AND pid = ?`,
		CronStatusIdle, startedAt.Unix(), int64(finishedAt.Sub(startedAt).Seconds()),
		store.SequelizeTime(finishedAt), cronID, pid); err != nil {
		s.logger.Error("[schedule] clear task status", "cron_id", cronID, "error", err)
	}
	s.finishInstance(instanceID, finishedAt, result)
	s.recordStat(cronID, result.ExitCode, int64(finishedAt.Sub(startedAt).Seconds()))
}

func (s *CronService) finishQueued(cronID, pid int64, queuedToken sql.NullString, logPath string, startedAt time.Time, result runner.Result, instanceID int64) {
	ctx := context.Background()
	finishedAt := time.Now()
	if _, err := s.store.DB().ExecContext(ctx,
		`UPDATE Crontabs
		 SET status = ?, pid = NULL, queued_token = NULL, last_execution_time = ?,
		     last_running_time = ?, updatedAt = ?
		 WHERE id = ? AND status = ? AND queued_token IS ? AND log_path = ?`,
		CronStatusIdle, startedAt.Unix(), int64(finishedAt.Sub(startedAt).Seconds()),
		store.SequelizeTime(finishedAt), cronID, CronStatusRunning, queuedToken, logPath); err != nil {
		s.logger.Error("[panel] clear task status", "cron_id", cronID, "error", err)
	}
	s.finishInstance(instanceID, finishedAt, result)
	s.recordStat(cronID, result.ExitCode, int64(finishedAt.Sub(startedAt).Seconds()))
}

// recordStat writes the per-day dashboard counters, the Go equivalent of
// shell/api.sh record_cron_stat -> POST /open/dashboard/record.
func (s *CronService) recordStat(cronID, exitCode, elapsedSeconds int64) {
	if cronID <= 0 {
		return
	}
	if elapsedSeconds <= 0 {
		elapsedSeconds = 1
	}
	if err := s.store.RecordCrontabStat(context.Background(), cronID, exitCode, elapsedSeconds); err != nil {
		s.logger.Error("[panel] record task stat", "cron_id", cronID, "error", err)
	}
}

func (s *CronService) insertInstance(cronID, pid int64, logPath string, startedAt int64) int64 {
	instance := &store.RunningInstance{
		CronID:    cronID,
		Pid:       sql.NullInt64{Int64: pid, Valid: true},
		LogPath:   sql.NullString{String: logPath, Valid: true},
		StartedAt: startedAt,
		Status:    InstanceRunning,
	}
	if err := s.store.InsertRunningInstance(context.Background(), instance); err != nil {
		s.logger.Error("[panel] record running instance", "cron_id", cronID, "error", err)
		return 0
	}
	return instance.ID
}

func (s *CronService) finishInstance(instanceID int64, finishedAt time.Time, result runner.Result) {
	if instanceID <= 0 {
		return
	}
	status := InstanceFinished
	switch {
	case result.Stopped:
		// A task the panel stopped is "stopped", not "failed", even though its
		// exit code is 143.
		status = InstanceStopped
	case result.ExitCode != 0:
		status = InstanceError
	}
	if err := s.store.UpdateRunningInstance(context.Background(), instanceID, map[string]any{
		"finished_at": finishedAt.Unix(),
		"status":      status,
		"exit_code":   result.ExitCode,
	}); err != nil {
		s.logger.Error("[panel] finish running instance", "instance_id", instanceID, "error", err)
	}
}

// openTaskLog builds the log path the same way runSingle does and creates the
// file. The returned path is relative to data/log, as stored in log_path.
//
// Two rules of one task can fire in the same millisecond (main schedule and an
// extra_schedules item), so the name is claimed with O_EXCL and a numeric
// suffix is appended on collision: every run gets its own log file.
func (s *CronService) openTaskLog(row *store.Crontab) (string, *os.File, error) {
	uniqPath := ""
	if !row.LogName.Valid || row.LogName.String == "" || row.LogName.String == "/dev/null" {
		uniqPath = GetUniqPath(row.Command, row.ID, true)
	} else {
		uniqPath = row.LogName.String
	}
	now := time.Now()
	base := fmt.Sprintf("%s/%s-%03d", uniqPath, now.Format("2006-01-02-15-04-05"), now.Nanosecond()/int(time.Millisecond))

	var lastErr error
	for attempt := 0; attempt < 50; attempt++ {
		name := base
		if attempt > 0 {
			name = fmt.Sprintf("%s-%d", base, attempt)
		}
		logPath := name + ".log"
		absolute, err := s.resolveLogPath(logPath)
		if err != nil {
			return "", nil, err
		}
		if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
			return "", nil, fmt.Errorf("create task log directory: %w", err)
		}
		file, err := os.OpenFile(absolute, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			return logPath, file, nil
		}
		lastErr = err
		if os.IsExist(err) {
			continue
		}
		// A virus scanner can hold a freshly created file for a few
		// milliseconds on Windows; retry under a new name.
		time.Sleep(10 * time.Millisecond)
	}
	return "", nil, fmt.Errorf("open task log: %w", lastErr)
}

// startTask spawns the command through the M5 runner. Manual runs pass
// no_delay=true and real_time like Node's runSingle; scheduled runs keep the
// crontab.list form so the runner applies RandomDelay itself.
//
// The runner starts node/python/bash directly - never through a shell - writes
// the log header and applies the random delay before the process exists.
func (s *CronService) startTask(row *store.Crontab, logPath string, out io.Writer, manual bool) (*runner.Execution, error) {
	var command string
	if manual {
		command = "real_log_path=" + logPath + " no_delay=true " + s.MakeCommand(row, true)
	} else {
		command = "real_log_path=" + logPath + " " + s.MakeCommand(row, false)
	}
	execution, err := s.runner.Prepare(runner.Request{
		Command: command,
		CronID:  row.ID,
		Sink:    out,
	})
	if err != nil {
		return nil, err
	}
	if err := execution.Start(); err != nil {
		return nil, fmt.Errorf("start task: %w", err)
	}
	return execution, nil
}

func (s *CronService) waitTask(execution *runner.Execution) runner.Result {
	return execution.Wait()
}

// beginStart reserves the task for one start attempt. It returns false when
// another goroutine is already starting it.
func (s *CronService) beginStart(cronID int64) bool {
	_, loaded := s.starting.LoadOrStore(cronID, struct{}{})
	return !loaded
}

func (s *CronService) endStart(cronID int64) { s.starting.Delete(cronID) }

func (s *CronService) isStarting(cronID int64) bool {
	_, ok := s.starting.Load(cronID)
	return ok
}

func (s *CronService) releaseQueued(cronID int64, token sql.NullString) {
	_, _ = s.store.DB().ExecContext(context.Background(),
		`UPDATE Crontabs SET status = ?, pid = NULL, queued_token = NULL, updatedAt = ?
		 WHERE id = ? AND status = ? AND queued_token IS ?`,
		CronStatusIdle, store.SequelizeTime(time.Now()), cronID, CronStatusQueued, token)
}

func allowMultipleInstances(row *store.Crontab) bool {
	return row.AllowMultipleInstances.Valid && row.AllowMultipleInstances.Int64 != 0
}

// trackRunning remembers child pids so shutdown can end them. Node lets the
// worker die with the panel; the Go panel kills the process tree explicitly so
// a stopped panel does not leave tasks running.
func (s *CronService) trackRunning(pid int64) {
	if pid > 0 {
		s.running.Store(pid, struct{}{})
	}
}

func (s *CronService) untrackRunning(pid int64) {
	s.running.Delete(pid)
}

// Shutdown ends every task the panel started and waits for the run goroutines
// to write their final state.
func (s *CronService) Shutdown(timeout time.Duration) {
	s.running.Range(func(key, _ any) bool {
		if pid, ok := key.(int64); ok {
			_ = platform.KillProcessTree(int(pid))
		}
		return true
	})
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		empty := true
		s.running.Range(func(_, _ any) bool {
			empty = false
			return false
		})
		if empty {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Stop implements PUT /api/crons/stop.
func (s *CronService) Stop(ctx context.Context, ids []int64) (*Reply, error) {
	docs := make([]*store.Crontab, 0, len(ids))
	for _, id := range ids {
		row, err := s.store.CrontabByID(ctx, id)
		if err != nil {
			return nil, err
		}
		if row != nil {
			docs = append(docs, row)
		}
	}

	for _, doc := range docs {
		if !doc.Status.Valid || doc.Status.Int64 != CronStatusQueued {
			continue
		}
		res, err := s.store.DB().ExecContext(ctx,
			`UPDATE Crontabs SET status = ?, pid = NULL, queued_token = NULL, updatedAt = ?
			 WHERE id = ? AND status = ? AND queued_token IS ? AND log_path IS ?`,
			CronStatusIdle, store.SequelizeTime(time.Now()), doc.ID, CronStatusQueued,
			doc.QueuedToken, doc.LogPath)
		if err != nil {
			return nil, err
		}
		if affected, _ := res.RowsAffected(); affected == 0 {
			if reloaded, err := s.store.CrontabByID(ctx, doc.ID); err == nil && reloaded != nil {
				*doc = *reloaded
			}
		}
	}

	type target struct {
		cronID int64
		pid    int64
	}
	targets := map[int64]target{}
	instancesByCron := map[int64][]store.RunningInstance{}
	for _, doc := range docs {
		instances, err := s.store.ListRunningInstances(ctx, doc.ID)
		if err != nil {
			return nil, err
		}
		for _, instance := range instances {
			if instance.Status != InstanceRunning || !instance.Pid.Valid || instance.Pid.Int64 <= 0 {
				continue
			}
			instancesByCron[doc.ID] = append(instancesByCron[doc.ID], instance)
			targets[instance.Pid.Int64] = target{cronID: doc.ID, pid: instance.Pid.Int64}
		}
		if doc.Status.Valid && doc.Status.Int64 == CronStatusRunning && doc.Pid.Valid && doc.Pid.Int64 > 0 {
			targets[doc.Pid.Int64] = target{cronID: doc.ID, pid: doc.Pid.Int64}
		}
	}

	stopped := map[int64]struct{}{}
	// Ask the runner first: it marks the run as stopped (so the footer says
	// "已停止" and the exit code is 143) and sends SIGTERM to the process
	// group. The hard kill below is both the escalation and the fallback for
	// pids this process does not own.
	for _, doc := range docs {
		s.runner.Stop(doc.ID)
	}
	for pid := range targets {
		if err := platform.KillProcessTree(int(pid)); err != nil {
			s.logger.Error("[panel] stop task failed", "pid", pid, "error", err)
			continue
		}
		stopped[pid] = struct{}{}
	}

	for _, instances := range instancesByCron {
		for _, instance := range instances {
			if _, ok := stopped[instance.Pid.Int64]; !ok {
				continue
			}
			if err := s.store.UpdateRunningInstance(ctx, instance.ID, map[string]any{
				"status":      InstanceStopped,
				"finished_at": time.Now().Unix(),
			}); err != nil {
				return nil, err
			}
		}
	}

	for _, doc := range docs {
		if !doc.Status.Valid || doc.Status.Int64 != CronStatusRunning {
			continue
		}
		if doc.Pid.Valid {
			if _, ok := stopped[doc.Pid.Int64]; !ok {
				continue
			}
		}
		remaining, err := s.store.CountRunningInstances(ctx, doc.ID)
		if err != nil {
			return nil, err
		}
		if remaining > 0 {
			continue
		}
		if _, err := s.store.DB().ExecContext(ctx,
			`UPDATE Crontabs SET status = ?, pid = NULL, queued_token = NULL, updatedAt = ?
			 WHERE id = ? AND status = ? AND queued_token IS ? AND pid IS ? AND log_path IS ?`,
			CronStatusIdle, store.SequelizeTime(time.Now()), doc.ID, CronStatusRunning,
			doc.QueuedToken, doc.Pid, doc.LogPath); err != nil {
			return nil, err
		}
	}
	return &Reply{Code: 200}, nil
}

// StopInstance implements POST /api/crons/:id/instances/:instanceId/stop.
func (s *CronService) StopInstance(ctx context.Context, instanceID int64) (*Reply, error) {
	instance, err := s.store.RunningInstanceByID(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	if instance == nil || instance.Status != InstanceRunning {
		return &Reply{Code: 400, Message: "实例不存在或已停止"}, nil
	}
	if instance.Pid.Valid && instance.Pid.Int64 > 0 {
		if err := platform.KillProcessTree(int(instance.Pid.Int64)); err != nil {
			s.logger.Error("stop instance failed", "instance_id", instanceID, "pid", instance.Pid.Int64, "error", err)
		}
	}
	if err := s.store.UpdateRunningInstance(ctx, instanceID, map[string]any{
		"status":      InstanceStopped,
		"finished_at": time.Now().Unix(),
		"exit_code":   int64(143),
	}); err != nil {
		return nil, err
	}

	remaining, err := s.store.CountRunningInstances(ctx, instance.CronID)
	if err != nil {
		return nil, err
	}
	if remaining == 0 {
		if _, err := s.store.UpdateCrontabs(ctx, []int64{instance.CronID}, map[string]any{
			"status": CronStatusIdle,
		}); err != nil {
			return nil, err
		}
	}
	return &Reply{Code: 200, Message: "实例已停止"}, nil
}
