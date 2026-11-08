package cronexpr

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// dayMode mirrors the branch split in back/shared/cronSchedule.ts. node-cron is
// fed one or two patterns depending on how the day fields are spelled, and the
// scheduler ORs them while de-duplicating the overlap.
type dayMode int

const (
	// modeDayOfMonth: the weekday field covers every day, so only the
	// day-of-month field constrains the rule.
	modeDayOfMonth dayMode = iota
	// modeDayOfWeek: the day-of-month field covers the whole month, so only
	// the weekday field constrains the rule.
	modeDayOfWeek
	// modeEither: both fields are restricted, Vixie-cron OR semantics apply.
	modeEither
)

// monthDays is the fixed table cronSchedule.ts uses. February stays 29 on
// purpose: the Node implementation compares the day-of-month value count
// against it instead of the real month length.
var monthDays = [12]int{31, 29, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}

// Schedule is a parsed cron expression. A rule that fires twice an hour is
// represented by two patterns in Node; here the day semantics are resolved at
// match time so a single Next() call is enough.
type Schedule struct {
	seconds [60]bool
	minutes [60]bool
	hours   [24]bool
	days    [32]bool
	months  [13]bool
	weekday [7]bool

	// dayCount is cron-parser's fields.dayOfMonth.values.length: the number of
	// spelled day-of-month values, 31 when the field is "*".
	dayCount int
	// weekdayAll is true when the weekday field expands to all seven days
	// (cron-parser reports 8 values because Sunday is 0 and 7).
	weekdayAll bool
	nthDay     int

	mode dayMode
}

type fieldSpec struct {
	name   string
	min    int
	max    int
	names  map[string]int
	isWeek bool
	// allowQuestion marks the two day fields, the only ones where "?" is a
	// valid wildcard (cronSchedule.ts rejects it elsewhere).
	allowQuestion bool
}

var (
	secondSpec = fieldSpec{name: "second", min: 0, max: 59}
	minuteSpec = fieldSpec{name: "minute", min: 0, max: 59}
	hourSpec   = fieldSpec{name: "hour", min: 0, max: 23}
	daySpec    = fieldSpec{name: "day-of-month", min: 1, max: 31, allowQuestion: true}
	monthSpec  = fieldSpec{name: "month", min: 1, max: 12, names: monthNames}
	weekSpec   = fieldSpec{name: "day-of-week", min: 0, max: 7, names: dayNames, isWeek: true, allowQuestion: true}
)

// Parse turns a 5 or 6 field expression into a Schedule. The aliases accepted
// by cronSchedule.ts (@daily, @hourly, ...) are expanded first; @once and
// @boot are rejected because the scheduler handles them outside the parser.
func Parse(expression string) (*Schedule, error) {
	source := strings.TrimSpace(expression)
	if source == "" {
		return nil, fmt.Errorf("cron: empty expression")
	}
	if strings.HasPrefix(source, "@") {
		expanded, ok := aliases[source]
		if !ok {
			if IsSpecial(source) {
				return nil, fmt.Errorf("cron: %s is not a calendar rule", source)
			}
			return nil, fmt.Errorf("cron: unknown alias %q", source)
		}
		source = expanded
	}
	fields := strings.Fields(source)
	if len(fields) < 5 || len(fields) > 6 {
		return nil, fmt.Errorf("cron: expected 5 or 6 fields, got %d", len(fields))
	}
	// The panel writes 5 field rules; node-cron always receives a leading
	// seconds field, so pad the same way.
	if len(fields) == 5 {
		fields = append([]string{"0"}, fields...)
	}

	schedule := &Schedule{}
	if err := schedule.parseInto(fields); err != nil {
		return nil, err
	}
	return schedule, nil
}

func (s *Schedule) parseInto(fields []string) error {
	specs := [6]fieldSpec{secondSpec, minuteSpec, hourSpec, daySpec, monthSpec, weekSpec}
	sets := [6][]bool{}
	for index, field := range fields {
		set, count, nth, err := parseField(specs[index], field)
		if err != nil {
			return err
		}
		sets[index] = set
		switch index {
		case 3:
			s.dayCount = count
		case 5:
			s.nthDay = nth
			s.weekdayAll = coversAllWeekdays(set)
		}
	}
	for index := 0; index < 60; index++ {
		s.seconds[index] = sets[0][index]
		s.minutes[index] = sets[1][index]
	}
	for index := 0; index < 24; index++ {
		s.hours[index] = sets[2][index]
	}
	for index := 0; index <= 31; index++ {
		s.days[index] = sets[3][index]
	}
	for index := 0; index <= 12; index++ {
		s.months[index] = sets[4][index]
	}
	for index := 0; index < 7; index++ {
		s.weekday[index] = sets[5][index]
	}

	switch {
	case s.weekdayAll:
		s.mode = modeDayOfMonth
	case s.dayCount < 31:
		s.mode = modeEither
	default:
		s.mode = modeDayOfWeek
	}
	return nil
}

// parseField expands one field into a value set. count is the number of values
// cron-parser would report (duplicates included), nth is the "#N" constraint.
func parseField(spec fieldSpec, field string) (set []bool, count int, nth int, err error) {
	set = make([]bool, spec.max+1)
	if strings.TrimSpace(field) == "" {
		return nil, 0, 0, fmt.Errorf("cron: empty %s field", spec.name)
	}
	if unsupportedStep.MatchString(field) || hashSyntax.MatchString(field) {
		return nil, 0, 0, fmt.Errorf("cron: unsupported %s field %q", spec.name, field)
	}
	for _, part := range strings.Split(field, ",") {
		if part == "" {
			return nil, 0, 0, fmt.Errorf("cron: empty list item in %s field", spec.name)
		}
		body := part
		partNth := 0
		if hash := strings.Index(body, "#"); hash >= 0 {
			if !spec.isWeek {
				return nil, 0, 0, fmt.Errorf("cron: # is only valid in day-of-week")
			}
			if nth != 0 {
				return nil, 0, 0, fmt.Errorf("cron: multiple # constraints")
			}
			parsed, convErr := strconv.Atoi(body[hash+1:])
			if convErr != nil || parsed < 1 || parsed > 5 {
				return nil, 0, 0, fmt.Errorf("cron: invalid # constraint %q", part)
			}
			partNth = parsed
			body = body[:hash]
		}
		step := 1
		if slash := strings.Index(body, "/"); slash >= 0 {
			parsed, convErr := strconv.Atoi(body[slash+1:])
			if convErr != nil || parsed <= 0 {
				return nil, 0, 0, fmt.Errorf("cron: invalid step in %q", part)
			}
			step = parsed
			body = body[:slash]
		}

		var start, end int
		switch {
		case body == "*" || body == "?":
			if body == "?" && !spec.allowQuestion {
				return nil, 0, 0, fmt.Errorf("cron: ? is only valid in day fields")
			}
			start, end = spec.min, spec.max
		case strings.Contains(body, "-"):
			left, right, _ := strings.Cut(body, "-")
			start, err = parseFieldValue(spec, left)
			if err != nil {
				return nil, 0, 0, err
			}
			end, err = parseFieldValue(spec, right)
			if err != nil {
				return nil, 0, 0, err
			}
			if start > end {
				return nil, 0, 0, fmt.Errorf("cron: descending range %q", body)
			}
		default:
			start, err = parseFieldValue(spec, body)
			if err != nil {
				return nil, 0, 0, err
			}
			end = start
			if step > 1 {
				// "5/10" means 5,15,25,... up to the field maximum.
				end = spec.max
			}
		}

		for value := start; value <= end; value += step {
			index := value
			if spec.isWeek && index == 7 {
				index = 0
			}
			set[index] = true
			count++
		}
		if partNth != 0 {
			nth = partNth
		}
	}
	return set, count, nth, nil
}

func parseFieldValue(spec fieldSpec, raw string) (int, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, fmt.Errorf("cron: empty value in %s field", spec.name)
	}
	if parsed, err := strconv.Atoi(value); err == nil {
		if parsed < spec.min || parsed > spec.max {
			return 0, fmt.Errorf("cron: %s value %d out of range %d-%d", spec.name, parsed, spec.min, spec.max)
		}
		return parsed, nil
	}
	if spec.names != nil {
		if parsed, ok := spec.names[strings.ToUpper(value)]; ok {
			return parsed, nil
		}
	}
	return 0, fmt.Errorf("cron: invalid %s value %q", spec.name, raw)
}

// coversAllWeekdays reports whether the field selects every weekday, which is
// how cron-parser represents "*" (0 through 7, Sunday counted twice).
func coversAllWeekdays(set []bool) bool {
	for day := 0; day < 7; day++ {
		if day >= len(set) || !set[day] {
			return false
		}
	}
	return true
}

// Next returns the first matching instant strictly after `after`. The result
// carries after's location.
//
// Matching happens on the wall clock, so the calendar fields are evaluated as
// the user sees them, not as UTC. Two DST consequences follow and are covered
// by tests: a local time skipped by the spring-forward gap fires once at the
// shifted time (02:30 becomes 03:30) instead of being lost, and a wall clock
// repeated by the autumn shift fires once instead of twice.
func (s *Schedule) Next(after time.Time) (time.Time, bool) {
	location := after.Location()
	local := after.In(location)
	// The carrier holds wall-clock fields in UTC so that day/hour arithmetic
	// never crosses a DST boundary; only the final tuple is resolved in the
	// panel's location.
	carrier := time.Date(local.Year(), local.Month(), local.Day(),
		local.Hour(), local.Minute(), local.Second(), 0, time.UTC).Add(time.Second)
	limit := carrier.AddDate(5, 0, 0)

	for carrier.Before(limit) {
		if !s.months[int(carrier.Month())] {
			carrier = time.Date(carrier.Year(), carrier.Month()+1, 1, 0, 0, 0, 0, time.UTC)
			continue
		}
		if !s.matchDay(carrier) {
			carrier = time.Date(carrier.Year(), carrier.Month(), carrier.Day()+1, 0, 0, 0, 0, time.UTC)
			continue
		}
		if !s.hours[carrier.Hour()] {
			carrier = time.Date(carrier.Year(), carrier.Month(), carrier.Day(), carrier.Hour()+1, 0, 0, 0, time.UTC)
			continue
		}
		if !s.minutes[carrier.Minute()] {
			carrier = time.Date(carrier.Year(), carrier.Month(), carrier.Day(), carrier.Hour(), carrier.Minute()+1, 0, 0, time.UTC)
			continue
		}
		if !s.seconds[carrier.Second()] {
			carrier = carrier.Add(time.Second)
			continue
		}
		return resolveWallClock(location, carrier.Year(), carrier.Month(),
			carrier.Day(), carrier.Hour(), carrier.Minute(), carrier.Second()), true
	}
	return time.Time{}, false
}

// resolveWallClock maps a wall-clock tuple to an instant in location. Go
// normalises a nonexistent local time backwards (02:30 becomes 01:30 EST on a
// spring-forward night), while cron-parser shifts it forward by the gap. Add
// the gap explicitly so both implementations agree.
func resolveWallClock(location *time.Location, year int, month time.Month, day, hour, minute, second int) time.Time {
	resolved := time.Date(year, month, day, hour, minute, second, 0, location)
	if resolved.Day() == day && resolved.Hour() == hour && resolved.Minute() == minute {
		return resolved
	}
	_, before := resolved.Zone()
	_, after := time.Date(year, month, day, hour+1, minute, second, 0, location).Zone()
	gap := time.Duration(after-before) * time.Second
	if gap <= 0 {
		return resolved
	}
	return resolved.Add(gap)
}

func (s *Schedule) matchDay(t time.Time) bool {
	if s.nthDay > 0 && (t.Day()-1)/7+1 != s.nthDay {
		return false
	}
	dayOfMonth := s.days[t.Day()]
	dayOfWeek := s.weekday[int(t.Weekday())]
	switch s.mode {
	case modeDayOfMonth:
		return dayOfMonth
	case modeDayOfWeek:
		return dayOfWeek
	default:
		dateBranch := dayOfMonth && (s.dayCount < monthDays[int(t.Month())-1] || dayOfWeek)
		weekBranch := dayOfWeek && !dayOfMonth
		return dateBranch || weekBranch
	}
}
