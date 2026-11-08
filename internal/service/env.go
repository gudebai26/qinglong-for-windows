package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"qinglong-go/internal/config"
	"qinglong-go/internal/i18n"
	"qinglong-go/internal/store"
)

// Position bookkeeping of back/data/env.ts. New entries are appended with a
// smaller position (the list is sorted by position DESC), leaving room to move
// a row between two neighbours without renumbering.
const (
	maxEnvPosition  = 9000000000000000.0
	initEnvPosition = 4500000000000000.0
	stepEnvPosition = 10000000000.0
	minEnvPosition  = 100.0
)

// EnvStatus values of back/data/env.ts.
const (
	envStatusNormal   = 0
	envStatusDisabled = 1
)

// EnvResponse is one row of GET /api/envs. The key order matches Sequelize's
// attribute order so the JSON is byte-identical to the Node panel (the export
// button just stringifies this array).
type EnvResponse struct {
	ID        int64    `json:"id"`
	Value     *string  `json:"value"`
	Timestamp *string  `json:"timestamp"`
	Status    *int64   `json:"status"`
	Position  *float64 `json:"position"`
	Name      *string  `json:"name"`
	Remarks   *string  `json:"remarks"`
	IsPinned  *int64   `json:"isPinned"`
	Labels    any      `json:"labels"`
	CreatedAt *string  `json:"createdAt"`
	UpdatedAt *string  `json:"updatedAt"`
	// SecurityBlocked marks reserved names; only the list endpoint adds it.
	SecurityBlocked *bool `json:"securityBlocked,omitempty"`
}

// EnvInput is one validated element of POST /api/envs.
type EnvInput struct {
	Value     string
	Name      string
	Remarks   *string
	Labels    []string
	LabelsSet bool
}

// EnvUpdateInput is the validated PUT /api/envs body. RemarksSet separates an
// omitted field (keep the stored value) from an explicit null/"" (clear it).
type EnvUpdateInput struct {
	ID         int64
	Value      string
	Name       string
	Remarks    *string
	RemarksSet bool
	Labels     []string
	LabelsSet  bool
}

// EnvService owns the Envs table and the three generated preload files
// (env.sh, env.js, env.py) that tasks source at startup.
type EnvService struct {
	cfg    *config.Config
	store  *store.Store
	logger *slog.Logger
}

// NewEnvService builds the environment-variable service.
func NewEnvService(cfg *config.Config, st *store.Store, logger *slog.Logger) *EnvService {
	if logger == nil {
		logger = slog.Default()
	}
	return &EnvService{cfg: cfg, store: st, logger: logger}
}

// PreloadPath is the directory share.sh calls $dir_preload.
func (s *EnvService) PreloadPath() string {
	return filepath.Join(s.cfg.RootPath, "shell", "preload")
}

func toEnvResponse(item store.Env, withSecurity bool) EnvResponse {
	response := EnvResponse{
		ID:        item.ID,
		Value:     nullableString(item.Value),
		Timestamp: nullableString(item.Timestamp),
		Status:    nullableInt(item.Status),
		Position:  nullableFloat(item.Position),
		Name:      nullableString(item.Name),
		Remarks:   nullableString(item.Remarks),
		IsPinned:  nullableInt(item.IsPinned),
		Labels:    store.EnvLabels(item.Labels),
		CreatedAt: store.ISO8601(item.CreatedAt),
		UpdatedAt: store.ISO8601(item.UpdatedAt),
	}
	if withSecurity {
		blocked := item.Name.Valid && !IsAllowedTaskEnvName(item.Name.String)
		response.SecurityBlocked = &blocked
	}
	return response
}

func nullableFloat(value sql.NullFloat64) *float64 {
	if !value.Valid {
		return nil
	}
	result := value.Float64
	return &result
}

func nullFloat(value float64) sql.NullFloat64 {
	return sql.NullFloat64{Float64: value, Valid: true}
}

// List returns every environment variable in panel order.
func (s *EnvService) List(ctx context.Context, searchValue string) ([]EnvResponse, error) {
	patterns := make([]string, 0, 2)
	if searchValue != "" {
		patterns = append(patterns, "%"+searchValue+"%", "%"+encodeURI(searchValue)+"%")
	}
	items, err := s.store.ListEnvs(ctx, store.EnvFilter{Patterns: patterns})
	if err != nil {
		return nil, err
	}
	result := make([]EnvResponse, 0, len(items))
	for _, item := range items {
		result = append(result, toEnvResponse(item, true))
	}
	return result, nil
}

// Get returns one row; a missing id is an error, like EnvService.getDb.
func (s *EnvService) Get(ctx context.Context, id int64) (*EnvResponse, error) {
	item, err := s.store.EnvByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if item == nil {
		return nil, envNotFound(id)
	}
	response := toEnvResponse(*item, false)
	return &response, nil
}

func envNotFound(id int64) error {
	return &StatusError{Status: 500, Message: fmt.Sprintf("Env {\"id\":%d} not found", id)}
}

// Create inserts the payloads in order, each with a lower position.
func (s *EnvService) Create(ctx context.Context, payloads []EnvInput) ([]EnvResponse, error) {
	for _, payload := range payloads {
		if !IsAllowedTaskEnvName(payload.Name) {
			return nil, &StatusError{Status: 400, Message: i18n.T("环境变量名称不可使用")}
		}
	}
	existing, err := s.store.ListEnvs(ctx, store.EnvFilter{})
	if err != nil {
		return nil, err
	}
	position := initEnvPosition
	if len(existing) > 0 {
		if last := existing[len(existing)-1]; last.Position.Valid {
			position = precisionPosition(last.Position.Float64)
		}
	}

	tabs := make([]store.Env, 0, len(payloads))
	for _, payload := range payloads {
		position = position - stepEnvPosition
		tabs = append(tabs, newEnvRow(payload, position))
	}

	docs := make([]EnvResponse, 0, len(tabs))
	for i := range tabs {
		if err := s.store.InsertEnv(ctx, &tabs[i]); err != nil {
			if isUniqueViolation(err) {
				return nil, &StatusError{Status: 409, Message: i18n.T("资源已存在，请检查重复的名称或值")}
			}
			return nil, err
		}
		created, err := s.store.EnvByID(ctx, tabs[i].ID)
		if err != nil {
			return nil, err
		}
		if created == nil {
			continue
		}
		docs = append(docs, toEnvResponse(*created, false))
	}
	if err := s.setEnvs(ctx); err != nil {
		return nil, err
	}
	if len(tabs) > 0 {
		if err := s.checkPosition(ctx, position, 0); err != nil {
			return nil, err
		}
	}
	return docs, nil
}

// newEnvRow mirrors `new Env({...payload, position})`: a fresh timestamp, the
// normal status and an empty label list.
func newEnvRow(payload EnvInput, position float64) store.Env {
	now := time.Now()
	remarks := ""
	if payload.Remarks != nil {
		remarks = *payload.Remarks
	}
	labels := payload.Labels
	if len(labels) == 0 {
		labels = []string{}
	}
	return store.Env{
		Value:     nullString(payload.Value),
		Timestamp: nullString(jsDateString(now)),
		Status:    nullInt(envStatusNormal),
		Position:  nullFloat(position),
		Name:      nullString(payload.Name),
		Remarks:   nullString(remarks),
		IsPinned:  nullInt(0),
		Labels:    nullString(jsonNoEscape(labels)),
		CreatedAt: store.SequelizeTime(now),
		UpdatedAt: store.SequelizeTime(now),
	}
}

// Update rewrites every field of one row, refreshing timestamp/updatedAt.
func (s *EnvService) Update(ctx context.Context, input EnvUpdateInput) (*EnvResponse, error) {
	doc, err := s.store.EnvByID(ctx, input.ID)
	if err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, envNotFound(input.ID)
	}
	if !IsAllowedTaskEnvName(input.Name) {
		return nil, &StatusError{Status: 400, Message: i18n.T("环境变量名称不可使用")}
	}

	remarks := doc.Remarks
	if input.RemarksSet {
		remarks = nullString(deref(input.Remarks))
	}
	labels := doc.Labels
	if input.LabelsSet {
		list := input.Labels
		if len(list) == 0 {
			list = []string{}
		}
		labels = nullString(jsonNoEscape(list))
	}

	fields := map[string]any{
		"value":     nullString(input.Value),
		"timestamp": nullString(jsDateString(time.Now())),
		"status":    doc.Status,
		"position":  doc.Position,
		"name":      nullString(input.Name),
		"remarks":   remarks,
		"isPinned":  doc.IsPinned,
		"labels":    labels,
	}
	if err := s.store.UpdateEnv(ctx, input.ID, fields); err != nil {
		if isUniqueViolation(err) {
			return nil, &StatusError{Status: 409, Message: i18n.T("资源已存在，请检查重复的名称或值")}
		}
		return nil, err
	}
	if err := s.setEnvs(ctx); err != nil {
		return nil, err
	}
	return s.Get(ctx, input.ID)
}

// Remove deletes rows and rewrites the task environment.
func (s *EnvService) Remove(ctx context.Context, ids []int64) error {
	if err := s.store.DeleteEnvs(ctx, ids); err != nil {
		return err
	}
	return s.setEnvs(ctx)
}

// Move reorders one row between two neighbours.
func (s *EnvService) Move(ctx context.Context, id int64, fromIndex, toIndex int) (*EnvResponse, error) {
	envs, err := s.List(ctx, "")
	if err != nil {
		return nil, err
	}
	if len(envs) == 0 || toIndex < 0 || toIndex >= len(envs) {
		return nil, envNotFound(id)
	}
	isUpward := fromIndex > toIndex
	positionOf := func(index int) float64 {
		if envs[index].Position == nil {
			return 0
		}
		return *envs[index].Position
	}
	var target float64
	switch {
	case toIndex == 0 || toIndex == len(envs)-1:
		if isUpward {
			target = positionOf(0) + stepEnvPosition
		} else {
			target = positionOf(toIndex) - stepEnvPosition
		}
	case isUpward:
		target = (positionOf(toIndex) + positionOf(toIndex-1)) / 2
	default:
		target = (positionOf(toIndex) + positionOf(toIndex+1)) / 2
	}

	doc, err := s.store.EnvByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, envNotFound(id)
	}
	if doc.Name.Valid && !IsAllowedTaskEnvName(doc.Name.String) {
		return nil, &StatusError{Status: 400, Message: i18n.T("环境变量名称不可使用")}
	}
	// update() rewrites the whole row: the Env constructor refreshes timestamp.
	fields := map[string]any{
		"value":     doc.Value,
		"timestamp": nullString(jsDateString(time.Now())),
		"status":    doc.Status,
		"position":  nullFloat(precisionPosition(target)),
		"name":      doc.Name,
		"remarks":   doc.Remarks,
		"isPinned":  doc.IsPinned,
		"labels":    doc.Labels,
	}
	if err := s.store.UpdateEnv(ctx, id, fields); err != nil {
		return nil, err
	}
	if err := s.setEnvs(ctx); err != nil {
		return nil, err
	}
	if err := s.checkPosition(ctx, target, positionOf(toIndex)); err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

// checkPosition renumbers every row when the positions get too close together
// or leave the allowed range.
func (s *EnvService) checkPosition(ctx context.Context, position, edge float64) error {
	precision := precisionPosition(position)
	if precision < minEnvPosition || precision > maxEnvPosition || math.Abs(precision-edge) < minEnvPosition {
		envs, err := s.store.ListEnvs(ctx, store.EnvFilter{})
		if err != nil {
			return err
		}
		next := initEnvPosition
		for _, env := range envs {
			next = next - stepEnvPosition
			if err := s.store.UpdateEnv(ctx, env.ID, map[string]any{"position": nullFloat(next)}); err != nil {
				return err
			}
		}
	}
	return nil
}

// SetDisabled toggles the status of the given rows.
func (s *EnvService) SetDisabled(ctx context.Context, ids []int64, disabled bool) error {
	if !disabled {
		docs, err := s.store.EnvsByIDs(ctx, ids)
		if err != nil {
			return err
		}
		for _, doc := range docs {
			if doc.Name.Valid && !IsAllowedTaskEnvName(doc.Name.String) {
				return &StatusError{Status: 400, Message: i18n.T("环境变量名称不可使用")}
			}
		}
	}
	status := int64(envStatusNormal)
	if disabled {
		status = envStatusDisabled
	}
	if err := s.store.UpdateEnvs(ctx, ids, map[string]any{"status": status}); err != nil {
		return err
	}
	return s.setEnvs(ctx)
}

// UpdateNames renames a batch of rows.
func (s *EnvService) UpdateNames(ctx context.Context, ids []int64, name string) error {
	if !IsAllowedTaskEnvName(name) {
		return &StatusError{Status: 400, Message: i18n.T("环境变量名称不可使用")}
	}
	if err := s.store.UpdateEnvs(ctx, ids, map[string]any{"name": name}); err != nil {
		if isUniqueViolation(err) {
			return &StatusError{Status: 409, Message: i18n.T("资源已存在，请检查重复的名称或值")}
		}
		return err
	}
	return s.setEnvs(ctx)
}

// SetPinned pins or unpins rows. Node does not regenerate env.sh here: pinning
// only changes the display order.
func (s *EnvService) SetPinned(ctx context.Context, ids []int64, pinned bool) error {
	value := int64(0)
	if pinned {
		value = 1
	}
	return s.store.UpdateEnvs(ctx, ids, map[string]any{"isPinned": value})
}

// AddLabels merges labels into each row and returns the updated rows.
func (s *EnvService) AddLabels(ctx context.Context, ids []int64, labels []string) ([]EnvResponse, error) {
	docs, err := s.store.EnvsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, doc := range docs {
		merged := mergeLabels(store.EnvLabels(doc.Labels), labels)
		if err := s.store.UpdateEnv(ctx, doc.ID, map[string]any{"labels": nullString(jsonNoEscape(merged))}); err != nil {
			return nil, err
		}
	}
	return s.envsByIDs(ctx, ids)
}

// RemoveLabels drops labels from each row and returns the updated rows.
func (s *EnvService) RemoveLabels(ctx context.Context, ids []int64, labels []string) ([]EnvResponse, error) {
	docs, err := s.store.EnvsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	drop := make(map[string]struct{}, len(labels))
	for _, label := range labels {
		drop[label] = struct{}{}
	}
	for _, doc := range docs {
		remaining := make([]string, 0, 4)
		for _, label := range labelList(store.EnvLabels(doc.Labels)) {
			if _, blocked := drop[label]; blocked {
				continue
			}
			remaining = append(remaining, label)
		}
		if err := s.store.UpdateEnv(ctx, doc.ID, map[string]any{"labels": nullString(jsonNoEscape(remaining))}); err != nil {
			return nil, err
		}
	}
	return s.envsByIDs(ctx, ids)
}

func (s *EnvService) envsByIDs(ctx context.Context, ids []int64) ([]EnvResponse, error) {
	docs, err := s.store.EnvsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	result := make([]EnvResponse, 0, len(docs))
	for _, doc := range docs {
		result = append(result, toEnvResponse(doc, false))
	}
	return result, nil
}

// mergeLabels keeps the existing order and appends new labels, deduplicated.
func mergeLabels(current any, labels []string) []string {
	merged := make([]string, 0, len(labels)+4)
	seen := map[string]struct{}{}
	for _, label := range append(labelList(current), labels...) {
		if _, ok := seen[label]; ok {
			continue
		}
		seen[label] = struct{}{}
		merged = append(merged, label)
	}
	return merged
}

// labelList normalizes a decoded JSON label column into a string slice.
func labelList(value any) []string {
	switch typed := value.(type) {
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				result = append(result, text)
			}
		}
		return result
	case []string:
		return typed
	default:
		return []string{}
	}
}

// Bootstrap regenerates the three task environment files from the database,
// exactly like initData.ts:226 does before recovered tasks can run. The rows are
// the source of truth, so an upgraded data/ directory must not keep whatever
// env.sh/env.js/env.py the previous version happened to write.
func (s *EnvService) Bootstrap(ctx context.Context) error {
	return s.setEnvs(ctx)
}

// setEnvs regenerates shell/preload/env.sh, env.js and env.py from the enabled
// rows. The output is byte-identical to EnvService.set_envs().
func (s *EnvService) setEnvs(ctx context.Context) error {
	envs, err := s.store.ListEnvs(ctx, store.EnvFilter{OnlyEnabled: true})
	if err != nil {
		return err
	}

	type group struct {
		name   string
		values []string
	}
	groups := make([]*group, 0, len(envs))
	index := map[string]*group{}
	invalid := make([]map[string]any, 0)
	for _, env := range envs {
		if !env.Name.Valid {
			continue
		}
		name := env.Name.String
		if !IsAllowedTaskEnvName(name) {
			invalid = append(invalid, map[string]any{"id": env.ID, "name": name})
			continue
		}
		value := ""
		if env.Value.Valid {
			value = env.Value.String
		}
		if existing, ok := index[name]; ok {
			existing.values = append(existing.values, value)
			continue
		}
		created := &group{name: name, values: []string{value}}
		index[name] = created
		groups = append(groups, created)
	}
	if len(invalid) > 0 {
		if encoded, err := json.Marshal(invalid); err == nil {
			s.logger.Warn("Skipped reserved environment variables in task files", "envs", string(encoded))
		}
	}

	var shell strings.Builder
	var js strings.Builder
	var py strings.Builder
	py.WriteString("import os\n")
	for _, item := range groups {
		joined := strings.Join(item.values, "&")

		escaped := strings.ReplaceAll(joined, "'", `'\''`)
		shell.WriteString("export " + item.name + "='" + strings.TrimSpace(escaped) + "'\n")

		js.WriteString("process.env[" + jsonString(item.name) + "]=" + jsonString(joined) + ";\n")

		python := strings.ReplaceAll(joined, `\`, `\\`)
		python = strings.ReplaceAll(python, "'", `\'`)
		py.WriteString("os.environ['" + item.name + "']='''" + python + "'''\n")
	}

	preload := s.PreloadPath()
	if err := os.MkdirAll(preload, 0o755); err != nil {
		return err
	}
	if err := writeFileWithLock(filepath.Join(preload, "env.sh"), shell.String()); err != nil {
		return err
	}
	if err := writeFileWithLock(filepath.Join(preload, "env.js"), js.String()); err != nil {
		return err
	}
	return writeFileWithLock(filepath.Join(preload, "env.py"), py.String())
}

// precisionPosition ports getPrecisionPosition(): toPrecision(16) then trunc.
func precisionPosition(value float64) float64 {
	parsed, err := strconv.ParseFloat(strconv.FormatFloat(value, 'g', 16, 64), 64)
	if err != nil {
		return math.Trunc(value)
	}
	return math.Trunc(parsed)
}

// jsonString renders a JSON string literal the way JSON.stringify does: no
// HTML escaping, UTF-8 preserved.
func jsonString(value string) string {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return `""`
	}
	return strings.TrimSuffix(buffer.String(), "\n")
}

func jsonNoEscape(value any) string {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return "null"
	}
	return strings.TrimSuffix(buffer.String(), "\n")
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// isUniqueViolation reports the SQLite UNIQUE constraint Sequelize maps to a
// 409 response.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "UNIQUE constraint failed") ||
		strings.Contains(message, "constraint failed: UNIQUE")
}

// StatusError is a business failure with an explicit HTTP status.
type StatusError struct {
	Status  int
	Message string
}

func (e *StatusError) Error() string { return e.Message }
