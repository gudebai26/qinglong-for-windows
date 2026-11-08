package platform

import (
	"os"
	"path/filepath"
	"strings"
)

// BundledRuntimeDirName is the directory a self-contained distribution keeps the
// interpreters in. It sits next to qinglong.exe so the whole thing can be
// copied as one folder.
const BundledRuntimeDirName = "runtime"

// BundledRuntimeRoot returns <root>/runtime when the distribution ships one.
func BundledRuntimeRoot(root string) string {
	dir := filepath.Join(root, BundledRuntimeDirName)
	if info, err := os.Stat(dir); err == nil && info.IsDir() {
		return dir
	}
	return ""
}

// PrependBundledRuntime puts the shipped interpreters ahead of the system ones
// on PATH.
//
// Everything downstream resolves tools through exec.LookPath (the task runner,
// the dependency installer, the doctor), so editing PATH once here is enough:
// `node`, `python`, `git` and `bash` start pointing at the bundled copies
// without touching any of those call sites.
//
// Returns the directories that were added, for the boot log.
func PrependBundledRuntime(root string) []string {
	runtimeRoot := BundledRuntimeRoot(root)
	if runtimeRoot == "" {
		return nil
	}

	candidates := []string{
		filepath.Join(runtimeRoot, "node"),
		filepath.Join(runtimeRoot, "python"),
		filepath.Join(runtimeRoot, "python", "Scripts"),
		filepath.Join(runtimeRoot, "git", "cmd"),
		filepath.Join(runtimeRoot, "git", "bin"),
		filepath.Join(runtimeRoot, "git", "usr", "bin"),
		filepath.Join(runtimeRoot, "git", "mingw64", "bin"),
	}

	added := make([]string, 0, len(candidates))
	for _, dir := range candidates {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			added = append(added, dir)
		}
	}
	if len(added) == 0 {
		return nil
	}

	parts := added
	if current := strings.TrimSpace(os.Getenv("PATH")); current != "" {
		parts = append(parts, current)
	}
	_ = os.Setenv("PATH", strings.Join(parts, string(os.PathListSeparator)))
	return added
}
