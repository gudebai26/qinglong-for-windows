package cronexpr

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

type parityFixture struct {
	GeneratedAt string `json:"generatedAt"`
	Cases       []struct {
		Expr  string   `json:"expr"`
		TZ    string   `json:"tz"`
		Start string   `json:"start"`
		Runs  []string `json:"runs"`
	} `json:"cases"`
}

// TestNextMatchesNode compares Next() against times produced by the Node
// scheduler stack (back/shared/cronSchedule.ts + cron-parser). The fixture is
// committed so the comparison runs without Node.
func TestNextMatchesNode(t *testing.T) {
	raw, err := os.ReadFile("testdata/next-runs.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var fixture parityFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	if len(fixture.Cases) == 0 {
		t.Fatal("fixture has no cases")
	}
	for _, test := range fixture.Cases {
		location, err := time.LoadLocation(test.TZ)
		if err != nil {
			t.Fatalf("load %s: %v", test.TZ, err)
		}
		start, err := time.Parse(time.RFC3339, test.Start)
		if err != nil {
			t.Fatalf("parse start %s: %v", test.Start, err)
		}
		if len(test.Runs) > 0 && len(test.Runs[0]) > 5 && test.Runs[0][:5] == "ERROR" {
			if _, err := Parse(test.Expr); err == nil {
				t.Fatalf("%q: Node rejected the rule (%s) but Parse accepted it", test.Expr, test.Runs[0])
			}
			continue
		}
		schedule, err := Parse(test.Expr)
		if err != nil {
			t.Fatalf("%q: Parse: %v", test.Expr, err)
		}
		cursor := start.In(location)
		for index, want := range test.Runs {
			wantTime, err := time.Parse(time.RFC3339, want)
			if err != nil {
				t.Fatalf("%q: parse run %q: %v", test.Expr, want, err)
			}
			next, ok := schedule.Next(cursor)
			if !ok {
				t.Fatalf("%q from %s: no run %d, want %s", test.Expr, test.Start, index, want)
			}
			if !next.Equal(wantTime) {
				t.Fatalf("%q from %s: run %d = %s, want %s",
					test.Expr, test.Start, index,
					next.UTC().Format(time.RFC3339), wantTime.UTC().Format(time.RFC3339))
			}
			cursor = next
		}
	}
}

func TestParseRejectsSpecialSchedules(t *testing.T) {
	for _, expr := range []string{"@once", "@boot", "", "bad", "0 0 * *", "0 0 * * 8", "0 0 * * /5"} {
		if _, err := Parse(expr); err == nil {
			t.Fatalf("Parse(%q) accepted an unschedulable rule", expr)
		}
	}
}

func TestSundaySevenEqualsZero(t *testing.T) {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 11, 7, 13, 40, 0, 0, shanghai)
	seven, err := Parse("0 0 * * 7")
	if err != nil {
		t.Fatal(err)
	}
	zero, err := Parse("0 0 * * 0")
	if err != nil {
		t.Fatal(err)
	}
	sevenNext, ok := seven.Next(start)
	if !ok {
		t.Fatal("no next run for 7")
	}
	zeroNext, _ := zero.Next(start)
	if !sevenNext.Equal(zeroNext) {
		t.Fatalf("7 = %s, 0 = %s", sevenNext, zeroNext)
	}
	if sevenNext.Weekday() != time.Sunday {
		t.Fatalf("expected Sunday, got %s", sevenNext.Weekday())
	}
}

func TestDayOfMonthAndWeekdayAreOred(t *testing.T) {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	// 2026-11-01 is a Sunday; the rule must fire on the 1st AND on Mondays.
	schedule, err := Parse("0 0 1 * 1")
	if err != nil {
		t.Fatal(err)
	}
	cursor := time.Date(2026, 10, 31, 12, 0, 0, 0, shanghai)
	var got []string
	for index := 0; index < 6; index++ {
		next, ok := schedule.Next(cursor)
		if !ok {
			t.Fatal("unexpected end of runs")
		}
		got = append(got, next.Format("2006-01-02"))
		cursor = next
	}
	want := []string{"2026-11-01", "2026-11-02", "2026-11-09", "2026-11-16", "2026-11-23", "2026-11-30"}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("run %d = %s, want %s (all: %v)", index, got[index], want[index], got)
		}
	}
}

func TestImpossibleRuleHasNoNextRun(t *testing.T) {
	schedule, err := Parse("0 0 30 2 *")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if next, ok := schedule.Next(start); ok {
		t.Fatalf("expected no run, got %s", next)
	}
}

// The panel timezone drives the calendar: 02:30 in New York exists on the day
// before the spring shift and does not exist on the shift day itself.
func TestDSTSpringForwardKeepsTheRun(t *testing.T) {
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	schedule, err := Parse("30 2 * * *")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2027, 3, 13, 12, 0, 0, 0, newYork)
	first, ok := schedule.Next(start)
	if !ok {
		t.Fatal("no run on the day before the shift")
	}
	if got := first.Format("2006-01-02 15:04"); got != "2027-03-14 03:30" {
		t.Fatalf("spring forward run = %s, want 2027-03-14 03:30", got)
	}
	if offset := first.Format("-07:00"); offset != "-04:00" {
		t.Fatalf("expected EDT (-04:00), got %s", offset)
	}
	second, ok := schedule.Next(first)
	if !ok {
		t.Fatal("no run after the shift")
	}
	if got := second.Format("2006-01-02 15:04"); got != "2027-03-15 02:30" {
		t.Fatalf("run after the shift = %s, want 2027-03-15 02:30", got)
	}
}

// The autumn shift repeats 01:00-01:59. A rule at 01:30 must fire once, and
// the following run must be the next calendar day.
func TestDSTFallBackFiresOnce(t *testing.T) {
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	schedule, err := Parse("30 1 * * *")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2027, 11, 6, 12, 0, 0, 0, newYork)
	first, ok := schedule.Next(start)
	if !ok {
		t.Fatal("no run on the shift day")
	}
	if got := first.Format("2006-01-02 15:04"); got != "2027-11-07 01:30" {
		t.Fatalf("fall back run = %s, want 2027-11-07 01:30", got)
	}
	second, ok := schedule.Next(first)
	if !ok {
		t.Fatal("no run after the shift")
	}
	if got := second.Format("2006-01-02 15:04"); got != "2027-11-08 01:30" {
		t.Fatalf("second run = %s, want 2027-11-08 01:30", got)
	}
	if second.Sub(first) != 24*time.Hour+time.Hour {
		t.Fatalf("gap = %s, want 25h", second.Sub(first))
	}
}

// A panel timezone without DST must be unaffected by the rules above.
func TestAsiaShanghaiHasNoDSTShift(t *testing.T) {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	schedule, err := Parse("30 2 * * *")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2027, 3, 13, 12, 0, 0, 0, shanghai)
	first, _ := schedule.Next(start)
	second, _ := schedule.Next(first)
	if second.Sub(first) != 24*time.Hour {
		t.Fatalf("gap = %s, want 24h", second.Sub(first))
	}
}

func TestSecondLevelRules(t *testing.T) {
	schedule, err := Parse("*/10 * * * * *")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 11, 7, 13, 40, 1, 0, time.UTC)
	next, ok := schedule.Next(start)
	if !ok {
		t.Fatal("no run")
	}
	if got := next.Format("15:04:05"); got != "13:40:10" {
		t.Fatalf("next = %s, want 13:40:10", got)
	}
}
