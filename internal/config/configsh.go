package config

import (
	"os"
	"path/filepath"
	"strings"
)

// ParseConfigSh extracts KEY="value" assignments from a shell-style config
// file. It intentionally does not evaluate the file: comments, blank lines and
// anything that is not a plain assignment are ignored.
//
// Supported forms (all present in sample/config.sample.sh):
//
//	KEY="value"
//	KEY='value'
//	KEY=value
//	export KEY="value"
func ParseConfigSh(content string) map[string]string {
	values := make(map[string]string)
	content = strings.TrimPrefix(content, "\ufeff")

	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSuffix(line, "\r")
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if rest, ok := trimExportPrefix(line); ok {
			line = rest
		}

		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		if !isValidKey(key) {
			continue
		}
		value, ok := parseAssignmentValue(strings.TrimSpace(line[eq+1:]))
		if !ok {
			continue
		}
		values[key] = value
	}

	return values
}

func trimExportPrefix(line string) (string, bool) {
	const prefix = "export"
	if !strings.HasPrefix(line, prefix) {
		return line, false
	}
	rest := line[len(prefix):]
	if rest == "" || (rest[0] != ' ' && rest[0] != '\t') {
		return line, false
	}
	return strings.TrimSpace(rest), true
}

func isValidKey(key string) bool {
	if key == "" {
		return false
	}
	for i, r := range key {
		switch {
		case r == '_':
		case r >= 'A' && r <= 'Z':
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// parseAssignmentValue understands double quotes, single quotes and bare
// values. Inside double quotes only \" \\ \$ \` are unescaped, matching bash.
func parseAssignmentValue(raw string) (string, bool) {
	if raw == "" {
		return "", true
	}

	switch raw[0] {
	case '"':
		var b strings.Builder
		for i := 1; i < len(raw); i++ {
			c := raw[i]
			if c == '\\' && i+1 < len(raw) {
				next := raw[i+1]
				switch next {
				case '"', '\\', '$', '`':
					b.WriteByte(next)
					i++
					continue
				}
				b.WriteByte(c)
				continue
			}
			if c == '"' {
				return b.String(), true
			}
			b.WriteByte(c)
		}
		// Unterminated quote: ignore the line, a shell would keep reading.
		return "", false
	case '\'':
		end := strings.IndexByte(raw[1:], '\'')
		if end < 0 {
			return "", false
		}
		return raw[1 : 1+end], true
	default:
		return stripInlineComment(raw), true
	}
}

// stripInlineComment removes a trailing " # comment" from an unquoted value,
// mirroring how the shell ends a word at an unquoted comment.
func stripInlineComment(raw string) string {
	for i := 1; i < len(raw); i++ {
		if raw[i] != '#' {
			continue
		}
		if raw[i-1] == ' ' || raw[i-1] == '\t' {
			return strings.TrimSpace(raw[:i])
		}
	}
	return strings.TrimSpace(raw)
}

// resolveRoot determines QL_DIR. Priority: explicit environment variable, the
// executable's directory when it looks like a panel root, then the working
// directory.
func resolveRoot() (string, error) {
	if value := strings.TrimSpace(os.Getenv("QL_DIR")); value != "" {
		return cleanDir(value), nil
	}

	candidates := make([]string, 0, 2)
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		candidates = append(candidates, filepath.Dir(exe))
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, wd)
	}

	for _, dir := range candidates {
		if looksLikeRoot(dir) {
			return cleanDir(dir), nil
		}
	}
	if len(candidates) > 0 {
		return cleanDir(candidates[0]), nil
	}
	return "", os.ErrNotExist
}

func looksLikeRoot(dir string) bool {
	for _, marker := range []string{"data", "version.yaml"} {
		if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
			return true
		}
	}
	return false
}

func cleanDir(dir string) string {
	cleaned := filepath.Clean(strings.TrimSpace(dir))
	if cleaned == "." {
		if wd, err := os.Getwd(); err == nil {
			return wd
		}
	}
	return cleaned
}
