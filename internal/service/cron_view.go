package service

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"qinglong-go/internal/store"
)

// Position constants from back/data/env.ts.
const (
	maxPosition  = 9000000000000000.0
	initPosition = 4500000000000000.0
	stepPosition = 10000000000.0
	minPosition  = 100.0
)

// CronViewResponse is the plain-object shape of CrontabViews rows.
type CronViewResponse struct {
	ID             int64    `json:"id"`
	Name           *string  `json:"name"`
	Position       *float64 `json:"position"`
	IsDisabled     *int64   `json:"isDisabled"`
	Filters        any      `json:"filters"`
	Sorts          any      `json:"sorts"`
	FilterRelation *string  `json:"filterRelation"`
	Type           *int64   `json:"type"`
	CreatedAt      *string  `json:"createdAt"`
	UpdatedAt      *string  `json:"updatedAt"`
}

// CronViewInput is the validated body of /api/crons/views.
type CronViewInput struct {
	ID             int64
	Name           string
	Sorts          []any
	SortsSet       bool
	Filters        []any
	FiltersSet     bool
	FilterRelation *string
	RelationSet    bool
}

// ToCronViewResponse converts a row into the API shape.
func ToCronViewResponse(v *store.CrontabView) *CronViewResponse {
	if v == nil {
		return nil
	}
	var position *float64
	if v.Position.Valid {
		value := v.Position.Float64
		position = &value
	}
	return &CronViewResponse{
		ID:             v.ID,
		Name:           nullableString(v.Name),
		Position:       position,
		IsDisabled:     nullableInt(v.IsDisabled),
		Filters:        store.CrontabLabels(v.Filters),
		Sorts:          store.CrontabLabels(v.Sorts),
		FilterRelation: nullableString(v.FilterRelation),
		Type:           nullableInt(v.Type),
		CreatedAt:      store.ISO8601(v.CreatedAt),
		UpdatedAt:      store.ISO8601(v.UpdatedAt),
	}
}

// ParseCronViewInput validates the create/update body.
func (s *CronService) ParseCronViewInput(body map[string]any, requireID bool) (*CronViewInput, error) {
	input := &CronViewInput{}
	var keys []string
	var messages []string
	addError := func(key, message string) {
		keys = append(keys, key)
		messages = append(messages, message)
	}

	allowed := map[string]struct{}{
		"name": {}, "sorts": {}, "filters": {}, "filterRelation": {},
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

	nameValue, ok := body["name"]
	switch {
	case !ok || nameValue == nil:
		addError("name", `"name" is required`)
	default:
		name, ok := nameValue.(string)
		if !ok {
			addError("name", `"name" must be a string`)
		} else if name == "" {
			addError("name", `"name" is not allowed to be empty`)
		} else {
			input.Name = name
		}
	}

	if value, exists := body["sorts"]; exists {
		input.SortsSet = true
		if value != nil {
			list, ok := value.([]any)
			if !ok {
				addError("sorts", `"sorts" must be an array`)
			} else {
				input.Sorts = list
			}
		}
	}
	if value, exists := body["filters"]; exists && value != nil {
		input.FiltersSet = true
		list, ok := value.([]any)
		if !ok {
			addError("filters", `"filters" must be an array`)
		} else {
			input.Filters = list
		}
	}
	if value, exists := body["filterRelation"]; exists && value != nil {
		input.RelationSet = true
		text, ok := value.(string)
		if !ok {
			addError("filterRelation", `"filterRelation" must be a string`)
		} else {
			input.FilterRelation = &text
		}
	}

	if len(keys) > 0 {
		return nil, &ValidationError{Keys: keys, Message: strings.Join(messages, ". ")}
	}
	return input, nil
}

// Bootstrap reproduces the task-related parts of back/loaders/initData.ts:
// ensure the system "全部任务" view, reset every task to idle and mark orphaned
// running instances as stopped. Node does this on every boot.
func (s *CronService) Bootstrap(ctx context.Context) error {
	views, err := s.store.ListCrontabViews(ctx)
	if err != nil {
		return err
	}
	found := false
	for _, view := range views {
		if view.Type.Valid && view.Type.Int64 == 1 &&
			view.Name.Valid && view.Name.String == "全部任务" {
			found = true
			break
		}
	}
	if !found {
		now := time.Now()
		row := &store.CrontabView{
			Name:      nullString("全部任务"),
			Position:  sql.NullFloat64{Float64: initPosition / 2, Valid: true},
			Type:      nullInt(1),
			CreatedAt: store.SequelizeTime(now),
			UpdatedAt: store.SequelizeTime(now),
		}
		if err := s.store.InsertCrontabView(ctx, row); err != nil {
			return err
		}
	}

	if err := s.store.UpdateAllCrontabs(ctx, map[string]any{"status": CronStatusIdle}); err != nil {
		return err
	}
	_, err = s.store.DB().ExecContext(ctx,
		`UPDATE RunningInstances SET status = ?, finished_at = ?, updatedAt = ? WHERE status = ?`,
		InstanceStopped, time.Now().Unix(), store.SequelizeTime(time.Now()), InstanceRunning)
	return err
}

// ListCronViews implements GET /api/crons/views.
func (s *CronService) ListCronViews(ctx context.Context) ([]CronViewResponse, error) {
	rows, err := s.store.ListCrontabViews(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]CronViewResponse, 0, len(rows))
	for i := range rows {
		result = append(result, *ToCronViewResponse(&rows[i]))
	}
	return result, nil
}

// CreateCronView implements POST /api/crons/views.
func (s *CronService) CreateCronView(ctx context.Context, input *CronViewInput) (*CronViewResponse, error) {
	position := initPosition
	views, err := s.store.ListCrontabViews(ctx)
	if err != nil {
		return nil, err
	}
	if len(views) > 0 && views[len(views)-1].Position.Valid && views[len(views)-1].Position.Float64 != 0 {
		position = views[len(views)-1].Position.Float64
	}
	position /= 2

	now := time.Now()
	row := &store.CrontabView{
		Name:       nullString(input.Name),
		Position:   sql.NullFloat64{Float64: position, Valid: true},
		IsDisabled: nullInt(0),
		Type:       nullInt(2),
		CreatedAt:  store.SequelizeTime(now),
		UpdatedAt:  store.SequelizeTime(now),
	}
	if input.SortsSet && input.Sorts != nil {
		row.Sorts = nullString(marshalJSON(input.Sorts))
	}
	if input.FiltersSet && input.Filters != nil {
		row.Filters = nullString(marshalJSON(input.Filters))
	}
	if input.RelationSet && input.FilterRelation != nil {
		row.FilterRelation = nullString(*input.FilterRelation)
	}
	if err := s.store.InsertCrontabView(ctx, row); err != nil {
		if isUniqueConstraint(err) {
			return nil, &ConflictError{Message: "资源已存在，请检查重复的名称或值"}
		}
		return nil, err
	}
	if err := s.checkViewPosition(ctx, position); err != nil {
		return nil, err
	}
	return ToCronViewResponse(row), nil
}

// UpdateCronView implements PUT /api/crons/views.
func (s *CronService) UpdateCronView(ctx context.Context, input *CronViewInput) (*CronViewResponse, error) {
	doc, err := s.store.CrontabViewByID(ctx, input.ID)
	if err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, &NotFoundError{
			Status:  500,
			Message: fmt.Sprintf("CronView {\"id\":%d} not found", input.ID),
		}
	}

	merged := *doc
	merged.Name = nullString(input.Name)
	if input.SortsSet {
		if input.Sorts == nil {
			merged.Sorts = sql.NullString{}
		} else {
			merged.Sorts = nullString(marshalJSON(input.Sorts))
		}
	}
	if input.FiltersSet {
		if input.Filters == nil {
			merged.Filters = sql.NullString{}
		} else {
			merged.Filters = nullString(marshalJSON(input.Filters))
		}
	}
	if input.RelationSet {
		if input.FilterRelation == nil {
			merged.FilterRelation = sql.NullString{}
		} else {
			merged.FilterRelation = nullString(*input.FilterRelation)
		}
	}
	if !merged.IsDisabled.Valid {
		merged.IsDisabled = nullInt(0)
	}
	if !merged.Type.Valid {
		merged.Type = nullInt(2)
	}

	fields := map[string]any{
		"name":           merged.Name,
		"position":       merged.Position,
		"isDisabled":     merged.IsDisabled,
		"filters":        merged.Filters,
		"sorts":          merged.Sorts,
		"filterRelation": merged.FilterRelation,
		"type":           merged.Type,
	}
	if err := s.store.UpdateCrontabView(ctx, input.ID, fields); err != nil {
		if isUniqueConstraint(err) {
			return nil, &ConflictError{Message: "资源已存在，请检查重复的名称或值"}
		}
		return nil, err
	}
	updated, err := s.store.CrontabViewByID(ctx, input.ID)
	if err != nil {
		return nil, err
	}
	return ToCronViewResponse(updated), nil
}

// RemoveCronViews implements DELETE /api/crons/views.
func (s *CronService) RemoveCronViews(ctx context.Context, ids []int64) error {
	return s.store.DeleteCrontabViews(ctx, ids)
}

// SetCronViewsDisabled implements PUT /api/crons/views/{disable,enable}.
func (s *CronService) SetCronViewsDisabled(ctx context.Context, ids []int64, disabled bool) error {
	value := 0
	if disabled {
		value = 1
	}
	return s.store.UpdateCrontabViewsDisabled(ctx, ids, value)
}

// MoveCronView implements PUT /api/crons/views/move.
func (s *CronService) MoveCronView(ctx context.Context, id int64, fromIndex, toIndex int) (*CronViewResponse, error) {
	views, err := s.store.ListCrontabViews(ctx)
	if err != nil {
		return nil, err
	}
	if len(views) == 0 {
		return nil, &NotFoundError{Status: 500, Message: "CronView list is empty"}
	}
	if fromIndex < 0 || fromIndex >= len(views) || toIndex < 0 || toIndex >= len(views) {
		return nil, &NotFoundError{Status: 500, Message: "CronView index out of range"}
	}

	isUpward := fromIndex > toIndex
	var target float64
	switch {
	case toIndex == 0 || toIndex == len(views)-1:
		if isUpward {
			target = float64Value(views[0].Position) * 2
		} else {
			target = float64Value(views[toIndex].Position) / 2
		}
	case isUpward:
		target = (float64Value(views[toIndex].Position) + float64Value(views[toIndex-1].Position)) / 2
	default:
		target = (float64Value(views[toIndex].Position) + float64Value(views[toIndex+1].Position)) / 2
	}

	doc, err := s.store.CrontabViewByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, &NotFoundError{Status: 500, Message: fmt.Sprintf("CronView {\"id\":%d} not found", id)}
	}
	precision := precision16(target)
	if err := s.store.UpdateCrontabView(ctx, id, map[string]any{
		"position": sql.NullFloat64{Float64: precision, Valid: true},
	}); err != nil {
		return nil, err
	}
	if err := s.checkViewPosition(ctx, target); err != nil {
		return nil, err
	}
	updated, err := s.store.CrontabViewByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return ToCronViewResponse(updated), nil
}

func (s *CronService) checkViewPosition(ctx context.Context, position float64) error {
	precision := precision16(position)
	if precision >= minPosition && precision <= maxPosition {
		return nil
	}
	views, err := s.store.ListCrontabViews(ctx)
	if err != nil {
		return err
	}
	current := initPosition
	for i := range views {
		current -= stepPosition
		if err := s.store.UpdateCrontabView(ctx, views[i].ID, map[string]any{
			"position": sql.NullFloat64{Float64: current, Valid: true},
		}); err != nil {
			return err
		}
	}
	return nil
}

func precision16(value float64) float64 {
	parsed, err := strconv.ParseFloat(strconv.FormatFloat(value, 'g', 16, 64), 64)
	if err != nil {
		return value
	}
	return parsed
}

func float64Value(value sql.NullFloat64) float64 {
	if !value.Valid {
		return 0
	}
	return value.Float64
}
