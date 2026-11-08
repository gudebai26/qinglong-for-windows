// Package service implements the panel's business logic. M3 adds the task
// domain from back/services/cron.ts and back/services/cronView.ts.
package service

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"qinglong-go/internal/config"
	"qinglong-go/internal/cronexpr"
	"qinglong-go/internal/runner"
	"qinglong-go/internal/store"
)

// Task status values (back/data/cron.ts CrontabStatus).
const (
	CronStatusRunning  int64 = 0
	CronStatusIdle     int64 = 1
	CronStatusDisabled int64 = 2
	CronStatusQueued   int64 = 3
)

// Instance status values (back/data/runningInstance.ts InstanceStatus).
const (
	InstanceRunning  int64 = 0
	InstanceFinished int64 = 1
	InstanceStopped  int64 = 2
	InstanceError    int64 = 3
)

// ValidationError is a celebrate-shaped body validation failure.
type ValidationError struct {
	Keys    []string
	Message string
}

func (e *ValidationError) Error() string { return e.Message }

// ConflictError maps to the unique-constraint middleware (HTTP 409).
type ConflictError struct{ Message string }

func (e *ConflictError) Error() string { return e.Message }

// NotFoundError keeps the Node message and status (500 for getDb misses).
type NotFoundError struct {
	Status  int
	Message string
}

func (e *NotFoundError) Error() string { return e.Message }

// CrontabResponse is the plain-object shape Sequelize returns. Every nullable
// column stays a pointer so NULL serializes as null, not as a zero value.
type CrontabResponse struct {
	ID        int64   `json:"id"`
	Name      *string `json:"name"`
	Command   string  `json:"command"`
	Schedule  *string `json:"schedule"`
	Timestamp *string `json:"timestamp"`
	Saved     bool    `json:"saved"`
	Status    *int64  `json:"status"`

	IsSystem   *int64 `json:"isSystem"`
	Pid        *int64 `json:"pid"`
	IsDisabled *int64 `json:"isDisabled"`
	IsPinned   *int64 `json:"isPinned"`

	LogPath     *string `json:"log_path"`
	QueuedToken *string `json:"queued_token"`

	Labels any `json:"labels"`

	LastRunningTime   *int64 `json:"last_running_time"`
	LastExecutionTime *int64 `json:"last_execution_time"`
	SubID             *int64 `json:"sub_id"`

	ExtraSchedules any `json:"extra_schedules"`

	TaskBefore *string `json:"task_before"`
	TaskAfter  *string `json:"task_after"`
	LogName    *string `json:"log_name"`

	AllowMultipleInstances *int64  `json:"allow_multiple_instances"`
	WorkDir                *string `json:"work_dir"`

	CreatedAt *string `json:"createdAt"`
	UpdatedAt *string `json:"updatedAt"`
}

// CrontabList is the paginated envelope body.
type CrontabList struct {
	Data  []CrontabResponse `json:"data"`
	Total int64             `json:"total"`
}

// CronListParams mirrors the query string of GET /api/crons.
type CronListParams struct {
	SearchValue string
	Page        int
	Size        int
	Sorter      string
	Filters     string
	QueryString string
}

// CronService owns the task domain.
type CronService struct {
	cfg    *config.Config
	store  *store.Store
	logger *slog.Logger

	fileMu sync.Mutex
	// mutationMu serializes create/update/delete/enable/disable the way
	// withSchedulerMutation() does in the Node service.
	mutationMu sync.Mutex

	// scheduler is set once main wires the M4 engine. It stays nil in unit
	// tests, where the service falls back to running tasks inline.
	scheduler SchedulerRunner
	// running holds the pids of live task processes so shutdown can end them.
	running sync.Map
	// starting holds the ids whose runner is between "queued/running" and the
	// moment the process exists (the random delay and the spawn). A scheduled
	// trigger that arrives during that window must skip a single-instance task
	// exactly as it would skip a live pid.
	starting sync.Map
	// stopEarly records a Stop() that arrives while the task is still in its
	// random delay, when there is no pid to signal yet.
	stopEarly sync.Map
	// runner executes tasks (M5).
	runner *runner.Runner
	// sock pushes debug-script output to the browser (M8).
	sock *SockService
}

// SchedulerRunner is the part of the scheduler the task domain drives: a full
// reschedule after every mutation, and the manual/boot run queue.
type SchedulerRunner interface {
	Reload(ctx context.Context) error
	RunManual(ctx context.Context, ids []int64) error
}

// NewCronService wires the service.
func NewCronService(cfg *config.Config, st *store.Store, logger *slog.Logger) *CronService {
	if logger == nil {
		logger = slog.Default()
	}
	service := &CronService{cfg: cfg, store: st, logger: logger}
	service.runner = runner.New(cfg, logger)
	if err := service.runner.Bootstrap(); err != nil {
		logger.Warn("[boot] prepare task runner failed", "error", err)
	}
	return service
}

// SetScheduler attaches the scheduler. It must be called before Start so CRUD
// mutations can reschedule.
func (s *CronService) SetScheduler(scheduler SchedulerRunner) {
	s.scheduler = scheduler
}

// reloadScheduler reschedules every rule. A failure is logged instead of
// failing the mutation: the database and crontab.list are already consistent,
// and the next successful reload picks the change up.
func (s *CronService) reloadScheduler(ctx context.Context) {
	if s.scheduler == nil {
		return
	}
	if err := s.scheduler.Reload(ctx); err != nil {
		s.logger.Error("[crontab] scheduler reload failed", "error", err)
	}
}

// SetSock attaches the websocket hub used by the script debugger.
func (s *CronService) SetSock(sock *SockService) { s.sock = sock }

// ReloadScheduler reschedules every rule on demand; the settings page calls it
// after a timezone change so triggers use the new zone immediately.
func (s *CronService) ReloadScheduler(ctx context.Context) error {
	if s.scheduler == nil {
		return nil
	}
	return s.scheduler.Reload(ctx)
}

// CrontabFile returns data/config/crontab.list.
func (s *CronService) CrontabFile() string {
	return filepath.Join(s.cfg.ConfigPath, "crontab.list")
}

func nullableString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	copied := value.String
	return &copied
}

func nullableInt(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	copied := value.Int64
	return &copied
}

func nullString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: true}
}

func nullInt(value int64) sql.NullInt64 {
	return sql.NullInt64{Int64: value, Valid: true}
}

// ToResponse converts a row into the API shape.
func ToResponse(c *store.Crontab) *CrontabResponse {
	if c == nil {
		return nil
	}
	return &CrontabResponse{
		ID:                     c.ID,
		Name:                   nullableString(c.Name),
		Command:                c.Command,
		Schedule:               nullableString(c.Schedule),
		Timestamp:              nullableString(c.Timestamp),
		Saved:                  c.Saved,
		Status:                 nullableInt(c.Status),
		IsSystem:               nullableInt(c.IsSystem),
		Pid:                    nullableInt(c.Pid),
		IsDisabled:             nullableInt(c.IsDisabled),
		IsPinned:               nullableInt(c.IsPinned),
		LogPath:                nullableString(c.LogPath),
		QueuedToken:            nullableString(c.QueuedToken),
		Labels:                 store.CrontabLabels(c.Labels),
		LastRunningTime:        nullableInt(c.LastRunningTime),
		LastExecutionTime:      nullableInt(c.LastExecutionTime),
		SubID:                  nullableInt(c.SubID),
		ExtraSchedules:         store.CrontabExtraSchedules(c.ExtraSchedules),
		TaskBefore:             nullableString(c.TaskBefore),
		TaskAfter:              nullableString(c.TaskAfter),
		LogName:                nullableString(c.LogName),
		AllowMultipleInstances: nullableInt(c.AllowMultipleInstances),
		WorkDir:                nullableString(c.WorkDir),
		CreatedAt:              store.ISO8601(c.CreatedAt),
		UpdatedAt:              store.ISO8601(c.UpdatedAt),
	}
}

// Get returns one task or a Node-compatible not-found error.
func (s *CronService) Get(ctx context.Context, id int64) (*CrontabResponse, error) {
	row, err := s.store.CrontabByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, &NotFoundError{
			Status:  500,
			Message: fmt.Sprintf("Cron {\"id\":%d} not found", id),
		}
	}
	return ToResponse(row), nil
}

// FindByLogPath mirrors GET /api/crons/detail.
func (s *CronService) FindByLogPath(ctx context.Context, logPath string) (*CrontabResponse, error) {
	row, err := s.store.CrontabByLogPath(ctx, logPath)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, nil
	}
	return ToResponse(row), nil
}

// List implements GET /api/crons, including the view/search/filter/sort query
// language of back/services/cron.ts.
func (s *CronService) List(ctx context.Context, params CronListParams) (*CrontabList, error) {
	where, args, err := buildCrontabWhere(params)
	if err != nil {
		return nil, err
	}
	order := crontabOrder(params)

	limit, offset := 0, 0
	if params.Page != 0 && params.Size != 0 {
		limit = params.Size
		offset = (params.Page - 1) * params.Size
	}
	rows, total, err := s.store.ListCrontabs(ctx, where, args, order, limit, offset)
	if err != nil {
		return nil, err
	}
	data := make([]CrontabResponse, 0, len(rows))
	for i := range rows {
		data = append(data, *ToResponse(&rows[i]))
	}
	return &CrontabList{Data: data, Total: total}, nil
}

var crontabFieldWhitelist = map[string]struct{}{
	"id": {}, "name": {}, "command": {}, "schedule": {}, "timestamp": {},
	"saved": {}, "status": {}, "isSystem": {}, "pid": {}, "isDisabled": {},
	"isPinned": {}, "log_path": {}, "queued_token": {}, "labels": {},
	"last_running_time": {}, "last_execution_time": {}, "sub_id": {},
	"extra_schedules": {}, "task_before": {}, "task_after": {}, "log_name": {},
	"allow_multiple_instances": {}, "work_dir": {}, "createdAt": {}, "updatedAt": {},
}

func crontabOrder(params CronListParams) []store.OrderTerm {
	order := append([]store.OrderTerm(nil), store.DefaultCrontabOrder...)

	var view struct {
		Sorts []struct {
			Property string `json:"property"`
			Type     string `json:"type"`
		} `json:"sorts"`
	}
	if params.QueryString != "" {
		_ = json.Unmarshal([]byte(params.QueryString), &view)
	}
	for _, item := range view.Sorts {
		if _, ok := crontabFieldWhitelist[item.Property]; !ok {
			continue
		}
		order = append([]store.OrderTerm{{
			Field: item.Property,
			Desc:  strings.EqualFold(item.Type, "DESC"),
		}}, order...)
	}

	if params.Sorter != "" {
		var sorter struct {
			Field string `json:"field"`
			Type  string `json:"type"`
		}
		if err := json.Unmarshal([]byte(params.Sorter), &sorter); err == nil {
			if _, ok := crontabFieldWhitelist[sorter.Field]; ok && sorter.Field != "" {
				order = append([]store.OrderTerm{{
					Field: sorter.Field,
					Desc:  strings.EqualFold(sorter.Type, "DESC"),
				}}, order...)
			}
		}
	}
	return order
}

type whereGroup struct {
	and  []string
	or   []string
	args []any
}

func (g *whereGroup) addAnd(clause string, args ...any) {
	g.and = append(g.and, clause)
	g.args = append(g.args, args...)
}

func (g *whereGroup) addOr(clause string, args ...any) {
	g.or = append(g.or, clause)
	g.args = append(g.args, args...)
}

func (g *whereGroup) clause() string {
	var parts []string
	if len(g.and) > 0 {
		parts = append(parts, "("+strings.Join(g.and, " AND ")+")")
	}
	if len(g.or) > 0 {
		parts = append(parts, "("+strings.Join(g.or, " OR ")+")")
	}
	return strings.Join(parts, " AND ")
}

func buildCrontabWhere(params CronListParams) (string, []any, error) {
	group := &whereGroup{}

	if params.QueryString != "" {
		var view struct {
			Filters []struct {
				Property  string `json:"property"`
				Value     any    `json:"value"`
				Operation string `json:"operation"`
			} `json:"filters"`
			FilterRelation string `json:"filterRelation"`
		}
		if err := json.Unmarshal([]byte(params.QueryString), &view); err == nil {
			relationOr := view.FilterRelation == "or"
			for _, filter := range view.Filters {
				clause, args := viewFilterClause(filter.Property, filter.Value, filter.Operation)
				if clause == "" {
					continue
				}
				if relationOr {
					group.addOr(clause, args...)
				} else {
					group.addAnd(clause, args...)
				}
			}
		}
	}

	if params.SearchValue != "" {
		clause, args := searchClause(params.SearchValue)
		group.addAnd(clause, args...)
	}

	if params.Filters != "" {
		var filters map[string]any
		if err := json.Unmarshal([]byte(params.Filters), &filters); err == nil {
			for key, value := range filters {
				if value == nil {
					continue
				}
				if _, ok := crontabFieldWhitelist[key]; !ok {
					continue
				}
				if key == "status" {
					values := toIntList(value)
					if len(values) == 0 {
						continue
					}
					placeholders, args := intPlaceholders(values)
					if containsInt(values, CronStatusDisabled) {
						group.addAnd("(status IN ("+placeholders+") OR isDisabled = 1)", args...)
					} else {
						group.addAnd("(status IN ("+placeholders+") AND isDisabled = 0)", args...)
					}
					continue
				}
				if list, ok := value.([]any); ok {
					values := make([]any, 0, len(list))
					for _, item := range list {
						values = append(values, item)
					}
					if len(values) == 0 {
						continue
					}
					placeholders := strings.TrimSuffix(strings.Repeat("?,", len(values)), ",")
					group.addAnd(quote(key)+" IN ("+placeholders+")", values...)
					continue
				}
				group.addAnd(quote(key)+" = ?", value)
			}
		}
	}

	return group.clause(), group.args, nil
}

func viewFilterClause(property string, value any, operation string) (string, []any) {
	if _, ok := crontabFieldWhitelist[property]; !ok {
		return "", nil
	}
	column := quote(property)
	switch operation {
	case "Reg":
		text := fmt.Sprint(value)
		encoded := encodeURI(text)
		return "(" + column + " LIKE ? OR " + column + " LIKE ?)", []any{"%" + text + "%", "%" + encoded + "%"}
	case "NotReg":
		text := fmt.Sprint(value)
		encoded := encodeURI(text)
		return "(" + column + " NOT LIKE ? AND " + column + " NOT LIKE ?)", []any{"%" + text + "%", "%" + encoded + "%"}
	case "In":
		values := toIntList(value)
		if property == "status" {
			if len(values) == 0 {
				return "", nil
			}
			placeholders, args := intPlaceholders(values)
			if !containsInt(values, CronStatusDisabled) {
				return "(status IN (" + placeholders + ") AND isDisabled = 0)", args
			}
			return "(status IN (" + placeholders + ") OR isDisabled = 1)", args
		}
		list := toAnyList(value)
		if len(list) == 0 {
			return "", nil
		}
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(list)), ",")
		return "(" + column + " IN (" + placeholders + "))", list
	case "Nin":
		if property == "status" {
			values := toIntList(value)
			if len(values) == 0 {
				return "", nil
			}
			placeholders, args := intPlaceholders(values)
			clause := "((" + column + " NOT IN (" + placeholders + ") OR " + column + " IS NULL)"
			if containsInt(values, CronStatusDisabled) {
				clause += " AND isDisabled != 1)"
			} else {
				clause += ")"
			}
			return clause, args
		}
		list := toAnyList(value)
		if len(list) == 0 {
			return "", nil
		}
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(list)), ",")
		return "((" + column + " NOT IN (" + placeholders + ") OR " + column + " IS NULL))", list
	default:
		return "", nil
	}
}

func searchClause(searchText string) (string, []any) {
	parts := strings.SplitN(searchText, ":", 2)
	columns := []string{"name", "command", "schedule", "labels"}
	switch parts[0] {
	case "name", "command", "schedule", "label":
		column := parts[0]
		if column == "label" {
			column = "labels"
		}
		text := ""
		if len(parts) > 1 {
			text = parts[1]
		}
		encoded := encodeURI(text)
		return "(" + quote(column) + " LIKE ? OR " + quote(column) + " LIKE ?)",
			[]any{"%" + text + "%", "%" + encoded + "%"}
	default:
		text := searchText
		encoded := encodeURI(text)
		clauses := make([]string, 0, len(columns))
		args := make([]any, 0, len(columns)*2)
		for _, column := range columns {
			clauses = append(clauses, "("+quote(column)+" LIKE ? OR "+quote(column)+" LIKE ?)")
			args = append(args, "%"+text+"%", "%"+encoded+"%")
		}
		return "(" + strings.Join(clauses, " OR ") + ")", args
	}
}

func quote(identifier string) string {
	return "`" + strings.ReplaceAll(identifier, "`", "``") + "`"
}

func toAnyList(value any) []any {
	switch typed := value.(type) {
	case []any:
		return typed
	case []string:
		result := make([]any, len(typed))
		for i, item := range typed {
			result[i] = item
		}
		return result
	default:
		return []any{value}
	}
}

func toIntList(value any) []int64 {
	list := toAnyList(value)
	result := make([]int64, 0, len(list))
	for _, item := range list {
		if parsed, ok := asInt64(item); ok {
			result = append(result, parsed)
		}
	}
	return result
}

func intPlaceholders(values []int64) (string, []any) {
	placeholders := make([]string, len(values))
	args := make([]any, len(values))
	for i, value := range values {
		placeholders[i] = "?"
		args[i] = value
	}
	return strings.Join(placeholders, ","), args
}

func containsInt(values []int64, target int64) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func asInt64(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case float64:
		return int64(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

// encodeURI mirrors JavaScript's encodeURI for the LIKE fallback.
func encodeURI(value string) string {
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case strings.ContainsRune(";,/?:@&=+$-_.!~*'()#", r):
			b.WriteRune(r)
		default:
			for _, byteValue := range []byte(string(r)) {
				fmt.Fprintf(&b, "%%%02X", byteValue)
			}
		}
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Create / update payload
// ---------------------------------------------------------------------------

// CrontabInput is the validated body of POST/PUT /api/crons.
type CrontabInput struct {
	ID int64

	Name    *string
	NameSet bool

	Command  string
	Schedule string

	Labels    []any
	LabelsSet bool

	SubID    *int64
	SubIDSet bool

	ExtraSchedules    []any
	ExtraSchedulesSet bool

	TaskBefore    *string
	TaskBeforeSet bool

	TaskAfter    *string
	TaskAfterSet bool

	LogName    *string
	LogNameSet bool

	AllowMultipleInstances    *int64
	AllowMultipleInstancesSet bool

	WorkDir    *string
	WorkDirSet bool
}

// ParseCrontabInput validates a JSON object against commonCronSchema.
func (s *CronService) ParseCrontabInput(body map[string]any, requireID bool) (*CrontabInput, error) {
	input := &CrontabInput{}
	var keys []string
	var messages []string
	addError := func(key, message string) {
		keys = append(keys, key)
		messages = append(messages, message)
	}

	allowed := map[string]struct{}{
		"name": {}, "command": {}, "schedule": {}, "labels": {}, "sub_id": {},
		"extra_schedules": {}, "task_before": {}, "task_after": {}, "log_name": {},
		"allow_multiple_instances": {}, "work_dir": {},
	}
	if requireID {
		allowed["id"] = struct{}{}
	}
	for key := range body {
		if _, ok := allowed[key]; !ok {
			addError(key, `"`+key+`" is not allowed`)
		}
	}

	if requireID {
		value, ok := body["id"]
		if !ok || value == nil {
			addError("id", `"id" is required`)
		} else if id, ok := asInt64(value); ok {
			input.ID = id
		} else {
			addError("id", `"id" must be a number`)
		}
	}

	commandValue, ok := body["command"]
	switch {
	case !ok || commandValue == nil:
		addError("command", `"command" is required`)
	default:
		command, ok := commandValue.(string)
		if !ok {
			addError("command", `"command" must be a string`)
		} else if command == "" {
			addError("command", `"command" is not allowed to be empty`)
		} else {
			input.Command = command
		}
	}

	scheduleValue, ok := body["schedule"]
	switch {
	case !ok || scheduleValue == nil:
		addError("schedule", "定时规则不能为空")
	default:
		schedule, ok := scheduleValue.(string)
		if !ok {
			addError("schedule", "定时规则必须是字符串")
		} else if strings.TrimSpace(schedule) == "" {
			addError("schedule", "定时规则不能为空")
		} else if !cronexpr.IsValid(schedule) {
			addError("schedule", "无效的定时规则")
		} else {
			input.Schedule = schedule
		}
	}

	if value, exists := body["name"]; exists && value != nil {
		input.NameSet = true
		name, ok := value.(string)
		if !ok {
			addError("name", `"name" must be a string`)
		} else if name == "" {
			addError("name", `"name" is not allowed to be empty`)
		} else {
			input.Name = &name
		}
	}

	if value, exists := body["labels"]; exists {
		input.LabelsSet = true
		if value != nil {
			list, ok := value.([]any)
			if !ok {
				addError("labels", `"labels" must be an array`)
			} else {
				input.Labels = list
			}
		}
	}

	if value, exists := body["sub_id"]; exists {
		input.SubIDSet = true
		if value != nil {
			if parsed, ok := asInt64(value); ok {
				input.SubID = &parsed
			} else {
				addError("sub_id", `"sub_id" must be a number`)
			}
		}
	}

	if value, exists := body["extra_schedules"]; exists {
		input.ExtraSchedulesSet = true
		if value != nil {
			list, ok := value.([]any)
			if !ok {
				addError("extra_schedules", "额外定时规则必须是数组")
			} else {
				for index, item := range list {
					object, ok := item.(map[string]any)
					if !ok {
						addError(fmt.Sprintf("extra_schedules.%d", index), "额外定时规则必须是对象")
						continue
					}
					schedule, ok := object["schedule"].(string)
					if !ok || strings.TrimSpace(schedule) == "" {
						addError(fmt.Sprintf("extra_schedules.%d.schedule", index), "定时规则不能为空")
						continue
					}
					if !cronexpr.IsValid(schedule) {
						addError(fmt.Sprintf("extra_schedules.%d.schedule", index), "无效的定时规则")
					}
				}
				input.ExtraSchedules = list
			}
		}
	}

	if value, exists := body["task_before"]; exists {
		input.TaskBeforeSet = true
		if value != nil {
			text, ok := value.(string)
			if !ok {
				addError("task_before", `"task_before" must be a string`)
			} else {
				input.TaskBefore = &text
			}
		}
	}

	if value, exists := body["task_after"]; exists {
		input.TaskAfterSet = true
		if value != nil {
			text, ok := value.(string)
			if !ok {
				addError("task_after", `"task_after" must be a string`)
			} else {
				input.TaskAfter = &text
			}
		}
	}

	if value, exists := body["log_name"]; exists {
		input.LogNameSet = true
		if value != nil {
			text, ok := value.(string)
			if !ok {
				addError("log_name", `"log_name" must be a string`)
			} else if text != "" {
				if message := s.validateLogName(text); message != "" {
					addError("log_name", message)
				} else {
					input.LogName = &text
				}
			} else {
				input.LogName = &text
			}
		}
	}

	if value, exists := body["allow_multiple_instances"]; exists {
		input.AllowMultipleInstancesSet = true
		if value != nil {
			parsed, ok := asInt64(value)
			if !ok {
				addError("allow_multiple_instances", `"allow_multiple_instances" must be a number`)
			} else if parsed != 0 && parsed != 1 {
				addError("allow_multiple_instances", `"allow_multiple_instances" must be one of [0, 1]`)
			} else {
				input.AllowMultipleInstances = &parsed
			}
		}
	}

	if value, exists := body["work_dir"]; exists {
		input.WorkDirSet = true
		if value != nil {
			text, ok := value.(string)
			if !ok {
				addError("work_dir", `"work_dir" must be a string`)
			} else {
				input.WorkDir = &text
			}
		}
	}

	if len(keys) > 0 {
		return nil, &ValidationError{Keys: keys, Message: strings.Join(messages, ". ")}
	}
	return input, nil
}

func (s *CronService) validateLogName(value string) string {
	if strings.HasPrefix(value, "/") {
		if value == "/dev/null" {
			return ""
		}
		normalized := filepath.Clean(value)
		root := filepath.Clean(s.cfg.LogPath)
		if !strings.HasPrefix(normalized, root) {
			return "绝对路径必须在日志目录内或使用 /dev/null"
		}
		return ""
	}
	if len([]rune(value)) > 100 {
		return "日志名称不能超过100个字符"
	}
	segments := strings.Split(value, "/")
	for index, segment := range segments {
		if segment == "" {
			if index == len(segments)-1 {
				continue
			}
			return "日志名称只能包含中文、字母、数字、下划线、连字符、点和路径分隔符"
		}
		if segment == "." || segment == ".." {
			return "日志名称只能包含中文、字母、数字、下划线、连字符、点和路径分隔符"
		}
		for _, r := range segment {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				continue
			}
			switch r {
			case '_', '-', '.':
				continue
			default:
				return "日志名称只能包含中文、字母、数字、下划线、连字符、点和路径分隔符"
			}
		}
	}
	return ""
}

// Create implements POST /api/crons.
func (s *CronService) Create(ctx context.Context, input *CrontabInput) (any, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	now := time.Now()
	labels := "[]"
	if input.LabelsSet && input.Labels != nil {
		labels = marshalJSON(input.Labels)
	}
	var extra sql.NullString
	if input.ExtraSchedulesSet && input.ExtraSchedules != nil {
		extra = nullString(marshalJSON(input.ExtraSchedules))
	}

	row := &store.Crontab{
		Command:                strings.TrimSpace(input.Command),
		Schedule:               nullString(input.Schedule),
		Timestamp:              nullString(jsDateString(now)),
		Saved:                  false,
		Status:                 nullInt(CronStatusIdle),
		IsSystem:               nullInt(0),
		IsDisabled:             nullInt(0),
		IsPinned:               nullInt(0),
		LogPath:                nullString(""),
		Labels:                 nullString(labels),
		LastRunningTime:        nullInt(0),
		LastExecutionTime:      nullInt(0),
		ExtraSchedules:         extra,
		AllowMultipleInstances: nullInt(0),
		CreatedAt:              store.SequelizeTime(now),
		UpdatedAt:              store.SequelizeTime(now),
	}
	if input.NameSet && input.Name != nil {
		row.Name = nullString(*input.Name)
	}
	if input.SubIDSet && input.SubID != nil {
		row.SubID = nullInt(*input.SubID)
	}
	if input.TaskBeforeSet && input.TaskBefore != nil {
		row.TaskBefore = nullString(*input.TaskBefore)
	}
	if input.TaskAfterSet && input.TaskAfter != nil {
		row.TaskAfter = nullString(*input.TaskAfter)
	}
	if input.AllowMultipleInstancesSet && input.AllowMultipleInstances != nil {
		row.AllowMultipleInstances = nullInt(*input.AllowMultipleInstances)
	}
	if input.WorkDirSet && input.WorkDir != nil {
		row.WorkDir = nullString(*input.WorkDir)
	}

	logName, err := s.getLogName(row.Command, 0, false, input.LogName)
	if err != nil {
		return nil, err
	}
	row.LogName = nullString(logName)

	if err := s.store.InsertCrontab(ctx, row); err != nil {
		if isUniqueConstraint(err) {
			return nil, &ConflictError{Message: "资源已存在，请检查重复的名称或值"}
		}
		return nil, err
	}
	if err := s.RefreshCrontab(ctx); err != nil {
		return nil, err
	}
	s.reloadScheduler(ctx)
	return s.insertResponse(row), nil
}

// CreateFromAPI is Create followed by a re-read. The gRPC ql API needs the row
// in the list shape, while POST /api/crons returns the insert-only payload.
func (s *CronService) CreateFromAPI(ctx context.Context, input *CrontabInput) (*CrontabResponse, error) {
	created, err := s.Create(ctx, input)
	if err != nil {
		return nil, err
	}
	var id int64
	switch typed := created.(type) {
	case crontabInsertResponse:
		id = typed.ID
	case *crontabInsertResponse:
		id = typed.ID
	case *CrontabResponse:
		id = typed.ID
	}
	if id == 0 {
		return nil, &NotFoundError{Status: 500, Message: "created cron has no id"}
	}
	return s.Get(ctx, id)
}

// crontabInsertResponse mirrors the plain object Sequelize returns from
// Model.create: constructor defaults are present, untouched optional columns
// are undefined (omitted), and the key order matches back/data/cron.ts.
type crontabInsertResponse struct {
	ID        int64   `json:"id"`
	Name      *string `json:"name,omitempty"`
	Command   string  `json:"command"`
	Schedule  string  `json:"schedule"`
	Timestamp string  `json:"timestamp"`
	Saved     bool    `json:"saved"`
	Status    int64   `json:"status"`

	IsSystem   int64  `json:"isSystem"`
	IsDisabled int64  `json:"isDisabled"`
	LogPath    string `json:"log_path"`
	IsPinned   int64  `json:"isPinned"`

	Labels any `json:"labels"`

	LastRunningTime   int64  `json:"last_running_time"`
	LastExecutionTime int64  `json:"last_execution_time"`
	SubID             *int64 `json:"sub_id,omitempty"`

	ExtraSchedules any `json:"extra_schedules,omitempty"`

	TaskBefore *string `json:"task_before,omitempty"`
	TaskAfter  *string `json:"task_after,omitempty"`
	LogName    string  `json:"log_name"`

	AllowMultipleInstances int64   `json:"allow_multiple_instances"`
	WorkDir                *string `json:"work_dir,omitempty"`

	CreatedAt *string `json:"createdAt"`
	UpdatedAt *string `json:"updatedAt"`
}

func (s *CronService) insertResponse(row *store.Crontab) crontabInsertResponse {
	response := crontabInsertResponse{
		ID:                     row.ID,
		Command:                row.Command,
		Schedule:               row.Schedule.String,
		Timestamp:              row.Timestamp.String,
		Saved:                  false,
		Status:                 row.Status.Int64,
		IsSystem:               row.IsSystem.Int64,
		IsDisabled:             row.IsDisabled.Int64,
		LogPath:                row.LogPath.String,
		IsPinned:               row.IsPinned.Int64,
		Labels:                 store.CrontabLabels(row.Labels),
		LastRunningTime:        row.LastRunningTime.Int64,
		LastExecutionTime:      row.LastExecutionTime.Int64,
		LogName:                row.LogName.String,
		AllowMultipleInstances: row.AllowMultipleInstances.Int64,
		CreatedAt:              store.ISO8601(row.CreatedAt),
		UpdatedAt:              store.ISO8601(row.UpdatedAt),
	}
	if row.Name.Valid {
		response.Name = &row.Name.String
	}
	if row.SubID.Valid {
		response.SubID = &row.SubID.Int64
	}
	if row.ExtraSchedules.Valid {
		response.ExtraSchedules = store.CrontabExtraSchedules(row.ExtraSchedules)
	}
	if row.TaskBefore.Valid {
		response.TaskBefore = &row.TaskBefore.String
	}
	if row.TaskAfter.Valid {
		response.TaskAfter = &row.TaskAfter.String
	}
	if row.WorkDir.Valid {
		response.WorkDir = &row.WorkDir.String
	}
	return response
}

// Update implements PUT /api/crons.
func (s *CronService) Update(ctx context.Context, input *CrontabInput) (*CrontabResponse, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	doc, err := s.store.CrontabByID(ctx, input.ID)
	if err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, &NotFoundError{
			Status:  500,
			Message: fmt.Sprintf("Cron {\"id\":%d} not found", input.ID),
		}
	}

	merged := *doc
	merged.Command = strings.TrimSpace(input.Command)
	merged.Schedule = nullString(input.Schedule)
	merged.Timestamp = nullString(jsDateString(time.Now()))
	merged.Saved = false
	// new Crontab({...doc}) re-applies the constructor defaults.
	if !merged.Status.Valid {
		merged.Status = nullInt(CronStatusIdle)
	}
	if !merged.IsSystem.Valid {
		merged.IsSystem = nullInt(0)
	}
	if !merged.IsDisabled.Valid {
		merged.IsDisabled = nullInt(0)
	}
	if !merged.IsPinned.Valid {
		merged.IsPinned = nullInt(0)
	}
	if !merged.LogPath.Valid {
		merged.LogPath = nullString("")
	}
	if !merged.Labels.Valid {
		merged.Labels = nullString("[]")
	}
	if !merged.LastRunningTime.Valid {
		merged.LastRunningTime = nullInt(0)
	}
	if !merged.LastExecutionTime.Valid {
		merged.LastExecutionTime = nullInt(0)
	}
	if !merged.AllowMultipleInstances.Valid {
		merged.AllowMultipleInstances = nullInt(0)
	}

	if input.NameSet {
		if input.Name == nil {
			merged.Name = sql.NullString{}
		} else {
			merged.Name = nullString(*input.Name)
		}
	}
	if input.LabelsSet {
		if input.Labels == nil {
			merged.Labels = nullString("[]")
		} else {
			merged.Labels = nullString(marshalJSON(input.Labels))
		}
	}
	if input.SubIDSet {
		if input.SubID == nil {
			merged.SubID = sql.NullInt64{}
		} else {
			merged.SubID = nullInt(*input.SubID)
		}
	}
	if input.ExtraSchedulesSet {
		if input.ExtraSchedules == nil {
			merged.ExtraSchedules = sql.NullString{}
		} else {
			merged.ExtraSchedules = nullString(marshalJSON(input.ExtraSchedules))
		}
	}
	if input.TaskBeforeSet {
		if input.TaskBefore == nil {
			merged.TaskBefore = sql.NullString{}
		} else {
			merged.TaskBefore = nullString(*input.TaskBefore)
		}
	}
	if input.TaskAfterSet {
		if input.TaskAfter == nil {
			merged.TaskAfter = sql.NullString{}
		} else {
			merged.TaskAfter = nullString(*input.TaskAfter)
		}
	}
	if input.LogNameSet {
		if input.LogName == nil {
			merged.LogName = sql.NullString{}
		} else {
			merged.LogName = nullString(*input.LogName)
		}
	}
	if input.AllowMultipleInstancesSet {
		if input.AllowMultipleInstances == nil {
			merged.AllowMultipleInstances = nullInt(0)
		} else {
			merged.AllowMultipleInstances = nullInt(*input.AllowMultipleInstances)
		}
	}
	if input.WorkDirSet {
		if input.WorkDir == nil {
			merged.WorkDir = sql.NullString{}
		} else {
			merged.WorkDir = nullString(*input.WorkDir)
		}
	}

	var logNameInput *string
	if merged.LogName.Valid {
		value := merged.LogName.String
		logNameInput = &value
	}
	logName, err := s.getLogName(merged.Command, merged.ID, true, logNameInput)
	if err != nil {
		return nil, err
	}
	merged.LogName = nullString(logName)

	fields := map[string]any{
		"name":                     merged.Name,
		"command":                  merged.Command,
		"schedule":                 merged.Schedule,
		"timestamp":                merged.Timestamp,
		"saved":                    0,
		"status":                   merged.Status,
		"isSystem":                 merged.IsSystem,
		"pid":                      merged.Pid,
		"isDisabled":               merged.IsDisabled,
		"isPinned":                 merged.IsPinned,
		"log_path":                 merged.LogPath,
		"labels":                   merged.Labels,
		"last_running_time":        merged.LastRunningTime,
		"last_execution_time":      merged.LastExecutionTime,
		"sub_id":                   merged.SubID,
		"extra_schedules":          merged.ExtraSchedules,
		"task_before":              merged.TaskBefore,
		"task_after":               merged.TaskAfter,
		"log_name":                 merged.LogName,
		"allow_multiple_instances": merged.AllowMultipleInstances,
		"work_dir":                 merged.WorkDir,
	}
	if err := s.store.UpdateCrontab(ctx, merged.ID, fields); err != nil {
		if isUniqueConstraint(err) {
			return nil, &ConflictError{Message: "资源已存在，请检查重复的名称或值"}
		}
		return nil, err
	}
	updated, err := s.store.CrontabByID(ctx, merged.ID)
	if err != nil {
		return nil, err
	}
	if err := s.RefreshCrontab(ctx); err != nil {
		return nil, err
	}
	s.reloadScheduler(ctx)
	return ToResponse(updated), nil
}

// Remove implements DELETE /api/crons.
func (s *CronService) Remove(ctx context.Context, ids []int64) error {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	if err := s.store.DeleteCrontabs(ctx, ids); err != nil {
		return err
	}
	if err := s.RefreshCrontab(ctx); err != nil {
		return err
	}
	s.reloadScheduler(ctx)
	return nil
}

// SetDisabled implements PUT /api/crons/disable and /enable.
func (s *CronService) SetDisabled(ctx context.Context, ids []int64, disabled bool) error {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	value := int64(0)
	if disabled {
		value = 1
	}
	if _, err := s.store.UpdateCrontabs(ctx, ids, map[string]any{"isDisabled": value}); err != nil {
		return err
	}
	if err := s.RefreshCrontab(ctx); err != nil {
		return err
	}
	s.reloadScheduler(ctx)
	return nil
}

// SetPinned implements PUT /api/crons/pin and /unpin. It intentionally does
// not rewrite crontab.list, matching the Node service.
func (s *CronService) SetPinned(ctx context.Context, ids []int64, pinned bool) error {
	value := int64(0)
	if pinned {
		value = 1
	}
	_, err := s.store.UpdateCrontabs(ctx, ids, map[string]any{"isPinned": value})
	return err
}

// AddLabels implements POST /api/crons/labels.
func (s *CronService) AddLabels(ctx context.Context, ids []int64, labels []string) error {
	for _, id := range ids {
		row, err := s.store.CrontabByID(ctx, id)
		if err != nil || row == nil {
			continue
		}
		current := labelStrings(row.Labels)
		seen := map[string]struct{}{}
		result := make([]string, 0, len(current)+len(labels))
		for _, label := range current {
			if _, ok := seen[label]; ok {
				continue
			}
			seen[label] = struct{}{}
			result = append(result, label)
		}
		for _, label := range labels {
			if _, ok := seen[label]; ok {
				continue
			}
			seen[label] = struct{}{}
			result = append(result, label)
		}
		if _, err := s.store.UpdateCrontabs(ctx, []int64{id}, map[string]any{
			"labels": nullString(marshalJSON(result)),
		}); err != nil {
			return err
		}
	}
	return nil
}

// RemoveLabels implements DELETE /api/crons/labels.
func (s *CronService) RemoveLabels(ctx context.Context, ids []int64, labels []string) error {
	remove := map[string]struct{}{}
	for _, label := range labels {
		remove[label] = struct{}{}
	}
	for _, id := range ids {
		row, err := s.store.CrontabByID(ctx, id)
		if err != nil || row == nil {
			continue
		}
		current := labelStrings(row.Labels)
		result := make([]string, 0, len(current))
		for _, label := range current {
			if _, ok := remove[label]; ok {
				continue
			}
			result = append(result, label)
		}
		if _, err := s.store.UpdateCrontabs(ctx, []int64{id}, map[string]any{
			"labels": nullString(marshalJSON(result)),
		}); err != nil {
			return err
		}
	}
	return nil
}

func labelStrings(raw sql.NullString) []string {
	if !raw.Valid {
		return nil
	}
	var values []any
	if err := json.Unmarshal([]byte(raw.String), &values); err != nil {
		return nil
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, fmt.Sprint(value))
	}
	return result
}

// CronStatusInput is the body of PUT /api/crons/status.
type CronStatusInput struct {
	IDs               []int64
	Status            int64
	Pid               *int64
	LogPath           *string
	LastRunningTime   int64
	LastExecutionTime int64
	ExitCode          *int64
}

// SetStatus implements PUT /api/crons/status.
func (s *CronService) SetStatus(ctx context.Context, input *CronStatusInput) error {
	if input.LogPath != nil && *input.LogPath != "" && *input.LogPath != "/dev/null" {
		if _, err := s.resolveLogPath(*input.LogPath); err != nil {
			return err
		}
	}

	options := map[string]any{
		"status":              input.Status,
		"pid":                 nil,
		"log_path":            nil,
		"last_execution_time": input.LastExecutionTime,
	}
	if input.Pid != nil {
		options["pid"] = *input.Pid
	}
	if input.LogPath != nil {
		options["log_path"] = *input.LogPath
	}
	if input.LastRunningTime > 0 {
		options["last_running_time"] = input.LastRunningTime
	}

	for _, id := range input.IDs {
		row, err := s.store.CrontabByID(ctx, id)
		if err != nil || row == nil {
			continue
		}
		if input.Status == CronStatusIdle && stringValue(row.LogPath) != derefString(input.LogPath) {
			delete(options, "status")
			delete(options, "log_path")
			delete(options, "pid")
		}

		if input.Status == CronStatusRunning {
			startedAt := input.LastExecutionTime
			if startedAt == 0 {
				startedAt = time.Now().Unix()
			}
			instance := &store.RunningInstance{
				CronID:    id,
				StartedAt: startedAt,
				Status:    InstanceRunning,
			}
			if input.Pid != nil && *input.Pid > 0 {
				instance.Pid = nullInt(*input.Pid)
			}
			if input.LogPath != nil && *input.LogPath != "" {
				instance.LogPath = nullString(*input.LogPath)
			}
			if err := s.store.InsertRunningInstance(ctx, instance); err != nil {
				return err
			}
		} else if input.Status == CronStatusIdle {
			instanceStatus := InstanceFinished
			if input.ExitCode != nil && *input.ExitCode != 0 {
				instanceStatus = InstanceError
			}
			var exitCode any
			if input.ExitCode != nil {
				exitCode = *input.ExitCode
			}
			query := `UPDATE RunningInstances SET finished_at = ?, status = ?, exit_code = ?, updatedAt = ?
				 WHERE cron_id = ? AND status = 0`
			args := []any{time.Now().Unix(), instanceStatus, exitCode, store.SequelizeTime(time.Now()), id}
			if input.Pid != nil {
				query += " AND pid = ?"
				args = append(args, *input.Pid)
			}
			if _, err := s.store.DB().ExecContext(ctx, query, args...); err != nil {
				return err
			}
		}

		fields := pickTruthy(options)
		if _, err := s.store.UpdateCrontabs(ctx, []int64{id}, fields); err != nil {
			return err
		}
	}
	return nil
}

func pickTruthy(options map[string]any) map[string]any {
	result := map[string]any{}
	for key, value := range options {
		switch typed := value.(type) {
		case nil:
			continue
		case int64:
			result[key] = typed
		case int:
			result[key] = typed
		case string:
			if typed != "" {
				result[key] = typed
			}
		default:
			result[key] = value
		}
	}
	return result
}

func stringValue(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// ---------------------------------------------------------------------------
// crontab.list generation
// ---------------------------------------------------------------------------

// RefreshCrontab regenerates data/config/crontab.list from the database and
// marks every row saved, exactly like CronService.setCrontab().
func (s *CronService) RefreshCrontab(ctx context.Context) error {
	s.fileMu.Lock()
	defer s.fileMu.Unlock()

	rows, err := s.store.AllCrontabs(ctx)
	if err != nil {
		return err
	}

	var builder strings.Builder
	for i := range rows {
		row := &rows[i]
		schedule := stringValue(row.Schedule)
		if isDisabled(row) || cronexpr.IsNodeCron(schedule, extraScheduleCount(row)) || cronexpr.IsSpecial(schedule) {
			builder.WriteString("# ")
		}
		builder.WriteString(schedule)
		builder.WriteString(" ")
		builder.WriteString(s.MakeCommand(row, false))
		builder.WriteString("\n")
	}

	path := s.CrontabFile()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(builder.String()), 0o644); err != nil {
		return fmt.Errorf("write crontab.list: %w", err)
	}
	return s.store.UpdateAllCrontabs(ctx, map[string]any{"saved": 1})
}

// MakeCommand is the Go port of CronService.makeCommand.
func (s *CronService) MakeCommand(row *store.Crontab, realTime bool) string {
	command := strings.TrimSpace(row.Command)
	if !strings.HasPrefix(command, "task ") && !strings.HasPrefix(command, "ql ") {
		command = "task " + row.Command
	}

	var builder strings.Builder
	fmt.Fprintf(&builder, "real_time=%t no_tee=true ID=%d ", realTime, row.ID)
	if row.LogName.Valid && row.LogName.String != "" {
		builder.WriteString("log_name=")
		builder.WriteString(row.LogName.String)
		builder.WriteString(" ")
	}
	if row.TaskBefore.Valid && row.TaskBefore.String != "" {
		builder.WriteString("task_before='")
		builder.WriteString(escapeTaskBlock(row.TaskBefore.String))
		builder.WriteString("' ")
	}
	if row.TaskAfter.Valid && row.TaskAfter.String != "" {
		builder.WriteString("task_after='")
		builder.WriteString(escapeTaskBlock(row.TaskAfter.String))
		builder.WriteString("' ")
	}
	if row.WorkDir.Valid && row.WorkDir.String != "" {
		builder.WriteString("work_dir='")
		builder.WriteString(strings.ReplaceAll(row.WorkDir.String, "'", "'\\''"))
		builder.WriteString("' ")
	}
	builder.WriteString(command)
	return builder.String()
}

var taskNewline = regexp.MustCompile(`;? *\n`)

func escapeTaskBlock(value string) string {
	value = strings.ReplaceAll(value, "'", "'\\''")
	value = taskNewline.ReplaceAllString(value, ";")
	return strings.TrimSpace(value)
}

func isDisabled(row *store.Crontab) bool {
	return row.IsDisabled.Valid && row.IsDisabled.Int64 == 1
}

func extraScheduleCount(row *store.Crontab) int {
	if !row.ExtraSchedules.Valid || strings.TrimSpace(row.ExtraSchedules.String) == "" {
		return 0
	}
	var items []any
	if err := json.Unmarshal([]byte(row.ExtraSchedules.String), &items); err != nil {
		return 0
	}
	return len(items)
}

// GetUniqPath is the Go port of config/util.ts getUniqPath.
func GetUniqPath(command string, id int64, hasID bool) string {
	suffix := ""
	if hasID {
		suffix = fmt.Sprintf("_%d", id)
	}
	items := regexp.MustCompile(` +`).Split(command, -1)
	for index, item := range items {
		if item == "-m" {
			if index+2 <= len(items) {
				items = items[index+2:]
			} else {
				items = nil
			}
			break
		}
	}
	if len(items) == 0 {
		return "task" + suffix
	}
	str := items[0]
	if str == "task" && len(items) > 1 {
		str = items[1]
	}
	if dot := strings.LastIndex(str, "."); dot != -1 {
		str = str[:dot]
	}
	if slash := strings.LastIndex(str, "/"); slash != -1 {
		prefix := str[:slash]
		if inner := strings.LastIndex(prefix, "/"); inner != -1 {
			prefix = prefix[inner+1:]
		}
		str = prefix + "_" + str[slash+1:]
	}
	return str + suffix
}

func (s *CronService) getLogName(command string, id int64, hasID bool, logName *string) (string, error) {
	if logName != nil && *logName == "/dev/null" {
		return "/dev/null", nil
	}
	uniqPath := GetUniqPath(command, id, hasID)
	if logName != nil && *logName != "" {
		normalized := *logName
		if !strings.HasPrefix(*logName, "/") {
			normalized = filepath.Join(s.cfg.LogPath, *logName)
		}
		if strings.HasPrefix(normalized, s.cfg.LogPath) {
			uniqPath = *logName
		}
	}
	dir := filepath.Join(s.cfg.LogPath, uniqPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create log directory: %w", err)
	}
	return uniqPath, nil
}

// resolveLogPath validates a stored log path against the log directory.
func (s *CronService) resolveLogPath(value string) (string, error) {
	if value == "" || strings.ContainsRune(value, '\x00') {
		return "", &NotFoundError{Status: 400, Message: "Log path is outside the log directory"}
	}
	root, err := filepath.Abs(s.cfg.LogPath)
	if err != nil {
		return "", err
	}
	target, err := filepath.Abs(filepath.Join(root, value))
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(value) {
		target = filepath.Clean(value)
	}
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", &NotFoundError{Status: 400, Message: "Log path is outside the log directory"}
	}
	if err := ensureNoSymlinkEscape(root, target); err != nil {
		return "", err
	}
	return target, nil
}

// ensureNoSymlinkEscape resolves the deepest existing ancestor of target and
// rejects paths that leave the log directory through a symlink, mirroring
// resolveFileAccess() from back/shared/fileAccess.ts.
func ensureNoSymlinkEscape(root, target string) error {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		// The log root does not exist yet; the lexical check above is enough.
		return nil
	}
	existing := target
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return nil
		}
		existing = parent
	}
	realExisting, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return &NotFoundError{Status: 400, Message: "Log path is outside the log directory"}
	}
	relative, err := filepath.Rel(existing, target)
	if err != nil {
		return &NotFoundError{Status: 400, Message: "Log path is outside the log directory"}
	}
	realTarget := filepath.Join(realExisting, relative)
	relToRoot, err := filepath.Rel(realRoot, realTarget)
	if err != nil || relToRoot == ".." || strings.HasPrefix(relToRoot, ".."+string(filepath.Separator)) {
		return &NotFoundError{Status: 400, Message: "Log path is outside the log directory"}
	}
	return nil
}

func marshalJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "null"
	}
	return string(encoded)
}

func isUniqueConstraint(err error) bool {
	return err != nil && strings.Contains(strings.ToUpper(err.Error()), "UNIQUE CONSTRAINT")
}

// jsDateString renders new Date().toString() with the same layout Node uses
// for the `timestamp` column.
func jsDateString(t time.Time) string {
	zoneName := "CST"
	if name, offset := t.Zone(); offset == 8*3600 {
		if name == "CST" || name == "" {
			zoneName = "中国标准时间"
		} else {
			zoneName = name
		}
	} else if name != "" {
		zoneName = name
	}
	_, offset := t.Zone()
	sign := "+"
	if offset < 0 {
		sign = "-"
		offset = -offset
	}
	return fmt.Sprintf("%s %s %02d %s %s GMT%s%02d%02d (%s)",
		t.Format("Mon"), t.Format("Jan"), t.Day(), t.Format("2006"),
		t.Format("15:04:05"), sign, offset/3600, (offset%3600)/60, zoneName)
}

// uuidV4 renders a random RFC 4122 version 4 UUID.
func uuidV4() string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	buffer[6] = (buffer[6] & 0x0f) | 0x40
	buffer[8] = (buffer[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", buffer[0:4], buffer[4:6], buffer[6:8], buffer[8:10], buffer[10:16])
}
