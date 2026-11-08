package preload

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMaterializeWritesHooksAndRevision(t *testing.T) {
	dir := t.TempDir()
	if err := Materialize(dir); err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	for _, name := range Files {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	if got := readRevision(dir); got != preloadRevision {
		t.Fatalf("revision = %q, want %q", got, preloadRevision)
	}
}

func TestMaterializeRefreshesStaleHooks(t *testing.T) {
	dir := t.TempDir()
	if err := Materialize(dir); err != nil {
		t.Fatalf("Materialize: %v", err)
	}

	// An existing install still carries the old hook and an older revision.
	hook := filepath.Join(dir, "sitecustomize.js")
	if err := os.WriteFile(hook, []byte("old hook"), 0o644); err != nil {
		t.Fatal(err)
	}
	generated := filepath.Join(dir, "env.js")
	if err := os.WriteFile(generated, []byte("process.env.FROM_DB='1';\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, revisionFile), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Materialize(dir); err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	content, err := os.ReadFile(hook)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), `source "${shellPath(file_task_before)}"`) {
		t.Fatalf("stale hook was not refreshed: %q", string(content))
	}
	// The environment module owns env.js; an upgrade must not empty it.
	envContent, err := os.ReadFile(generated)
	if err != nil {
		t.Fatal(err)
	}
	if string(envContent) != "process.env.FROM_DB='1';\n" {
		t.Fatalf("env.js was overwritten: %q", string(envContent))
	}
}

func TestMaterializeKeepsPatchedHookOnSameRevision(t *testing.T) {
	dir := t.TempDir()
	if err := Materialize(dir); err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	hook := filepath.Join(dir, "sitecustomize.js")
	if err := os.WriteFile(hook, []byte("local patch"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Materialize(dir); err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	content, err := os.ReadFile(hook)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "local patch" {
		t.Fatalf("a hook patched at the current revision was overwritten: %q", string(content))
	}
}
