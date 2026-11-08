package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// rotatingFile writes to data/syslog/<date>.log and rolls over at midnight or
// when maxBytes is reached, mirroring winston-daily-rotate-file in the Node
// panel (filename "%DATE%.log", maxSize 20m, maxFiles 7d).
type rotatingFile struct {
	mu       sync.Mutex
	dir      string
	maxBytes int64
	maxAge   time.Duration

	file  *os.File
	day   string
	index int
	size  int64
}

var rotatedName = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})(?:\.(\d+))?\.log$`)

func newRotatingFile(dir string, maxBytes int64, maxAge time.Duration) (*rotatingFile, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}
	r := &rotatingFile{dir: dir, maxBytes: maxBytes, maxAge: maxAge}
	r.cleanup(time.Now())
	return r, nil
}

func (r *rotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	day := now.Format("2006-01-02")
	needRotate := r.file == nil || day != r.day
	if !needRotate && r.maxBytes > 0 && r.size > 0 && r.size+int64(len(p)) > r.maxBytes {
		needRotate = true
	}
	if needRotate {
		if err := r.rotate(day); err != nil {
			return 0, err
		}
	}

	n, err := r.file.Write(p)
	r.size += int64(n)
	return n, err
}

func (r *rotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	return err
}

// rotate opens the file for the given day. Same-day rotation bumps the index
// (2026-11-07.log -> 2026-11-07.1.log) like winston does.
func (r *rotatingFile) rotate(day string) error {
	if r.file != nil {
		r.file.Close()
		r.file = nil
	}
	if day == r.day {
		r.index++
	} else {
		r.day = day
		r.index = 0
	}

	name := r.day + ".log"
	if r.index > 0 {
		name = r.day + "." + strconv.Itoa(r.index) + ".log"
	}
	file, err := os.OpenFile(filepath.Join(r.dir, name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open syslog file: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return fmt.Errorf("stat syslog file: %w", err)
	}
	r.file = file
	r.size = info.Size()

	if r.index == 0 {
		r.cleanup(time.Now())
	}
	return nil
}

// cleanup removes rotated files older than maxAge days.
func (r *rotatingFile) cleanup(now time.Time) {
	if r.maxAge <= 0 {
		return
	}
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return
	}

	type candidate struct {
		name    string
		modTime time.Time
	}
	var files []candidate
	for _, entry := range entries {
		if entry.IsDir() || !rotatedName.MatchString(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, candidate{name: entry.Name(), modTime: info.ModTime()})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].modTime.After(files[j].modTime) })

	cutoff := now.Add(-r.maxAge)
	for _, file := range files {
		if file.modTime.Before(cutoff) {
			os.Remove(filepath.Join(r.dir, file.name))
		}
	}
}

// ParseLevel maps the panel's LOG_LEVEL values onto slog levels. "silly" and
// "verbose" are Node/winston names kept for configuration compatibility.
func ParseLevel(raw string) Level {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "silly", "debug", "verbose":
		return LevelDebug
	case "warn", "warning":
		return LevelWarn
	case "error":
		return LevelError
	default:
		return LevelInfo
	}
}
