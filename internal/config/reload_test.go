package config

import (
	"os"
	"path/filepath"
	"testing"
)

// writeConfigSh replaces data/config/config.sh and returns its path.
func writeConfigSh(t *testing.T, dataDir, content string) string {
	t.Helper()
	path := filepath.Join(dataDir, "config", "config.sh")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestReloadPicksUpSavedChanges covers the panel path: POST /api/configs/save
// writes config.sh and calls Reload, so a new RandomDelay applies to the next
// scheduled run without restarting the panel.
func TestReloadPicksUpSavedChanges(t *testing.T) {
	dataDir := t.TempDir()
	writeConfigSh(t, dataDir, "RandomDelay=\"\"\n")
	cfg := &Config{DataPath: dataDir, ConfigPath: filepath.Join(dataDir, "config")}
	if err := cfg.Reload(); err != nil {
		t.Fatal(err)
	}
	if got := cfg.GetInt("RandomDelay", 0); got != 0 {
		t.Fatalf("initial RandomDelay = %d, want 0", got)
	}

	writeConfigSh(t, dataDir, "RandomDelay=\"300\"\n")
	if err := cfg.Reload(); err != nil {
		t.Fatal(err)
	}
	if got := cfg.GetInt("RandomDelay", 0); got != 300 {
		t.Fatalf("RandomDelay after reload = %d, want 300", got)
	}
}

// TestReloadIfChangedSkipsUnchangedFile makes sure the per-task hook does not
// re-parse the file on every run.
func TestReloadIfChangedSkipsUnchangedFile(t *testing.T) {
	dataDir := t.TempDir()
	writeConfigSh(t, dataDir, "RandomDelay=\"7\"\n")
	cfg := &Config{DataPath: dataDir, ConfigPath: filepath.Join(dataDir, "config")}
	if err := cfg.Reload(); err != nil {
		t.Fatal(err)
	}

	// Change the file behind the panel's back (SSH, editor, CLI). The size
	// differs so the mtime+size stamp cannot match by accident.
	writeConfigSh(t, dataDir, "RandomDelay=\"42\"\n")
	if err := cfg.ReloadIfChanged(); err != nil {
		t.Fatal(err)
	}
	if got := cfg.GetInt("RandomDelay", 0); got != 42 {
		t.Fatalf("RandomDelay after ReloadIfChanged = %d, want 42", got)
	}

	// A second call without touching the file keeps the value and does not
	// clobber it with defaults.
	if err := cfg.ReloadIfChanged(); err != nil {
		t.Fatal(err)
	}
	if got := cfg.GetInt("RandomDelay", 0); got != 42 {
		t.Fatalf("RandomDelay after no-op ReloadIfChanged = %d, want 42", got)
	}
}

// TestReloadKeepsValuesOnTruncatedFile guards against the plain os.WriteFile
// used by the panel: a reader that catches the file mid-write must not wipe
// every setting.
func TestReloadKeepsValuesOnTruncatedFile(t *testing.T) {
	dataDir := t.TempDir()
	writeConfigSh(t, dataDir, "RandomDelay=\"9\"\nCommandTimeoutTime=\"60\"\n")
	cfg := &Config{DataPath: dataDir, ConfigPath: filepath.Join(dataDir, "config")}
	if err := cfg.Reload(); err != nil {
		t.Fatal(err)
	}

	writeConfigSh(t, dataDir, "")
	if err := cfg.Reload(); err == nil {
		t.Fatal("expected an error for an empty parse, got nil")
	}
	if got := cfg.GetInt("RandomDelay", 0); got != 9 {
		t.Fatalf("RandomDelay after truncated read = %d, want 9", got)
	}
}

// TestGetIfSetDistinguishesEmptyFromMissing pins the difference between Node's
// ${VAR-default} and ${VAR:-default}.
func TestGetIfSetDistinguishesEmptyFromMissing(t *testing.T) {
	cfg := &Config{Values: map[string]string{"RandomDelayFileExtensions": ""}}
	if got := cfg.GetIfSet("RandomDelayFileExtensions", "js"); got != "" {
		t.Fatalf("present but empty = %q, want \"\"", got)
	}
	if got := cfg.GetIfSet("RandomDelayIgnoredMinutes", "0 30"); got != "0 30" {
		t.Fatalf("missing key = %q, want the fallback", got)
	}
}
