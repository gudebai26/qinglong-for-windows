package service

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"qinglong-go/internal/scheduler"
	"qinglong-go/internal/store"
)

// Timezone resolves the panel timezone. The scheduler computes trigger points
// in this zone instead of the machine's, which is what "时区跟随面板配置"
// means in the milestone plan. systemConfig.info.timezone defaults to
// Asia/Shanghai (back/services/system.ts:632).
func (s *CronService) Timezone(ctx context.Context) *time.Location {
	name := scheduler.DefaultLocation
	if value, ok := s.systemInfo(ctx)["timezone"].(string); ok && strings.TrimSpace(value) != "" {
		name = strings.TrimSpace(value)
	}
	location, err := time.LoadLocation(name)
	if err == nil {
		return location
	}
	s.logger.Warn("[schedule] unknown timezone, falling back",
		"timezone", name, "fallback", scheduler.DefaultLocation, "error", err)
	if fallback, fallbackErr := time.LoadLocation(scheduler.DefaultLocation); fallbackErr == nil {
		return fallback
	}
	return time.Local
}

// CronConcurrency returns systemConfig.info.cronConcurrency, or 0 when the
// panel did not configure it. Zero makes the scheduler use max(NumCPU, 4).
func (s *CronService) CronConcurrency(ctx context.Context) int {
	switch value := s.systemInfo(ctx)["cronConcurrency"].(type) {
	case float64:
		if value > 0 {
			return int(value)
		}
	case int:
		if value > 0 {
			return value
		}
	case int64:
		if value > 0 {
			return int(value)
		}
	}
	return 0
}

func (s *CronService) systemInfo(ctx context.Context) map[string]any {
	row, err := s.store.AuthRowByType(ctx, store.AuthTypeSystemConfig)
	if err != nil || row == nil {
		return map[string]any{}
	}
	if len(row.Info) == 0 || strings.TrimSpace(string(row.Info)) == "null" {
		return map[string]any{}
	}
	info := map[string]any{}
	if err := json.Unmarshal(row.Info, &info); err != nil {
		return map[string]any{}
	}
	return info
}
