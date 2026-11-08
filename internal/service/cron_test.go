package service

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"qinglong-go/internal/config"
	"qinglong-go/internal/store"
)

func TestGetUniqPath(t *testing.T) {
	cases := []struct {
		command string
		id      int64
		hasID   bool
		want    string
	}{
		{"task x.js", 0, false, "x"},
		{"task x.js", 7, true, "x_7"},
		{"task dir/y.py", 0, false, "dir_y"},
		{"task a/b/c.sh", 3, true, "b_c_3"},
		{"ql repo https://example.com/a.git", 0, false, "ql"},
		{"task -m 300 z.js", 9, true, "z_9"},
		{"task noext", 0, false, "noext"},
	}
	for _, test := range cases {
		if got := GetUniqPath(test.command, test.id, test.hasID); got != test.want {
			t.Fatalf("GetUniqPath(%q, %d, %v) = %q, want %q",
				test.command, test.id, test.hasID, got, test.want)
		}
	}
}

func TestMakeCommand(t *testing.T) {
	row := &store.Crontab{
		ID:         12,
		Command:    " task x.js ",
		LogName:    sql.NullString{String: "x_12", Valid: true},
		TaskBefore: sql.NullString{String: "echo 'a'\necho b;", Valid: true},
		TaskAfter:  sql.NullString{String: "echo done", Valid: true},
		WorkDir:    sql.NullString{String: "/tmp/a b", Valid: true},
	}
	svc := &CronService{}
	got := svc.MakeCommand(row, false)
	want := `real_time=false no_tee=true ID=12 log_name=x_12 task_before='echo '\''a'\'';echo b;' task_after='echo done' work_dir='/tmp/a b' task x.js`
	if got != want {
		t.Fatalf("MakeCommand mismatch:\n got %q\nwant %q", got, want)
	}

	row.Command = "ql hello"
	row.TaskBefore = sql.NullString{}
	row.TaskAfter = sql.NullString{}
	row.WorkDir = sql.NullString{}
	row.LogName = sql.NullString{}
	got = svc.MakeCommand(row, true)
	want = "real_time=true no_tee=true ID=12 ql hello"
	if got != want {
		t.Fatalf("MakeCommand prefix mismatch: %q", got)
	}
}

func TestRefreshCrontabOrderAndBytes(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(dir, "database.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	// RootPath is required: CronService builds the task runner, which
	// materializes the preload hooks under <root>/shell/preload. Leaving it
	// empty made the test write into the package directory.
	cfg := &config.Config{
		RootPath:   dir,
		DataPath:   dir,
		ConfigPath: filepath.Join(dir, "config"),
		LogPath:    filepath.Join(dir, "log"),
	}
	svc := NewCronService(cfg, st, slog.Default())

	rows := []*store.Crontab{
		{
			Command: "task a.js", Schedule: nullString("0 0 * * *"),
			Status: nullInt(CronStatusIdle), IsDisabled: nullInt(0), IsPinned: nullInt(0),
			LogName:    nullString("a"),
			TaskBefore: nullString("echo 'a'\necho b;"),
			WorkDir:    nullString("/tmp/a b"),
			CreatedAt:  "2026-11-07 05:00:00.000 +00:00", UpdatedAt: "2026-11-07 05:00:00.000 +00:00",
		},
		{
			Command: "task b.js", Schedule: nullString("*/5 * * * *"),
			Status: nullInt(CronStatusIdle), IsDisabled: nullInt(0), IsPinned: nullInt(1),
			LogName:   nullString("b"),
			CreatedAt: "2026-11-07 05:01:00.000 +00:00", UpdatedAt: "2026-11-07 05:01:00.000 +00:00",
		},
		{
			Command: "task c.js", Schedule: nullString("@once"),
			Status: nullInt(CronStatusIdle), IsDisabled: nullInt(1), IsPinned: nullInt(0),
			LogName:   nullString("c"),
			CreatedAt: "2026-11-07 05:02:00.000 +00:00", UpdatedAt: "2026-11-07 05:02:00.000 +00:00",
		},
		{
			Command: "task d.js", Schedule: nullString("0 0 1 * 1"),
			Status: nullInt(CronStatusIdle), IsDisabled: nullInt(0), IsPinned: nullInt(0),
			LogName:   nullString("d"),
			CreatedAt: "2026-11-07 05:03:00.000 +00:00", UpdatedAt: "2026-11-07 05:03:00.000 +00:00",
		},
		{
			Command: "task e.js", Schedule: nullString("0 0 * * *"),
			Status: nullInt(CronStatusIdle), IsDisabled: nullInt(0), IsPinned: nullInt(0),
			ExtraSchedules: nullString(`[{"schedule":"0 1 * * *"}]`),
			LogName:        nullString("e"),
			CreatedAt:      "2026-11-07 05:04:00.000 +00:00", UpdatedAt: "2026-11-07 05:04:00.000 +00:00",
		},
	}
	for _, row := range rows {
		if err := st.InsertCrontab(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.RefreshCrontab(ctx); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(dir, "config", "crontab.list"))
	if err != nil {
		t.Fatal(err)
	}
	want := "" +
		"*/5 * * * * real_time=false no_tee=true ID=2 log_name=b task b.js\n" +
		"# 0 0 * * * real_time=false no_tee=true ID=5 log_name=e task e.js\n" +
		"# 0 0 1 * 1 real_time=false no_tee=true ID=4 log_name=d task d.js\n" +
		"0 0 * * * real_time=false no_tee=true ID=1 log_name=a task_before='echo '\\''a'\\'';echo b;' work_dir='/tmp/a b' task a.js\n" +
		"# @once real_time=false no_tee=true ID=3 log_name=c task c.js\n"
	if string(content) != want {
		t.Fatalf("crontab.list mismatch:\n got %q\nwant %q", string(content), want)
	}

	// setCrontab marks every row saved, like the Node service.
	items, err := st.AllCrontabs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if !item.Saved {
			t.Fatalf("task %d was not marked saved", item.ID)
		}
	}
}
