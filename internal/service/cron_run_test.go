package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"qinglong-go/internal/platform"
	"qinglong-go/internal/store"
)

// runScheduledAsync starts a task and waits until the row reports running.
func runScheduledAsync(t *testing.T, svc *CronService, cronID int64) (chan struct{}, int64) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		svc.RunScheduled(context.Background(), cronID)
		close(done)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		row, err := svc.store.CrontabByID(context.Background(), cronID)
		if err != nil {
			t.Fatalf("load row: %v", err)
		}
		if row != nil && row.Status.Valid && row.Status.Int64 == CronStatusRunning && row.Pid.Valid {
			return done, row.Pid.Int64
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("task never reached the running state")
	return nil, 0
}

// readLog loads the log file a run wrote.
func readLog(t *testing.T, svc *CronService, cronID int64) string {
	t.Helper()
	row, err := svc.store.CrontabByID(context.Background(), cronID)
	if err != nil || row == nil {
		t.Fatalf("load row: %v", err)
	}
	if !row.LogPath.Valid {
		t.Fatal("row has no log_path")
	}
	content, err := os.ReadFile(filepath.Join(svc.cfg.LogPath, row.LogPath.String))
	if err != nil {
		t.Fatalf("read log %s: %v", row.LogPath.String, err)
	}
	return string(content)
}

// A finished run must add one row to the dashboard counters.
func TestScheduledRunRecordsStat(t *testing.T) {
	requireNode(t)
	svc, st := newTestCronService(t)
	writeScript(t, svc.cfg, "demo.js", "console.log('stat ok')\n")
	cronID := insertRow(t, st, func(row *store.Crontab) { row.Command = "task demo.js" })

	svc.RunScheduled(context.Background(), cronID)

	var runCount, successCount, failCount, totalTime int64
	err := st.DB().QueryRow(
		`SELECT run_count, success_count, fail_count, total_time FROM CrontabStats
		 WHERE ref_id = ? AND date = ?`,
		cronID, time.Now().Format("2006-01-02"),
	).Scan(&runCount, &successCount, &failCount, &totalTime)
	if err != nil {
		t.Fatalf("load stat: %v", err)
	}
	if runCount != 1 || successCount != 1 || failCount != 0 {
		t.Fatalf("stat = run %d success %d fail %d", runCount, successCount, failCount)
	}
	if totalTime <= 0 {
		t.Fatalf("total_time = %d, want milliseconds", totalTime)
	}
}

// A failing task must count as a failure in the same row.
func TestScheduledFailureRecordsStat(t *testing.T) {
	requireNode(t)
	svc, st := newTestCronService(t)
	writeScript(t, svc.cfg, "demo.js", "process.exit(7)\n")
	cronID := insertRow(t, st, func(row *store.Crontab) { row.Command = "task demo.js" })

	svc.RunScheduled(context.Background(), cronID)

	var runCount, failCount int64
	if err := st.DB().QueryRow(
		`SELECT run_count, fail_count FROM CrontabStats WHERE ref_id = ? AND date = ?`,
		cronID, time.Now().Format("2006-01-02"),
	).Scan(&runCount, &failCount); err != nil {
		t.Fatalf("load stat: %v", err)
	}
	if runCount != 1 || failCount != 1 {
		t.Fatalf("stat = run %d fail %d", runCount, failCount)
	}

	instances, err := st.ListRunningInstances(context.Background(), cronID)
	if err != nil {
		t.Fatalf("list instances: %v", err)
	}
	if len(instances) != 1 || !instances[0].ExitCode.Valid || instances[0].ExitCode.Int64 != 7 {
		t.Fatalf("instance exit code not recorded: %#v", instances)
	}
}

// Stopping a task must kill the process tree, write the stop footer, record
// exit code 143 and leave the row idle.
func TestStopKillsTaskAndWritesFooter(t *testing.T) {
	requireNode(t)
	svc, st := newTestCronService(t)
	writeScript(t, svc.cfg, "slow.js", "setTimeout(() => {}, 30000);\n")
	cronID := insertRow(t, st, func(row *store.Crontab) { row.Command = "task slow.js" })

	done, pid := runScheduledAsync(t, svc, cronID)

	if _, err := svc.Stop(context.Background(), []int64{cronID}); err != nil {
		t.Fatalf("stop: %v", err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("task did not stop")
	}

	if platform.ProcessAlive(int(pid)) {
		t.Fatalf("pid %d is still alive after stop", pid)
	}

	row, err := st.CrontabByID(context.Background(), cronID)
	if err != nil {
		t.Fatalf("load row: %v", err)
	}
	if row.Status.Int64 != CronStatusIdle || row.Pid.Valid {
		t.Fatalf("row not idle after stop: status=%d pid=%v", row.Status.Int64, row.Pid)
	}

	log := readLog(t, svc, cronID)
	if !strings.Contains(log, "## 已停止 🛑... ") {
		t.Fatalf("missing stop footer:\n%s", log)
	}

	instances, err := st.ListRunningInstances(context.Background(), cronID)
	if err != nil {
		t.Fatalf("list instances: %v", err)
	}
	if len(instances) != 1 {
		t.Fatalf("instances = %d, want 1", len(instances))
	}
	if instances[0].Status != InstanceStopped {
		t.Fatalf("instance status = %d, want stopped", instances[0].Status)
	}
	if !instances[0].ExitCode.Valid || instances[0].ExitCode.Int64 != 143 {
		t.Fatalf("instance exit code = %v, want 143", instances[0].ExitCode)
	}
}

// CommandTimeoutTime must end the task with 124 and record it.
func TestScheduledTimeoutRecords124(t *testing.T) {
	requireNode(t)
	svc, st := newTestCronService(t)
	svc.cfg.Values = map[string]string{"CommandTimeoutTime": "1s"}
	writeScript(t, svc.cfg, "slow.js", "setTimeout(() => {}, 30000);\n")
	cronID := insertRow(t, st, func(row *store.Crontab) { row.Command = "task slow.js" })

	start := time.Now()
	svc.RunScheduled(context.Background(), cronID)
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Fatalf("timeout took %s", elapsed)
	}

	instances, err := st.ListRunningInstances(context.Background(), cronID)
	if err != nil {
		t.Fatalf("list instances: %v", err)
	}
	if len(instances) != 1 || !instances[0].ExitCode.Valid || instances[0].ExitCode.Int64 != 124 {
		t.Fatalf("instance exit code not 124: %#v", instances)
	}
	log := readLog(t, svc, cronID)
	if !strings.Contains(log, "## 失败 ❌(退出码 124)... ") {
		t.Fatalf("missing timeout footer:\n%s", log)
	}
}

// RandomDelay must be applied by the runner for scheduled runs only.
func TestScheduledRandomDelay(t *testing.T) {
	requireNode(t)
	svc, st := newTestCronService(t)
	svc.cfg.Values = map[string]string{
		"RandomDelay":               "2",
		"RandomDelayFileExtensions": "js",
		"RandomDelayIgnoredMinutes": "",
	}
	writeScript(t, svc.cfg, "demo.js", "console.log('delayed ok')\n")
	cronID := insertRow(t, st, func(row *store.Crontab) { row.Command = "task demo.js" })

	start := time.Now()
	svc.RunScheduled(context.Background(), cronID)
	if elapsed := time.Since(start); elapsed < 1*time.Second {
		t.Fatalf("scheduled run was not delayed: %s", elapsed)
	}
	log := readLog(t, svc, cronID)
	if !strings.Contains(log, "任务随机延迟 ") {
		t.Fatalf("missing delay message:\n%s", log)
	}
}

// The random delay can last minutes (RandomDelay=300). Node marks the task
// running in handle_task_start before sleeping, so the list shows 运行中; the Go
// panel used to leave the row idle until the process existed, which made a
// delayed task look like it never ran.
func TestScheduledDelayMarksTaskRunning(t *testing.T) {
	requireNode(t)
	svc, st := newTestCronService(t)
	svc.cfg.Values = map[string]string{
		"RandomDelay":               "3",
		"RandomDelayFileExtensions": "",
		"RandomDelayIgnoredMinutes": "",
	}
	writeScript(t, svc.cfg, "delay_status.js", "console.log('delay status ok')\n")
	cronID := insertRow(t, st, func(row *store.Crontab) { row.Command = "task delay_status.js" })

	done := make(chan struct{})
	go func() {
		svc.RunScheduled(context.Background(), cronID)
		close(done)
	}()

	// Poll until the delay has started (the log header appears), then check the
	// row is already running even though no process exists.
	deadline := time.Now().Add(5 * time.Second)
	var running bool
	for time.Now().Before(deadline) {
		row, err := st.CrontabByID(context.Background(), cronID)
		if err != nil {
			t.Fatalf("load row: %v", err)
		}
		if row != nil && row.Status.Valid && row.Status.Int64 == CronStatusRunning && !row.Pid.Valid {
			running = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !running {
		t.Fatal("row never switched to running while the random delay was pending")
	}

	<-done
	row, err := st.CrontabByID(context.Background(), cronID)
	if err != nil {
		t.Fatalf("load row: %v", err)
	}
	if !row.Status.Valid || row.Status.Int64 != CronStatusIdle || row.Pid.Valid {
		t.Fatalf("row not idle after the run: status=%v pid=%v", row.Status, row.Pid)
	}
	if log := readLog(t, svc, cronID); !strings.Contains(log, "delay status ok") {
		t.Fatalf("task did not run:/n%s", log)
	}
}

// Stop() during the random delay has no pid to signal; the row must go idle at
// once and the pending start must not spawn a process.
func TestStopDuringRandomDelay(t *testing.T) {
	requireNode(t)
	svc, st := newTestCronService(t)
	svc.cfg.Values = map[string]string{
		"RandomDelay":               "6",
		"RandomDelayFileExtensions": "",
		"RandomDelayIgnoredMinutes": "",
	}
	writeScript(t, svc.cfg, "delay_stop.js", "console.log('should not run')\n")
	cronID := insertRow(t, st, func(row *store.Crontab) { row.Command = "task delay_stop.js" })

	done := make(chan struct{})
	go func() {
		svc.RunScheduled(context.Background(), cronID)
		close(done)
	}()

	deadline := time.Now().Add(5 * time.Second)
	waiting := false
	for time.Now().Before(deadline) {
		row, err := st.CrontabByID(context.Background(), cronID)
		if err != nil {
			t.Fatalf("load row: %v", err)
		}
		if row != nil && row.Status.Valid && row.Status.Int64 == CronStatusRunning && !row.Pid.Valid {
			waiting = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !waiting {
		t.Fatal("task never reached the random delay")
	}

	if _, err := svc.Stop(context.Background(), []int64{cronID}); err != nil {
		t.Fatalf("stop: %v", err)
	}
	row, err := st.CrontabByID(context.Background(), cronID)
	if err != nil {
		t.Fatalf("load row: %v", err)
	}
	if !row.Status.Valid || row.Status.Int64 != CronStatusIdle {
		t.Fatalf("row not idle right after stop: %v", row.Status)
	}

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("run did not return after the delay")
	}
	if log := readLog(t, svc, cronID); strings.Contains(log, "should not run") {
		t.Fatalf("task ran anyway://n%s", log)
	}
}
