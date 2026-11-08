package service

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// This file ports back/shared/fileAccess.ts and the path helpers of
// back/api/script.ts. The rules are deliberately the same as the Node panel:
//
//   - a target must live strictly inside a writable root,
//   - blacklisted names are matched per path segment, case insensitively, and
//     cover every descendant,
//   - existing symlinks are resolved and re-checked so a link cannot escape
//     the root, and a dangling link is never treated as a missing file,
//   - the returned value is the *unresolved* cleaned path, because the API
//     echoes it back and the history archive is keyed by it.
//
// Node's path.resolve/path.relative are reimplemented because Go's filepath
// treats a leading "/" as relative on Windows while Node re-roots it on the
// current drive; script paths arrive from HTTP and must behave identically.

// resolvePath ports Node's path.resolve for the shapes the panel produces: an
// absolute base followed by segments that may themselves be absolute.
func resolvePath(parts ...string) string {
	acc := ""
	for _, part := range parts {
		if part == "" {
			continue
		}
		if acc == "" {
			if filepath.VolumeName(part) != "" || filepath.IsAbs(part) {
				acc = part
				continue
			}
			cwd, err := os.Getwd()
			if err != nil {
				cwd = string(filepath.Separator)
			}
			acc = filepath.Join(cwd, part)
			continue
		}
		if filepath.VolumeName(part) != "" {
			// "C:\x" (or "C:x") restarts resolution on that drive.
			acc = part
			continue
		}
		if isRootedPath(part) {
			// "/x" is absolute on the drive of the accumulated path.
			acc = filepath.VolumeName(acc) + part
			continue
		}
		acc += string(filepath.Separator) + part
	}
	if acc == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return string(filepath.Separator)
		}
		return filepath.Clean(cwd)
	}
	if !filepath.IsAbs(acc) {
		cwd, err := os.Getwd()
		if err == nil {
			acc = cwd + string(filepath.Separator) + acc
		}
	}
	return filepath.Clean(acc)
}

func isRootedPath(p string) bool {
	return strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`)
}

// splitPath separates the volume from the segments of an absolute path.
func splitPath(p string) (string, []string) {
	volume := filepath.VolumeName(p)
	rest := strings.TrimPrefix(p, volume)
	rest = strings.TrimLeft(rest, `/\`)
	if rest == "" {
		return volume, nil
	}
	return volume, strings.Split(rest, string(filepath.Separator))
}

// equalPath reports whether two path strings name the same entry. Windows
// comparisons are case insensitive, exactly like Node's path.win32.relative.
func equalPath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// pathRelative ports path.relative: "" for the same path, an absolute path when
// the two inputs live on different volumes, and otherwise the relative walk.
func pathRelative(from, to string) string {
	if equalPath(from, to) {
		return ""
	}
	fromVolume, fromSegments := splitPath(from)
	toVolume, toSegments := splitPath(to)
	if !equalPath(fromVolume, toVolume) {
		return to
	}
	common := 0
	for common < len(fromSegments) && common < len(toSegments) &&
		equalPath(fromSegments[common], toSegments[common]) {
		common++
	}
	parts := make([]string, 0, len(fromSegments)-common+len(toSegments)-common)
	for i := common; i < len(fromSegments); i++ {
		parts = append(parts, "..")
	}
	parts = append(parts, toSegments[common:]...)
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, string(filepath.Separator))
}

// isWithinPath ports isWithin(): the target is the root itself or a descendant.
func isWithinPath(root, target string) bool {
	relative := pathRelative(root, target)
	if relative == "" {
		return true
	}
	return relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator)) &&
		!filepath.IsAbs(relative)
}

// isStrictlyWithinPath mirrors `resolved.startsWith(root + sep)`, i.e. the root
// itself does not count as inside.
func isStrictlyWithinPath(root, target string) bool {
	relative := pathRelative(root, target)
	return relative != "" && isWithinPath(root, target)
}

// blockedNameSet lowercases the blacklist once per call site.
func blockedNameSet(blacklist []string) map[string]struct{} {
	if len(blacklist) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(blacklist))
	for _, name := range blacklist {
		set[strings.ToLower(name)] = struct{}{}
	}
	return set
}

func isBlockedRelative(relative string, blocked map[string]struct{}) bool {
	if len(blocked) == 0 {
		return false
	}
	for _, segment := range strings.Split(relative, string(filepath.Separator)) {
		if _, ok := blocked[strings.ToLower(segment)]; ok {
			return true
		}
	}
	return false
}

// resolveFileAccess ports resolveFileAccess(): resolve existing files and
// not-yet-created children without following an existing symlink outside the
// root. It returns "" when the path is not allowed.
func resolveFileAccess(root string, parts []string, blacklist []string) string {
	for _, part := range parts {
		if strings.ContainsRune(part, 0) {
			return ""
		}
	}
	resolvedRoot := resolvePath(root)
	args := make([]string, 0, len(parts)+1)
	args = append(args, resolvedRoot)
	args = append(args, parts...)
	target := resolvePath(args...)
	if equalPath(target, resolvedRoot) || !isWithinPath(resolvedRoot, target) {
		return ""
	}

	blocked := blockedNameSet(blacklist)
	if isBlockedRelative(pathRelative(resolvedRoot, target), blocked) {
		return ""
	}

	realRoot, err := filepath.EvalSymlinks(resolvedRoot)
	if err != nil {
		return ""
	}

	existing := target
	var missing []string
	for {
		if _, err := os.Stat(existing); err == nil {
			break
		}
		// existsSync() follows links, so a dangling symlink looks absent; the
		// lstat probe below catches it and refuses the path.
		if _, lerr := os.Lstat(existing); lerr == nil {
			return ""
		} else if !os.IsNotExist(lerr) {
			return ""
		}
		if equalPath(existing, resolvedRoot) {
			return ""
		}
		missing = append([]string{filepath.Base(existing)}, missing...)
		parent := filepath.Dir(existing)
		if parent == existing {
			return ""
		}
		existing = parent
	}

	realExisting, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return ""
	}
	realArgs := make([]string, 0, len(missing)+1)
	realArgs = append(realArgs, realExisting)
	realArgs = append(realArgs, missing...)
	realTarget := resolvePath(realArgs...)
	if !isWithinPath(realRoot, realTarget) ||
		isBlockedRelative(pathRelative(realRoot, realTarget), blocked) {
		return ""
	}
	return target
}
