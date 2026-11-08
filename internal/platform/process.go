package platform

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// ShellCommand mirrors back/shared/shell.ts. Windows falls back to cmd.exe
// (with /d /s /c) unless QL_SHELL points at Git Bash; Unix uses /bin/bash.
func ShellCommand(command string) (string, []string) {
	configured := strings.TrimSpace(os.Getenv("QL_SHELL"))
	if isWindows() {
		file := configured
		if file == "" {
			file = os.Getenv("ComSpec")
		}
		if file == "" {
			file = "cmd.exe"
		}
		if isCmdExe(file) {
			return file, []string{"/d", "/s", "/c", command}
		}
		return file, []string{"-c", command}
	}
	if configured == "" {
		configured = "/bin/bash"
	}
	return configured, []string{"-c", command}
}

// PosixShellCommand runs a command through bash when one is available.
//
// The panel builds POSIX command lines (single-quoted arguments, `source ...`)
// for dependency installs and subscription hooks. On Windows ShellCommand
// falls back to cmd.exe, which cannot parse either construct — Node hits the
// same wall. When bash exists (Git Bash, WSL, QL_SHELL) it is used instead so
// the same command string works on every platform.
func PosixShellCommand(command, bashPath string) (string, []string) {
	if strings.TrimSpace(bashPath) != "" {
		return bashPath, []string{"-c", command}
	}
	return ShellCommand(command)
}

// PrepareCommand applies the process-group attributes needed to kill a whole
// task tree later.
func PrepareCommand(cmd *exec.Cmd) {
	prepareCommand(cmd)
}

// TerminateTree asks a whole process group to exit. The task runner uses it
// first and escalates to KillProcessTree after a short grace period, which is
// what shell/task-timeout.sh does on timeout.
func TerminateTree(pid int) error {
	if pid <= 0 {
		return nil
	}
	return terminateTree(pid)
}

func isCmdExe(file string) bool {
	lower := strings.ToLower(file)
	lower = strings.ReplaceAll(lower, "\\", "/")
	return lower == "cmd" || strings.HasSuffix(lower, "/cmd") ||
		lower == "cmd.exe" || strings.HasSuffix(lower, "/cmd.exe")
}

// IsProcessGone reports whether err means the target process no longer exists.
//
// Stopping an already finished task must be a success: Node's kill() ignores a
// missing pid the same way, and the panel's stop button can race with a task
// that just exited. Windows answers OpenProcess with ERROR_INVALID_PARAMETER
// (87) for a dead pid, Unix with ESRCH.
func IsProcessGone(err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, os.ErrProcessDone) || errors.Is(err, os.ErrNotExist) {
		return true
	}
	if errors.Is(err, syscall.Errno(87)) || errors.Is(err, syscall.ESRCH) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "process already finished") ||
		strings.Contains(message, "no such process") ||
		strings.Contains(message, "the parameter is incorrect") ||
		strings.Contains(message, "参数不正确")
}
