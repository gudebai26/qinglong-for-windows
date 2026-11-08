//go:build !windows

package platform

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func isWindows() bool { return false }

// ProcessAlive mirrors process.kill(pid, 0) in runCron.ts: EPERM means the
// process exists but belongs to another user, ESRCH means it is gone.
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	return errors.Is(err, syscall.EPERM)
}

func prepareCommand(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// terminateTree signals the whole process group with SIGTERM so a task can
// flush its output before the hard kill.
func terminateTree(pid int) error {
	if err := syscall.Kill(-pid, syscall.SIGTERM); err == nil {
		return nil
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Signal(syscall.SIGTERM)
}

// KillProcessTree signals the whole process group.
func KillProcessTree(pid int) error {
	if pid <= 0 {
		return nil
	}
	if err := syscall.Kill(-pid, syscall.SIGKILL); err == nil {
		return nil
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Kill()
}
