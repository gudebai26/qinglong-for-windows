package platform

import (
	"os"
	"os/exec"
	"path/filepath"
)

// BashPath locates a POSIX shell for `.sh` tasks and the sh hooks.
//
// `bash` is frequently missing from PATH on Windows: Git for Windows installs it
// under <Git>\bin or <Git>\usr\bin, while the installer only adds <Git>\cmd
// (git.exe) unless the user ticked "Add to PATH". Three fallbacks cover the
// usual layouts:
//
//  1. derive it from git.exe (<Git>\cmd\git.exe -> <Git>\bin\bash.exe)
//  2. ask the registry where Git was installed
//  3. probe the default install folders
//
// Returns "" when no shell exists; callers degrade with a warning instead of
// failing the task.
func BashPath() string {
	path, _ := BashPathTrace()
	return path
}

// BashPathTrace is BashPath plus the locations it looked at, so the warning a
// task writes can show what was searched instead of only what failed.
func BashPathTrace() (string, []string) {
	var tried []string

	if path, err := exec.LookPath("bash"); err == nil {
		return path, []string{path}
	}
	tried = append(tried, "PATH")

	// <Git>\cmd\git.exe -> <Git>\bin\bash.exe, <Git>\usr\bin\bash.exe.
	if git, err := exec.LookPath("git"); err == nil {
		dir := filepath.Dir(git)
		for _, rel := range []string{
			filepath.Join("..", "bin", "bash.exe"),
			filepath.Join("..", "usr", "bin", "bash.exe"),
			filepath.Join("bin", "bash.exe"),
		} {
			candidate := filepath.Clean(filepath.Join(dir, rel))
			tried = append(tried, candidate)
			if isRegularFile(candidate) {
				return candidate, tried
			}
		}
	}

	// A Git installed outside PATH and the default folders still records its
	// location in the registry.
	roots := gitInstallRoots()
	for _, root := range roots {
		tried = append(tried, "registry: "+root)
	}
	roots = append(roots,
		`C:\Program Files\Git`,
		`C:\Program Files (x86)\Git`,
	)
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		roots = append(roots, filepath.Join(local, "Programs", "Git"))
	}
	if home := os.Getenv("USERPROFILE"); home != "" {
		roots = append(roots, filepath.Join(home, "scoop", "apps", "git", "current"))
	}

	seen := map[string]struct{}{}
	for _, root := range roots {
		if root == "" {
			continue
		}
		if _, ok := seen[root]; ok {
			continue
		}
		seen[root] = struct{}{}
		for _, rel := range []string{
			filepath.Join("bin", "bash.exe"),
			filepath.Join("usr", "bin", "bash.exe"),
		} {
			candidate := filepath.Join(root, rel)
			tried = append(tried, candidate)
			if isRegularFile(candidate) {
				return candidate, tried
			}
		}
	}
	return "", tried
}

// GitPath reports the git executable, falling back to the registry and the
// default install folders when git is not on PATH. A Git installed without
// "Add to PATH" is otherwise invisible to the subscription page.
func GitPath() string {
	if path, err := exec.LookPath("git"); err == nil {
		return path
	}
	roots := gitInstallRoots()
	roots = append(roots,
		`C:\Program Files\Git`,
		`C:\Program Files (x86)\Git`,
	)
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		roots = append(roots, filepath.Join(local, "Programs", "Git"))
	}
	for _, root := range roots {
		if root == "" {
			continue
		}
		for _, rel := range []string{
			filepath.Join("cmd", "git.exe"),
			filepath.Join("bin", "git.exe"),
			filepath.Join("mingw64", "bin", "git.exe"),
		} {
			if candidate := filepath.Join(root, rel); isRegularFile(candidate) {
				return candidate
			}
		}
	}
	return ""
}

func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
