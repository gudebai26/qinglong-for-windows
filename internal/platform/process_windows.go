//go:build windows

package platform

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

const createNewProcessGroup = 0x00000200

// processQueryLimitedInformation is PROCESS_QUERY_LIMITED_INFORMATION, which
// syscall does not export but every supported Windows version accepts.
const processQueryLimitedInformation = 0x1000

const stillActive = 259

func isWindows() bool { return true }

// ProcessAlive mirrors process.kill(pid, 0) in runCron.ts: it answers whether
// the pid still names a running process, without touching it.
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(handle)
	var code uint32
	if err := syscall.GetExitCodeProcess(handle, &code); err != nil {
		return false
	}
	return code == stillActive
}

func prepareCommand(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= createNewProcessGroup
}

// terminateTree tries a graceful taskkill first. Console programs usually
// refuse it, which is fine: the runner escalates to KillProcessTree after the
// grace period.
func terminateTree(pid int) error {
	cmd := exec.Command("taskkill", "/T", "/PID", strconv.Itoa(pid))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Run()
}

// KillProcessTree uses taskkill /T so child processes started by a script are
// terminated too.
func KillProcessTree(pid int) error {
	if pid <= 0 {
		return nil
	}
	cmd := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Run(); err == nil {
		return nil
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		if IsProcessGone(err) {
			return nil
		}
		return err
	}
	if err := process.Kill(); err != nil && !IsProcessGone(err) {
		return err
	}
	return nil
}
