// Package cronexpr holds the cron expression rules the panel needs before the
// scheduler exists (M4). It mirrors back/shared/cronSchedule.ts closely enough
// to validate create/update payloads and to decide whether a rule can be
// written to the system crontab.
package cronexpr

import (
	"regexp"
	"strconv"
	"strings"
)

// Schedule types recognised by the panel (back/interface/schedule.ts).
const (
	TypeBoot = "@boot"
	TypeOnce = "@once"
)

var aliases = map[string]string{
	"@yearly":   "0 0 0 1 1 *",
	"@annually": "0 0 0 1 1 *",
	"@monthly":  "0 0 0 1 * *",
	"@weekly":   "0 0 0 * * 0",
	"@daily":    "0 0 0 * * *",
	"@midnight": "0 0 0 * * *",
	"@hourly":   "0 0 * * * *",
	"@minutely": "0 * * * * *",
}

var (
	unsupportedStep = regexp.MustCompile(`(^|,)/`)
	hashSyntax      = regexp.MustCompile(`H(?:\(|/|$)`)
	notPortable     = regexp.MustCompile(`[^\d\s*,/\-]`)
	numericStep     = regexp.MustCompile(`^\d+/`)
)

var monthNames = map[string]int{
	"JAN": 1, "FEB": 2, "MAR": 3, "APR": 4, "MAY": 5, "JUN": 6,
	"JUL": 7, "AUG": 8, "SEP": 9, "OCT": 10, "NOV": 11, "DEC": 12,
}

var dayNames = map[string]int{
	"SUN": 0, "MON": 1, "TUE": 2, "WED": 3, "THU": 4, "FRI": 5, "SAT": 6,
}

// IsOnce reports whether the schedule is the one-shot marker.
func IsOnce(schedule string) bool { return strings.HasPrefix(schedule, TypeOnce) }

// IsBoot reports whether the schedule runs once at panel start.
func IsBoot(schedule string) bool { return strings.HasPrefix(schedule, TypeBoot) }

// IsSpecial reports whether the rule is handled outside the cron parser.
func IsSpecial(schedule string) bool { return IsOnce(schedule) || IsBoot(schedule) }

// IsNodeCron is the port of CronService.isNodeCron: it answers "does this rule
// need the Node scheduler instead of the system crontab?" and therefore whether
// the line must be commented out in crontab.list.
func IsNodeCron(schedule string, extraSchedules int) bool {
	trimmed := strings.TrimSpace(schedule)
	fields := strings.Fields(trimmed)
	if len(fields) != 5 {
		return true
	}
	if notPortable.MatchString(schedule) {
		return true
	}
	if extraSchedules > 0 {
		return true
	}
	for _, field := range fields {
		for _, part := range strings.Split(field, ",") {
			if numericStep.MatchString(part) {
				return true
			}
		}
	}
	if strings.Contains(fields[4], "7") {
		return true
	}
	if strings.Contains(fields[2], "/") {
		return true
	}
	if fields[2] != "*" && fields[4] != "*" {
		return true
	}
	return false
}

// IsValid reports whether schedule is accepted by the create/update validator.
func IsValid(schedule string) bool {
	trimmed := strings.TrimSpace(schedule)
	if trimmed == "" {
		return false
	}
	if strings.HasPrefix(trimmed, "@") {
		if IsSpecial(trimmed) {
			return true
		}
		expanded, ok := aliases[trimmed]
		if !ok {
			return false
		}
		trimmed = expanded
	}
	fields := strings.Fields(trimmed)
	if len(fields) < 5 || len(fields) > 6 {
		return false
	}
	for _, field := range fields {
		if unsupportedStep.MatchString(field) || hashSyntax.MatchString(field) {
			return false
		}
	}
	if len(fields) == 5 {
		fields = append([]string{"0"}, fields...)
	}
	bounds := [6][2]int{{0, 59}, {0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}
	for index, field := range fields {
		if !validField(field, index, bounds[index][0], bounds[index][1]) {
			return false
		}
	}
	return true
}

func validField(field string, index, min, max int) bool {
	if field == "" {
		return false
	}
	for _, part := range strings.Split(field, ",") {
		if part == "" {
			return false
		}
		if !validPart(part, index, min, max) {
			return false
		}
	}
	return true
}

func validPart(part string, index, min, max int) bool {
	if part == "*" {
		return true
	}
	if strings.Contains(part, "#") {
		if index != 5 {
			return false
		}
		left, right, ok := strings.Cut(part, "#")
		if !ok || strings.Contains(right, "#") {
			return false
		}
		if _, ok := parseValue(left, index, min, max); !ok {
			return false
		}
		nth, err := strconv.Atoi(right)
		return err == nil && nth >= 1 && nth <= 5
	}
	base := part
	step := 0
	if slash := strings.Index(part, "/"); slash >= 0 {
		base = part[:slash]
		stepText := part[slash+1:]
		parsed, err := strconv.Atoi(stepText)
		if err != nil || parsed <= 0 {
			return false
		}
		step = parsed
	}
	_ = step
	if base == "*" || base == "?" {
		if base == "?" && index != 3 && index != 5 {
			return false
		}
		return true
	}
	if dash := strings.Index(base, "-"); dash > 0 {
		left := base[:dash]
		right := base[dash+1:]
		start, ok := parseValue(left, index, min, max)
		if !ok {
			return false
		}
		end, ok := parseValue(right, index, min, max)
		if !ok {
			return false
		}
		return start <= end || index == 5 // Sunday=0..7 wrap
	}
	_, ok := parseValue(base, index, min, max)
	return ok
}

func parseValue(value string, index, min, max int) (int, bool) {
	if value == "" || value == "?" {
		return 0, false
	}
	if parsed, err := strconv.Atoi(value); err == nil {
		return parsed, parsed >= min && parsed <= max
	}
	upper := strings.ToUpper(value)
	if index == 4 {
		if parsed, ok := monthNames[upper]; ok {
			return parsed, true
		}
	}
	if index == 5 {
		if parsed, ok := dayNames[upper]; ok {
			return parsed, true
		}
	}
	return 0, false
}
