package service

import (
	"io"
	"os"
	"path/filepath"

	"qinglong-go/internal/platform"
	"qinglong-go/internal/runner"
)

// RunScriptFile runs a debug script the way ScriptService.runScript does:
// `real_time=true task <relative> now`, which means the output is not written
// to a log file but streamed to the panel (the websocket arrives in M8). The
// .swap file is removed as soon as the process ends.
func (s *CronService) RunScriptFile(filePath string) (int64, error) {
	relative, err := filepath.Rel(s.cfg.ScriptPath, filePath)
	if err != nil {
		return 0, err
	}
	// The shell wrapper receives forward slashes from bash on every platform;
	// the Go parser treats a backslash as an escape character.
	relative = filepath.ToSlash(relative)

	execution, err := s.runner.Prepare(runner.Request{
		Command: "real_time=true task " + relative + " now",
	})
	if err != nil {
		return 0, err
	}
	// task.sh creates the log directory even when real_time skips the file.
	if logPath := execution.LogPath(); logPath != "" && logPath != "/dev/null" {
		if absolute, err := s.resolveLogPath(logPath); err == nil {
			_ = os.MkdirAll(filepath.Dir(absolute), 0o755)
		}
	}
	if s.sock != nil {
		// The debugger panel tailors the run log from these frames.
		execution.SetSink(sockWriter{sock: s.sock})
	} else {
		execution.SetSink(io.Discard)
	}

	if err := execution.Start(); err != nil {
		return 0, err
	}
	pid := int64(execution.Pid())
	go func() {
		execution.Wait()
		// Node's onEnd callback: the debug copy disappears with the process.
		_ = os.Remove(filePath)
	}()
	return pid, nil
}

// sockWriter forwards task output to the manuallyRunScript topic.
type sockWriter struct{ sock *SockService }

func (w sockWriter) Write(p []byte) (int, error) {
	if w.sock != nil && len(p) > 0 {
		w.sock.Send(SockMessage{Type: "manuallyRunScript", Message: string(p)})
	}
	return len(p), nil
}

// StopScriptFile kills a running debug script. Without a pid Node falls back to
// `ps | grep`, which does not exist on Windows, so nothing is killed there.
func (s *CronService) StopScriptFile(pid int64) error {
	if pid <= 0 {
		return nil
	}
	return platform.KillProcessTree(int(pid))
}
