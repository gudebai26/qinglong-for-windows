package service

import (
	"context"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"time"
)

// MaxRetentionDays caps both retention windows (back/shared/retention.ts).
const MaxRetentionDays = 3650

// RetentionPolicy is the pair of windows the storage page edits.
type RetentionPolicy struct {
	RunningInstanceRetentionDays int64 `json:"runningInstanceRetentionDays"`
	CronStatRetentionDays        int64 `json:"cronStatRetentionDays"`
}

// StorageCleanupRequest is the body of preview/cleanup.
type StorageCleanupRequest struct {
	RetentionPolicy
	DependenceCacheTypes []string `json:"dependenceCacheTypes"`
	CompactDatabase      bool     `json:"compactDatabase"`
}

// DependenceCache describes one runtime cache directory.
type DependenceCache struct {
	Type  string `json:"type"`
	Bytes int64  `json:"bytes"`
}

// RetentionPreview is the response of POST /storage-retention/preview.
type RetentionPreview struct {
	Policy               RetentionPolicy   `json:"policy"`
	RunningInstances     int64             `json:"runningInstances"`
	CronStats            int64             `json:"cronStats"`
	DependenceCaches     []DependenceCache `json:"dependenceCaches"`
	DependenceCacheBytes int64             `json:"dependenceCacheBytes"`
	CompactDatabase      bool              `json:"compactDatabase"`
}

// RetentionDeleted is the cleanup result.
type RetentionDeleted struct {
	RunningInstances     int64             `json:"runningInstances"`
	CronStats            int64             `json:"cronStats"`
	DependenceCaches     []DependenceCache `json:"dependenceCaches"`
	DependenceCacheBytes int64             `json:"dependenceCacheBytes"`
}

// RetentionCleanupResult is the response of POST /storage-retention/cleanup.
type RetentionCleanupResult struct {
	Deleted           RetentionDeleted `json:"deleted"`
	CompactedDatabase bool             `json:"compactedDatabase"`
}

// NormalizeRetentionDays mirrors normalizeRetentionDays: truncate, clamp.
func NormalizeRetentionDays(value any) int64 {
	number, ok := floatValue(value)
	if !ok || math.IsNaN(number) || math.IsInf(number, 0) {
		return 0
	}
	days := int64(number)
	if days < 0 {
		return 0
	}
	if days > MaxRetentionDays {
		return MaxRetentionDays
	}
	return days
}

// NormalizeRetentionPolicy fills both windows.
func NormalizeRetentionPolicy(policy RetentionPolicy) RetentionPolicy {
	return RetentionPolicy{
		RunningInstanceRetentionDays: clampDays(policy.RunningInstanceRetentionDays),
		CronStatRetentionDays:        clampDays(policy.CronStatRetentionDays),
	}
}

func clampDays(days int64) int64 {
	if days < 0 {
		return 0
	}
	if days > MaxRetentionDays {
		return MaxRetentionDays
	}
	return days
}

// UpdatePolicy implements PUT /api/system/storage-retention/config.
func (s *SystemService) UpdatePolicy(ctx context.Context, policy RetentionPolicy) (RetentionPolicy, error) {
	normalized := NormalizeRetentionPolicy(policy)
	if _, err := s.updateInfo(ctx, map[string]any{
		"runningInstanceRetentionDays": normalized.RunningInstanceRetentionDays,
		"cronStatRetentionDays":        normalized.CronStatRetentionDays,
	}); err != nil {
		return RetentionPolicy{}, err
	}
	return normalized, nil
}

// PreviewRetention implements POST /api/system/storage-retention/preview.
func (s *SystemService) PreviewRetention(ctx context.Context, request StorageCleanupRequest) (*RetentionPreview, error) {
	policy := NormalizeRetentionPolicy(request.RetentionPolicy)
	types := filterCacheTypes(request.DependenceCacheTypes)

	var running int64
	if policy.RunningInstanceRetentionDays > 0 {
		cutoff := time.Now().Add(-time.Duration(policy.RunningInstanceRetentionDays) * 24 * time.Hour).Unix()
		count, err := s.store.CountRetainedRunningInstances(ctx, cutoff)
		if err != nil {
			return nil, err
		}
		running = count
	}
	var stats int64
	if policy.CronStatRetentionDays > 0 {
		before := time.Now().
			Add(-time.Duration(policy.CronStatRetentionDays) * 24 * time.Hour).
			Format("2006-01-02")
		count, err := s.store.CountRetainedCronStats(ctx, before)
		if err != nil {
			return nil, err
		}
		stats = count
	}
	caches := make([]DependenceCache, 0, len(types))
	var cacheBytes int64
	for _, kind := range types {
		size := directorySize(filepath.Join(s.cfg.DepCachePath, kind))
		caches = append(caches, DependenceCache{Type: kind, Bytes: size})
		cacheBytes += size
	}
	return &RetentionPreview{
		Policy:               policy,
		RunningInstances:     running,
		CronStats:            stats,
		DependenceCaches:     caches,
		DependenceCacheBytes: cacheBytes,
		CompactDatabase:      request.CompactDatabase,
	}, nil
}

// CleanupRetention implements POST /api/system/storage-retention/cleanup.
func (s *SystemService) CleanupRetention(ctx context.Context, request StorageCleanupRequest) (*RetentionCleanupResult, error) {
	preview, err := s.PreviewRetention(ctx, request)
	if err != nil {
		return nil, err
	}
	var deletedInstances, deletedStats int64
	if preview.Policy.RunningInstanceRetentionDays > 0 {
		cutoff := time.Now().Add(-time.Duration(preview.Policy.RunningInstanceRetentionDays) * 24 * time.Hour).Unix()
		deletedInstances, err = s.store.DeleteRetainedRunningInstances(ctx, cutoff)
		if err != nil {
			return nil, err
		}
	}
	if preview.Policy.CronStatRetentionDays > 0 {
		before := time.Now().
			Add(-time.Duration(preview.Policy.CronStatRetentionDays) * 24 * time.Hour).
			Format("2006-01-02")
		deletedStats, err = s.store.DeleteRetainedCronStats(ctx, before)
		if err != nil {
			return nil, err
		}
	}
	for _, cache := range preview.DependenceCaches {
		if err := os.RemoveAll(filepath.Join(s.cfg.DepCachePath, cache.Type)); err != nil {
			return nil, err
		}
	}
	compacted := false
	if request.CompactDatabase && (deletedInstances > 0 || deletedStats > 0) {
		if err := s.store.Vacuum(ctx); err != nil {
			return nil, err
		}
		compacted = true
	}
	return &RetentionCleanupResult{
		Deleted: RetentionDeleted{
			RunningInstances:     deletedInstances,
			CronStats:            deletedStats,
			DependenceCaches:     preview.DependenceCaches,
			DependenceCacheBytes: preview.DependenceCacheBytes,
		},
		CompactedDatabase: compacted,
	}, nil
}

// filterCacheTypes keeps only the two supported runtime names.
func filterCacheTypes(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		if value != "node" && value != "python3" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

// directorySize sums the regular files below root, skipping symlinks.
func directorySize(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if info, err := entry.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

// RetentionPolicyFromInfo reads the stored windows back for the settings page.
func RetentionPolicyFromInfo(info map[string]any) RetentionPolicy {
	return RetentionPolicy{
		RunningInstanceRetentionDays: NormalizeRetentionDays(info["runningInstanceRetentionDays"]),
		CronStatRetentionDays:        NormalizeRetentionDays(info["cronStatRetentionDays"]),
	}
}
