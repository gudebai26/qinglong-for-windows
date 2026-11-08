package runner

import (
	"os"
	"path/filepath"
	"strings"
)

// isAbsPath accepts both the OS form and a leading slash: task commands are
// written for a Unix shell, so `/data/x.js` must count as absolute even on
// Windows.
func isAbsPath(value string) bool {
	return strings.HasPrefix(value, "/") || filepath.IsAbs(value)
}

func joinPath(parts ...string) string { return filepath.Join(parts...) }

func baseName(value string) string { return filepath.Base(value) }

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
