package service

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"qinglong-go/internal/i18n"
	"qinglong-go/internal/platform"
	"qinglong-go/internal/runner"
)

// sysLogEntry is one file of data/syslog used by GET /api/system/log.
type sysLogEntry struct {
	Name       string
	Size       int64
	CreateTime int64
}

// SystemLogPage is the result of the syslog concatenation. The handler writes
// the headers first, then calls CopyTo.
type SystemLogPage struct {
	Total      int64
	Length     int64
	Truncated  bool
	selections []sysLogSelection
}

type sysLogSelection struct {
	path  string
	start int64
	size  int64
}

// ListSystemLogs implements readDirs(systemLogPath, systemLogPath) for the flat
// syslog directory: .log files only.
func (s *SystemService) ListSystemLogs() ([]sysLogEntry, error) {
	entries, err := os.ReadDir(s.cfg.SyslogPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]sysLogEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".log") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		out = append(out, sysLogEntry{
			Name:       entry.Name(),
			Size:       info.Size(),
			CreateTime: fileBirthTime(info),
		})
	}
	// dirSort puts the newest file first; the caller reverses it.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CreateTime != out[j].CreateTime {
			return out[i].CreateTime > out[j].CreateTime
		}
		return out[i].Name > out[j].Name
	})
	return out, nil
}

// PrepareSystemLog implements GET /api/system/log: it computes which byte
// ranges to stream so the response never exceeds the requested limit.
func (s *SystemService) PrepareSystemLog(startTime, endTime string, limit int64) (*SystemLogPage, error) {
	entries, err := s.ListSystemLogs()
	if err != nil {
		return nil, err
	}
	start := dayStart(startTime)
	end := dayEnd(endTime)

	// readDirs order is newest first; reverse it to get chronological order.
	logs := make([]sysLogEntry, 0, len(entries))
	for index := len(entries) - 1; index >= 0; index-- {
		entry := entries[index]
		if entry.CreateTime >= start && entry.CreateTime <= end {
			logs = append(logs, entry)
		}
	}

	if limit <= 0 {
		limit = 1024 * 1024
	}
	if limit > 1024*1024 {
		limit = 1024 * 1024
	}
	var total int64
	for _, entry := range logs {
		total += entry.Size
	}

	remaining := limit
	selections := make([]sysLogSelection, 0, len(logs))
	for index := len(logs) - 1; index >= 0 && remaining > 0; index-- {
		entry := logs[index]
		length := entry.Size
		if length > remaining {
			length = remaining
		}
		if length > 0 {
			selections = append([]sysLogSelection{{
				path:  filepath.Join(s.cfg.SyslogPath, entry.Name),
				start: entry.Size - length,
				size:  length,
			}}, selections...)
			remaining -= length
		}
	}
	var length int64
	for _, selection := range selections {
		length += selection.size
	}
	return &SystemLogPage{
		Total:      total,
		Length:     length,
		Truncated:  total > length,
		selections: selections,
	}, nil
}

// CopyTo streams the selected byte ranges in chronological order.
func (p *SystemLogPage) CopyTo(w io.Writer) error {
	for _, selection := range p.selections {
		file, err := os.Open(selection.path)
		if err != nil {
			// A log rotated away between listing and streaming is skipped, the
			// way Node's createReadStream error is ignored by the pipe.
			continue
		}
		if _, err := file.Seek(selection.start, io.SeekStart); err != nil {
			file.Close()
			continue
		}
		_, err = io.CopyN(w, file, selection.size)
		file.Close()
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
	}
	return nil
}

// DeleteSystemLog implements DELETE /api/system/log. Files are truncated, not
// unlinked: the logger keeps an append handle open.
func (s *SystemService) DeleteSystemLog() error {
	entries, err := s.ListSystemLogs()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		file, err := os.OpenFile(filepath.Join(s.cfg.SyslogPath, entry.Name), os.O_WRONLY, 0)
		if err != nil {
			continue
		}
		_ = file.Truncate(0)
		_ = file.Close()
	}
	return nil
}

// PruneLogs is the `ql rmlog N` purge: delete task logs older than N days and
// drop the directories that become empty. Files still registered as the log of
// a running task are kept.
func (s *SystemService) PruneLogs(days int64) (int, error) {
	if days <= 0 {
		return 0, nil
	}
	cutoff := time.Now().AddDate(0, 0, -int(days)).UnixMilli()
	// Node asks the panel whether a log is still referenced by a running task
	// (pruneLogs -> crons/detail). The same guard keeps a long run readable.
	active, err := s.store.RunningLogPaths(context.Background())
	if err != nil {
		active = map[string]struct{}{}
	}
	removed := 0
	err = filepath.WalkDir(s.cfg.LogPath, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if entry.IsDir() {
			if path != s.cfg.LogPath && strings.HasPrefix(entry.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".log") || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		age := info.ModTime().UnixMilli()
		if datePart := logDatePrefix(entry.Name()); datePart != "" {
			if parsed, err := time.ParseInLocation("2006-01-02", datePart, time.Local); err == nil {
				age = parsed.UnixMilli()
			}
		}
		if age > cutoff {
			return nil
		}
		if relative, relErr := filepath.Rel(s.cfg.LogPath, path); relErr == nil {
			if _, busy := active[filepath.ToSlash(relative)]; busy {
				return nil
			}
		}
		if err := os.Remove(path); err == nil {
			removed++
		}
		return nil
	})
	if err != nil {
		return removed, err
	}
	removeEmptyDirs(s.cfg.LogPath)
	return removed, nil
}

func logDatePrefix(name string) string {
	if len(name) < 10 {
		return ""
	}
	candidate := name[:10]
	if _, err := time.ParseInLocation("2006-01-02", candidate, time.Local); err != nil {
		return ""
	}
	return candidate
}

func removeEmptyDirs(root string) {
	var dirs []string
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.IsDir() {
			return nil
		}
		if path != root {
			dirs = append(dirs, path)
		}
		return nil
	})
	// Deepest first so a parent becomes empty before it is visited.
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err == nil && len(entries) == 0 {
			_ = os.Remove(dir)
		}
	}
}

// ExportData implements PUT /api/system/data/export. It writes the same archive
// `tar -zcf data.tgz -- data/db data/upload ...` produces, without shelling out.
func (s *SystemService) ExportData(kinds []string) (string, error) {
	dirs := []string{"db", "upload"}
	for _, kind := range kinds {
		if kind == "base" {
			continue
		}
		dirs = append(dirs, kind)
	}
	allowed := map[string]struct{}{
		"db": {}, "upload": {}, "config": {}, "scripts": {}, "log": {},
		"deps": {}, "syslog": {}, "dep_cache": {}, "raw": {}, "repo": {},
		"ssh.d": {}, "script-history": {},
	}
	for _, dir := range dirs {
		if _, ok := allowed[dir]; !ok {
			return "", &BusinessError{Code: 400, Message: i18n.T("参数错误")}
		}
	}
	if containsString(dirs, "scripts") {
		dirs = append(dirs, "script-history")
	}
	if _, err := os.Stat(s.cfg.ScriptHistoryPath); err != nil {
		dirs = removeString(dirs, "script-history")
	}

	// Deduplicate while keeping the order Node builds.
	seen := map[string]struct{}{}
	unique := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		if _, ok := seen[dir]; ok {
			continue
		}
		seen[dir] = struct{}{}
		unique = append(unique, dir)
	}

	if err := os.MkdirAll(filepath.Dir(s.cfg.DataTgzFile), 0o755); err != nil {
		return "", err
	}
	file, err := os.Create(s.cfg.DataTgzFile)
	if err != nil {
		return "", err
	}
	defer file.Close()

	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	base := filepath.Dir(s.cfg.DataPath)
	for _, dir := range unique {
		source := filepath.Join(s.cfg.DataPath, dir)
		if _, err := os.Stat(source); err != nil {
			continue
		}
		if err := addTarTree(tarWriter, source, base); err != nil {
			tarWriter.Close()
			gzipWriter.Close()
			return "", err
		}
	}
	if err := tarWriter.Close(); err != nil {
		return "", err
	}
	if err := gzipWriter.Close(); err != nil {
		return "", err
	}
	return s.cfg.DataTgzFile, nil
}

// addTarTree appends one directory tree with paths relative to base, so the
// archive contains "data/<dir>/..." like the Node command.
func addTarTree(tw *tar.Writer, source, base string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(base, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return nil
			}
			link = target
		}
		header, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		header.Name = relative
		if entry.IsDir() {
			header.Name += "/"
		}
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if entry.IsDir() || link != "" {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = io.Copy(tw, file)
		return err
	})
}

// ImportData implements PUT /api/system/data/import: unpack the uploaded archive
// into .tmp/data, exactly like `cd .tmp && tar -zxvf data.tgz`.
func (s *SystemService) ImportData() (string, error) {
	target := filepath.Join(s.cfg.TmpPath, "data")
	if err := os.RemoveAll(target); err != nil {
		return "", err
	}
	file, err := os.Open(s.cfg.DataTgzFile)
	if err != nil {
		return "", err
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return "", err
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	root := filepath.Clean(s.cfg.TmpPath)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		name := filepath.Clean(filepath.Join(root, header.Name))
		if name != root && !strings.HasPrefix(name, root+string(os.PathSeparator)) {
			return "", fmt.Errorf("invalid archive entry %q", header.Name)
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(name, 0o755); err != nil {
				return "", err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
				return "", err
			}
			out, err := os.OpenFile(name, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(header.Mode)&0o777)
			if err != nil {
				return "", err
			}
			if _, err := io.Copy(out, tarReader); err != nil {
				out.Close()
				return "", err
			}
			out.Close()
		case tar.TypeSymlink:
			_ = os.Symlink(header.Linkname, name)
		}
	}
	return "", nil
}

// ---------------------------------------------------------------------------
// command run / stop
// ---------------------------------------------------------------------------

// CommandExecution is one running system command (PUT /api/system/command-run).
type CommandExecution struct {
	// Pid is the process id reported through QL-Task-Pid.
	Pid int
	// LogPath is relative to data/log and reported through QL-Task-Log.
	LogPath string

	file      *os.File
	execution *runner.Execution
}

// StartCommand starts `real_time=true task <command>` and streams its output to
// out while it is also written to the log file.
func (s *SystemService) StartCommand(ctx context.Context, command string, out io.Writer) (*CommandExecution, error) {
	trimmed := strings.TrimSpace(command)
	if !strings.HasPrefix(trimmed, "task ") && !strings.HasPrefix(trimmed, "ql ") {
		trimmed = "task " + trimmed
	}
	idText := findCronID(s.readCrontab(), command)
	id, _ := strconv.ParseInt(idText, 10, 64)
	uniqPath := GetUniqPath(command, id, idText != "")
	now := time.Now()
	logPath := uniqPath + "/" + now.Format("2006-01-02-15-04-05") +
		fmt.Sprintf("-%03d", now.Nanosecond()/int(time.Millisecond)) + ".log"

	absolute, err := s.crons.resolveLogPath(logPath)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		return nil, err
	}
	file, err := os.Create(absolute)
	if err != nil {
		return nil, err
	}

	sink := io.Writer(file)
	if out != nil {
		sink = io.MultiWriter(file, out)
	}
	execution, err := s.crons.runner.Prepare(runner.Request{
		Command: "real_log_path=" + logPath + " real_time=true " + trimmed,
		Sink:    sink,
	})
	if err != nil {
		file.Close()
		return nil, err
	}
	if err := execution.Start(); err != nil {
		file.Close()
		return nil, err
	}
	return &CommandExecution{
		Pid:       execution.Pid(),
		LogPath:   logPath,
		file:      file,
		execution: execution,
	}, nil
}

// Wait blocks until the command ends and closes the log file.
func (e *CommandExecution) Wait() {
	e.execution.Wait()
	_ = e.file.Close()
}

// StopCommand implements PUT /api/system/command-stop.
func (s *SystemService) StopCommand(ctx context.Context, command string, pid int64) (*Reply, error) {
	if pid == 0 && strings.TrimSpace(command) == "" {
		return &Reply{Code: 400, Message: i18n.T("参数错误")}, nil
	}
	if pid > 0 {
		if err := killProcess(pid); err != nil {
			// Node's `kill` on a missing pid is a no-op success.
			if platform.IsProcessGone(err) {
				return &Reply{Code: 200}, nil
			}
			return nil, err
		}
		return &Reply{Code: 200}, nil
	}
	// Without a pid Node greps `ps -eo pid,command`, which does not exist on
	// Windows; the panel always passes the pid it received from QL-Task-Pid.
	return &Reply{Code: 400, Message: i18n.T("任务未找到")}, nil
}

func (s *SystemService) readCrontab() string {
	content, err := os.ReadFile(filepath.Join(s.cfg.ConfigPath, "crontab.list"))
	if err != nil {
		return ""
	}
	return string(content)
}

// crontabIDPattern finds the ID= assignment of a crontab.list line.
var crontabIDPattern = regexp.MustCompile(`\bID=(\d+)\s`)

// findCronID matches the command text literally (back/shared/cronCommand.ts).
func findCronID(crontab, command string) string {
	if command == "" {
		return ""
	}
	for _, line := range strings.Split(crontab, "\n") {
		match := crontabIDPattern.FindStringSubmatchIndex(line)
		if match == nil {
			continue
		}
		idEnd := match[3]
		if strings.Contains(line[idEnd:], command) {
			return line[match[2]:match[3]]
		}
	}
	return ""
}

// killProcess terminates a process tree, which is what killTask(pid) does.
// Killing only the parent would leave the node/python child running.
func killProcess(pid int64) error {
	return platform.KillProcessTree(int(pid))
}

func dayStart(value string) int64 {
	day := time.Now()
	if parsed, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(value), time.Local); err == nil {
		day = parsed
	}
	return time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.Local).UnixMilli()
}

func dayEnd(value string) int64 {
	day := time.Now()
	if parsed, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(value), time.Local); err == nil {
		day = parsed
	}
	return time.Date(day.Year(), day.Month(), day.Day(), 23, 59, 59, int(999*time.Millisecond), time.Local).UnixMilli()
}

func removeString(values []string, target string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != target {
			out = append(out, value)
		}
	}
	return out
}
