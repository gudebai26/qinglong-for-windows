package service

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"qinglong-go/internal/config"
	"qinglong-go/internal/platform"
	"qinglong-go/internal/store"
)

type fakeScheduler struct {
	mu          sync.Mutex
	reloads     int
	manualCalls [][]int64
}

func (f *fakeScheduler) Reload(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reloads++
	return nil
}

func (f *fakeScheduler) RunManual(_ context.Context, ids []int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.manualCalls = append(f.manualCalls, append([]int64(nil), ids...))
	return nil
}

func (f *fakeScheduler) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reloads, len(f.manualCalls)
}

func newTestCronService(t *testing.T) (*CronService, *store.Store) {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{
		RootPath:   root,
		DataPath:   filepath.Join(root, "data"),
		ConfigPath: filepath.Join(root, "data", "config"),
		LogPath:    filepath.Join(root, "data", "log"),
		DBPath:     filepath.Join(root, "data", "db"),
		ScriptPath: filepath.Join(root, "data", "scripts"),
		DepPath:    filepath.Join(root, "data", "deps"),
	}
	for _, dir := range []string{cfg.ConfigPath, cfg.LogPath, cfg.DBPath, cfg.ScriptPath} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	st, err := store.Open(filepath.Join(cfg.DBPath, "database.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(testLogWriter{t}, &slog.HandlerOptions{Level: slog.LevelError}))
	return NewCronService(cfg, st, logger), st
}

// writeScript puts a task script into data/scripts and returns its name.
func writeScript(t *testing.T, cfg *config.Config, name, content string) string {
	t.Helper()
	path := filepath.Join(cfg.ScriptPath, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write script %s: %v", name, err)
	}
	return name
}

// requireNode skips a test when the interpreter the runner needs is missing.
func requireNode(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
}

type testLogWriter struct{ t *testing.T }

func (w testLogWriter) Write(p []byte) (int, error) {
	w.t.Log(string(p))
	return len(p), nil
}

func insertRow(t *testing.T, st *store.Store, mutate func(*store.Crontab)) int64 {
	t.Helper()
	now := store.SequelizeTime(time.Now())
	row := &store.Crontab{
		Command:                "task demo.js",
		Schedule:               sql.NullString{String: "0 0 * * *", Valid: true},
		Timestamp:              sql.NullString{String: now, Valid: true},
		Status:                 sql.NullInt64{Int64: CronStatusIdle, Valid: true},
		IsSystem:               sql.NullInt64{Int64: 0, Valid: true},
		IsDisabled:             sql.NullInt64{Int64: 0, Valid: true},
		IsPinned:               sql.NullInt64{Int64: 0, Valid: true},
		LogPath:                sql.NullString{String: "", Valid: true},
		Labels:                 sql.NullString{String: "[]", Valid: true},
		LastRunningTime:        sql.NullInt64{Int64: 0, Valid: true},
		LastExecutionTime:      sql.NullInt64{Int64: 0, Valid: true},
		AllowMultipleInstances: sql.NullInt64{Int64: 0, Valid: true},
		LogName:                sql.NullString{String: "demo_1", Valid: true},
		CreatedAt:              now,
		UpdatedAt:              now,
	}
	if mutate != nil {
		mutate(row)
	}
	if err := st.InsertCrontab(context.Background(), row); err != nil {
		t.Fatalf("insert: %v", err)
	}
	return row.ID
}

// A scheduled trigger must expose running + pid + log_path while the task
// runs, then settle on idle + last_execution_time + exit_code.
func TestRunScheduledStateMachine(t *testing.T) {
	requireNode(t)
	svc, st := newTestCronService(t)
	writeScript(t, svc.cfg, "demo.js", "setTimeout(() => console.log('done'), 800);\n")
	cronID := insertRow(t, st, func(row *store.Crontab) { row.Command = "task demo.js" })

	done := make(chan struct{})
	go func() {
		svc.RunScheduled(context.Background(), cronID)
		close(done)
	}()

	var running *store.Crontab
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		row, err := st.CrontabByID(context.Background(), cronID)
		if err != nil {
			t.Fatalf("load row: %v", err)
		}
		if row != nil && row.Status.Valid && row.Status.Int64 == CronStatusRunning {
			running = row
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if running == nil {
		t.Fatal("task never reported running")
	}
	if !running.Pid.Valid || running.Pid.Int64 <= 0 {
		t.Fatal("running row has no pid")
	}
	if !running.LogPath.Valid || running.LogPath.String == "" {
		t.Fatal("running row has no log_path")
	}
	if !running.LastExecutionTime.Valid || running.LastExecutionTime.Int64 == 0 {
		t.Fatal("running row has no last_execution_time")
	}
	if _, err := os.Stat(filepath.Join(svc.cfg.LogPath, running.LogPath.String)); err != nil {
		t.Fatalf("log file %s was not created", running.LogPath.String)
	}

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("task did not finish")
	}

	final, err := st.CrontabByID(context.Background(), cronID)
	if err != nil {
		t.Fatalf("load final row: %v", err)
	}
	if final.Status.Int64 != CronStatusIdle {
		t.Fatalf("final status = %d, want idle", final.Status.Int64)
	}
	if final.Pid.Valid {
		t.Fatalf("final pid = %v, want NULL", final.Pid.Int64)
	}
	if final.LastExecutionTime.Int64 == 0 {
		t.Fatal("last_execution_time was not written")
	}

	instances, err := st.ListRunningInstances(context.Background(), cronID)
	if err != nil {
		t.Fatalf("list instances: %v", err)
	}
	if len(instances) != 1 {
		t.Fatalf("instances = %d, want 1", len(instances))
	}
	instance := instances[0]
	if instance.Status == InstanceRunning {
		t.Fatal("instance was left running")
	}
	if !instance.ExitCode.Valid {
		t.Fatal("instance has no exit_code")
	}
	if !instance.FinishedAt.Valid {
		t.Fatal("instance has no finished_at")
	}
}

func TestSingleInstanceSkipsLivePid(t *testing.T) {
	svc, st := newTestCronService(t)
	self := int64(os.Getpid())
	cronID := insertRow(t, st, func(row *store.Crontab) {
		row.Status = sql.NullInt64{Int64: CronStatusRunning, Valid: true}
		row.Pid = sql.NullInt64{Int64: self, Valid: true}
	})
	row, err := st.CrontabByID(context.Background(), cronID)
	if err != nil {
		t.Fatal(err)
	}
	skip, err := svc.checkSingleInstance(context.Background(), row)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !skip {
		t.Fatal("live pid should skip the run")
	}
}

func TestSingleInstanceRecoversDeadPid(t *testing.T) {
	svc, st := newTestCronService(t)
	deadPid := int64(0x7FFFFFF0)
	cronID := insertRow(t, st, func(row *store.Crontab) {
		row.Status = sql.NullInt64{Int64: CronStatusRunning, Valid: true}
		row.Pid = sql.NullInt64{Int64: deadPid, Valid: true}
	})
	instance := &store.RunningInstance{
		CronID:    cronID,
		Pid:       sql.NullInt64{Int64: deadPid, Valid: true},
		StartedAt: time.Now().Add(-time.Minute).Unix(),
		Status:    InstanceRunning,
	}
	if err := st.InsertRunningInstance(context.Background(), instance); err != nil {
		t.Fatalf("insert instance: %v", err)
	}

	row, err := st.CrontabByID(context.Background(), cronID)
	if err != nil {
		t.Fatal(err)
	}
	skip, err := svc.checkSingleInstance(context.Background(), row)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if skip {
		t.Fatal("dead pid must be recovered, not skipped")
	}

	recovered, err := st.CrontabByID(context.Background(), cronID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status.Int64 != CronStatusIdle || recovered.Pid.Valid {
		t.Fatalf("row not recovered: status=%d pid=%v", recovered.Status.Int64, recovered.Pid)
	}
	reloaded, err := st.RunningInstanceByID(context.Background(), instance.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Status != InstanceStopped {
		t.Fatalf("stale instance status = %d, want stopped", reloaded.Status)
	}
}

func TestAllowMultipleInstancesBypassesGuard(t *testing.T) {
	svc, st := newTestCronService(t)
	cronID := insertRow(t, st, func(row *store.Crontab) {
		row.Status = sql.NullInt64{Int64: CronStatusRunning, Valid: true}
		row.Pid = sql.NullInt64{Int64: int64(os.Getpid()), Valid: true}
		row.AllowMultipleInstances = sql.NullInt64{Int64: 1, Valid: true}
	})
	row, err := st.CrontabByID(context.Background(), cronID)
	if err != nil {
		t.Fatal(err)
	}
	skip, err := svc.checkSingleInstance(context.Background(), row)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if skip {
		t.Fatal("allow_multiple_instances=1 must not skip")
	}
}

// Every mutation that changes the rule set has to reschedule.
func TestMutationsReloadScheduler(t *testing.T) {
	svc, st := newTestCronService(t)
	sched := &fakeScheduler{}
	svc.SetScheduler(sched)
	ctx := context.Background()

	created, err := svc.Create(ctx, &CrontabInput{Command: "task demo.js", Schedule: "0 0 * * *"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	insert := created.(crontabInsertResponse)

	schedule := "0 1 * * *"
	if _, err := svc.Update(ctx, &CrontabInput{ID: insert.ID, Command: "task demo.js", Schedule: schedule}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := svc.SetDisabled(ctx, []int64{insert.ID}, true); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if err := svc.SetDisabled(ctx, []int64{insert.ID}, false); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if err := svc.Remove(ctx, []int64{insert.ID}); err != nil {
		t.Fatalf("remove: %v", err)
	}

	reloads, _ := sched.counts()
	if reloads != 5 {
		t.Fatalf("reloads = %d, want 5 (create/update/disable/enable/remove)", reloads)
	}

	if _, err := st.CrontabByID(ctx, insert.ID); err != nil {
		t.Fatalf("load deleted row: %v", err)
	}
}

func TestManualRunUsesSchedulerQueue(t *testing.T) {
	svc, _ := newTestCronService(t)
	sched := &fakeScheduler{}
	svc.SetScheduler(sched)

	if err := svc.Run(context.Background(), []int64{7, 9}); err != nil {
		t.Fatalf("run: %v", err)
	}
	_, calls := sched.counts()
	if calls != 1 {
		t.Fatalf("manual queue calls = %d, want 1", calls)
	}
	sched.mu.Lock()
	ids := sched.manualCalls[0]
	sched.mu.Unlock()
	if len(ids) != 2 || ids[0] != 7 || ids[1] != 9 {
		t.Fatalf("manual ids = %v, want [7 9]", ids)
	}
}

func TestTimezoneDefaultsToShanghai(t *testing.T) {
	svc, _ := newTestCronService(t)
	location := svc.Timezone(context.Background())
	if location.String() != "Asia/Shanghai" {
		t.Fatalf("timezone = %s, want Asia/Shanghai", location)
	}
}

func TestTimezoneFollowsSystemConfig(t *testing.T) {
	svc, st := newTestCronService(t)
	if err := st.UpsertAuthRow(context.Background(), store.AuthTypeSystemConfig,
		[]byte(`{"timezone":"America/New_York","cronConcurrency":2}`)); err != nil {
		t.Fatalf("write system config: %v", err)
	}
	if got := svc.Timezone(context.Background()).String(); got != "America/New_York" {
		t.Fatalf("timezone = %s, want America/New_York", got)
	}
	if got := svc.CronConcurrency(context.Background()); got != 2 {
		t.Fatalf("cronConcurrency = %d, want 2", got)
	}
}

func TestCronConcurrencyUnsetIsZero(t *testing.T) {
	svc, _ := newTestCronService(t)
	if got := svc.CronConcurrency(context.Background()); got != 0 {
		t.Fatalf("cronConcurrency = %d, want 0 (scheduler falls back to max(NumCPU,4))", got)
	}
}

// Two rules firing in the same millisecond must not share a log file.
func TestOpenTaskLogIsUniquePerRun(t *testing.T) {
	svc, st := newTestCronService(t)
	cronID := insertRow(t, st, nil)
	row, err := st.CrontabByID(context.Background(), cronID)
	if err != nil {
		t.Fatalf("load row: %v", err)
	}
	seen := map[string]bool{}
	for index := 0; index < 20; index++ {
		path, file, err := svc.openTaskLog(row)
		if err != nil {
			t.Fatalf("open log %d: %v", index, err)
		}
		_ = file.Close()
		if seen[path] {
			t.Fatalf("duplicate log path %q", path)
		}
		seen[path] = true
	}
}

// Shutdown must end the child process tree and leave the row idle, so a
// stopped panel does not keep tasks running.
func TestShutdownKillsRunningTask(t *testing.T) {
	requireNode(t)
	svc, st := newTestCronService(t)
	writeScript(t, svc.cfg, "demo.js", "setTimeout(() => {}, 30000);\n")
	cronID := insertRow(t, st, func(row *store.Crontab) { row.Command = "task demo.js" })

	done := make(chan struct{})
	go func() {
		svc.RunScheduled(context.Background(), cronID)
		close(done)
	}()

	var pid int64
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		row, err := st.CrontabByID(context.Background(), cronID)
		if err != nil {
			t.Fatalf("load row: %v", err)
		}
		if row != nil && row.Status.Valid && row.Status.Int64 == CronStatusRunning && row.Pid.Valid {
			pid = row.Pid.Int64
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("task never started")
	}

	start := time.Now()
	svc.Shutdown(5 * time.Second)
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("shutdown took %s", elapsed)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("run goroutine did not finish after shutdown")
	}

	if platform.ProcessAlive(int(pid)) {
		t.Fatalf("pid %d still alive after shutdown", pid)
	}
	final, err := st.CrontabByID(context.Background(), cronID)
	if err != nil {
		t.Fatalf("load final row: %v", err)
	}
	if final.Status.Int64 != CronStatusIdle {
		t.Fatalf("final status = %d, want idle", final.Status.Int64)
	}
}
