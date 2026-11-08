package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"qinglong-go/internal/config"
)

// testConfig builds a Config pointing at a throw-away root.
func testConfig(t *testing.T, root string, values map[string]string) *config.Config {
	t.Helper()
	data := filepath.Join(root, "data")
	if values == nil {
		values = map[string]string{}
	}
	return &config.Config{
		RootPath:   root,
		DataPath:   data,
		ConfigPath: filepath.Join(data, "config"),
		DBPath:     filepath.Join(data, "db"),
		SyslogPath: filepath.Join(data, "syslog"),
		UploadPath: filepath.Join(data, "upload"),
		ScriptPath: filepath.Join(data, "scripts"),
		LogPath:    filepath.Join(data, "log"),
		RepoPath:   filepath.Join(data, "repo"),
		SamplePath: filepath.Join(root, "sample"),
		TmpPath:    filepath.Join(root, ".tmp"),
		DepPath:    filepath.Join(data, "deps"),
		Port:       5700,
		Values:     values,
	}
}

func TestSplitCommand(t *testing.T) {
	cases := []struct {
		input string
		want  []string
	}{
		{"task a.js", []string{"task", "a.js"}},
		{"task a.js now -- --flag 1", []string{"task", "a.js", "now", "--", "--flag", "1"}},
		{"real_time=false ID=1 task a.js", []string{"real_time=false", "ID=1", "task", "a.js"}},
		{"task_before='a;b' task a.js", []string{"task_before=a;b", "task", "a.js"}},
		{"task_before='it'\\''s' task a.js", []string{"task_before=it's", "task", "a.js"}},
		{`work_dir="my dir" task a.js`, []string{"work_dir=my dir", "task", "a.js"}},
		{"  task   a.js  ", []string{"task", "a.js"}},
	}
	for _, item := range cases {
		got, err := splitCommand(item.input)
		if err != nil {
			t.Fatalf("splitCommand(%q) error: %v", item.input, err)
		}
		if len(got) != len(item.want) {
			t.Fatalf("splitCommand(%q) = %v, want %v", item.input, got, item.want)
		}
		for index := range got {
			if got[index] != item.want[index] {
				t.Fatalf("splitCommand(%q)[%d] = %q, want %q", item.input, index, got[index], item.want[index])
			}
		}
	}

	if _, err := splitCommand("task 'a.js"); err == nil {
		t.Fatal("expected an error for an unterminated quote")
	}
}

func TestParseCommandModes(t *testing.T) {
	root := t.TempDir()
	cfg := testConfig(t, root, nil)
	r := New(cfg, nil)
	// The interpreters are resolved from PATH; only the mode matters here.
	r.node = "node"

	tests := []struct {
		command  string
		mode     Mode
		script   string
		args     []string
		envParam string
		numParam string
		timeout  string
		showLog  bool
	}{
		{command: "task a.js", mode: ModeNormal, script: "a.js"},
		{command: "task a.js now", mode: ModeNormal, script: "a.js"},
		{command: "task a.js -- --foo 1", mode: ModeNormal, script: "a.js", args: []string{"--foo", "1"}},
		{command: "task a.js conc Test 1-2", mode: ModeConcurrent, script: "a.js", envParam: "Test", numParam: "1-2"},
		{command: "task a.js desi Test 1 3", mode: ModeDesignated, script: "a.js", envParam: "Test", numParam: "1 3"},
		{command: "task a.js extra", mode: ModeElse, script: "a.js", args: []string{"extra"}},
		{command: "task node a.js", mode: ModeElse, script: "node", args: []string{"a.js"}},
		{command: "task -m 30 a.js", mode: ModeNormal, script: "a.js", timeout: "30"},
		{command: "task -l -m 1.5m a.js", mode: ModeNormal, script: "a.js", timeout: "1.5m", showLog: true},
	}

	for _, item := range tests {
		plan, err := r.ParseCommand(item.command)
		if err != nil {
			t.Fatalf("ParseCommand(%q) error: %v", item.command, err)
		}
		if plan.Mode != item.mode {
			t.Fatalf("ParseCommand(%q) mode = %v, want %v", item.command, plan.Mode, item.mode)
		}
		if plan.Script != item.script {
			t.Fatalf("ParseCommand(%q) script = %q, want %q", item.command, plan.Script, item.script)
		}
		if strings.Join(plan.Args, "|") != strings.Join(item.args, "|") {
			t.Fatalf("ParseCommand(%q) args = %v, want %v", item.command, plan.Args, item.args)
		}
		if plan.EnvParam != item.envParam || plan.NumParam != item.numParam {
			t.Fatalf("ParseCommand(%q) account = (%q,%q), want (%q,%q)",
				item.command, plan.EnvParam, plan.NumParam, item.envParam, item.numParam)
		}
		if plan.TimeoutRaw != item.timeout {
			t.Fatalf("ParseCommand(%q) timeout = %q, want %q", item.command, plan.TimeoutRaw, item.timeout)
		}
		if plan.ShowLog != item.showLog {
			t.Fatalf("ParseCommand(%q) showLog = %v, want %v", item.command, plan.ShowLog, item.showLog)
		}
	}
}

func TestParseCommandAssignments(t *testing.T) {
	r := New(testConfig(t, t.TempDir(), nil), nil)
	plan, err := r.ParseCommand("real_time=true no_tee=true ID=7 log_name=my/log task_before='echo hi' task a.js")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Env["real_time"] != "true" || plan.Env["no_tee"] != "true" || plan.Env["ID"] != "7" {
		t.Fatalf("assignments not parsed: %#v", plan.Env)
	}
	if plan.Env["log_name"] != "my/log" {
		t.Fatalf("log_name = %q", plan.Env["log_name"])
	}
	if plan.Env["task_before"] != "echo hi" {
		t.Fatalf("task_before = %q", plan.Env["task_before"])
	}
}

func TestParseCommandMissingAccount(t *testing.T) {
	r := New(testConfig(t, t.TempDir(), nil), nil)
	plan, err := r.ParseCommand("task a.js conc")
	if err != nil {
		t.Fatal(err)
	}
	if plan.FailureCode != 1 || !strings.Contains(plan.Failure, "缺少并发运行的环境变量参数") {
		t.Fatalf("unexpected failure: %d %q", plan.FailureCode, plan.Failure)
	}
}

func TestParseTimeout(t *testing.T) {
	valid := map[string]time.Duration{
		"":      0,
		"0":     0,
		"30":    30 * time.Second,
		"0.5":   500 * time.Millisecond,
		".5":    500 * time.Millisecond,
		"2m":    2 * time.Minute,
		"1.5h":  90 * time.Minute,
		"1d":    24 * time.Hour,
		"1e3":   1000 * time.Second,
		"1.5e1": 15 * time.Second,
	}
	for raw, want := range valid {
		got, err := ParseTimeout(raw)
		if err != nil {
			t.Fatalf("ParseTimeout(%q) error: %v", raw, err)
		}
		if got != want {
			t.Fatalf("ParseTimeout(%q) = %v, want %v", raw, got, want)
		}
	}
	for _, raw := range []string{"abc", "5x", "-1", "1m30", "1.2.3"} {
		if _, err := ParseTimeout(raw); err == nil {
			t.Fatalf("ParseTimeout(%q) should fail", raw)
		}
	}
}

func TestResolveLogPath(t *testing.T) {
	cfg := testConfig(t, t.TempDir(), nil)
	r := New(cfg, nil)
	now := time.Date(2026, 11, 7, 14, 10, 5, 123_000_000, time.Local)

	cases := []struct {
		name   string
		plan   *Plan
		expect string
		mode   logMode
	}{
		{
			name:   "plain script",
			plan:   &Plan{FileParam: "a.js", Env: map[string]string{}},
			expect: "a/2026-11-07-14-10-05-123.log",
			mode:   logTee,
		},
		{
			name:   "id suffix",
			plan:   &Plan{FileParam: "a.js", Env: map[string]string{"ID": "7"}},
			expect: "a_7/2026-11-07-14-10-05-123.log",
			mode:   logTee,
		},
		{
			name:   "sub directory",
			plan:   &Plan{FileParam: "sub/a.js", Env: map[string]string{"ID": "7"}},
			expect: "sub_a_7/2026-11-07-14-10-05-123.log",
			mode:   logTee,
		},
		{
			name:   "log_name wins",
			plan:   &Plan{FileParam: "a.js", Env: map[string]string{"ID": "7", "log_name": "custom"}},
			expect: "custom/2026-11-07-14-10-05-123.log",
			mode:   logTee,
		},
		{
			name:   "real_log_path wins",
			plan:   &Plan{FileParam: "a.js", Env: map[string]string{"real_log_path": "x/y.log"}},
			expect: "x/y.log",
			mode:   logTee,
		},
		{
			name:   "no_tee",
			plan:   &Plan{FileParam: "a.js", Env: map[string]string{"no_tee": "true"}},
			expect: "a/2026-11-07-14-10-05-123.log",
			mode:   logAppend,
		},
		{
			name:   "real_time",
			plan:   &Plan{FileParam: "a.js", Env: map[string]string{"real_time": "true"}},
			expect: "a/2026-11-07-14-10-05-123.log",
			mode:   logStdout,
		},
		{
			name:   "dev null",
			plan:   &Plan{FileParam: "a.js", Env: map[string]string{"log_name": "/dev/null"}},
			expect: "/dev/null",
			mode:   logDiscard,
		},
	}

	for _, item := range cases {
		got := r.resolveLog(item.plan, now)
		if got.Path != item.expect {
			t.Fatalf("%s: path = %q, want %q", item.name, got.Path, item.expect)
		}
		if got.Mode != item.mode {
			t.Fatalf("%s: mode = %v, want %v", item.name, got.Mode, item.mode)
		}
	}
}

func TestExpandRange(t *testing.T) {
	cases := []struct {
		spec   string
		length int
		want   string
	}{
		{"", 3, ""},
		{"1-max", 3, "1 2 3"},
		{"max-1", 3, "3 2 1"},
		{"1-3", 5, "1 2 3"},
		{"3-1", 5, "3 2 1"},
		{"1 3", 5, "1 3"},
		{"1-2 4", 5, "1 2 4"},
		{"1~2", 5, "1 2"},
		{"2", 5, "2"},
	}
	for _, item := range cases {
		got := expandRange(item.spec, item.length)
		parts := make([]string, 0, len(got))
		for _, value := range got {
			parts = append(parts, itoa(value))
		}
		if strings.Join(parts, " ") != item.want {
			t.Fatalf("expandRange(%q,%d) = %v, want %s", item.spec, item.length, got, item.want)
		}
	}
}

func TestRandomDelayRules(t *testing.T) {
	cfg := testConfig(t, t.TempDir(), map[string]string{
		"RandomDelay":               "5",
		"RandomDelayFileExtensions": "js",
		"RandomDelayIgnoredMinutes": "0 30",
	})
	r := New(cfg, nil)
	// 14:10 is not an ignored minute.
	now := time.Date(2026, 11, 7, 14, 10, 0, 0, time.Local)

	cases := []struct {
		name  string
		plan  *Plan
		delay bool
	}{
		{"single js argument", &Plan{FileParam: "a.js", ArgsAll: []string{"a.js"}, Env: map[string]string{}}, true},
		{"real_time skips", &Plan{FileParam: "a.js", ArgsAll: []string{"a.js"}, Env: map[string]string{"real_time": "true"}}, false},
		{"no_delay skips", &Plan{FileParam: "a.js", ArgsAll: []string{"a.js"}, Env: map[string]string{"no_delay": "true"}}, false},
		{"extra argument skips", &Plan{FileParam: "a.js", ArgsAll: []string{"a.js", "now"}, Env: map[string]string{}}, false},
		{"other extension skips", &Plan{FileParam: "a.sh", ArgsAll: []string{"a.sh"}, Env: map[string]string{}}, false},
	}
	for _, item := range cases {
		delay, message := r.randomDelay(item.plan, now)
		if item.delay {
			if delay <= 0 || delay > 5*time.Second || message == "" {
				t.Fatalf("%s: expected a delay, got %v %q", item.name, delay, message)
			}
			if !strings.Contains(message, "任务随机延迟") {
				t.Fatalf("%s: unexpected message %q", item.name, message)
			}
		} else if delay != 0 || message != "" {
			t.Fatalf("%s: expected no delay, got %v %q", item.name, delay, message)
		}
	}

	ignored := time.Date(2026, 11, 7, 14, 30, 0, 0, time.Local)
	plan := &Plan{FileParam: "a.js", ArgsAll: []string{"a.js"}, Env: map[string]string{}}
	if delay, _ := r.randomDelay(plan, ignored); delay != 0 {
		t.Fatalf("ignored minute should skip the delay, got %v", delay)
	}
}

func TestBuildEnv(t *testing.T) {
	root := t.TempDir()
	cfg := testConfig(t, root, nil)
	r := New(cfg, nil)
	r.node = "node"
	// Pretend the preload probe already succeeded: this test covers the
	// environment assembly, not whether node_modules exists in the temp root.
	r.probeOnce.Do(func() {})
	r.probeOK = true
	r.pyProbeOnce.Do(func() {})
	r.pyProbeOK = true

	js := &Plan{Script: "a.js", Env: map[string]string{"ID": "3", "real_time": "true"}}
	env := newEnvList(r.buildEnv(js, nil))
	if env.Get("QL_DIR") != filepath.ToSlash(cfg.RootPath) || env.Get("QL_DATA_DIR") != filepath.ToSlash(cfg.DataPath) {
		t.Fatalf("panel paths missing: %q %q", env.Get("QL_DIR"), env.Get("QL_DATA_DIR"))
	}
	if env.Get("ID") != "3" || env.Get("real_time") != "true" {
		t.Fatalf("command assignments missing")
	}
	if !strings.Contains(env.Get("NODE_PATH"), cfg.DepPath) {
		t.Fatalf("NODE_PATH missing data/deps: %q", env.Get("NODE_PATH"))
	}
	if !strings.Contains(env.Get("NODE_OPTIONS"), "sitecustomize.js") {
		t.Fatalf("NODE_OPTIONS missing preload: %q", env.Get("NODE_OPTIONS"))
	}
	if env.Get("PREV_NODE_OPTIONS") != os.Getenv("NODE_OPTIONS") {
		t.Fatalf("PREV_NODE_OPTIONS = %q", env.Get("PREV_NODE_OPTIONS"))
	}

	py := &Plan{Script: "a.py", Env: map[string]string{}}
	pyEnv := newEnvList(r.buildEnv(py, nil))
	pythonPath := pyEnv.Get("PYTHONPATH")
	if !strings.HasPrefix(pythonPath, filepath.ToSlash(r.preloadDir)) {
		t.Fatalf("PYTHONPATH should start with the preload dir: %q", pythonPath)
	}
	if !strings.Contains(pythonPath, filepath.ToSlash(cfg.ConfigPath)) {
		t.Fatalf("PYTHONPATH should contain data/config: %q", pythonPath)
	}

	sh := &Plan{Script: "a.sh", Env: map[string]string{}}
	shEnv := newEnvList(r.buildEnv(sh, nil))
	if shEnv.Get("NODE_OPTIONS") != os.Getenv("NODE_OPTIONS") {
		t.Fatalf("shell tasks must not change NODE_OPTIONS")
	}
}

func TestParseEnvSh(t *testing.T) {
	content := `# comment
export A="1"
export B='two words'
export C=plain
D="not exported"
export E="quote\"inside"
`
	values := parseEnvSh(content)
	if values["A"] != "1" || values["B"] != "two words" || values["C"] != "plain" {
		t.Fatalf("unexpected values: %#v", values)
	}
	if _, ok := values["D"]; ok {
		t.Fatalf("non-exported line must be ignored")
	}
	if values["E"] != `quote"inside` {
		t.Fatalf("E = %q", values["E"])
	}
}

func TestImportConfigMergesConfigShAndLangEnv(t *testing.T) {
	root := t.TempDir()
	cfg := testConfig(t, root, nil)
	r := New(cfg, nil)

	configSh := `## panel settings and legacy notification keys
PUSH_KEY="sct123t"
WEBHOOK_URL="http://127.0.0.1:8899/hook"
QL_DIR="/somewhere/else"
PATH="/broken"
node_options="-r evil"
QL_LANG="en"
`
	if err := os.MkdirAll(cfg.ConfigPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.ConfigPath, "config.sh"), []byte(configSh), 0o644); err != nil {
		t.Fatal(err)
	}
	langEnv := filepath.Join(root, "shell", "preload", "lang_env.sh")
	if err := os.MkdirAll(filepath.Dir(langEnv), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(langEnv, []byte("export QL_LANG='zh'\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	plan := &Plan{Script: "a.sh", Env: map[string]string{}}
	env := newEnvList(r.buildEnv(plan, nil))

	if env.Get("PUSH_KEY") != "sct123t" {
		t.Fatalf("PUSH_KEY = %q, want the value from config.sh", env.Get("PUSH_KEY"))
	}
	if env.Get("WEBHOOK_URL") != "http://127.0.0.1:8899/hook" {
		t.Fatalf("WEBHOOK_URL = %q", env.Get("WEBHOOK_URL"))
	}
	// lang_env.sh is imported after config.sh, exactly like import_config().
	if env.Get("QL_LANG") != "zh" {
		t.Fatalf("QL_LANG = %q, want lang_env.sh to win", env.Get("QL_LANG"))
	}
	// Reserved variables keep the value the runner computed.
	if env.Get("QL_DIR") != filepath.ToSlash(cfg.RootPath) {
		t.Fatalf("QL_DIR was overridden by config.sh: %q", env.Get("QL_DIR"))
	}
	if env.Get("PATH") == "/broken" {
		t.Fatal("PATH must not be taken from config.sh")
	}
	if env.Get("NODE_OPTIONS") == "-r evil" {
		t.Fatal("NODE_OPTIONS must not be taken from config.sh")
	}
}

func TestImportConfigSkipsEmptyValues(t *testing.T) {
	root := t.TempDir()
	cfg := testConfig(t, root, nil)
	r := New(cfg, nil)

	if err := os.MkdirAll(cfg.ConfigPath, 0o755); err != nil {
		t.Fatal(err)
	}
	configSh := "PUSH_KEY=\nBARK_PUSH=''\nFRESH_KEY=\n"
	if err := os.WriteFile(filepath.Join(cfg.ConfigPath, "config.sh"), []byte(configSh), 0o644); err != nil {
		t.Fatal(err)
	}

	// An inherited value must survive `PUSH_KEY=` in config.sh: Node only
	// changes a shell variable there, so the child still sees the old value.
	t.Setenv("PUSH_KEY", "inherited")

	plan := &Plan{Script: "a.sh", Env: map[string]string{}}
	env := newEnvList(r.buildEnv(plan, nil))
	if env.Get("PUSH_KEY") != "inherited" {
		t.Fatalf("PUSH_KEY = %q, want the inherited value", env.Get("PUSH_KEY"))
	}
	if _, exists := env.values["FRESH_KEY"]; exists {
		t.Fatalf("an empty assignment must not create a variable: %q", env.Get("FRESH_KEY"))
	}
}
