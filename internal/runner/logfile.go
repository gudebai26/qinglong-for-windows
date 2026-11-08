package runner

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// logMode is the three-state redirection of shell/otask.sh handle_log_path.
type logMode int

const (
	// logTee writes to the log file and to the console (default).
	logTee logMode = iota
	// logAppend writes to the log file only (no_tee=true).
	logAppend
	// logStdout writes to the console only (real_time=true).
	logStdout
	// logDiscard throws the output away (log_name=/dev/null).
	logDiscard
)

// logTarget is where one run writes its output.
type logTarget struct {
	// Path is relative to data/log and is what Crontabs.log_path stores.
	Path string
	// Absolute is the resolved file path; empty when discarding.
	Absolute string
	// Dir is the log directory relative to data/log.
	Dir  string
	Mode logMode
}

// resolveLog ports handle_log_path(): real_log_path wins, then log_name, then a
// directory derived from the script path, and the file is always named after
// the start time.
func (r *Runner) resolveLog(plan *Plan, now time.Time) logTarget {
	fileParam := plan.FileParam
	if fileParam == "" {
		fileParam = "task"
	}

	suffix := ""
	if id := plan.Env["ID"]; id != "" {
		if numericID(id) > 0 {
			suffix = "_" + id
		}
	}

	logDir := ""
	if name := plan.Env["log_name"]; name != "" {
		logDir = name
	} else {
		base := path.Base(fileParam)
		dirName := ""
		if strings.Contains(fileParam, "/") {
			trimmed := fileParam
			if strings.HasPrefix(trimmed, "/") {
				trimmed = trimmed[1:]
			}
			if idx := strings.LastIndex(trimmed, "/"); idx >= 0 {
				parent := trimmed[:idx]
				if slash := strings.LastIndex(parent, "/"); slash >= 0 {
					parent = parent[slash+1:]
				}
				dirName = parent
			}
		}
		name := base
		if dirName != "" {
			name = dirName + "_" + base
		}
		if dot := strings.LastIndex(name, "."); dot > 0 {
			name = name[:dot]
		}
		logDir = name + suffix
	}

	fileName := fmt.Sprintf("%s-%03d.log",
		now.Format("2006-01-02-15-04-05"), now.Nanosecond()/int(time.Millisecond))
	logPath := logDir + "/" + fileName
	if realPath := plan.Env["real_log_path"]; realPath != "" {
		logPath = realPath
	}

	target := logTarget{Path: logPath, Dir: logDir}
	switch {
	case logDir == "/dev/null":
		target.Mode = logDiscard
		target.Path = "/dev/null"
		return target
	case plan.Env["real_time"] == "true":
		target.Mode = logStdout
	case plan.Env["no_tee"] == "true":
		target.Mode = logAppend
	default:
		target.Mode = logTee
	}

	if filepath.IsAbs(logPath) {
		target.Absolute = filepath.Clean(logPath)
	} else {
		target.Absolute = filepath.Join(r.cfg.LogPath, filepath.FromSlash(logPath))
	}
	return target
}

// prepareLogDir creates the directory the log file lives in.
func (t logTarget) prepareLogDir() error {
	if t.Mode == logDiscard || t.Absolute == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(t.Absolute), 0o755); err != nil {
		return fmt.Errorf("create log directory: %w", err)
	}
	return nil
}

// open creates the log file in append mode. Runs are never truncated: a task
// that reports its own progress must not lose earlier lines.
func (t logTarget) open() (*os.File, error) {
	if t.Mode == logDiscard || t.Absolute == "" {
		return nil, nil
	}
	return os.OpenFile(t.Absolute, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
}

func numericID(raw string) int64 {
	var value int64
	if raw == "" {
		return 0
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] < '0' || raw[i] > '9' {
			return 0
		}
		value = value*10 + int64(raw[i]-'0')
	}
	return value
}
