package config

import "testing"

func TestParseConfigSh(t *testing.T) {
	content := "\ufeff" + `## comment line
AutoDelCron="true"
AutoAddCron='false'
CpuWarn=80
export PUSH_KEY=""
export BARK_ICON="https://qn.whyour.cn/logo.png"
ProxyUrl="http://127.0.0.1:7890" # inline comment
Escaped="a\"b\\c"
WithHash="a#b"
not an assignment
123INVALID="x"
Unterminated="oops
CommandTimeoutTime="1h"
`
	got := ParseConfigSh(content)

	want := map[string]string{
		"AutoDelCron":        "true",
		"AutoAddCron":        "false",
		"CpuWarn":            "80",
		"PUSH_KEY":           "",
		"BARK_ICON":          "https://qn.whyour.cn/logo.png",
		"ProxyUrl":           "http://127.0.0.1:7890",
		"Escaped":            `a"b\c`,
		"WithHash":           "a#b",
		"CommandTimeoutTime": "1h",
	}

	if len(got) != len(want) {
		t.Fatalf("got %d assignments (%v), want %d", len(got), got, len(want))
	}
	for key, expected := range want {
		if got[key] != expected {
			t.Errorf("%s = %q, want %q", key, got[key], expected)
		}
	}
}

func TestNormalizeBaseURL(t *testing.T) {
	cases := map[string]string{
		"":        "",
		"ql":      "/ql",
		"/ql/":    "/ql",
		"/ql/sub": "/ql/sub",
		"  /x/  ": "/x",
	}
	for input, expected := range cases {
		if got := NormalizeBaseURL(input); got != expected {
			t.Errorf("NormalizeBaseURL(%q) = %q, want %q", input, got, expected)
		}
	}
}

func TestConfigHelpers(t *testing.T) {
	cfg := &Config{Values: map[string]string{
		"Flag":    "true",
		"Other":   "1",
		"Number":  "42",
		"Empty":   "",
		"Invalid": "abc",
	}}

	if !cfg.GetBool("Flag") || !cfg.GetBool("Other") {
		t.Error("expected truthy flags")
	}
	if cfg.GetBool("Empty") || cfg.GetBool("Invalid") || cfg.GetBool("Missing") {
		t.Error("expected falsy flags")
	}
	if got := cfg.GetInt("Number", 7); got != 42 {
		t.Errorf("GetInt(Number) = %d, want 42", got)
	}
	if got := cfg.GetInt("Invalid", 7); got != 7 {
		t.Errorf("GetInt(Invalid) = %d, want 7", got)
	}
	if got := cfg.GetDefault("Empty", "fallback"); got != "fallback" {
		t.Errorf("GetDefault(Empty) = %q, want fallback", got)
	}
}
