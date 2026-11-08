package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"runtime"
	"strconv"
	"strings"
	"time"

	"qinglong-go/internal/config"
	"qinglong-go/internal/i18n"
	"qinglong-go/internal/platform"
	"qinglong-go/internal/store"
)

// DashboardService owns the ten aggregation endpoints of back/api/dashboard.ts.
// The numbers come from CrontabStats and RunningInstances, which the runner
// already fills, so the charts show the same data as the Node panel.
type DashboardService struct {
	cfg   *config.Config
	store *store.Store
	start time.Time
}

// NewDashboardService captures the process start time used by /dashboard/system.
func NewDashboardService(cfg *config.Config, st *store.Store) *DashboardService {
	return &DashboardService{cfg: cfg, store: st, start: time.Now()}
}

// RecordRequest is POST /dashboard/record. The endpoint is called by the shell
// wrapper (record_cron_stat) with the raw body, so it validates by hand instead
// of going through the celebrate rules.
type RecordRequest struct {
	RefID   any `json:"ref_id"`
	Code    any `json:"code"`
	Elapsed any `json:"elapsed"`
}

// Record implements POST /dashboard/record. Errors are reported as
// {code:500} with HTTP 200, exactly like the Node try/catch.
func (s *DashboardService) Record(ctx context.Context, body map[string]any) map[string]any {
	refRaw, exists := body["ref_id"]
	if !exists || refRaw == nil || isFalsyNumber(refRaw) {
		return map[string]any{"code": 400, "message": "ref_id required"}
	}
	refID, ok := int64Value(refRaw)
	if !ok {
		return map[string]any{"code": 500}
	}
	// code === 0 is a strict comparison in Node: only a JSON number zero counts
	// as success.
	code := int64(1)
	if value, ok := int64Value(body["code"]); ok && value == 0 && isNumber(body["code"]) {
		code = 0
	}
	elapsedSeconds := int64(0)
	if value, ok := floatValue(body["elapsed"]); ok {
		elapsedSeconds = int64(value)
	}
	if err := s.store.RecordCrontabStat(ctx, refID, code, elapsedSeconds); err != nil {
		return map[string]any{"code": 500}
	}
	return map[string]any{"code": 200}
}

// Overview implements GET /dashboard/overview.
type OverviewResponse struct {
	Total       int64  `json:"total"`
	Enabled     int64  `json:"enabled"`
	Disabled    int64  `json:"disabled"`
	TodayRuns   int64  `json:"todayRuns"`
	TodayOK     int64  `json:"todaySuccess"`
	TodayFail   int64  `json:"todayFail"`
	SuccessRate string `json:"successRate"`
	AvgTime     int64  `json:"avgTime"`
}

// Overview returns today's headline counters.
func (s *DashboardService) Overview(ctx context.Context) (*OverviewResponse, error) {
	today := time.Now().Format("2006-01-02")
	total, enabled, disabled, err := s.store.CountCrons(ctx)
	if err != nil {
		return nil, err
	}
	stats, err := s.store.StatOverview(ctx, today)
	if err != nil {
		return nil, err
	}
	response := &OverviewResponse{
		Total:     total,
		Enabled:   enabled,
		Disabled:  disabled,
		TodayRuns: stats.RunCount,
		TodayOK:   stats.SuccessCount,
		TodayFail: stats.FailCount,
	}
	if stats.RunCount > 0 {
		response.SuccessRate = fmt.Sprintf("%.1f", float64(stats.SuccessCount)/float64(stats.RunCount)*100)
		response.AvgTime = int64(math.Round(float64(stats.TotalTime) / float64(stats.RunCount)))
	} else {
		response.SuccessRate = "0"
	}
	return response, nil
}

// TaskResultRow is one row of /dashboard/successes and /dashboard/failures.
type TaskResultRow struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Command string `json:"command"`
	// SuccessCount and FailCount are mutually exclusive; the unused one stays
	// absent, like the computed key in Node.
	SuccessCount *int64 `json:"successCount,omitempty"`
	FailCount    *int64 `json:"failCount,omitempty"`
	Deleted      bool   `json:"deleted"`
}

// TaskResults lists today's successes or failures per task.
func (s *DashboardService) TaskResults(ctx context.Context, successes bool) ([]TaskResultRow, error) {
	column := "fail_count"
	if successes {
		column = "success_count"
	}
	rows, err := s.store.StatByRef(ctx, time.Now().Format("2006-01-02"), column)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.RefID)
	}
	briefs, err := s.store.CronBriefs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]TaskResultRow, 0, len(rows))
	for _, row := range rows {
		brief, found := briefs[row.RefID]
		name := taskLabel(brief, found, row.RefID)
		item := TaskResultRow{
			ID:      row.RefID,
			Name:    name,
			Command: commandOf(brief, found),
			Deleted: !found,
		}
		count := row.RunCount
		if successes {
			item.SuccessCount = &count
		} else {
			item.FailCount = &count
		}
		out = append(out, item)
	}
	return out, nil
}

// TrendPoint is one day of GET /dashboard/trend.
type TrendPoint struct {
	Date    string `json:"date"`
	Total   int64  `json:"total"`
	Success int64  `json:"success"`
	Fail    int64  `json:"fail"`
}

// Trend returns the last `days` days, zero filled.
func (s *DashboardService) Trend(ctx context.Context, days int) ([]TrendPoint, error) {
	if days == 0 {
		days = 7
	}
	if days < 0 {
		// parseInt("-3") is truthy in Node, and the loop then produces no dates.
		return []TrendPoint{}, nil
	}
	now := time.Now()
	dates := make([]string, 0, days)
	for i := days - 1; i >= 0; i-- {
		dates = append(dates, now.AddDate(0, 0, -i).Format("2006-01-02"))
	}
	rows, err := s.store.StatTrend(ctx, dates)
	if err != nil {
		return nil, err
	}
	byDate := make(map[string]store.CrontabStat, len(rows))
	for _, row := range rows {
		byDate[row.Date] = row
	}
	out := make([]TrendPoint, 0, len(dates))
	for _, date := range dates {
		stat := byDate[date]
		out = append(out, TrendPoint{
			Date:    date[5:],
			Total:   stat.RunCount,
			Success: stat.SuccessCount,
			Fail:    stat.FailCount,
		})
	}
	return out, nil
}

// TopTimeRow is one row of GET /dashboard/top-time.
type TopTimeRow struct {
	Rank    int    `json:"rank"`
	Name    string `json:"name"`
	AvgTime int64  `json:"avgTime"`
	MaxTime int64  `json:"maxTime"`
}

// TopTime lists the slowest tasks of the day.
func (s *DashboardService) TopTime(ctx context.Context) ([]TopTimeRow, error) {
	rows, err := s.store.StatTopByTime(ctx, time.Now().Format("2006-01-02"), 5)
	if err != nil {
		return nil, err
	}
	briefs, err := s.store.CronBriefs(ctx, statIDs(rows))
	if err != nil {
		return nil, err
	}
	out := make([]TopTimeRow, 0, len(rows))
	for index, row := range rows {
		brief, found := briefs[row.RefID]
		avg := int64(0)
		if row.RunCount > 0 {
			avg = int64(math.Round(float64(row.TotalTime) / float64(row.RunCount)))
		}
		out = append(out, TopTimeRow{
			Rank:    index + 1,
			Name:    taskLabel(brief, found, row.RefID),
			AvgTime: avg,
			MaxTime: row.MaxTime,
		})
	}
	return out, nil
}

// TopCountRow is one row of GET /dashboard/top-count.
type TopCountRow struct {
	Rank        int    `json:"rank"`
	Name        string `json:"name"`
	RunCount    int64  `json:"runCount"`
	AvgTime     int64  `json:"avgTime"`
	SuccessRate string `json:"successRate"`
}

// TopCount lists the most frequently run tasks of the day.
func (s *DashboardService) TopCount(ctx context.Context) ([]TopCountRow, error) {
	rows, err := s.store.StatTopByCount(ctx, time.Now().Format("2006-01-02"), 5)
	if err != nil {
		return nil, err
	}
	briefs, err := s.store.CronBriefs(ctx, statIDs(rows))
	if err != nil {
		return nil, err
	}
	out := make([]TopCountRow, 0, len(rows))
	for index, row := range rows {
		brief, found := briefs[row.RefID]
		avg := int64(0)
		rate := "0"
		if row.RunCount > 0 {
			avg = int64(math.Round(float64(row.TotalTime) / float64(row.RunCount)))
			rate = fmt.Sprintf("%.1f", float64(row.SuccessCount)/float64(row.RunCount)*100)
		}
		out = append(out, TopCountRow{
			Rank:        index + 1,
			Name:        taskLabel(brief, found, row.RefID),
			RunCount:    row.RunCount,
			AvgTime:     avg,
			SuccessRate: rate,
		})
	}
	return out, nil
}

// RunningTask is one running instance in GET /dashboard/runtime.
type RunningTask struct {
	InstanceID int64   `json:"instanceId"`
	ID         int64   `json:"id"`
	Name       string  `json:"name"`
	Pid        *int64  `json:"pid"`
	Elapsed    int64   `json:"elapsed"`
	LogPath    *string `json:"logPath"`
}

// IdleTask is one idle task row.
type IdleTask struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	LastRun string `json:"lastRun"`
}

// RuntimeResponse is GET /dashboard/runtime.
type RuntimeResponse struct {
	RunningCount int           `json:"runningCount"`
	QueuedCount  int           `json:"queuedCount"`
	Running      []RunningTask `json:"running"`
	IdleTasks    []IdleTask    `json:"idleTasks"`
}

// Runtime returns the live task picture.
func (s *DashboardService) Runtime(ctx context.Context) (*RuntimeResponse, error) {
	instances, err := s.store.RunningInstancesByStatus(ctx, 0)
	if err != nil {
		return nil, err
	}
	queued, err := s.store.CronIDsByStatus(ctx, 3)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(instances))
	for _, instance := range instances {
		ids = append(ids, instance.CronID)
	}
	briefs, err := s.store.CronBriefs(ctx, ids)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	running := make([]RunningTask, 0, len(instances))
	for _, instance := range instances {
		brief, found := briefs[instance.CronID]
		elapsed := int64(0)
		if instance.StartedAt > 0 {
			elapsed = now - instance.StartedAt
		}
		var pid *int64
		if instance.Pid.Valid {
			value := instance.Pid.Int64
			pid = &value
		}
		var logPath *string
		if instance.LogPath.Valid {
			value := instance.LogPath.String
			logPath = &value
		}
		running = append(running, RunningTask{
			InstanceID: instance.ID,
			ID:         instance.CronID,
			Name:       taskLabel(brief, found, instance.CronID),
			Pid:        pid,
			Elapsed:    elapsed,
			LogPath:    logPath,
		})
	}

	dayAgo := now - 24*3600
	idle, err := s.store.IdleCrons(ctx, dayAgo, 5)
	if err != nil {
		return nil, err
	}
	idleTasks := make([]IdleTask, 0, len(idle))
	for _, brief := range idle {
		last, err := s.store.LastExecution(ctx, brief.ID)
		if err != nil {
			return nil, err
		}
		lastRun := "-"
		if last > 0 {
			lastRun = time.Unix(last, 0).Format("01-02 15:04")
		}
		idleTasks = append(idleTasks, IdleTask{
			ID:      brief.ID,
			Name:    taskLabel(brief, true, brief.ID),
			LastRun: lastRun,
		})
	}
	return &RuntimeResponse{
		RunningCount: len(running),
		QueuedCount:  len(queued),
		Running:      running,
		IdleTasks:    idleTasks,
	}, nil
}

// LabelRow is one label bucket of GET /dashboard/labels.
type LabelRow struct {
	Label       string `json:"label"`
	Count       int    `json:"count"`
	TodayRuns   int64  `json:"todayRuns"`
	SuccessRate string `json:"successRate"`
	AvgTime     int64  `json:"avgTime"`
}

// Labels aggregates today's runs per task label.
func (s *DashboardService) Labels(ctx context.Context) ([]LabelRow, error) {
	sources, err := s.store.LabelSource(ctx, time.Now().Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	type bucket struct {
		count     int
		runs      int64
		success   int64
		totalTime int64
	}
	buckets := map[string]*bucket{}
	order := make([]string, 0, 8)
	for _, source := range sources {
		labels := parseLabels(source.Labels)
		if len(labels) == 0 {
			labels = []string{i18n.T("未分类")}
		}
		for _, label := range labels {
			entry, ok := buckets[label]
			if !ok {
				entry = &bucket{}
				buckets[label] = entry
				order = append(order, label)
			}
			entry.count++
			entry.runs += source.RunCount
			entry.success += source.SuccessCnt
			entry.totalTime += source.TotalTime
		}
	}
	out := make([]LabelRow, 0, len(order))
	for _, label := range order {
		entry := buckets[label]
		row := LabelRow{
			Label:     label,
			Count:     entry.count,
			TodayRuns: entry.runs,
			SuccessRate: func() string {
				if entry.runs <= 0 {
					return "0"
				}
				return fmt.Sprintf("%.1f", float64(entry.success)/float64(entry.runs)*100)
			}(),
		}
		if entry.runs > 0 {
			row.AvgTime = int64(math.Round(float64(entry.totalTime) / float64(entry.runs)))
		}
		out = append(out, row)
	}
	// Node sorts by todayRuns descending; sort.Slice is stable enough here
	// because labels with equal counts keep their insertion order.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].TodayRuns > out[j-1].TodayRuns; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out, nil
}

// SystemStats is GET /dashboard/system.
type SystemStats struct {
	Platform        string     `json:"platform"`
	Uptime          int64      `json:"uptime"`
	MemTotal        uint64     `json:"memTotal"`
	MemFree         uint64     `json:"memFree"`
	MemUsagePercent string     `json:"memUsagePercent"`
	HeapUsed        int64      `json:"heapUsed"`
	HeapTotal       int64      `json:"heapTotal"`
	LoadAvg         [3]float64 `json:"loadAvg"`
	Cpus            int        `json:"cpus"`
}

// System reports the host and process metrics of the system panel.
func (s *DashboardService) System() *SystemStats {
	total, free := platform.Memory()
	usage := "0.0"
	if total > 0 {
		usage = fmt.Sprintf("%.1f", (1-float64(free)/float64(total))*100)
	}
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	load := platform.LoadAvg()
	rounded := [3]float64{
		math.Round(load[0]*100) / 100,
		math.Round(load[1]*100) / 100,
		math.Round(load[2]*100) / 100,
	}
	platformName := runtime.GOOS
	if platformName == "windows" {
		platformName = "win32"
	}
	return &SystemStats{
		Platform:        platformName,
		Uptime:          int64(time.Since(s.start).Seconds()),
		MemTotal:        total,
		MemFree:         free,
		MemUsagePercent: usage,
		HeapUsed:        int64(math.Round(float64(mem.HeapAlloc) / 1024 / 1024)),
		HeapTotal:       int64(math.Round(float64(mem.HeapSys) / 1024 / 1024)),
		LoadAvg:         rounded,
		Cpus:            runtime.NumCPU(),
	}
}

// taskLabel mirrors `cron?.name || cron?.command || tf('任务#%s', id)`.
func taskLabel(brief store.CronBrief, found bool, id int64) string {
	if found {
		if name := strings.TrimSpace(brief.Name.String); name != "" {
			return name
		}
		if brief.Command != "" {
			return brief.Command
		}
	}
	return i18n.Tf("任务#%s", id)
}

func commandOf(brief store.CronBrief, found bool) string {
	if found {
		return brief.Command
	}
	return ""
}

func statIDs(rows []store.CrontabStat) []int64 {
	out := make([]int64, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.RefID)
	}
	return out
}

// parseLabels accepts the JSON column as an array or as a JSON string holding
// an array, because Sequelize stores both forms depending on the write path.
func parseLabels(raw sql.NullString) []string {
	if !raw.Valid || strings.TrimSpace(raw.String) == "" || raw.String == "null" {
		return nil
	}
	var labels []string
	text := raw.String
	if strings.HasPrefix(strings.TrimSpace(text), `"`) {
		var inner string
		if err := json.Unmarshal([]byte(text), &inner); err != nil {
			return nil
		}
		text = inner
	}
	if err := json.Unmarshal([]byte(text), &labels); err != nil {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(labels))
	for _, label := range labels {
		trimmed := strings.TrimSpace(label)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func int64Value(value any) (int64, bool) {
	switch typed := value.(type) {
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return parsed, true
		}
		if parsed, err := typed.Float64(); err == nil {
			return int64(parsed), true
		}
	case float64:
		return int64(typed), true
	case int64:
		return typed, true
	case int:
		return int64(typed), true
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return 0, false
		}
		parsed, err := strconv.ParseFloat(trimmed, 64)
		if err != nil {
			return 0, false
		}
		return int64(parsed), true
	}
	return 0, false
}

func floatValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	case float64:
		return typed, true
	case int64:
		return float64(typed), true
	case int:
		return float64(typed), true
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return parsed, err == nil
	}
	return 0, false
}

func isNumber(value any) bool {
	switch value.(type) {
	case json.Number, float64, int64, int:
		return true
	}
	return false
}

// isFalsyNumber reports whether the value is the number zero, which Node treats
// as a missing ref_id.
func isFalsyNumber(value any) bool {
	if !isNumber(value) {
		return false
	}
	parsed, ok := floatValue(value)
	return ok && parsed == 0
}
