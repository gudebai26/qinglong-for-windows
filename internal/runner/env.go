package runner

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// envList is an ordered KEY=VALUE environment with case-insensitive keys, the
// way Windows treats them. Building the task environment through a map keeps
// the merge rules explicit: later sources win.
type envList struct {
	values map[string]string
	keys   map[string]string // lower(key) -> key as last written
}

func newEnvList(base []string) *envList {
	list := &envList{values: map[string]string{}, keys: map[string]string{}}
	for _, item := range base {
		key, value, ok := strings.Cut(item, "=")
		if !ok || key == "" {
			continue
		}
		list.Set(key, value)
	}
	return list
}

func (e *envList) Set(key, value string) {
	lower := strings.ToLower(key)
	if existing, ok := e.keys[lower]; ok && existing != key {
		delete(e.values, existing)
	}
	e.keys[lower] = key
	e.values[key] = value
}

func (e *envList) Get(key string) string {
	return e.values[e.keys[strings.ToLower(key)]]
}

func (e *envList) Has(key string) bool {
	_, ok := e.keys[strings.ToLower(key)]
	return ok
}

// Unset removes a key, matching `unset` in share.sh.
func (e *envList) Unset(key string) {
	lower := strings.ToLower(key)
	if existing, ok := e.keys[lower]; ok {
		delete(e.values, existing)
		delete(e.keys, lower)
	}
}

// AppendPathList appends entries to a PATH-like variable using the platform
// separator. Node's shell wrapper joined with ":" everywhere, which corrupts
// NODE_PATH on Windows because every drive letter contains a colon; Go uses
// os.PathListSeparator so module resolution actually works.
func (e *envList) AppendPathList(key string, entries ...string) {
	current := e.Get(key)
	parts := make([]string, 0, len(entries)+1)
	if current != "" {
		parts = append(parts, current)
	}
	for _, entry := range entries {
		if entry != "" {
			parts = append(parts, entry)
		}
	}
	e.Set(key, strings.Join(parts, string(os.PathListSeparator)))
}

// PrependPathList is AppendPathList for variables where order matters and the
// new entries must come first (PYTHONPATH).
func (e *envList) PrependPathList(key string, entries ...string) {
	current := e.Get(key)
	parts := make([]string, 0, len(entries)+1)
	for _, entry := range entries {
		if entry != "" {
			parts = append(parts, entry)
		}
	}
	if current != "" {
		parts = append(parts, current)
	}
	e.Set(key, strings.Join(parts, string(os.PathListSeparator)))
}

// Slice renders the environment for exec.Cmd, sorted so repeated runs of the
// same task produce identical environments.
func (e *envList) Slice() []string {
	keys := make([]string, 0, len(e.values))
	for key := range e.values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+e.values[key])
	}
	return result
}

// parseEnvSh reads shell/preload/env.sh. Node sources the file, so only lines
// that start with `export` end up in the task environment; the value is taken
// literally (quotes stripped, no variable expansion) because the panel writes
// this file itself and never expects shell code in it.
func parseEnvSh(content string) map[string]string {
	values := map[string]string{}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.HasPrefix(line, "export ") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		key, raw, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		values[key] = unquoteValue(strings.TrimSpace(raw))
	}
	return values
}

func unquoteValue(raw string) string {
	if len(raw) >= 2 {
		if raw[0] == '"' && raw[len(raw)-1] == '"' {
			inner := raw[1 : len(raw)-1]
			inner = strings.ReplaceAll(inner, `\"`, `"`)
			inner = strings.ReplaceAll(inner, `\\`, `\`)
			inner = strings.ReplaceAll(inner, `\$`, `$`)
			return inner
		}
		if raw[0] == '\'' && raw[len(raw)-1] == '\'' {
			return raw[1 : len(raw)-1]
		}
	}
	if idx := strings.Index(raw, " #"); idx >= 0 {
		return strings.TrimSpace(raw[:idx])
	}
	return raw
}

// shellPaths mirrors the directory/file variables share.sh exports. User
// scripts read them (file_task_before, dir_scripts, cmd_task, ...), so the Go
// runner publishes the same names with the same values.
//
// The paths use forward slashes on every platform because that is what the
// shell wrapper produces on Windows too (bash reports C:/... paths), and the
// preload hooks build shell commands out of these values.
func (r *Runner) shellPaths() map[string]string {
	root := r.cfg.RootPath
	data := r.cfg.DataPath
	join := func(parts ...string) string { return filepath.ToSlash(filepath.Join(parts...)) }

	shell := join(root, "shell")
	preload := join(shell, "preload")
	config := join(data, "config")
	scripts := join(data, "scripts")
	repo := join(data, "repo")
	raw := join(data, "raw")
	log := join(data, "log")
	db := join(data, "db")
	deps := join(data, "deps")
	sample := join(root, "sample")

	return map[string]string{
		"dir_root":            root,
		"dir_tmp":             join(root, ".tmp"),
		"dir_data":            data,
		"dir_shell":           shell,
		"dir_preload":         preload,
		"dir_sample":          sample,
		"dir_static":          join(root, "static"),
		"dir_config":          config,
		"dir_scripts":         scripts,
		"dir_repo":            repo,
		"dir_raw":             raw,
		"dir_log":             log,
		"dir_db":              db,
		"dir_dep":             deps,
		"dir_list_tmp":        join(log, ".tmp"),
		"dir_update_log":      join(log, "update"),
		"ql_static_repo":      join(repo, "static"),
		"file_config_user":    join(config, "config.sh"),
		"file_env":            join(preload, "env.sh"),
		"file_preload_js":     join(preload, "sitecustomize.js"),
		"file_sharecode":      join(config, "sharecode.sh"),
		"file_extra_shell":    join(config, "extra.sh"),
		"file_task_before":    join(config, "task_before.sh"),
		"file_task_before_js": join(config, "task_before.js"),
		"file_task_before_py": join(config, "task_before.py"),
		"file_task_after":     join(config, "task_after.sh"),
		"file_notify_py":      join(scripts, "notify.py"),
		"file_notify_js":      join(scripts, "sendNotify.js"),
		"file_test_js":        join(scripts, "ql_sample.js"),
		"file_test_py":        join(scripts, "ql_sample.py"),
		"dep_notify_py":       join(deps, "notify.py"),
		"dep_notify_js":       join(deps, "sendNotify.js"),
		"list_crontab_user":   join(config, "crontab.list"),
		"list_crontab_sample": join(sample, "crontab.sample.list"),
		"list_own_scripts":    join(log, ".tmp", "own_scripts.list"),
		"list_own_user":       join(log, ".tmp", "own_user.list"),
		"list_own_add":        join(log, ".tmp", "own_add.list"),
		"list_own_drop":       join(log, ".tmp", "own_drop.list"),
		"cmd_task":            "task",
		"cmd_ql":              "ql",
		"ql_base_url":         r.cfg.BaseURL,
		"ql_port":             itoa(r.cfg.Port),
		"ql_grpc_port":        "5500",
		"current_branch":      os.Getenv("QL_BRANCH"),
	}
}

func itoa(value int) string { return strconv.Itoa(value) }
