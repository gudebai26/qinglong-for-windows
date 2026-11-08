package service

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"qinglong-go/internal/i18n"
)

// Log chunk limits from back/shared/logReader.ts.
const (
	DefaultLogChunkBytes = 256 * 1024
	MaxLogChunkBytes     = 1024 * 1024
)

// LogResult is the payload of GET /api/crons/:id/log.
type LogResult struct {
	Content    string
	Status     string
	Offset     int64
	NextOffset int64
	Total      int64
	Truncated  bool
}

// LogFile is one entry of GET /api/crons/:id/logs.
type LogFile struct {
	Filename  string  `json:"filename"`
	Directory string  `json:"directory"`
	Time      float64 `json:"time"`
}

// FileNode is one entry of GET /api/logs (readDirs).
type FileNode struct {
	Title      string      `json:"title"`
	Key        string      `json:"key"`
	Type       string      `json:"type"`
	Parent     string      `json:"parent"`
	CreateTime float64     `json:"createTime"`
	Size       *int64      `json:"size,omitempty"`
	Children   *[]FileNode `json:"children,omitempty"`
}

// Log implements GET /api/crons/:id/log.
func (s *CronService) Log(ctx context.Context, id int64, offset *int64, limit *int64, tail bool) (*LogResult, error) {
	doc, err := s.store.CrontabByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, &NotFoundError{
			Status:  500,
			Message: `Cron {"id":` + strconv.FormatInt(id, 10) + `} not found`,
		}
	}
	if doc.LogName.Valid && doc.LogName.String == "/dev/null" {
		return &LogResult{Content: i18n.T("日志设置为忽略"), Status: "ignored"}, nil
	}

	absolute := ""
	if doc.LogPath.Valid && doc.LogPath.String != "" {
		absolute, err = s.resolveLogPath(doc.LogPath.String)
		if err != nil {
			return nil, err
		}
	}

	if absolute != "" && fileExists(absolute) {
		chunk, err := readLogChunk(absolute, offset, limit, tail)
		if err != nil {
			return nil, err
		}
		status := "completed"
		if doc.Status.Valid && (doc.Status.Int64 == CronStatusRunning || doc.Status.Int64 == CronStatusQueued) {
			status = "running"
		}
		return &LogResult{
			Content:    chunk.Content,
			Status:     status,
			Offset:     chunk.Offset,
			NextOffset: chunk.NextOffset,
			Total:      chunk.Total,
			Truncated:  chunk.Truncated,
		}, nil
	}

	status := "notFound"
	if doc.Status.Valid && (doc.Status.Int64 == CronStatusQueued || doc.Status.Int64 == CronStatusRunning) {
		status = "running"
	}
	content := i18n.T("日志不存在...")
	if status == "running" {
		content = i18n.T("运行中...")
	}
	return &LogResult{Content: content, Status: status}, nil
}

// Logs implements GET /api/crons/:id/logs.
func (s *CronService) Logs(ctx context.Context, id int64) ([]LogFile, error) {
	doc, err := s.store.CrontabByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if doc == nil {
		// Node's getDb() throws for an unknown id instead of returning [].
		return nil, &NotFoundError{
			Status:  500,
			Message: `Cron {"id":` + strconv.FormatInt(id, 10) + `} not found`,
		}
	}
	if !doc.LogPath.Valid || doc.LogPath.String == "" || doc.LogPath.String == "/dev/null" {
		return []LogFile{}, nil
	}

	relativeDir := filepath.Dir(doc.LogPath.String)
	absolute, err := s.resolveLogPath(doc.LogPath.String)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(absolute)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []LogFile{}, nil
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
			Time:      float64(info.ModTime().UnixNano()) / 1e6,
		})
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].Time > result[j].Time })
	return result, nil
}

// ReadDirs implements the recursive listing behind GET /api/logs.
func (s *CronService) ReadDirs(ctx context.Context, dir string) ([]FileNode, error) {
	return s.readDirs(dir, s.cfg.LogPath, map[string]struct{}{".tmp": {}})
}

func (s *CronService) readDirs(dir, baseDir string, blacklist map[string]struct{}) ([]FileNode, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []FileNode{}, nil
		}
		return nil, err
	}
	relativePath, err := filepath.Rel(baseDir, dir)
	if err != nil {
		relativePath = ""
	}
	if relativePath == "." {
		relativePath = ""
	}

	result := make([]FileNode, 0, len(entries))
	for _, entry := range entries {
		if _, blocked := blacklist[entry.Name()]; blocked {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		subPath := filepath.Join(dir, entry.Name())
		info, err := os.Lstat(subPath)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		key := filepath.Join(relativePath, entry.Name())
		createTime := float64(info.ModTime().UnixMilli())
		if info.IsDir() {
			children, err := s.readDirs(subPath, baseDir, blacklist)
			if err != nil {
				return nil, err
			}
			node := FileNode{
				Title:      entry.Name(),
				Key:        key,
				Type:       "directory",
				Parent:     relativePath,
				CreateTime: createTime,
				Children:   &children,
			}
			result = append(result, node)
			continue
		}
		size := info.Size()
		result = append(result, FileNode{
			Title:      entry.Name(),
			Key:        key,
			Type:       "file",
			Parent:     relativePath,
			CreateTime: createTime,
			Size:       &size,
		})
	}

	sort.SliceStable(result, func(i, j int) bool {
		left, right := result[i], result[j]
		if left.Type == "file" && right.Type == "file" {
			return left.CreateTime > right.CreateTime
		}
		if left.Type == "directory" && right.Type == "directory" {
			return strings.ToLower(left.Title) < strings.ToLower(right.Title)
		}
		return left.Type == "directory"
	})
	return result, nil
}

// CheckLogFilePath validates /api/logs path/file parameters.
func (s *CronService) CheckLogFilePath(path, file string) (string, bool) {
	for _, segment := range strings.FieldsFunc(path+"/"+file, func(r rune) bool {
		return r == '/' || r == '\\'
	}) {
		if strings.EqualFold(segment, ".tmp") {
			return "", false
		}
	}
	joined := filepath.Join(path, file)
	target, err := s.resolveLogPath(joined)
	if err != nil {
		return "", false
	}
	return target, true
}

// RemoveLog deletes a log file or directory under data/log.
func (s *CronService) RemoveLog(ctx context.Context, path, file string) (bool, error) {
	target, ok := s.CheckLogFilePath(path, file)
	if !ok {
		return false, nil
	}
	if err := os.RemoveAll(target); err != nil {
		return false, err
	}
	return true, nil
}

var ansiPattern = regexp.MustCompile(`\x1b\[\d+m`)

// RemoveAnsi mirrors removeAnsi() from config/util.ts.
func RemoveAnsi(text string) string {
	return ansiPattern.ReplaceAllString(text, "")
}

// LogChunk is the raw chunk returned by readLogChunk.
type LogChunk struct {
	Content    string
	Offset     int64
	NextOffset int64
	Total      int64
	Truncated  bool
}

func normalizeLimit(limit *int64) int64 {
	if limit == nil {
		return DefaultLogChunkBytes
	}
	value := *limit
	if value < 4 {
		return 4
	}
	if value > MaxLogChunkBytes {
		return MaxLogChunkBytes
	}
	return value
}

func readLogChunk(path string, offset *int64, limit *int64, tail bool) (*LogChunk, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &LogChunk{}, nil
		}
		return nil, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	total := info.Size()
	chunkLimit := normalizeLimit(limit)

	start := int64(0)
	if tail || offset == nil {
		start = total - chunkLimit
		if start < 0 {
			start = 0
		}
	} else {
		start = *offset
		if start < 0 {
			start = 0
		}
		if start > total {
			start = total
		}
	}

	length := chunkLimit + 3
	if remaining := total - start; remaining < length {
		length = remaining
	}
	if length < 0 {
		length = 0
	}
	buffer := make([]byte, length)
	read, err := file.ReadAt(buffer, start)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	buffer = buffer[:read]

	leading := 0
	for leading < len(buffer) && isUTF8Continuation(buffer[leading]) {
		leading++
	}
	contentOffset := start + int64(leading)
	candidateEnd := int64(leading) + chunkLimit
	if candidateEnd > int64(len(buffer)) {
		candidateEnd = int64(len(buffer))
	}
	contentEnd := completeUTF8End(buffer, leading, int(candidateEnd))
	nextOffset := start + int64(contentEnd)

	return &LogChunk{
		Content:    string(buffer[leading:contentEnd]),
		Offset:     contentOffset,
		NextOffset: nextOffset,
		Total:      total,
		Truncated:  contentOffset > 0 || nextOffset < total,
	}, nil
}

func isUTF8Continuation(value byte) bool { return value&0xc0 == 0x80 }

func completeUTF8End(buffer []byte, start, end int) int {
	if end <= start {
		return start
	}
	sequenceStart := end - 1
	for sequenceStart > start && isUTF8Continuation(buffer[sequenceStart]) {
		sequenceStart--
	}
	first := buffer[sequenceStart]
	expected := 1
	switch {
	case first < 0x80:
		expected = 1
	case first < 0xe0:
		expected = 2
	case first < 0xf0:
		expected = 3
	default:
		expected = 4
	}
	if end-sequenceStart < expected {
		return sequenceStart
	}
	return end
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// LogDetail implements GET /api/logs/detail.
func (s *CronService) LogDetail(ctx context.Context, path, file string, offset *int64, limit *int64, tail bool) (*LogResult, bool, error) {
	target, ok := s.CheckLogFilePath(path, file)
	if !ok {
		return nil, false, nil
	}
	logPath := path + "/" + file
	instance, err := s.store.RunningInstanceByLogPath(ctx, logPath)
	if err != nil {
		return nil, false, err
	}
	chunk, err := readLogChunk(target, offset, limit, tail)
	if err != nil {
		return nil, false, err
	}
	result := &LogResult{
		Content:    RemoveAnsi(chunk.Content),
		Offset:     chunk.Offset,
		NextOffset: chunk.NextOffset,
		Total:      chunk.Total,
		Truncated:  chunk.Truncated,
	}
	if instance != nil {
		result.Status = "running"
	}
	return result, true, nil
}
