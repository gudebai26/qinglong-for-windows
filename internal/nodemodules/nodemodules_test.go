package nodemodules

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// sampleFS is a miniature node_modules tree: two packages, a nested file and a
// scoped name, which is enough to exercise the walk, the skip rule and the
// directory creation.
func sampleFS() fstest.MapFS {
	return fstest.MapFS{
		"@grpc/grpc-js/package.json": {Data: []byte(`{"name":"@grpc/grpc-js"}`)},
		"@grpc/grpc-js/build/src/index.js": {Data: []byte("module.exports = {};\n")},
		"undici/index.js":            {Data: []byte("module.exports = {};\n")},
		"undici/package.json":        {Data: []byte(`{"name":"undici"}`)},
	}
}

func TestEnsureExtractsEveryFile(t *testing.T) {
	root := t.TempDir()
	result, err := Ensure(sampleFS(), root)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if result.Extracted != 4 || result.Skipped != 0 {
		t.Fatalf("extracted=%d skipped=%d, want 4/0", result.Extracted, result.Skipped)
	}
	if result.UpToDate {
		t.Fatal("first run must not report an up-to-date tree")
	}

	for _, name := range []string{
		"@grpc/grpc-js/package.json",
		"@grpc/grpc-js/build/src/index.js",
		"undici/index.js",
	} {
		content, err := os.ReadFile(filepath.Join(root, DirName, filepath.FromSlash(name)))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if len(content) == 0 {
			t.Fatalf("%s is empty", name)
		}
	}

	marker, err := os.ReadFile(filepath.Join(root, MarkerName))
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if strings.TrimSpace(string(marker)) != Revision {
		t.Fatalf("marker=%q, want %q", marker, Revision)
	}
}

func TestEnsureNeverOverwritesAnExistingInstall(t *testing.T) {
	root := t.TempDir()
	existing := filepath.Join(root, DirName, "undici", "index.js")
	if err := os.MkdirAll(filepath.Dir(existing), 0o755); err != nil {
		t.Fatal(err)
	}
	// A user's own build: same path, different content, much bigger.
	original := "// user installed undici 6.19.2\n" + strings.Repeat("x", 4096)
	if err := os.WriteFile(existing, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := Ensure(sampleFS(), root)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if result.Extracted != 3 || result.Skipped != 1 {
		t.Fatalf("extracted=%d skipped=%d, want 3/1", result.Extracted, result.Skipped)
	}

	content, err := os.ReadFile(existing)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != original {
		t.Fatalf("existing file was rewritten (%d bytes, want %d)", len(content), len(original))
	}
}

func TestEnsureSkipsWalkWhenRevisionMatchesAndTreeIsComplete(t *testing.T) {
	root := t.TempDir()
	if _, err := Ensure(sampleFS(), root); err != nil {
		t.Fatalf("first Ensure: %v", err)
	}

	result, err := Ensure(sampleFS(), root)
	if err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	if !result.UpToDate {
		t.Fatalf("second run should take the fast path, got %+v", result)
	}
	if result.Extracted != 0 {
		t.Fatalf("extracted=%d on an up-to-date tree", result.Extracted)
	}
}

func TestEnsureRefillsMissingFilesAfterRevisionChange(t *testing.T) {
	root := t.TempDir()
	if _, err := Ensure(sampleFS(), root); err != nil {
		t.Fatalf("first Ensure: %v", err)
	}

	// Simulate an older extraction whose marker is stale and that lost a file.
	if err := os.WriteFile(filepath.Join(root, MarkerName), []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lost := filepath.Join(root, DirName, "undici", "package.json")
	if err := os.Remove(lost); err != nil {
		t.Fatal(err)
	}

	result, err := Ensure(sampleFS(), root)
	if err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	if result.PreviousRevision != "0" {
		t.Fatalf("previous revision=%q, want 0", result.PreviousRevision)
	}
	if result.Extracted != 1 {
		t.Fatalf("extracted=%d, want only the missing file", result.Extracted)
	}
	if _, err := os.Stat(lost); err != nil {
		t.Fatalf("missing file was not restored: %v", err)
	}
}

func TestEnsureLeavesNoTemporaryFiles(t *testing.T) {
	root := t.TempDir()
	if _, err := Ensure(sampleFS(), root); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.Contains(entry.Name(), ".ql-tmp-") {
			t.Fatalf("temporary file left behind: %s", name)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestEnsureRejectsMissingFileSystem(t *testing.T) {
	if _, err := Ensure(nil, t.TempDir()); err == nil {
		t.Fatal("expected an error for a nil file system")
	}
}

func TestCount(t *testing.T) {
	total, err := Count(sampleFS())
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if total != 4 {
		t.Fatalf("count=%d, want 4", total)
	}
}

// TestEmbeddedRuntimeMatchesSourceTree guards the shipped dependency set: the
// files compiled into the executable must be the same ones a Node deployment
// resolves, otherwise a single-file install silently loses QLAPI.
func TestEmbeddedRuntimeMatchesSourceTree(t *testing.T) {
	embedded := os.DirFS(filepath.Join("..", "..", DirName))
	if _, err := fs.Stat(embedded, "."); err != nil {
		t.Skip("node_modules is not present in this checkout")
	}

	for _, name := range []string{
		"@grpc/grpc-js/package.json",
		"@grpc/proto-loader/package.json",
		"undici/package.json",
	} {
		if _, err := fs.Stat(embedded, filepath.ToSlash(name)); err != nil {
			t.Fatalf("vendored runtime is missing %s: %v", name, err)
		}
	}
}
