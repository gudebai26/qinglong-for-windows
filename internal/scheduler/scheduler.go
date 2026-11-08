// Package scheduler owns the in-process cron engine. It replaces the gRPC
// scheduler process the Node panel talks to: every rule lives in a timer
// goroutine, triggers go through a concurrency-limited queue, and the task
// itself is still executed by the service (M5 swaps the runner behind the same
// interface).
package scheduler

import (
	"context"
	"encoding/json"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"time"

	"qinglong-go/internal/cronexpr"
	"qinglong-go/internal/store"
)

// DefaultLocation mirrors the panel default in back/services/system.ts.
const DefaultLocation = "Asia/Shanghai"

// minConcurrency matches Math.max(os.cpus().length, 4) in back/shared/pLimit.ts.
const minConcurrency = 4

// Executor is the task domain as seen by the scheduler. service.CronService
// implements it; tests can substitute a fake.
type Executor interface {
	// MarkQueued flags the rows as queued and mints a queued_token, exactly
	// like CronService.run(ids).
	MarkQueued(ctx context.Context, ids []int64) error
	// RunQueued executes one row that is already marked queued.
	RunQueued(ctx context.Context, cronID int64)
	// RunScheduled executes a scheduled trigger: single-instance check, status
	// transitions, then the task itself.
	RunScheduled(ctx context.Context, cronID int64)
}

// Store is the read side the scheduler needs.
type Store interface {
	AllCrontabs(ctx context.Context) ([]store.Crontab, error)
}

// Options wires the scheduler.
type Options struct {
	Store    Store
	Executor Executor
	Logger   *slog.Logger
	// Location resolves the panel timezone. nil falls back to Asia/Shanghai.
	Location func(ctx context.Context) *time.Location
	// Concurrency resolves cronConcurrency. Zero or negative falls back to
	// max(NumCPU, 4), matching setCustomLimit().
	Concurrency func(ctx context.Context) int
}

// Scheduler runs the cron rules held in the database.
type Scheduler struct {
	store    Store
	exec     Executor
	logger   *slog.Logger
	location func(ctx context.Context) *time.Location
	limit    func(ctx context.Context) int

	mu      sync.Mutex
	started bool
	entries map[int64][]*entry
	cronQ   *queue
	manualQ *queue
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// entry is one rule of one task: the main schedule or one extra_schedules
// item. Node registers each of them as an independent cron job.
type entry struct {
	cronID   int64
	schedule *cronexpr.Schedule
	source   string
	location *time.Location
	stopOnce sync.Once
	stop     chan struct{}
}

func (e *entry) shutdown() {
	e.stopOnce.Do(func() { close(e.stop) })
}

// New builds a scheduler; call Start to register the rules.
func New(options Options) *Scheduler {
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	s := &Scheduler{
		store:    options.Store,
		exec:     options.Executor,
		logger:   logger,
		location: options.Location,
		limit:    options.Concurrency,
		entries:  map[int64][]*entry{},
	}
	if s.location == nil {
		s.location = func(context.Context) *time.Location {
			location, err := time.LoadLocation(DefaultLocation)
			if err != nil {
				return time.Local
			}
			return location
		}
	}
	return s
}

// Start registers every enabled rule and then runs the @boot tasks once.
// Reload alone never re-runs @boot, which is what "boot" means in Node.
func (s *Scheduler) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return nil
	}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.started = true
	limit := s.concurrency(ctx)
	s.cronQ = newQueue(limit)
	s.manualQ = newQueue(limit)
	s.mu.Unlock()

	if err := s.Reload(ctx); err != nil {
		return err
	}
	return s.runBootTasks(ctx)
}

// Reload rebuilds every timer from the database. It is a full reschedule: the
// task list is small and a partial diff would have to replicate Node's
// addCron/delCron bookkeeping for no benefit.
func (s *Scheduler) Reload(ctx context.Context) error {
	s.mu.Lock()
	started := s.started
	s.mu.Unlock()
	if !started {
		return nil
	}

	rows, err := s.store.AllCrontabs(ctx)
	if err != nil {
		return err
	}
	location := s.resolveLocation(ctx)
	next := make(map[int64][]*entry, len(rows))
	for index := range rows {
		row := &rows[index]
		if isDisabled(row) {
			continue
		}
		schedule := strings.TrimSpace(row.Schedule.String)
		if schedule == "" || cronexpr.IsSpecial(schedule) {
			continue
		}
		rules, err := rulesFor(row)
		if err != nil {
			// Node skips a task whose persisted rule cannot be parsed instead
			// of failing the whole reload.
			s.logger.Warn("[schedule] skip task with invalid schedule",
				"cron_id", row.ID, "schedule", schedule, "error", err)
			continue
		}
		for _, rule := range rules {
			next[row.ID] = append(next[row.ID], &entry{
				cronID:   row.ID,
				schedule: rule.schedule,
				source:   rule.source,
				location: location,
				stop:     make(chan struct{}),
			})
		}
	}

	s.mu.Lock()
	previous := s.entries
	s.entries = next
	s.mu.Unlock()

	for _, list := range previous {
		for _, item := range list {
			item.shutdown()
		}
	}
	if limit := s.concurrency(ctx); limit > 0 {
		if s.cronQ != nil {
			s.cronQ.SetLimit(limit)
		}
		if s.manualQ != nil {
			s.manualQ.SetLimit(limit)
		}
	}

	// Start the new timers under the lock Stop() takes, so a reload that races
	// a shutdown can never add to the wait group after Wait() began.
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return nil
	}
	for _, list := range next {
		for _, item := range list {
			s.wg.Add(1)
			go s.runEntry(s.ctx, item)
		}
	}
	s.mu.Unlock()
	s.logger.Debug("[schedule] rules reloaded", "tasks", len(next), "timezone", location.String())
	return nil
}

type rule struct {
	source   string
	schedule *cronexpr.Schedule
}

// rulesFor expands the main schedule plus extra_schedules. Node validates the
// whole batch before registering anything, so one bad rule drops the task.
func rulesFor(row *store.Crontab) ([]rule, error) {
	sources := []string{strings.TrimSpace(row.Schedule.String)}
	sources = append(sources, extraSchedules(row)...)
	rules := make([]rule, 0, len(sources))
	for _, source := range sources {
		parsed, err := cronexpr.Parse(source)
		if err != nil {
			return nil, err
		}
		rules = append(rules, rule{source: source, schedule: parsed})
	}
	return rules, nil
}

func extraSchedules(row *store.Crontab) []string {
	if !row.ExtraSchedules.Valid || strings.TrimSpace(row.ExtraSchedules.String) == "" {
		return nil
	}
	var items []struct {
		Schedule string `json:"schedule"`
	}
	if err := json.Unmarshal([]byte(row.ExtraSchedules.String), &items); err != nil {
		return nil
	}
	sources := make([]string, 0, len(items))
	for _, item := range items {
		if schedule := strings.TrimSpace(item.Schedule); schedule != "" {
			sources = append(sources, schedule)
		}
	}
	return sources
}

func isDisabled(row *store.Crontab) bool {
	return row.IsDisabled.Valid && row.IsDisabled.Int64 == 1
}

// runEntry owns one timer. It computes the next trigger, sleeps until then,
// hands the task to the cron queue and repeats.
func (s *Scheduler) runEntry(ctx context.Context, item *entry) {
	defer s.wg.Done()
	for {
		next, ok := item.schedule.Next(time.Now().In(item.location))
		if !ok {
			s.logger.Warn("[schedule] rule has no future run",
				"cron_id", item.cronID, "schedule", item.source)
			return
		}
		delay := time.Until(next)
		if delay < 0 {
			delay = 0
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-item.stop:
			timer.Stop()
			return
		case <-timer.C:
		}
		s.submitCron(item.cronID, item.source, next)
	}
}

func (s *Scheduler) submitCron(cronID int64, source string, planned time.Time) {
	queue := s.cronQ
	if queue == nil {
		return
	}
	s.logger.Info("[schedule] task triggered",
		"cron_id", cronID, "schedule", source, "planned", planned.Format(time.RFC3339))
	queue.Submit(func() {
		s.exec.RunScheduled(s.ctx, cronID)
	})
}

// RunManual queues tasks the way CronService.run(ids) does: mark every row
// queued first, then execute them in order through the manual queue.
func (s *Scheduler) RunManual(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	if err := s.exec.MarkQueued(ctx, ids); err != nil {
		return err
	}
	s.mu.Lock()
	queue := s.manualQ
	s.mu.Unlock()
	if queue == nil {
		// Not started yet: run inline so a boot task is never silently dropped.
		for _, id := range ids {
			go s.exec.RunQueued(context.Background(), id)
		}
		return nil
	}
	for _, id := range ids {
		cronID := id
		queue.Submit(func() {
			s.exec.RunQueued(s.ctx, cronID)
		})
	}
	return nil
}

// runBootTasks mirrors CronService.bootTask: one full crontabs() snapshot,
// filter enabled @boot rows, keep the list order, hand them to run(ids).
func (s *Scheduler) runBootTasks(ctx context.Context) error {
	rows, err := s.store.AllCrontabs(ctx)
	if err != nil {
		return err
	}
	ids := make([]int64, 0, len(rows))
	for index := range rows {
		row := &rows[index]
		if isDisabled(row) {
			continue
		}
		if !cronexpr.IsBoot(strings.TrimSpace(row.Schedule.String)) {
			continue
		}
		ids = append(ids, row.ID)
	}
	if len(ids) == 0 {
		return nil
	}
	s.logger.Info("[schedule] running @boot tasks", "count", len(ids), "ids", ids)
	return s.RunManual(ctx, ids)
}

// Stop cancels every timer and drops the queues. Running tasks are ended by
// the service, which owns the child processes.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return
	}
	s.started = false
	cancel := s.cancel
	entries := s.entries
	s.entries = map[int64][]*entry{}
	cronQ, manualQ := s.cronQ, s.manualQ
	s.cronQ, s.manualQ = nil, nil
	s.mu.Unlock()

	for _, list := range entries {
		for _, item := range list {
			item.shutdown()
		}
	}
	if cancel != nil {
		cancel()
	}
	if cronQ != nil {
		cronQ.Close()
	}
	if manualQ != nil {
		manualQ.Close()
	}
	s.wg.Wait()
}

func (s *Scheduler) resolveLocation(ctx context.Context) *time.Location {
	if s.location != nil {
		if location := s.location(ctx); location != nil {
			return location
		}
	}
	location, err := time.LoadLocation(DefaultLocation)
	if err != nil {
		return time.Local
	}
	return location
}

// concurrency resolves the queue width: cronConcurrency when configured,
// otherwise max(NumCPU, 4) like pLimit.ts.
func (s *Scheduler) concurrency(ctx context.Context) int {
	if s.limit != nil {
		if value := s.limit(ctx); value > 0 {
			return value
		}
	}
	return defaultConcurrency()
}

func defaultConcurrency() int {
	if cpus := runtime.NumCPU(); cpus > minConcurrency {
		return cpus
	}
	return minConcurrency
}
