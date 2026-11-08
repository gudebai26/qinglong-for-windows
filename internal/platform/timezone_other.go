//go:build !windows

package platform

import (
	"os"
	"os/exec"
)

// LocalTimeZoneName returns "" on platforms where the zone name is derived
// from the tz database abbreviation instead.
func LocalTimeZoneName() string { return "" }

// SetSystemTimezone points /etc/localtime at the requested zone and records the
// name in /etc/timezone, mirroring setSystemTimezone() in back/config/util.ts.
// Failures return false; the caller answers 400 like Node.
func SetSystemTimezone(timezone string) bool {
	if _, err := os.Stat("/usr/share/zoneinfo/" + timezone); err != nil {
		return false
	}
	if err := os.Remove("/etc/localtime"); err != nil && !os.IsNotExist(err) {
		return runSudo("ln", "-sf", "/usr/share/zoneinfo/"+timezone, "/etc/localtime")
	}
	if err := os.Symlink("/usr/share/zoneinfo/"+timezone, "/etc/localtime"); err != nil {
		if !runSudo("ln", "-sf", "/usr/share/zoneinfo/"+timezone, "/etc/localtime") {
			return false
		}
	}
	if err := os.WriteFile("/etc/timezone", []byte(timezone+"\n"), 0o644); err != nil {
		return runSudo("tee", "/etc/timezone")
	}
	return true
}

// runSudo retries a privileged command through sudo, which is how the panel
// applies a timezone when it does not run as root.
func runSudo(name string, args ...string) bool {
	sudo, err := exec.LookPath("sudo")
	if err != nil {
		return false
	}
	cmd := exec.Command(sudo, append([]string{"-n", name}, args...)...)
	return cmd.Run() == nil
}
