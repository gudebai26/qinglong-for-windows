package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"qinglong-go/internal/config"
)

// newScriptFixture builds a service over a temporary data directory.
func newScriptFixture(t *testing.T) (*ScriptService, *config.Config) {
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
	return NewScriptService(cfg, nil, nil), cfg
}

func TestResolveFileAccess(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "sub")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inside, "a.js"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name      string
		parts     []string
		blacklist []string
		wantEmpty bool
	}{
		{"inside", []string{"sub", "a.js"}, nil, false},
		{"new file", []string{"sub", "new.js"}, nil, false},
		{"root itself", nil, nil, true},
		{"escape", []string{"..", "config.sh"}, nil, true},
		{"nested escape", []string{"sub", "..", "..", "x"}, nil, true},
		{"absolute outside", []string{string(filepath.Separator) + "etc", "passwd"}, nil, true},
		{"blacklisted", []string{"sub", "a.js"}, []string{"a.js"}, true},
		{"blacklisted dir", []string{"grpc", "x"}, []string{"grpc"}, true},
		{"blacklist is case insensitive", []string{"sub", "A.JS"}, []string{"a.js"}, true},
		{"nul byte", []string{"a\x00b"}, nil, true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got := resolveFileAccess(root, test.parts, test.blacklist)
			if test.wantEmpty && got != "" {
				t.Fatalf("expected rejection, got %q", got)
			}
			if !test.wantEmpty && got == "" {
				t.Fatalf("expected a path for %v", test.parts)
			}
		})
	}
}

// TestCollateCompare checks the ordering the panel's tree uses
// (title.localeCompare(title) in readDirs).
func TestCollateCompare(t *testing.T) {
	ordered := []string{
		"a b.js", "a_b.js", "a-b.js", "a.js", "A.js", "a01.js", "a1.js",
		"abc.js", "Abc.js", "ABC.js", "b.js", "B.js", "z.js", "Z.js",
	}
	for i := 0; i < len(ordered)-1; i++ {
		if collateCompare(ordered[i], ordered[i+1]) >= 0 {
			t.Fatalf("%q should sort before %q", ordered[i], ordered[i+1])
		}
	}
}

func TestScriptHistoryLifecycle(t *testing.T) {
	scripts, cfg := newScriptFixture(t)
	path := filepath.Join(cfg.ScriptPath, "a.js")
	if err := os.WriteFile(path, []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	first, err := scripts.Save("", "a.js", "v2\n", SaveOptions{})
	if err != nil {
		t.Fatalf("save v2: %v", err)
	}
	if !first.Changed || first.HistoryRecorded == nil || !*first.HistoryRecorded {
		t.Fatalf("unexpected save result: %+v", first)
	}
	second, err := scripts.Save("", "a.js", "v3\n", SaveOptions{})
	if err != nil {
		t.Fatalf("save v3: %v", err)
	}
	if !second.Changed {
		t.Fatalf("second save should change the file")
	}

	list, err := scripts.History().List("", "a.js")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Versions) != 2 {
		t.Fatalf("expected two versions, got %d", len(list.Versions))
	}
	if list.Versions[0].Source != sourceSave || list.Versions[1].Source != sourceBaseline {
		t.Fatalf("unexpected sources: %+v", list.Versions)
	}
	if list.CurrentHash != hashContent("v3\n") {
		t.Fatalf("currentHash mismatch")
	}
	if !list.Versions[0].Identical && list.Versions[0].Hash == list.CurrentHash {
		t.Fatalf("identical flag wrong")
	}

	detail, err := scripts.History().Detail("", "a.js", list.Versions[1].ID)
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if detail.Version.Content != "v1\n" || detail.Current != "v3\n" {
		t.Fatalf("unexpected detail: %+v", detail)
	}

	restored, err := scripts.Restore("", "a.js", list.Versions[1].ID, list.CurrentHash)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if restored.Content != "v1\n" {
		t.Fatalf("restore content: %q", restored.Content)
	}
	if content, _ := os.ReadFile(path); string(content) != "v1\n" {
		t.Fatalf("file after restore: %q", content)
	}
}

func TestScriptHistoryOptimisticLock(t *testing.T) {
	scripts, cfg := newScriptFixture(t)
	if err := os.WriteFile(filepath.Join(cfg.ScriptPath, "a.js"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := scripts.Save("", "a.js", "v2\n", SaveOptions{}); err != nil {
		t.Fatal(err)
	}

	_, err := scripts.Save("", "a.js", "v3\n", SaveOptions{SkipHistory: true, ExpectedHash: strings.Repeat("0", 64)})
	historyErr, ok := err.(*ScriptHistoryError)
	if !ok || historyErr.Status != 409 {
		t.Fatalf("expected 409, got %v", err)
	}

	// A matching hash is accepted and, for a normal file, still records the
	// previous content - Node behaves the same way; skipHistory only matters
	// when the current file cannot be versioned at all.
	result, err := scripts.Save("", "a.js", "v3\n", SaveOptions{
		SkipHistory: true, ExpectedHash: hashContent("v2\n"),
	})
	if err != nil {
		t.Fatalf("save with hash: %v", err)
	}
	if result.HistoryRecorded == nil || !*result.HistoryRecorded {
		t.Fatalf("expected a recorded version: %+v", result)
	}
}

func TestScriptHistoryOversizedFile(t *testing.T) {
	scripts, cfg := newScriptFixture(t)
	path := filepath.Join(cfg.ScriptPath, "big.js")
	big := strings.Repeat("a", historyFileBytes+10)
	if err := os.WriteFile(path, []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := scripts.Save("", "big.js", "small\n", SaveOptions{})
	unavailable, ok := err.(*HistoryUnavailableError)
	if !ok {
		t.Fatalf("expected HistoryUnavailableError, got %v", err)
	}
	if unavailable.CurrentHash != hashContent(big) {
		t.Fatalf("unexpected currentHash")
	}

	// Without skipHistory nothing is written.
	if content, _ := os.ReadFile(path); len(content) != len(big) {
		t.Fatalf("file changed despite the error")
	}

	result, err := scripts.Save("", "big.js", "small\n", SaveOptions{
		SkipHistory: true, ExpectedHash: unavailable.CurrentHash,
	})
	if err != nil {
		t.Fatalf("skipHistory save: %v", err)
	}
	if result.HistoryRecorded == nil || *result.HistoryRecorded {
		t.Fatalf("historyRecorded should be false: %+v", result)
	}
	if content, _ := os.ReadFile(path); string(content) != "small\n" {
		t.Fatalf("file content: %q", content)
	}
}

// TestScriptHistoryArchiveShape pins the on-disk format: Brotli-compressed
// JSON with the exact key order the Node writer uses.
func TestScriptHistoryArchiveShape(t *testing.T) {
	scripts, cfg := newScriptFixture(t)
	if err := os.WriteFile(filepath.Join(cfg.ScriptPath, "a.js"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := scripts.Save("", "a.js", "v2\n", SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(cfg.ScriptHistoryPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected one archive, got %d", len(entries))
	}
	name := entries[0].Name()
	if !strings.HasSuffix(name, ".br") || len(name) != 67 {
		t.Fatalf("archive name should be sha256(relative)+.br, got %q", name)
	}
	if name != hashContent("a.js")+".br" {
		t.Fatalf("archive key is not sha256 of the relative path: %q", name)
	}
	raw, err := os.ReadFile(filepath.Join(cfg.ScriptHistoryPath, name))
	if err != nil {
		t.Fatal(err)
	}
	archive, err := scripts.History().readArchive(filepath.Join(cfg.ScriptHistoryPath, name), "a.js")
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	encoded, err := json.Marshal(archive)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(encoded), `{"format":1,"path":"a.js","versions":[`) {
		t.Fatalf("unexpected key order: %s", encoded)
	}
	if len(raw) == 0 {
		t.Fatalf("empty archive")
	}
}

func TestCreateInScriptsRoot(t *testing.T) {
	scripts, cfg := newScriptFixture(t)
	content := "console.log(1)\n"
	// The panel's "new file" dialog sends an empty path for the scripts root.
	// Node answers 403 on Windows here because it re-joins the defaulted
	// absolute scriptPath; the Go panel resolves it to the root.
	if _, err := scripts.Create(ScriptCreateInput{Filename: "root.js", Content: &content}); err != nil {
		t.Fatalf("create in root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.ScriptPath, "root.js")); err != nil {
		t.Fatalf("file not created: %v", err)
	}
}

func TestScriptCreateRejectsBlacklist(t *testing.T) {
	scripts, _ := newScriptFixture(t)
	content := "x"
	_, err := scripts.Create(ScriptCreateInput{
		Filename: "auth.json", Path: "../config", Content: &content,
	})
	business, ok := err.(*BusinessError)
	if !ok || business.Code != 403 {
		t.Fatalf("expected a 403 business error, got %v", err)
	}
}

func TestScriptListSkipsBlacklistAndSymlinks(t *testing.T) {
	scripts, cfg := newScriptFixture(t)
	for _, name := range []string{"a.js", "node_modules", ".git"} {
		if name == "a.js" {
			if err := os.WriteFile(filepath.Join(cfg.ScriptPath, name), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Join(cfg.ScriptPath, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	items, err := scripts.List("")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected only a.js, got %d entries", len(items))
	}
}
