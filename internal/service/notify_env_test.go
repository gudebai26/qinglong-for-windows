package service

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"qinglong-go/internal/config"
	"qinglong-go/internal/store"
)

// newNotifyFixture opens an empty store, which is what a fresh panel has.
func newNotifyFixture(t *testing.T) (*NotifyService, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{
		RootPath:   dir,
		DataPath:   filepath.Join(dir, "data"),
		ConfigPath: filepath.Join(dir, "data", "config"),
		DBPath:     filepath.Join(dir, "data", "db"),
	}
	if err := os.MkdirAll(cfg.DBPath, 0o755); err != nil {
		t.Fatalf("mkdir db: %v", err)
	}
	st, err := store.Open(filepath.Join(cfg.DBPath, "database.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewNotifyService(cfg, st, logger), st
}

// The settings page used to store the channel only in Auths.notification, which
// no task ever read: a script calling notify.send() saw an empty channel table
// unless the operator duplicated the keys in config.sh. ScriptEnv is the bridge.
func TestNotifyScriptEnvMapsSavedChannel(t *testing.T) {
	svc, st := newNotifyFixture(t)
	ctx := context.Background()

	if got := svc.ScriptEnv(ctx); len(got) != 0 {
		t.Fatalf("fresh store returned %v, want an empty map", got)
	}

	payload := map[string]any{
		"type":               "webhook",
		"webhookUrl":         "https://example.test/hook?title=$title",
		"webhookMethod":      "POST",
		"webhookBody":        `{"title":"$title"}`,
		"webhookContentType": "application/json",
		// Fields the chosen channel does not use are submitted empty.
		"barkPush":       "",
		"webhookHeaders": "",
		// The mail channel shares one recipient list.
		"emailUser": "sender@example.test",
		"emailPass": "secret",
		"emailTo":   "a@example.test;b@example.test",
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	if err := st.UpsertAuthRow(ctx, store.AuthTypeNotification, encoded); err != nil {
		t.Fatalf("save notification config: %v", err)
	}

	env := svc.ScriptEnv(ctx)
	want := map[string]string{
		"WEBHOOK_URL":          "https://example.test/hook?title=$title",
		"WEBHOOK_METHOD":       "POST",
		"WEBHOOK_BODY":         `{"title":"$title"}`,
		"WEBHOOK_CONTENT_TYPE": "application/json",
		"SMTP_EMAIL":           "sender@example.test",
		"SMTP_PASSWORD":        "secret",
		"SMTP_EMAIL_TO":        "a@example.test;b@example.test",
		// The JS notifier falls back to the legacy name, the Python one only
		// knows SMTP_EMAIL_TO; publish both.
		"SMTP_TO": "a@example.test;b@example.test",
	}
	for key, value := range want {
		if env[key] != value {
			t.Errorf("%s = %q, want %q", key, env[key], value)
		}
	}
	for _, key := range []string{"BARK_PUSH", "WEBHOOK_HEADERS"} {
		if _, ok := env[key]; ok {
			t.Errorf("%s should be dropped when empty", key)
		}
	}
}

// Every target name must exist in one of the notifier tables the panel ships
// (JS preload, Python preload, and the sendNotify.js/notify.py copies scripts
// require), otherwise the injected variable is dead weight and the channel
// stays silent.
func TestNotifyScriptEnvTargetsExistInPreload(t *testing.T) {
	declared := map[string]struct{}{}
	files := []string{
		filepath.Join("..", "..", "shell", "preload", "__ql_notify__.js"),
		filepath.Join("..", "..", "shell", "preload", "__ql_notify__.py"),
	}
	found := 0
	for _, name := range files {
		source, err := os.ReadFile(name)
		if err != nil {
			continue
		}
		found++
		// JS: `KEY: ''`, Python: `'KEY': ''`.
		for _, match := range regexp.MustCompile(`(?m)^\s+'?([A-Z][A-Z0-9_]*)'?:`).FindAllStringSubmatch(string(source), -1) {
			declared[match[1]] = struct{}{}
		}
	}
	if found == 0 {
		t.Skip("no notifier table available")
	}
	if len(declared) < 60 {
		t.Fatalf("parsed only %d push_config keys, the pattern is probably wrong", len(declared))
	}
	for field, key := range notificationEnvKeys {
		if _, ok := declared[key]; !ok {
			t.Errorf("%s maps to %s, which no notifier reads", field, key)
		}
	}
	for _, key := range []string{"SMTP_SERVER", "SMTP_SSL", "SMTP_TO"} {
		if _, ok := declared[key]; !ok {
			t.Errorf("derived variable %s is not read by any notifier", key)
		}
	}
}

// The mail channel stores a service name ("QQ"); the Python notifier only knows
// SMTP_SERVER/SMTP_SSL, so the bridge has to spell the host out.
func TestNotifyScriptEnvDerivesSMTPServer(t *testing.T) {
	svc, st := newNotifyFixture(t)
	ctx := context.Background()
	save := func(payload map[string]any) {
		t.Helper()
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("encode payload: %v", err)
		}
		if err := st.UpsertAuthRow(ctx, store.AuthTypeNotification, encoded); err != nil {
			t.Fatalf("save notification config: %v", err)
		}
	}

	save(map[string]any{"type": "email", "emailService": "QQ", "emailUser": "a@qq.com",
		"emailPass": "x", "emailTo": "b@qq.com"})
	env := svc.ScriptEnv(ctx)
	if env["SMTP_SERVER"] != "smtp.qq.com:465" || env["SMTP_SSL"] != "true" {
		t.Errorf("QQ service -> %q / %q, want smtp.qq.com:465 / true", env["SMTP_SERVER"], env["SMTP_SSL"])
	}

	save(map[string]any{"type": "email", "emailService": "smtp.example.test",
		"emailUser": "a@example.test", "emailPass": "x", "emailTo": "b@example.test"})
	env = svc.ScriptEnv(ctx)
	if env["SMTP_SERVER"] != "smtp.example.test:465" {
		t.Errorf("hostname service -> %q, want smtp.example.test:465", env["SMTP_SERVER"])
	}

	save(map[string]any{"type": "bark", "barkPush": "https://api.day.app/key"})
	env = svc.ScriptEnv(ctx)
	if env["SMTP_SERVER"] != "" || env["BARK_PUSH"] != "https://api.day.app/key" {
		t.Errorf("bark config leaked mail fields: %v", env)
	}
}
