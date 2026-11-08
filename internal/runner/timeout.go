package runner

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ErrInvalidTimeout marks a CommandTimeoutTime value the shell wrapper would
// reject. shell/task-timeout.sh exits with 125 in that case.
var ErrInvalidTimeout = fmt.Errorf("invalid task timeout")

// timeoutPattern is the awk regex from shell/task-timeout.sh: a decimal number
// with an optional exponent and an optional s/m/h/d unit.
var timeoutPattern = regexp.MustCompile(`^([0-9]+([.][0-9]*)?|[.][0-9]+)([eE][+-]?[0-9]+)?([smhd])?$`)

// ParseTimeout converts CommandTimeoutTime into a duration. An empty value
// means "no timeout" and returns 0.
func ParseTimeout(raw string) (time.Duration, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, nil
	}
	match := timeoutPattern.FindStringSubmatch(value)
	if match == nil {
		return 0, fmt.Errorf("%w: %s", ErrInvalidTimeout, raw)
	}
	number := strings.TrimSuffix(value, match[4])
	seconds, err := strconv.ParseFloat(number, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %s", ErrInvalidTimeout, raw)
	}
	factor := 1.0
	switch match[4] {
	case "d":
		factor = 86400
	case "h":
		factor = 3600
	case "m":
		factor = 60
	}
	seconds *= factor
	if seconds <= 0 {
		return 0, nil
	}
	return time.Duration(seconds * float64(time.Second)), nil
}

// timeoutExitCode is what a task reports when the timer killed it.
const timeoutExitCode = 124

// invalidTimeoutExitCode is what a task reports when CommandTimeoutTime is
// malformed.
const invalidTimeoutExitCode = 125

// stopExitCode is what a manually stopped task reports, matching the
// exit_code the panel stores for a stopped instance.
const stopExitCode = 143

// killGrace is how long a task has to exit after SIGTERM before it is killed
// outright (shell/task-timeout.sh uses 0.5s).
const killGrace = 500 * time.Millisecond
