package cronexpr

import "testing"

func TestIsNodeCron(t *testing.T) {
	cases := []struct {
		schedule string
		extra    int
		want     bool
	}{
		{"0 0 * * *", 0, false},
		{"*/5 * * * *", 0, false},
		{"0 0 * * 1-5", 0, false},
		{"0 0 1 * 1", 0, true},
		{"0 0 * * 7", 0, true},
		{"0 0 */2 * *", 0, true},
		{"0 0 1/2 * *", 0, true},
		{"@once", 0, true},
		{"@boot", 0, true},
		{"0 0 * * *", 1, true},
		{"0 0 * * SUN", 0, true},
	}
	for _, test := range cases {
		if got := IsNodeCron(test.schedule, test.extra); got != test.want {
			t.Fatalf("IsNodeCron(%q, %d) = %v, want %v", test.schedule, test.extra, got, test.want)
		}
	}
}

func TestIsValid(t *testing.T) {
	valid := []string{
		"0 0 * * *",
		"*/5 * * * *",
		"0 0 1 * 1",
		"0 0 * * SUN",
		"0 0 * * 1#2",
		"0 0 ? * *",
		"0 0 * * ?",
		"0 0 * * * *",
		"@once",
		"@boot",
		"@daily",
		"@minutely",
	}
	for _, schedule := range valid {
		if !IsValid(schedule) {
			t.Fatalf("IsValid(%q) = false, want true", schedule)
		}
	}
	invalid := []string{
		"",
		"bad",
		"0 0 * * 8",
		"0 0 * *",
		"0 0 * * * * *",
		"@sometimes",
		"0 0 * * /5",
		"0 0 * * H",
	}
	for _, schedule := range invalid {
		if IsValid(schedule) {
			t.Fatalf("IsValid(%q) = true, want false", schedule)
		}
	}
}
