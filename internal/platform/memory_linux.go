//go:build linux

package platform

import (
	"os"
	"strconv"
	"strings"
)

// Memory reads MemTotal and MemAvailable from /proc/meminfo, which is what
// os.totalmem()/os.freemem() report on Linux.
func Memory() (total, free uint64) {
	content, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(string(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			total = value * 1024
		case "MemAvailable:":
			free = value * 1024
		}
	}
	return total, free
}

// LoadAvg reads the three load averages from /proc/loadavg.
func LoadAvg() [3]float64 {
	var out [3]float64
	content, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return out
	}
	fields := strings.Fields(string(content))
	for i := 0; i < 3 && i < len(fields); i++ {
		out[i], _ = strconv.ParseFloat(fields[i], 64)
	}
	return out
}
