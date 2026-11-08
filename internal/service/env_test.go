package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"qinglong-go/internal/config"
	"qinglong-go/internal/store"
)

func newEnvFixture(t *testing.T) (*EnvService, *config.Config) {
	t.Helper()
	root := t.TempDir()
	data := filepath.Join(root, "data")
	cfg := &config.Config{
		RootPath:          root,
		DataPath:          data,
		ConfigPath:        filepath.Join(data, "config"),
		DBPath:            filepath.Join(data, "db"),
		SyslogPath:        filepath.Join(data, "syslog"),
		UploadPath:        filepath.Join(data, "upload"),
		ScriptPath:        filepath.Join(data, "scripts"),
		ScriptHistoryPath: filepath.Join(data, "script-history"),
		BakPath:           filepath.Join(data, "bak"),
		LogPath:           filepath.Join(data, "log"),
		TmpPath:           filepath.Join(root, ".tmp"),
	}
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatalf("ensure dirs: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "shell", "preload"), 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(cfg.DBPath, "database.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewEnvService(cfg, st, nil), cfg
}

func TestIsAllowedTaskEnvName(t *testing.T) {
	allowed := []string{"ALPHA", "a_b1", "_x", "myVar"}
	for _, name := range allowed {
		if !IsAllowedTaskEnvName(name) {
			t.Fatalf("%q should be allowed", name)
		}
	}
	blocked := []string{"", "1A", "A-B", "PATH", "ID", "QL_DIR", "NODE_PATH",
		"FILE_X", "DIR_X", "PYTHONPATH", "GIT_CONFIG_X", "BASH_ENV", "TASK_BEFORE"}
	for _, name := range blocked {
		if IsAllowedTaskEnvName(name) {
			t.Fatalf("%q should be reserved", name)
		}
	}
}

// TestPrecisionPosition pins the JS `toPrecision(16)` rounding the panel uses
// before storing a position.
func TestPrecisionPosition(t *testing.T) {
	cases := []struct{ in, want float64 }{
		{4500000000000000, 4500000000000000},
		{4499999990000000, 4499999990000000},
		{4499999990000000.5, 4499999990000000},
		{4500000000000000.7, 4500000000000000},
		{2249999995000000, 2249999995000000},
	}
	for _, test := range cases {
		if got := precisionPosition(test.in); got != test.want {
			t.Fatalf("precisionPosition(%v) = %v, want %v", test.in, got, test.want)
		}
	}
}

func TestEnvCreateAndPosition(t *testing.T) {
	envs, cfg := newEnvFixture(t)
	ctx := context.Background()

	created, err := envs.Create(ctx, []EnvInput{
		{Name: "ALPHA", Value: "1"},
		{Name: "BETA", Value: "2"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(created) != 2 {
		t.Fatalf("expected two rows, got %d", len(created))
	}
	// The list is ordered by position DESC and every new row gets a smaller
	// position, so a new variable is appended at the end of the list.
	list, err := envs.List(ctx, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 || *list[0].Name != "ALPHA" || *list[1].Name != "BETA" {
		t.Fatalf("unexpected order: %+v", list)
	}
	if *list[0].Position <= *list[1].Position {
		t.Fatalf("positions should decrease: %v <= %v", *list[0].Position, *list[1].Position)
	}

	// env.sh carries every enabled row, joined with & for repeated names.
	content, err := os.ReadFile(filepath.Join(cfg.RootPath, "shell", "preload", "env.sh"))
	if err != nil {
		t.Fatal(err)
	}
	want := "export ALPHA='1'\nexport BETA='2'\n"
	if string(content) != want {
		t.Fatalf("env.sh = %q, want %q", content, want)
	}
	js, _ := os.ReadFile(filepath.Join(cfg.RootPath, "shell", "preload", "env.js"))
	if string(js) != "process.env[\"ALPHA\"]=\"1\";\nprocess.env[\"BETA\"]=\"2\";\n" {
		t.Fatalf("env.js = %q", js)
	}
	py, _ := os.ReadFile(filepath.Join(cfg.RootPath, "shell", "preload", "env.py"))
	if string(py) != "import os\nos.environ['ALPHA']='''1'''\nos.environ['BETA']='''2'''\n" {
		t.Fatalf("env.py = %q", py)
	}
}

func TestEnvDuplicateNameJoinsValues(t *testing.T) {
	envs, cfg := newEnvFixture(t)
	ctx := context.Background()
	if _, err := envs.Create(ctx, []EnvInput{
		{Name: "MULTI", Value: "a"},
		{Name: "MULTI", Value: "b"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(cfg.RootPath, "shell", "preload", "env.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "export MULTI='a&b'\n" {
		t.Fatalf("env.sh = %q", content)
	}
}

func TestEnvDisabledRowsAreSkipped(t *testing.T) {
	envs, cfg := newEnvFixture(t)
	ctx := context.Background()
	created, err := envs.Create(ctx, []EnvInput{{Name: "ALPHA", Value: "1"}, {Name: "BETA", Value: "2"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := envs.SetDisabled(ctx, []int64{created[0].ID}, true); err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(filepath.Join(cfg.RootPath, "shell", "preload", "env.sh"))
	if string(content) != "export BETA='2'\n" {
		t.Fatalf("env.sh = %q", content)
	}
}

func TestEnvMoveReorders(t *testing.T) {
	envs, _ := newEnvFixture(t)
	ctx := context.Background()
	if _, err := envs.Create(ctx, []EnvInput{
		{Name: "A", Value: "1"}, {Name: "B", Value: "2"}, {Name: "C", Value: "3"},
	}); err != nil {
		t.Fatal(err)
	}
	list, _ := envs.List(ctx, "")
	// Order after creation is A, B, C (new rows get smaller positions).
	if *list[0].Name != "A" || *list[2].Name != "C" {
		t.Fatalf("unexpected initial order: %v %v %v",
			*list[0].Name, *list[1].Name, *list[2].Name)
	}
	last := list[len(list)-1]
	// Dragging C to the first row lifts it above the current head.
	if _, err := envs.Move(ctx, last.ID, len(list)-1, 0); err != nil {
		t.Fatalf("move: %v", err)
	}
	moved, _ := envs.List(ctx, "")
	if *moved[0].Name != "C" {
		t.Fatalf("expected C to move to the top, got %v", *moved[0].Name)
	}
}

func TestEnvReservedNameRejected(t *testing.T) {
	envs, _ := newEnvFixture(t)
	ctx := context.Background()
	_, err := envs.Create(ctx, []EnvInput{{Name: "PATH", Value: "/bin"}})
	statusErr, ok := err.(*StatusError)
	if !ok || statusErr.Status != 400 {
		t.Fatalf("expected a 400 status error, got %v", err)
	}
}

func TestEnvLabelsMerge(t *testing.T) {
	envs, _ := newEnvFixture(t)
	ctx := context.Background()
	created, err := envs.Create(ctx, []EnvInput{{Name: "A", Value: "1", Labels: []string{"x"}, LabelsSet: true}})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := envs.AddLabels(ctx, []int64{created[0].ID}, []string{"x", "y"})
	if err != nil {
		t.Fatal(err)
	}
	labels := labelList(updated[0].Labels)
	if len(labels) != 2 || labels[0] != "x" || labels[1] != "y" {
		t.Fatalf("labels = %v", labels)
	}
	removed, err := envs.RemoveLabels(ctx, []int64{created[0].ID}, []string{"x"})
	if err != nil {
		t.Fatal(err)
	}
	if labels := labelList(removed[0].Labels); len(labels) != 1 || labels[0] != "y" {
		t.Fatalf("labels after removal = %v", labels)
	}
}

// TestEnvSetEnvsEscapesQuotes pins the three generated files for values that
// contain quotes and backslashes.
func TestEnvSetEnvsEscapesQuotes(t *testing.T) {
	envs, cfg := newEnvFixture(t)
	ctx := context.Background()
	if _, err := envs.Create(ctx, []EnvInput{{Name: "Q", Value: `it's a \ test`}}); err != nil {
		t.Fatal(err)
	}
	shell, _ := os.ReadFile(filepath.Join(cfg.RootPath, "shell", "preload", "env.sh"))
	if string(shell) != "export Q='it'\\''s a \\ test'\n" {
		t.Fatalf("env.sh = %q", shell)
	}
	js, _ := os.ReadFile(filepath.Join(cfg.RootPath, "shell", "preload", "env.js"))
	if string(js) != "process.env[\"Q\"]=\"it's a \\\\ test\";\n" {
		t.Fatalf("env.js = %q", js)
	}
	py, _ := os.ReadFile(filepath.Join(cfg.RootPath, "shell", "preload", "env.py"))
	if string(py) != "import os\nos.environ['Q']='''it\\'s a \\\\ test'''\n" {
		t.Fatalf("env.py = %q", py)
	}
}

func TestBootstrapRewritesStaleEnvironmentFiles(t *testing.T) {
	svc, cfg := newEnvFixture(t)
	ctx := context.Background()
	if _, err := svc.Create(ctx, []EnvInput{{Name: "BOOT_TOKEN", Value: "from-db"}}); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Simulate files left behind by an older version: the rows are the source
	// of truth, so boot must regenerate them.
	preload := filepath.Join(cfg.RootPath, "shell", "preload")
	for _, name := range []string{"env.sh", "env.js", "env.py"} {
		if err := os.WriteFile(filepath.Join(preload, name), []byte("# stale\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := svc.Bootstrap(ctx); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	expected := map[string]string{
		"env.sh": "BOOT_TOKEN='from-db'",
		"env.js": `process.env["BOOT_TOKEN"]="from-db";`,
		"env.py": "os.environ['BOOT_TOKEN']='''from-db'''",
	}
	for name, want := range expected {
		content, err := os.ReadFile(filepath.Join(preload, name))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(content), want) {
			t.Fatalf("%s = %q, want it to contain %q", name, string(content), want)
		}
	}
}
