package scheduler

import (
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"qinglong-go/internal/store"
)

type fakeExecutor struct {
	mu sync.Mutex

	queuedBatches [][]int64
	queuedRuns    []int64
	scheduled     []int64
	startedAt     []time.Time

	runDelay time.Duration

	active    int
	maxActive int
}

func (f *fakeExecutor) MarkQueued(_ context.Context, ids []int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queuedBatches = append(f.queuedBatches, append([]int64(nil), ids...))
	return nil
}

func (f *fakeExecutor) RunQueued(_ context.Context, cronID int64) {
	f.mu.Lock()
	f.queuedRuns = append(f.queuedRuns, cronID)
	f.mu.Unlock()
}

func (f *fakeExecutor) RunScheduled(_ context.Context, cronID int64) {
	f.mu.Lock()
	f.scheduled = append(f.scheduled, cronID)
	f.startedAt = append(f.startedAt, time.Now())
	f.active++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	delay := f.runDelay
	f.mu.Unlock()

	if delay > 0 {
		time.Sleep(delay)
	}

	f.mu.Lock()
	f.active--
	f.mu.Unlock()
}

func (f *fakeExecutor) scheduledCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.scheduled)
}

func (f *fakeExecutor) peak() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.maxActive
}

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "database.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return st
}

func insertTask(t *testing.T, st *store.Store, schedule string, extras []string, disabled bool) int64 {
	t.Helper()
	now := store.SequelizeTime(time.Now())
	row := &store.Crontab{
		Command:                "task demo.js",
		Schedule:               sql.NullString{String: schedule, Valid: true},
		Timestamp:              sql.NullString{String: now, Valid: true},
		Status:                 sql.NullInt64{Int64: 1, Valid: true},
		IsSystem:               sql.NullInt64{Int64: 0, Valid: true},
		IsDisabled:             sql.NullInt64{Int64: 0, Valid: true},
		IsPinned:               sql.NullInt64{Int64: 0, Valid: true},
		LogPath:                sql.NullString{String: "", Valid: true},
		Labels:                 sql.NullString{String: "[]", Valid: true},
		LastRunningTime:        sql.NullInt64{Int64: 0, Valid: true},
		LastExecutionTime:      sql.NullInt64{Int64: 0, Valid: true},
		AllowMultipleInstances: sql.NullInt64{Int64: 0, Valid: true},
		CreatedAt:              now,
		UpdatedAt:              now,
	}
	if disabled {
		row.IsDisabled = sql.NullInt64{Int64: 1, Valid: true}
	}
	if len(extras) > 0 {
		payload := `[`
		for index, extra := range extras {
			if index > 0 {
				payload += ","
			}
			payload += `{"schedule":"` + extra + `"}`
		}
		payload += `]`
		row.ExtraSchedules = sql.NullString{String: payload, Valid: true}
	}
	if err := st.InsertCrontab(context.Background(), row); err != nil {
		t.Fatalf("insert crontab: %v", err)
	}
	return row.ID
}

func newTestScheduler(t *testing.T, st *store.Store, exec *fakeExecutor, limit int) *Scheduler {
	t.Helper()
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	return New(Options{
		Store:       st,
		Executor:    exec,
		Logger:      slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelError})),
		Location:    func(context.Context) *time.Location { return location },
		Concurrency: func(context.Context) int { return limit },
	})
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(string(p))
	return len(p), nil
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return condition()
}

// A rule that fires every second must trigger within the 2s tolerance the
// milestone asks for.
func TestTriggerAccuracy(t *testing.T) {
	st := newTestStore(t)
	insertTask(t, st, "* * * * * *", nil, false)

	exec := &fakeExecutor{}
	sched := newTestScheduler(t, st, exec, 4)
	startedAt := time.Now()
	if err := sched.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer sched.Stop()

	if !waitFor(t, 3*time.Second, func() bool { return exec.scheduledCount() >= 2 }) {
		t.Fatalf("expected 2 triggers, got %d", exec.scheduledCount())
	}

	exec.mu.Lock()
	first := exec.startedAt[0]
	exec.mu.Unlock()
	// The first trigger is the next whole second after start.
	if delay := first.Sub(startedAt); delay > 2*time.Second {
		t.Fatalf("first trigger came %s after start, want < 2s", delay)
	}
	if millis := first.Nanosecond() / int(time.Millisecond); millis > 500 {
		t.Fatalf("trigger drifted %dms off the second boundary", millis)
	}
}

// extra_schedules items are independent rules, exactly like Node's addCron
// creating one job per schedule.
func TestExtraSchedulesTrigger(t *testing.T) {
	st := newTestStore(t)
	cronID := insertTask(t, st, "*/2 * * * * *", []string{"*/3 * * * * *"}, false)

	exec := &fakeExecutor{}
	sched := newTestScheduler(t, st, exec, 4)
	if err := sched.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer sched.Stop()

	if !waitFor(t, 7*time.Second, func() bool { return exec.scheduledCount() >= 5 }) {
		t.Fatalf("expected both rules to fire, got %d triggers", exec.scheduledCount())
	}
	exec.mu.Lock()
	ids := append([]int64(nil), exec.scheduled...)
	exec.mu.Unlock()
	for _, id := range ids {
		if id != cronID {
			t.Fatalf("unexpected cron id %d", id)
		}
	}
}

func TestConcurrencyLimit(t *testing.T) {
	st := newTestStore(t)
	for index := 0; index < 6; index++ {
		insertTask(t, st, "* * * * * *", nil, false)
	}

	exec := &fakeExecutor{runDelay: 400 * time.Millisecond}
	sched := newTestScheduler(t, st, exec, 2)
	if err := sched.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer sched.Stop()

	if !waitFor(t, 5*time.Second, func() bool { return exec.scheduledCount() >= 6 }) {
		t.Fatalf("expected 6 triggers, got %d", exec.scheduledCount())
	}
	if peak := exec.peak(); peak > 2 {
		t.Fatalf("peak concurrency = %d, want <= 2", peak)
	}
}

func TestDisabledTaskIsNotRegistered(t *testing.T) {
	st := newTestStore(t)
	insertTask(t, st, "* * * * * *", nil, true)

	exec := &fakeExecutor{}
	sched := newTestScheduler(t, st, exec, 4)
	if err := sched.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer sched.Stop()

	time.Sleep(1500 * time.Millisecond)
	if count := exec.scheduledCount(); count != 0 {
		t.Fatalf("disabled task triggered %d times", count)
	}
}

// Reload is the only way a mutation reaches the timers; a task created after
// Start must trigger once it is reloaded, and disappear after deletion.
func TestReloadPicksUpCreateAndDelete(t *testing.T) {
	st := newTestStore(t)
	exec := &fakeExecutor{}
	sched := newTestScheduler(t, st, exec, 4)
	if err := sched.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer sched.Stop()

	cronID := insertTask(t, st, "* * * * * *", nil, false)
	if err := sched.Reload(context.Background()); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !waitFor(t, 3*time.Second, func() bool { return exec.scheduledCount() >= 1 }) {
		t.Fatal("task created after start never triggered")
	}

	if err := st.DeleteCrontabs(context.Background(), []int64{cronID}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := sched.Reload(context.Background()); err != nil {
		t.Fatalf("reload: %v", err)
	}
	before := exec.scheduledCount()
	time.Sleep(1500 * time.Millisecond)
	if after := exec.scheduledCount(); after > before+1 {
		t.Fatalf("deleted task kept triggering: %d -> %d", before, after)
	}
}

// @boot runs once per process start, through run(ids): one MarkQueued batch in
// list order, then one queued run per id. Reload must not repeat it.
func TestBootTasksRunOnceInOrder(t *testing.T) {
	st := newTestStore(t)
	first := insertTask(t, st, "@boot", nil, false)
	second := insertTask(t, st, "@boot", nil, false)
	insertTask(t, st, "@boot", nil, true)
	insertTask(t, st, "* * * * * *", nil, false)

	// createdAt only has millisecond precision, so give the two boot rows
	// distinct timestamps; otherwise ORDER BY createdAt DESC is a tie and the
	// list order is not reproducible (Node has the same ambiguity).
	base := time.Now().Add(-time.Minute)
	if _, err := st.DB().Exec(
		"UPDATE Crontabs SET createdAt = ? WHERE id IN (?, ?)",
		store.SequelizeTime(base), first, second); err != nil {
		t.Fatalf("stamp first row: %v", err)
	}
	if _, err := st.DB().Exec(
		"UPDATE Crontabs SET createdAt = ? WHERE id = ?",
		store.SequelizeTime(base.Add(time.Second)), second); err != nil {
		t.Fatalf("stamp second row: %v", err)
	}

	// "List order" is whatever crontabs() returns (isPinned DESC, isDisabled
	// ASC, status ASC, createdAt DESC), not insertion order.
	rows, err := st.AllCrontabs(context.Background())
	if err != nil {
		t.Fatalf("list crontabs: %v", err)
	}
	var want []int64
	for index := range rows {
		row := &rows[index]
		if row.IsDisabled.Valid && row.IsDisabled.Int64 == 1 {
			continue
		}
		if row.Schedule.String != "@boot" {
			continue
		}
		want = append(want, row.ID)
	}
	if len(want) != 2 {
		t.Fatalf("expected 2 boot rows, got %v", want)
	}

	exec := &fakeExecutor{}
	sched := newTestScheduler(t, st, exec, 4)
	if err := sched.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer sched.Stop()

	if !waitFor(t, 2*time.Second, func() bool {
		exec.mu.Lock()
		defer exec.mu.Unlock()
		return len(exec.queuedRuns) == 2
	}) {
		t.Fatalf("boot runs = %v, want 2", exec.queuedRuns)
	}

	exec.mu.Lock()
	batches := append([][]int64(nil), exec.queuedBatches...)
	runs := append([]int64(nil), exec.queuedRuns...)
	exec.mu.Unlock()

	if len(batches) != 1 {
		t.Fatalf("boot should mark one batch, got %d", len(batches))
	}
	if len(batches[0]) != 2 || batches[0][0] != want[0] || batches[0][1] != want[1] {
		t.Fatalf("boot batch = %v, want %v", batches[0], want)
	}
	// The queue keeps submission order, but with concurrency > 1 the runs start
	// in parallel (Node's p-queue behaves the same), so compare as a set.
	if len(runs) != 2 {
		t.Fatalf("boot runs = %v, want 2 entries", runs)
	}
	seen := map[int64]bool{runs[0]: true, runs[1]: true}
	if !seen[want[0]] || !seen[want[1]] {
		t.Fatalf("boot runs = %v, want %v", runs, want)
	}

	if err := sched.Reload(context.Background()); err != nil {
		t.Fatalf("reload: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	exec.mu.Lock()
	afterReload := len(exec.queuedRuns)
	exec.mu.Unlock()
	if afterReload != 2 {
		t.Fatalf("reload re-ran boot tasks: %d runs", afterReload)
	}
}

// @once has no automatic trigger in Node: creating one must not run it.
func TestOnceIsNotTriggeredAutomatically(t *testing.T) {
	st := newTestStore(t)
	insertTask(t, st, "@once", nil, false)

	exec := &fakeExecutor{}
	sched := newTestScheduler(t, st, exec, 4)
	if err := sched.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer sched.Stop()

	time.Sleep(700 * time.Millisecond)
	if count := exec.scheduledCount(); count != 0 {
		t.Fatalf("@once triggered %d times", count)
	}
	if len(exec.queuedRuns) != 0 {
		t.Fatalf("@once was queued %d times", len(exec.queuedRuns))
	}
}

// An invalid persisted rule must be skipped without taking the other tasks
// down, like autosave_crontab filtering bad schedules.
func TestInvalidScheduleIsSkipped(t *testing.T) {
	st := newTestStore(t)
	insertTask(t, st, "not a cron", nil, false)
	insertTask(t, st, "* * * * * *", nil, false)

	exec := &fakeExecutor{}
	sched := newTestScheduler(t, st, exec, 4)
	if err := sched.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer sched.Stop()

	if !waitFor(t, 3*time.Second, func() bool { return exec.scheduledCount() >= 1 }) {
		t.Fatal("valid task did not trigger next to an invalid one")
	}
}

func TestDefaultConcurrencyIsAtLeastFour(t *testing.T) {
	if got := defaultConcurrency(); got < 4 {
		t.Fatalf("default concurrency = %d, want >= 4", got)
	}
}
