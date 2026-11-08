// Package service: this file ports back/services/subscription.ts and the
// scheduling half of back/services/schedule.ts for the subscription domain.
package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"qinglong-go/internal/config"
	"qinglong-go/internal/cronexpr"
	"qinglong-go/internal/i18n"
	"qinglong-go/internal/platform"
	"qinglong-go/internal/preload"
	"qinglong-go/internal/store"
)

// Subscription status values (back/data/subscription.ts SubscriptionStatus).
const (
	SubStatusRunning  int64 = 0
	SubStatusIdle     int64 = 1
	SubStatusDisabled int64 = 2
	SubStatusQueued   int64 = 3
)

// LogEndSymbol mirrors config/const.ts LOG_END_SYMBOL: five ideographic spaces
// that the log modal looks for to stop polling.
const LogEndSymbol = "\u3000\u3000\u3000\u3000\u3000"

// subscriptionDoc is the business view of a row: JSON columns are decoded and
// the string columns lose their sql.NullString wrapper.
type subscriptionDoc struct {
	ID int64

	Name           string
	URL            string
	Schedule       string
	IntervalType   string
	IntervalValue  int64
	Type           string
	Whitelist      string
	Blacklist      string
	Status         int64
	Dependences    string
	Extensions     string
	SubBefore      string
	SubAfter       string
	Branch         string
	PullType       string
	PullOption     map[string]string
	Pid            *int64
	IsDisabled     bool
	LogPath        string
	ScheduleType   string
	Alias          string
	Proxy          string
	AutoAddCron    bool
	AutoDelCron    bool
	HasAutoAddCron bool
	HasAutoDelCron bool
}

// SubscriptionResponse is the plain-object shape Sequelize returns.
type SubscriptionResponse struct {
	ID               int64   `json:"id"`
	Name             *string `json:"name"`
	URL              *string `json:"url"`
	Schedule         *string `json:"schedule"`
	IntervalSchedule any     `json:"interval_schedule"`
	Type             *string `json:"type"`
	Whitelist        *string `json:"whitelist"`
	Blacklist        *string `json:"blacklist"`
	Status           *int64  `json:"status"`
	Dependences      *string `json:"dependences"`
	Extensions       *string `json:"extensions"`
	SubBefore        *string `json:"sub_before"`
	SubAfter         *string `json:"sub_after"`
	Branch           *string `json:"branch"`
	PullType         *string `json:"pull_type"`
	PullOption       any     `json:"pull_option"`
	Pid              *int64  `json:"pid"`
	IsDisabled       *int64  `json:"is_disabled"`
	LogPath          *string `json:"log_path"`
	ScheduleType     *string `json:"schedule_type"`
	Alias            *string `json:"alias"`
	Proxy            *string `json:"proxy"`
	AutoAddCron      *int64  `json:"autoAddCron"`
	AutoDelCron      *int64  `json:"autoDelCron"`
	CreatedAt        *string `json:"createdAt"`
	UpdatedAt        *string `json:"updatedAt"`
}

// SubscriptionInsertResponse mirrors the plain object Sequelize returns from
// Model.create: attributes absent from the payload stay undefined and are
// dropped by JSON.stringify, while the command handleTask computed is added at
// the end of dataValues.
type SubscriptionInsertResponse struct {
	ID               int64             `json:"id"`
	Name             string            `json:"name"`
	URL              string            `json:"url"`
	Schedule         string            `json:"schedule,omitempty"`
	IntervalSchedule *IntervalSchedule `json:"interval_schedule,omitempty"`
	Type             string            `json:"type"`
	Whitelist        *string           `json:"whitelist,omitempty"`
	Blacklist        *string           `json:"blacklist,omitempty"`
	Status           int64             `json:"status"`
	Dependences      *string           `json:"dependences,omitempty"`
	Extensions       *string           `json:"extensions,omitempty"`
	SubBefore        *string           `json:"sub_before,omitempty"`
	SubAfter         *string           `json:"sub_after,omitempty"`
	Branch           *string           `json:"branch,omitempty"`
	PullType         *string           `json:"pull_type,omitempty"`
	PullOption       any               `json:"pull_option,omitempty"`
	Pid              *int64            `json:"pid,omitempty"`
	IsDisabled       *int64            `json:"is_disabled,omitempty"`
	LogPath          *string           `json:"log_path,omitempty"`
	ScheduleType     string            `json:"schedule_type,omitempty"`
	Alias            string            `json:"alias"`
	Proxy            *string           `json:"proxy,omitempty"`
	AutoAddCron      int64             `json:"autoAddCron"`
	AutoDelCron      int64             `json:"autoDelCron"`
	CreatedAt        string            `json:"createdAt"`
	UpdatedAt        string            `json:"updatedAt"`
	Command          string            `json:"command"`
}

// SubscriptionUpdateResponse is the stored document plus the command
// handleTask wrote onto it (Node mutates the same plain object it returns).
type SubscriptionUpdateResponse struct {
	*SubscriptionResponse
	Command string `json:"command"`
}

// SubscriptionInput is the validated body of POST/PUT /api/subscriptions.
type SubscriptionInput struct {
	ID int64

	Type     string
	URL      string
	Alias    string
	Schedule string

	IntervalSchedule *IntervalSchedule

	Name        *string
	Whitelist   *string
	Blacklist   *string
	Branch      *string
	Dependences *string
	PullType    *string
	Extensions  *string
	SubBefore   *string
	SubAfter    *string
	Proxy       *string

	PullOption any

	ScheduleType string

	AutoAddCron *bool
	AutoDelCron *bool
}

// IntervalSchedule is the {type, value} object of an interval subscription.
type IntervalSchedule struct {
	Type  string `json:"type"`
	Value int64  `json:"value"`
}

// SubscriptionService owns the subscription domain.
type SubscriptionService struct {
	cfg    *config.Config
	store  *store.Store
	logger *slog.Logger
	crons  *CronService
	notify *NotifyService
	tools  *platform.Toolchain

	mu     sync.Mutex
	jobs   map[int64]*subscriptionJob
	runs   map[int64]*subscriptionRun
	sem    chan struct{}
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	// sock announces a finished run (runSubscriptionEnd).
	sock *SockService
}

// NewSubscriptionService wires the service.
func NewSubscriptionService(cfg *config.Config, st *store.Store, crons *CronService, notify *NotifyService, tools *platform.Toolchain, logger *slog.Logger) *SubscriptionService {
	if logger == nil {
		logger = slog.Default()
	}
	if tools == nil {
		tools = platform.NewToolchain()
	}
	ctx, cancel := context.WithCancel(context.Background())
	limit := runtime.NumCPU()
	if limit < 4 {
		limit = 4
	}
	return &SubscriptionService{
		cfg:    cfg,
		store:  st,
		logger: logger,
		crons:  crons,
		notify: notify,
		tools:  tools,
		jobs:   map[int64]*subscriptionJob{},
		runs:   map[int64]*subscriptionRun{},
		sem:    make(chan struct{}, limit),
		ctx:    ctx,
		cancel: cancel,
	}
}

// Tools exposes the toolchain so the HTTP layer can report a doctor summary.
func (s *SubscriptionService) Tools() *platform.Toolchain { return s.tools }

// ---------------------------------------------------------------------------
// row <-> doc
// ---------------------------------------------------------------------------

func decodeJSONColumn(raw sql.NullString) any {
	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return nil
	}
	var value any
	if err := json.Unmarshal([]byte(raw.String), &value); err != nil {
		return raw.String
	}
	return value
}

func nullStringValue(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func boolFromNullInt(value sql.NullInt64) bool {
	return value.Valid && value.Int64 != 0
}

func toDoc(row *store.Subscription) *subscriptionDoc {
	doc := &subscriptionDoc{
		ID:             row.ID,
		Name:           nullStringValue(row.Name),
		URL:            nullStringValue(row.URL),
		Schedule:       nullStringValue(row.Schedule),
		Type:           nullStringValue(row.Type),
		Whitelist:      nullStringValue(row.Whitelist),
		Blacklist:      nullStringValue(row.Blacklist),
		Status:         SubStatusIdle,
		Dependences:    nullStringValue(row.Dependences),
		Extensions:     nullStringValue(row.Extensions),
		SubBefore:      nullStringValue(row.SubBefore),
		SubAfter:       nullStringValue(row.SubAfter),
		Branch:         nullStringValue(row.Branch),
		PullType:       nullStringValue(row.PullType),
		PullOption:     map[string]string{},
		LogPath:        nullStringValue(row.LogPath),
		ScheduleType:   nullStringValue(row.ScheduleType),
		Alias:          nullStringValue(row.Alias),
		Proxy:          nullStringValue(row.Proxy),
		AutoAddCron:    boolFromNullInt(row.AutoAddCron),
		AutoDelCron:    boolFromNullInt(row.AutoDelCron),
		HasAutoAddCron: row.AutoAddCron.Valid,
		HasAutoDelCron: row.AutoDelCron.Valid,
	}
	if row.Status.Valid {
		doc.Status = row.Status.Int64
	}
	if row.IsDisabled.Valid {
		doc.IsDisabled = row.IsDisabled.Int64 != 0
	}
	if row.Pid.Valid {
		pid := row.Pid.Int64
		doc.Pid = &pid
	}
	if decoded, ok := decodeJSONColumn(row.IntervalSchedule).(map[string]any); ok {
		if kind, ok := decoded["type"].(string); ok {
			doc.IntervalType = kind
		}
		switch value := decoded["value"].(type) {
		case float64:
			doc.IntervalValue = int64(value)
		case json.Number:
			if parsed, err := value.Int64(); err == nil {
				doc.IntervalValue = parsed
			}
		}
	}
	if decoded, ok := decodeJSONColumn(row.PullOption).(map[string]any); ok {
		for key, value := range decoded {
			if text, ok := value.(string); ok {
				doc.PullOption[key] = text
			}
		}
	}
	return doc
}

// ToSubscriptionResponse converts a row into the API shape.
func ToSubscriptionResponse(row *store.Subscription) *SubscriptionResponse {
	if row == nil {
		return nil
	}
	return &SubscriptionResponse{
		ID:               row.ID,
		Name:             nullableString(row.Name),
		URL:              nullableString(row.URL),
		Schedule:         nullableString(row.Schedule),
		IntervalSchedule: decodeJSONColumn(row.IntervalSchedule),
		Type:             nullableString(row.Type),
		Whitelist:        nullableString(row.Whitelist),
		Blacklist:        nullableString(row.Blacklist),
		Status:           nullableInt(row.Status),
		Dependences:      nullableString(row.Dependences),
		Extensions:       nullableString(row.Extensions),
		SubBefore:        nullableString(row.SubBefore),
		SubAfter:         nullableString(row.SubAfter),
		Branch:           nullableString(row.Branch),
		PullType:         nullableString(row.PullType),
		PullOption:       decodeJSONColumn(row.PullOption),
		Pid:              nullableInt(row.Pid),
		IsDisabled:       nullableInt(row.IsDisabled),
		LogPath:          nullableString(row.LogPath),
		ScheduleType:     nullableString(row.ScheduleType),
		Alias:            nullableString(row.Alias),
		Proxy:            nullableString(row.Proxy),
		AutoAddCron:      nullableInt(row.AutoAddCron),
		AutoDelCron:      nullableInt(row.AutoDelCron),
		CreatedAt:        store.ISO8601(row.CreatedAt),
		UpdatedAt:        store.ISO8601(row.UpdatedAt),
	}
}

// ---------------------------------------------------------------------------
// CRUD
// ---------------------------------------------------------------------------

// SubscriptionListParams mirrors the query string of GET /api/subscriptions.
type SubscriptionListParams struct {
	SearchValue string
	IDs         string
	IDsSet      bool
}

// List implements GET /api/subscriptions.
func (s *SubscriptionService) List(ctx context.Context, params SubscriptionListParams) ([]SubscriptionResponse, error) {
	clauses := make([]string, 0, 2)
	args := make([]any, 0, 6)

	if params.SearchValue != "" {
		encoded := encodeURI(params.SearchValue)
		clauses = append(clauses, "((name LIKE ? OR name LIKE ?) OR (url LIKE ? OR url LIKE ?))")
		args = append(args, "%"+params.SearchValue+"%", "%"+encoded+"%",
			"%"+params.SearchValue+"%", "%"+encoded+"%")
	}
	if params.IDsSet {
		ids, err := parseIDList(params.IDs)
		if err != nil {
			return nil, &BusinessError{Code: 400, Message: "ids must be a JSON array of numbers"}
		}
		if len(ids) == 0 {
			return []SubscriptionResponse{}, nil
		}
		placeholders := make([]string, len(ids))
		for i, id := range ids {
			placeholders[i] = "?"
			args = append(args, id)
		}
		clauses = append(clauses, "id IN ("+strings.Join(placeholders, ",")+")")
	}

	rows, err := s.store.ListSubscriptions(ctx, strings.Join(clauses, " AND "), args, store.SubscriptionDefaultOrder)
	if err != nil {
		return nil, err
	}
	data := make([]SubscriptionResponse, 0, len(rows))
	for i := range rows {
		data = append(data, *ToSubscriptionResponse(&rows[i]))
	}
	return data, nil
}

// parseIDList decodes the `ids` query parameter (a JSON array of numbers).
func parseIDList(raw string) ([]int64, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var values []any
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(values))
	for _, value := range values {
		switch typed := value.(type) {
		case float64:
			ids = append(ids, int64(typed))
		default:
			return nil, errors.New("ids must be numbers")
		}
	}
	return ids, nil
}

// Get implements GET /api/subscriptions/:id.
func (s *SubscriptionService) Get(ctx context.Context, id int64) (*SubscriptionResponse, error) {
	row, err := s.store.SubscriptionByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, &NotFoundError{
			Status:  500,
			Message: fmt.Sprintf("Subscription {\"id\":%d} not found", id),
		}
	}
	return ToSubscriptionResponse(row), nil
}

// Create implements POST /api/subscriptions.
func (s *SubscriptionService) Create(ctx context.Context, input *SubscriptionInput) (*SubscriptionInsertResponse, error) {
	if err := validateSubscriptionAlias(input.Alias); err != nil {
		return nil, &NotFoundError{Status: 400, Message: err.Error()}
	}
	row := s.rowFromInput(nil, input)
	if err := s.store.InsertSubscription(ctx, row); err != nil {
		return nil, mapSubscriptionWriteError(err)
	}
	doc := toDoc(row)
	s.handleTask(doc, true)
	if err := s.SetSSHConfig(ctx); err != nil {
		s.logger.Warn("[subscription] apply ssh config failed", "error", err)
	}
	return subscriptionInsertResponse(row, doc), nil
}

// subscriptionInsertResponse builds the create response from the row and the
// input it was built from.
func subscriptionInsertResponse(row *store.Subscription, doc *subscriptionDoc) *SubscriptionInsertResponse {
	response := &SubscriptionInsertResponse{
		ID:           row.ID,
		Name:         doc.Name,
		URL:          doc.URL,
		Schedule:     doc.Schedule,
		Type:         doc.Type,
		Status:       doc.Status,
		ScheduleType: doc.ScheduleType,
		Alias:        doc.Alias,
		AutoAddCron:  boolToInt64(doc.AutoAddCron),
		AutoDelCron:  boolToInt64(doc.AutoDelCron),
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
		Command:      subscriptionCommand(doc),
	}
	if row.IntervalSchedule.Valid {
		response.IntervalSchedule = &IntervalSchedule{Type: doc.IntervalType, Value: doc.IntervalValue}
	}
	response.Whitelist = nullableString(row.Whitelist)
	response.Blacklist = nullableString(row.Blacklist)
	response.Dependences = nullableString(row.Dependences)
	response.Extensions = nullableString(row.Extensions)
	response.SubBefore = nullableString(row.SubBefore)
	response.SubAfter = nullableString(row.SubAfter)
	response.Branch = nullableString(row.Branch)
	response.PullType = nullableString(row.PullType)
	response.PullOption = decodeJSONColumn(row.PullOption)
	response.Pid = nullableInt(row.Pid)
	response.IsDisabled = nullableInt(row.IsDisabled)
	response.LogPath = nullableString(row.LogPath)
	response.Proxy = nullableString(row.Proxy)
	if response.CreatedAt != "" {
		if iso := store.ISO8601(response.CreatedAt); iso != nil {
			response.CreatedAt = *iso
		}
	}
	if response.UpdatedAt != "" {
		if iso := store.ISO8601(response.UpdatedAt); iso != nil {
			response.UpdatedAt = *iso
		}
	}
	return response
}

// Update implements PUT /api/subscriptions.
func (s *SubscriptionService) Update(ctx context.Context, input *SubscriptionInput) (*SubscriptionUpdateResponse, error) {
	if err := validateSubscriptionAlias(input.Alias); err != nil {
		return nil, &NotFoundError{Status: 400, Message: err.Error()}
	}
	current, err := s.store.SubscriptionByID(ctx, input.ID)
	if err != nil {
		return nil, err
	}
	if current == nil {
		return nil, &NotFoundError{
			Status:  500,
			Message: fmt.Sprintf("Subscription {\"id\":%d} not found", input.ID),
		}
	}
	merged := s.rowFromInput(current, input)
	fields := map[string]any{
		"name": merged.Name, "url": merged.URL, "schedule": merged.Schedule,
		"interval_schedule": merged.IntervalSchedule, "type": merged.Type,
		"whitelist": merged.Whitelist, "blacklist": merged.Blacklist,
		"status": merged.Status, "dependences": merged.Dependences,
		"extensions": merged.Extensions, "sub_before": merged.SubBefore,
		"sub_after": merged.SubAfter, "branch": merged.Branch,
		"pull_type": merged.PullType, "pull_option": merged.PullOption,
		"pid": merged.Pid, "is_disabled": merged.IsDisabled,
		"log_path": merged.LogPath, "schedule_type": merged.ScheduleType,
		"alias": merged.Alias, "proxy": merged.Proxy,
		"autoAddCron": merged.AutoAddCron, "autoDelCron": merged.AutoDelCron,
	}
	if err := s.store.UpdateSubscription(ctx, input.ID, fields); err != nil {
		return nil, mapSubscriptionWriteError(err)
	}
	updated, err := s.store.SubscriptionByID(ctx, input.ID)
	if err != nil {
		return nil, err
	}
	doc := toDoc(updated)
	s.handleTask(doc, !doc.IsDisabled)
	s.removeSshConfigForSubscription(toDoc(current))
	if err := s.SetSSHConfig(ctx); err != nil {
		s.logger.Warn("[subscription] apply ssh config failed", "error", err)
	}
	return &SubscriptionUpdateResponse{
		SubscriptionResponse: ToSubscriptionResponse(updated),
		Command:              subscriptionCommand(doc),
	}, nil
}

// rowFromInput merges the request over the stored row (nil for a create).
func (s *SubscriptionService) rowFromInput(current *store.Subscription, input *SubscriptionInput) *store.Subscription {
	row := &store.Subscription{}
	if current != nil {
		*row = *current
	}

	name := input.Alias
	if input.Name != nil && *input.Name != "" {
		name = *input.Name
	} else if current != nil && current.Name.Valid && current.Name.String != "" && input.Name == nil {
		name = current.Name.String
	}
	row.Name = sql.NullString{String: name, Valid: true}
	row.URL = sql.NullString{String: input.URL, Valid: true}
	row.Type = sql.NullString{String: input.Type, Valid: true}
	row.Alias = sql.NullString{String: input.Alias, Valid: true}
	row.ScheduleType = sql.NullString{String: input.ScheduleType, Valid: input.ScheduleType != ""}
	// A new row starts idle; an update keeps whatever state the row is in
	// (Node spreads the stored document into the new model instance).
	row.Status = sql.NullInt64{Int64: SubStatusIdle, Valid: true}
	if current != nil && current.Status.Valid {
		row.Status = sql.NullInt64{Int64: current.Status.Int64, Valid: true}
	}

	row.Schedule = optionalString(input.Schedule)
	if input.IntervalSchedule != nil {
		row.IntervalSchedule = optionalJSON(input.IntervalSchedule)
	} else if current == nil {
		row.IntervalSchedule = sql.NullString{}
	}
	row.Whitelist = optionalPtr(input.Whitelist)
	row.Blacklist = optionalPtr(input.Blacklist)
	row.Branch = optionalPtr(input.Branch)
	row.Dependences = optionalPtr(input.Dependences)
	row.Extensions = optionalPtr(input.Extensions)
	row.SubBefore = optionalPtr(input.SubBefore)
	row.SubAfter = optionalPtr(input.SubAfter)
	row.Proxy = optionalPtr(input.Proxy)
	row.PullType = optionalPtr(input.PullType)
	if input.PullOption != nil {
		encoded, err := json.Marshal(input.PullOption)
		if err == nil {
			row.PullOption = sql.NullString{String: string(encoded), Valid: true}
		}
	} else if current == nil {
		row.PullOption = sql.NullString{}
	}

	if input.AutoAddCron != nil {
		row.AutoAddCron = sql.NullInt64{Int64: boolToInt64(*input.AutoAddCron), Valid: true}
	} else if current == nil {
		row.AutoAddCron = sql.NullInt64{Int64: 0, Valid: true}
	}
	if input.AutoDelCron != nil {
		row.AutoDelCron = sql.NullInt64{Int64: boolToInt64(*input.AutoDelCron), Valid: true}
	} else if current == nil {
		row.AutoDelCron = sql.NullInt64{Int64: 0, Valid: true}
	}
	return row
}

func boolToInt64(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

func optionalString(value string) sql.NullString {
	if value == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: value, Valid: true}
}

func optionalPtr(value *string) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *value, Valid: true}
}

func optionalJSON(value *IntervalSchedule) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return sql.NullString{}
	}
	return sql.NullString{String: string(encoded), Valid: true}
}

// mapSubscriptionWriteError turns a SQLite UNIQUE violation into the 409 the
// Node uniqueConstraintError middleware returns.
func mapSubscriptionWriteError(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(strings.ToUpper(err.Error()), "UNIQUE CONSTRAINT FAILED") {
		return &ConflictError{Message: i18n.T("资源已存在，请检查重复的名称或值")}
	}
	return err
}

// Remove implements DELETE /api/subscriptions.
func (s *SubscriptionService) Remove(ctx context.Context, ids []int64, force bool) error {
	if len(ids) == 0 {
		return nil
	}
	rows, err := s.store.ListSubscriptions(ctx, idClause(len(ids)), int64Args(ids), store.SubscriptionDefaultOrder)
	if err != nil {
		return err
	}

	var storagePaths []string
	var retainedPaths []string
	if force {
		for i := range rows {
			doc := toDoc(&rows[i])
			if err := validateSubscriptionAlias(doc.Alias); err != nil {
				return &NotFoundError{Status: 400, Message: err.Error()}
			}
			if _, err := s.resolveStoragePath(s.cfg.ScriptPath, doc.Alias); err != nil {
				return err
			}
			if _, err := s.resolveStoragePath(s.cfg.RepoPath, doc.Alias); err != nil {
				return err
			}
			targets, err := s.collectStoragePaths(doc, true)
			if err != nil {
				return err
			}
			storagePaths = append(storagePaths, targets...)
		}
		remaining, err := s.store.ListSubscriptions(ctx, "", nil, store.SubscriptionDefaultOrder)
		if err != nil {
			return err
		}
		for i := range remaining {
			if containsID(ids, remaining[i].ID) {
				continue
			}
			targets, err := s.collectStoragePaths(toDoc(&remaining[i]), false)
			if err != nil {
				continue
			}
			retainedPaths = append(retainedPaths, targets...)
		}
	}

	for i := range rows {
		s.handleTask(toDoc(&rows[i]), false)
	}
	if err := s.store.DeleteSubscriptions(ctx, ids); err != nil {
		return err
	}
	for i := range rows {
		s.removeSshConfigForSubscription(toDoc(&rows[i]))
	}
	if err := s.SetSSHConfig(ctx); err != nil {
		s.logger.Warn("[subscription] apply ssh config failed", "error", err)
	}

	if !force {
		return nil
	}
	crons, _, err := s.store.ListCrontabs(ctx, "sub_id IN ("+placeholders(len(ids))+")", int64Args(ids), store.DefaultCrontabOrder, 0, 0)
	if err != nil {
		return err
	}
	if len(crons) > 0 {
		cronIDs := make([]int64, 0, len(crons))
		for _, cron := range crons {
			cronIDs = append(cronIDs, cron.ID)
		}
		if err := s.crons.Remove(ctx, cronIDs); err != nil {
			return err
		}
	}
	for _, target := range uniqueStrings(storagePaths) {
		shared := false
		for _, retained := range retainedPaths {
			if equalPath(retained, target) ||
				isStrictlyWithinPath(target, retained) ||
				isStrictlyWithinPath(retained, target) {
				shared = true
				break
			}
		}
		if shared {
			continue
		}
		if err := os.RemoveAll(target); err != nil && !errors.Is(err, os.ErrNotExist) {
			s.logger.Warn("[subscription] remove storage failed", "path", target, "error", err)
		}
	}
	return nil
}

// collectStoragePaths lists the script/repo/raw paths a subscription owns.
func (s *SubscriptionService) collectStoragePaths(doc *subscriptionDoc, strict bool) ([]string, error) {
	script, repo, raw := subscriptionStorageNames(doc)
	targets := make([]string, 0, 3)
	appendPath := func(root, name string) error {
		if name == "" {
			return nil
		}
		if !isSubscriptionStorageName(name) {
			return nil
		}
		if strict {
			resolved, err := s.resolveStoragePath(root, name)
			if err != nil {
				return err
			}
			targets = append(targets, resolved)
			return nil
		}
		if resolved := resolveFileAccess(root, []string{name}, nil); resolved != "" {
			targets = append(targets, resolved)
		}
		return nil
	}
	if err := appendPath(s.cfg.ScriptPath, script); err != nil {
		return nil, err
	}
	if err := appendPath(s.cfg.RepoPath, repo); err != nil {
		return nil, err
	}
	if err := appendPath(filepath.Join(s.cfg.DataPath, "raw"), raw); err != nil {
		return nil, err
	}
	return targets, nil
}

// resolveStoragePath ports resolveSubscriptionStoragePath.
func (s *SubscriptionService) resolveStoragePath(root, name string) (string, error) {
	if !isSubscriptionStorageName(name) {
		return "", &NotFoundError{Status: 400, Message: "Invalid subscription path"}
	}
	resolved := resolveFileAccess(root, []string{name}, nil)
	if resolved == "" {
		return "", &NotFoundError{Status: 400, Message: "Invalid subscription path"}
	}
	return resolved, nil
}

func idClause(count int) string {
	return "id IN (" + placeholders(count) + ")"
}

func placeholders(count int) string {
	if count <= 0 {
		return "NULL"
	}
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}

func int64Args(values []int64) []any {
	args := make([]any, len(values))
	for i, value := range values {
		args[i] = value
	}
	return args
}

func containsID(ids []int64, id int64) bool {
	for _, value := range ids {
		if value == id {
			return true
		}
	}
	return false
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

// ---------------------------------------------------------------------------
// run / stop / enable / disable / status
// ---------------------------------------------------------------------------

// SetSock attaches the websocket hub used by runSubscriptionEnd.
func (s *SubscriptionService) SetSock(sock *SockService) { s.sock = sock }

// Run implements PUT /api/subscriptions/run.
func (s *SubscriptionService) Run(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	rows, err := s.store.ListSubscriptions(ctx, idClause(len(ids)), int64Args(ids), store.SubscriptionDefaultOrder)
	if err != nil {
		return err
	}
	// Fail loudly before queueing: a missing git would otherwise show up as a
	// silent no-op in the log a few seconds later.
	for i := range rows {
		doc := toDoc(&rows[i])
		if doc.Type == "file" {
			continue
		}
		if s.tools.Path("git") == "" {
			return &BusinessError{Code: 400, Message: i18n.T("未检测到 git，无法拉取仓库订阅，请先安装 git")}
		}
	}
	if _, err := s.store.UpdateSubscriptions(ctx, ids, map[string]any{"status": SubStatusQueued}); err != nil {
		return err
	}
	for _, id := range ids {
		go s.runOne(id, true)
	}
	return nil
}

// Stop implements PUT /api/subscriptions/stop.
func (s *SubscriptionService) Stop(ctx context.Context, ids []int64) error {
	rows, err := s.store.ListSubscriptions(ctx, idClause(len(ids)), int64Args(ids), store.SubscriptionDefaultOrder)
	if err != nil {
		return err
	}
	var failure error
	for i := range rows {
		id := rows[i].ID
		if run := s.lookupRun(id); run != nil {
			run.stop()
		}
		fields := map[string]any{"status": SubStatusIdle, "pid": nil}
		if err := s.store.UpdateSubscription(ctx, id, fields); err != nil {
			s.logger.Error("[subscription] stop failed", "id", id, "error", err)
			if failure == nil {
				failure = err
			}
		}
	}
	return failure
}

// SetDisabled implements PUT /api/subscriptions/{disable,enable}.
func (s *SubscriptionService) SetDisabled(ctx context.Context, ids []int64, disabled bool) error {
	value := int64(0)
	if disabled {
		value = 1
	}
	if _, err := s.store.UpdateSubscriptions(ctx, ids, map[string]any{"is_disabled": value}); err != nil {
		return err
	}
	if err := s.SetSSHConfig(ctx); err != nil {
		s.logger.Warn("[subscription] apply ssh config failed", "error", err)
	}
	rows, err := s.store.ListSubscriptions(ctx, idClause(len(ids)), int64Args(ids), store.SubscriptionDefaultOrder)
	if err != nil {
		return err
	}
	for i := range rows {
		doc := toDoc(&rows[i])
		s.handleTask(doc, !disabled)
	}
	return nil
}

// SubscriptionStatusInput is the body of PUT /api/subscriptions/status.
type SubscriptionStatusInput struct {
	IDs               []int64
	Status            int64
	Pid               *int64
	LogPath           string
	LastRunningTime   int64
	LastExecutionTime int64
}

// SetStatus implements PUT /api/subscriptions/status.
func (s *SubscriptionService) SetStatus(ctx context.Context, input *SubscriptionStatusInput) (int64, error) {
	fields := map[string]any{
		"status":              input.Status,
		"log_path":            optionalString(input.LogPath),
		"last_execution_time": input.LastExecutionTime,
		"pid":                 input.Pid,
	}
	if input.LogPath != "" {
		if _, err := s.crons.resolveLogPath(input.LogPath); err != nil {
			return 0, err
		}
	}
	if input.LastRunningTime > 0 {
		fields["last_running_time"] = input.LastRunningTime
	}
	return s.store.UpdateSubscriptions(ctx, input.IDs, fields)
}

// ---------------------------------------------------------------------------
// logs
// ---------------------------------------------------------------------------

// SubscriptionLogResult is the payload of GET /api/subscriptions/:id/log.
type SubscriptionLogResult struct {
	Content    string
	Offset     int64
	NextOffset int64
	Total      int64
	Truncated  bool
}

// Log implements GET /api/subscriptions/:id/log.
func (s *SubscriptionService) Log(ctx context.Context, id int64, offset *int64, limit *int64, tail bool) (*SubscriptionLogResult, error) {
	row, err := s.store.SubscriptionByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, &NotFoundError{
			Status:  500,
			Message: fmt.Sprintf("Subscription {\"id\":%d} not found", id),
		}
	}
	if !row.LogPath.Valid || row.LogPath.String == "" {
		return &SubscriptionLogResult{}, nil
	}
	absolute, err := s.crons.resolveLogPath(row.LogPath.String)
	if err != nil {
		return nil, err
	}
	chunk, err := readLogChunk(absolute, offset, limit, tail)
	if err != nil {
		return nil, err
	}
	return &SubscriptionLogResult{
		Content:    chunk.Content,
		Offset:     chunk.Offset,
		NextOffset: chunk.NextOffset,
		Total:      chunk.Total,
		Truncated:  chunk.Truncated,
	}, nil
}

// Logs implements GET /api/subscriptions/:id/logs.
func (s *SubscriptionService) Logs(ctx context.Context, id int64) ([]LogFile, error) {
	row, err := s.store.SubscriptionByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, &NotFoundError{
			Status:  500,
			Message: fmt.Sprintf("Subscription {\"id\":%d} not found", id),
		}
	}
	if !row.LogPath.Valid || row.LogPath.String == "" {
		return nil, nil
	}
	relativeDir := filepath.Dir(row.LogPath.String)
	absolute, err := s.crons.resolveLogPath(row.LogPath.String)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(absolute)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	result := make([]LogFile, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		result = append(result, LogFile{
			Filename:  entry.Name(),
			Directory: strings.Replace(relativeDir, s.cfg.LogPath, "", 1),
			Time:      float64(fileBirthTime(info)),
		})
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].Time > result[j].Time })
	return result, nil
}

// ---------------------------------------------------------------------------
// scheduling
// ---------------------------------------------------------------------------

// subscriptionJob is one scheduled trigger: a cron expression or an interval.
type subscriptionJob struct {
	id       int64
	source   string
	stop     chan struct{}
	stopOnce sync.Once
}

func (j *subscriptionJob) shutdown() {
	j.stopOnce.Do(func() { close(j.stop) })
}

// ensureNotifyScripts seeds data/scripts/sendNotify.js and notify.py, which
// initFile.ts creates from sample/notify.*. Every repository copy carries both
// so `require('./sendNotify')` resolves inside a subscription script.
func (s *SubscriptionService) ensureNotifyScripts() {
	for _, item := range []struct{ source, target string }{
		{"__ql_notify__.js", "sendNotify.js"},
		{"__ql_notify__.py", "notify.py"},
	} {
		target := filepath.Join(s.cfg.ScriptPath, item.target)
		if info, err := os.Stat(target); err == nil && info.Size() > 0 {
			continue
		}
		content, err := preload.Asset(item.source)
		if err != nil {
			s.logger.Warn("[boot] read embedded notify script failed", "name", item.source, "error", err)
			continue
		}
		if err := os.MkdirAll(s.cfg.ScriptPath, 0o755); err != nil {
			s.logger.Warn("[boot] create scripts directory failed", "error", err)
			continue
		}
		if err := os.WriteFile(target, content, 0o644); err != nil {
			s.logger.Warn("[boot] write notify script failed", "path", target, "error", err)
		}
	}
}

// Bootstrap replays initTask.ts: apply the SSH configuration, then register
// every enabled subscription.
func (s *SubscriptionService) Bootstrap(ctx context.Context) error {
	s.ensureNotifyScripts()
	if err := s.SetSSHConfig(ctx); err != nil {
		s.logger.Warn("[boot] apply ssh config failed", "error", err)
	}
	rows, err := s.store.AllSubscriptions(ctx)
	if err != nil {
		return err
	}
	for i := range rows {
		doc := toDoc(&rows[i])
		if err := validateSubscriptionAlias(doc.Alias); err != nil {
			s.logger.Warn("[boot] skip invalid subscription", "id", doc.ID, "error", err)
			continue
		}
		s.handleTask(doc, !doc.IsDisabled)
	}
	return nil
}

// handleTask ports SubscriptionService.handleTask: drop the previous trigger
// and register the new one when needCreate is set.
func (s *SubscriptionService) handleTask(doc *subscriptionDoc, needCreate bool) {
	s.cancelJob(doc.ID)
	if !needCreate {
		return
	}
	switch doc.ScheduleType {
	case "crontab":
		s.registerCronJob(doc)
	default:
		if doc.IntervalType != "" && doc.IntervalValue > 0 {
			s.registerIntervalJob(doc)
		}
	}
}

func (s *SubscriptionService) cancelJob(id int64) {
	s.mu.Lock()
	job := s.jobs[id]
	delete(s.jobs, id)
	s.mu.Unlock()
	if job != nil {
		job.shutdown()
	}
}

func (s *SubscriptionService) registerCronJob(doc *subscriptionDoc) {
	if strings.TrimSpace(doc.Schedule) == "" {
		return
	}
	schedule, err := cronexpr.Parse(doc.Schedule)
	if err != nil {
		s.logger.Warn("[subscription] skip invalid schedule", "id", doc.ID, "schedule", doc.Schedule, "error", err)
		return
	}
	job := &subscriptionJob{id: doc.ID, source: doc.Schedule, stop: make(chan struct{})}
	s.mu.Lock()
	s.jobs[doc.ID] = job
	s.mu.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			next, ok := schedule.Next(time.Now())
			if !ok {
				return
			}
			wait := time.Until(next)
			if wait < 0 {
				wait = 0
			}
			timer := time.NewTimer(wait)
			select {
			case <-job.stop:
				timer.Stop()
				return
			case <-s.ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
				go s.runOne(doc.ID, false)
			}
		}
	}()
}

func (s *SubscriptionService) registerIntervalJob(doc *subscriptionDoc) {
	unit := time.Second
	switch doc.IntervalType {
	case "seconds":
		unit = time.Second
	case "minutes":
		unit = time.Minute
	case "hours":
		unit = time.Hour
	case "days":
		unit = 24 * time.Hour
	default:
		s.logger.Warn("[subscription] unknown interval unit", "id", doc.ID, "unit", doc.IntervalType)
		return
	}
	interval := unit * time.Duration(doc.IntervalValue)
	if interval <= 0 {
		return
	}
	job := &subscriptionJob{id: doc.ID, source: doc.IntervalType, stop: make(chan struct{})}
	s.mu.Lock()
	s.jobs[doc.ID] = job
	s.mu.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-job.stop:
				return
			case <-s.ctx.Done():
				return
			case <-ticker.C:
				go s.runOne(doc.ID, false)
			}
		}
	}()
}

// Shutdown stops every trigger and ends in-flight syncs.
func (s *SubscriptionService) Shutdown(timeout time.Duration) {
	s.mu.Lock()
	jobs := make([]*subscriptionJob, 0, len(s.jobs))
	for _, job := range s.jobs {
		jobs = append(jobs, job)
	}
	s.jobs = map[int64]*subscriptionJob{}
	s.mu.Unlock()
	for _, job := range jobs {
		job.shutdown()
	}
	s.cancel()

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
	}
}

// subscriptionRun tracks one in-flight sync so Stop can end it.
type subscriptionRun struct {
	cancel context.CancelFunc
	mu     sync.Mutex
	cmd    *execHandle
}

func (r *subscriptionRun) stop() {
	r.mu.Lock()
	handle := r.cmd
	r.mu.Unlock()
	if handle != nil {
		handle.kill()
	}
	r.cancel()
}

func (r *subscriptionRun) setCommand(handle *execHandle) {
	r.mu.Lock()
	r.cmd = handle
	r.mu.Unlock()
}

func (s *SubscriptionService) lookupRun(id int64) *subscriptionRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runs[id]
}

func (s *SubscriptionService) setRun(id int64, run *subscriptionRun) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run == nil {
		delete(s.runs, id)
		return
	}
	s.runs[id] = run
}

func (s *SubscriptionService) acquire() {
	select {
	case s.sem <- struct{}{}:
	case <-s.ctx.Done():
	}
}

func (s *SubscriptionService) release() {
	select {
	case <-s.sem:
	default:
	}
}

// runOne executes one subscription. requireQueued mirrors runSingle's
// `status !== queued` guard, which only manual runs have.
func (s *SubscriptionService) runOne(id int64, requireQueued bool) {
	ctx := context.Background()
	row, err := s.store.SubscriptionByID(ctx, id)
	if err != nil || row == nil {
		if err != nil {
			s.logger.Error("[subscription] load failed", "id", id, "error", err)
		}
		return
	}
	doc := toDoc(row)
	if requireQueued && doc.Status != SubStatusQueued {
		return
	}

	s.acquire()
	defer s.release()

	runCtx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	run := &subscriptionRun{cancel: cancel}
	s.setRun(id, run)
	defer s.setRun(id, nil)

	startedAt := time.Now()
	logRel := doc.Alias + "/" + startedAt.Format("2006-01-02-15-04-05") + ".log"
	if err := s.store.UpdateSubscription(ctx, id, map[string]any{
		"status":   SubStatusRunning,
		"log_path": logRel,
	}); err != nil {
		s.logger.Error("[subscription] mark running failed", "id", id, "error", err)
		return
	}

	logger, err := openSubscriptionLog(s.cfg.LogPath, logRel, startedAt)
	if err != nil {
		s.logger.Error("[subscription] open log failed", "id", id, "error", err)
		s.finish(ctx, id, nil)
		return
	}

	s.runBefore(runCtx, doc, logger)
	syncErr := s.syncSubscription(runCtx, doc, logger, run)
	s.runAfter(runCtx, doc, logger)

	if syncErr != nil {
		logger.Write(syncErr.Error())
	}
	logger.Close(time.Now(), startedAt)
	s.finish(ctx, id, nil)
}

// finish resets the row to idle and clears the pid, like the onEnd callback.
func (s *SubscriptionService) finish(ctx context.Context, id int64, pid *int64) {
	fields := map[string]any{"status": SubStatusIdle, "pid": pid}
	if err := s.store.UpdateSubscription(ctx, id, fields); err != nil {
		s.logger.Error("[subscription] reset status failed", "id", id, "error", err)
	}
	if s.sock != nil {
		s.sock.Send(SockMessage{
			Type:       "runSubscriptionEnd",
			Message:    i18n.T("订阅执行完成"),
			References: []int64{id},
		})
	}
}

// runBefore ports the onBefore callback: run sub_before and append its output.
func (s *SubscriptionService) runBefore(ctx context.Context, doc *subscriptionDoc, log *subscriptionLog) {
	if doc.SubBefore == "" {
		return
	}
	log.Write("\n## " + i18n.T("执行before命令...") + "\n\n")
	output, err := s.runShellCommand(ctx, doc.SubBefore)
	if err != nil && output == "" {
		output = err.Error()
	}
	if output != "" {
		log.Write(output + "\n")
	}
}

// runAfter ports the onEnd callback's sub_after step.
func (s *SubscriptionService) runAfter(ctx context.Context, doc *subscriptionDoc, log *subscriptionLog) {
	if doc.SubAfter == "" {
		return
	}
	log.Write("\n\n## " + i18n.T("执行after命令...") + "\n\n")
	output, err := s.runShellCommand(ctx, doc.SubAfter)
	if err != nil && output == "" {
		output = err.Error()
	}
	if output != "" {
		log.Write(output + "\n")
	}
}

// subscriptionLog appends to data/log/<alias>/<timestamp>.log. Every chunk is
// prefixed with a newline, exactly like the onLog/onError callbacks.
type subscriptionLog struct {
	mu   sync.Mutex
	file *os.File
}

func openSubscriptionLog(root, relative string, startedAt time.Time) (*subscriptionLog, error) {
	absolute, err := subscriptionLogPath(root, relative)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(absolute, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	log := &subscriptionLog{file: file}
	// handleLogPath only writes the header when the file is new.
	if info, statErr := file.Stat(); statErr == nil && info.Size() == 0 {
		header := i18n.Tf("## 开始执行... %s\n", startedAt.Format("2006-01-02 15:04:05"))
		if _, err := file.WriteString(header); err != nil {
			file.Close()
			return nil, err
		}
	}
	return log, nil
}

func subscriptionLogPath(root, relative string) (string, error) {
	resolved := resolveFileAccess(root, []string{relative}, nil)
	if resolved == "" {
		return "", errors.New("Log path is outside the log directory")
	}
	return resolved, nil
}

// Write appends one chunk with the leading newline Node adds.
func (l *subscriptionLog) Write(chunk string) {
	if l == nil || l.file == nil || chunk == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.file.WriteString("\n" + chunk)
}

// Close writes the footer and releases the handle.
func (l *subscriptionLog) Close(endedAt, startedAt time.Time) {
	if l == nil || l.file == nil {
		return
	}
	elapsed := int64(endedAt.Sub(startedAt) / time.Second)
	footer := "\n" + i18n.Tf("## 执行结束... %s  耗时 %s 秒",
		endedAt.Format("2006-01-02 15:04:05"), strconv.FormatInt(elapsed, 10)) + LogEndSymbol
	l.mu.Lock()
	_, _ = l.file.WriteString(footer)
	l.mu.Unlock()
	_ = l.file.Close()
}
