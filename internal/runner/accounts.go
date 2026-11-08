package runner

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"qinglong-go/internal/config"
)

// baseEnv assembles the environment every task of this run shares: the panel
// paths, the shell variables share.sh exports, data/config/env.sh and the
// NAME=value assignments from the command line.
func (r *Runner) baseEnv(plan *Plan) *envList {
	env := newEnvList(os.Environ())

	for key, value := range r.shellPaths() {
		env.Set(key, value)
	}
	env.Set("QL_DIR", filepath.ToSlash(r.cfg.RootPath))
	env.Set("QL_DATA_DIR", filepath.ToSlash(r.cfg.DataPath))
	env.Set("PYTHONUNBUFFERED", "1")
	// Windows hands a piped stdout the ANSI code page (cp936 on Chinese
	// systems), so a task that prints an emoji dies with UnicodeEncodeError
	// and Chinese arrives as mojibake. Force UTF-8 for the standard streams
	// only: PYTHONUTF8 would also change open()'s default encoding and break
	// scripts that read GBK files without passing one.
	env.Set("PYTHONIOENCODING", "utf-8")
	env.Set("QL_LANG", r.cfg.GetDefault("QL_LANG", "zh"))

	// The Node and Python preloads source the before hook through a shell. On
	// Windows they fall back to cmd.exe, where `source` does not exist, so the
	// hook fails on every task. Publishing the bash we resolved makes both
	// preloads use it (on Unix this is the same shell they default to).
	if r.bash != "" && env.Get("QL_SHELL") == "" {
		env.Set("QL_SHELL", filepath.ToSlash(r.bash))
	}

	// The command line assignments come from the panel (ID, log_name,
	// real_time, no_tee, task_before, ...); env.sh is sourced afterwards, so a
	// user variable of the same name wins, exactly like `. "$file_env"`.
	//
	// share.sh sets file_env=$dir_preload/env.sh, so the environment-variable
	// page writes <root>/shell/preload/env.sh - not data/config/env.sh.
	for key, value := range plan.Env {
		env.Set(key, value)
	}
	if content, err := os.ReadFile(filepath.Join(r.cfg.RootPath, "shell", "preload", "env.sh")); err == nil {
		for key, value := range parseEnvSh(string(content)) {
			env.Set(key, value)
		}
	}
	r.importConfig(env)
	r.applyEnvSource(env)
	return env
}

// SetEnvSource registers the database-backed variables (the notification
// channel at the moment). Passing nil disables the lookup.
func (r *Runner) SetEnvSource(source func() map[string]string) {
	r.envMu.Lock()
	r.envSource = source
	r.envMu.Unlock()
}

// applyEnvSource fills variables the database owns. An existing non-empty value
// wins: config.sh and the environment page are explicit operator choices, so
// the settings page must never override them. An empty placeholder (the
// `export BARK_PUSH=""` line the sample config ships) counts as unset.
func (r *Runner) applyEnvSource(env *envList) {
	r.envMu.RLock()
	source := r.envSource
	r.envMu.RUnlock()
	if source == nil {
		return
	}
	for key, value := range source() {
		if value == "" || env.Get(key) != "" {
			continue
		}
		env.Set(key, value)
	}
}

// protectedTaskEnv lists the variables a config.sh line may not override. The
// runner computes these itself (paths, interpreter hooks); letting a stray
// `PATH=` in config.sh win would make every task unstartable. Skipping them is
// a deliberate difference from Node, which sources the file unconditionally.
var protectedTaskEnv = map[string]struct{}{
	"ql_dir":       {},
	"ql_data_dir":  {},
	"path":         {},
	"node_options": {},
	"pythonpath":   {},
	"ql_shell":     {},
}

// importConfig ports import_config() from shell/share.sh: data/config/config.sh
// followed by shell/preload/lang_env.sh. Both files are parsed as KEY="value"
// assignments, never sourced, so a config file cannot execute code.
//
// This is what lets a task (and QLAPI.notify, which reads PUSH_KEY/WEBHOOK_*)
// see the panel configuration. Node only hands `export`ed assignments to the
// child process, so the Go runner is a superset here: every plain assignment
// becomes part of the task environment.
func (r *Runner) importConfig(env *envList) {
	files := []string{
		filepath.Join(r.cfg.ConfigPath, "config.sh"),
		filepath.Join(r.cfg.RootPath, "shell", "preload", "lang_env.sh"),
	}
	for _, path := range files {
		content, err := os.ReadFile(path)
		if err != nil {
			if !os.IsNotExist(err) {
				r.logger.Warn("[task] read config failed", "path", path, "error", err)
			}
			continue
		}
		for key, value := range config.ParseConfigSh(string(content)) {
			if value == "" {
				// An empty assignment must not shadow an inherited variable:
				// Node only changes a shell variable here (no export), so the
				// child process still sees the value it inherited. Injecting ""
				// would break `PUSH_KEY=` over an environment that already
				// carries a working key.
				continue
			}
			if _, blocked := protectedTaskEnv[strings.ToLower(key)]; blocked {
				r.logger.Warn("[task] reserved variable in config ignored", "path", path, "key", key)
				continue
			}
			env.Set(key, value)
		}
	}
}

// buildEnv returns the final child environment, adding the dependency paths and
// the interpreter preload hooks.
func (r *Runner) buildEnv(plan *Plan, overrides map[string]string) []string {
	env := r.baseEnv(plan)
	for key, value := range overrides {
		env.Set(key, value)
	}

	// append_node_dependency_path: user dependencies and the pnpm global root.
	// data/deps/node_modules is included as well so `npm i --prefix data/deps`
	// (which installs into a nested node_modules) is importable, not only a
	// flat module directory. <root>/node_modules holds the ql API runtime the
	// panel ships (grpc-js and friends), so scripts started from a custom
	// work_dir resolve the same modules the preload probe verified.
	env.Set("PREV_NODE_PATH", env.Get("NODE_PATH"))
	// The pnpm global root moves between pnpm versions, so every candidate
	// directory is appended; a missing entry is simply ignored by node.
	pnpmPaths := r.pnpmNodePaths()
	env.AppendPathList("NODE_PATH", append([]string{
		r.cfg.DepPath,
		filepath.Join(r.cfg.DepPath, "node_modules"),
		filepath.Join(r.cfg.RootPath, "node_modules"),
	}, pnpmPaths...)...)

	switch {
	case isJSFile(plan.Script):
		env.Set("PREV_NODE_OPTIONS", env.Get("NODE_OPTIONS"))
		if r.preloadAvailable() {
			env.Set("NODE_OPTIONS", `-r "`+filepath.ToSlash(preloadJSEntry(r.preloadDir))+`" `+env.Get("NODE_OPTIONS"))
		}
	case isPythonFile(plan.Script):
		env.Set("PREV_PYTHONPATH", env.Get("PYTHONPATH"))
		if r.pythonPreloadAvailable() {
			env.PrependPathList("PYTHONPATH",
				filepath.ToSlash(r.preloadDir), filepath.ToSlash(r.cfg.ConfigPath))
		}
	}
	return env.Slice()
}

func isJSFile(name string) bool {
	return strings.HasSuffix(name, ".js") || strings.HasSuffix(name, ".mjs") ||
		strings.HasSuffix(name, ".ts")
}

func isPythonFile(name string) bool {
	return strings.HasSuffix(name, ".py") || strings.HasSuffix(name, ".pyc")
}

// rangePattern matches the account ranges conc/desi accept: 1-3, 1~3, 1_3.
var rangePattern = regexp.MustCompile(`^(\d+)([-~_])(\d+)$`)

// expandRange ports handle_env_split(): "-max"/"max-" become the array length,
// ranges expand inclusively in either direction, and duplicates are dropped.
func expandRange(spec string, length int) []int {
	normalized := strings.ReplaceAll(spec, "-max", "-"+strconv.Itoa(length))
	normalized = strings.ReplaceAll(normalized, "max-", strconv.Itoa(length)+"-")

	result := make([]int, 0, length)
	seen := map[int]struct{}{}
	add := func(value int) {
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}

	for _, part := range strings.Fields(normalized) {
		if match := rangePattern.FindStringSubmatch(part); match != nil {
			start, _ := strconv.Atoi(match[1])
			end, _ := strconv.Atoi(match[3])
			step := 1
			if start > end {
				step = -1
			}
			for value := start; ; value += step {
				add(value)
				if value == end {
					break
				}
			}
			continue
		}
		if value, err := strconv.Atoi(part); err == nil {
			add(value)
		}
	}
	return result
}

// splitAccounts resolves the account list of a conc/desi run: the env var value
// split on "&" and the 1-based indexes the command selected.
func (r *Runner) splitAccounts(plan *Plan) ([]string, []int, error) {
	raw := r.baseEnv(plan).Get(plan.EnvParam)
	if strings.TrimSpace(raw) == "" {
		return nil, nil, nil
	}
	values := strings.Split(raw, "&")

	spec := plan.NumParam
	if spec == "" {
		spec = "1-max"
	}
	indexes := make([]int, 0, len(values))
	for _, index := range expandRange(spec, len(values)) {
		if index >= 1 && index <= len(values) {
			indexes = append(indexes, index)
		}
	}
	return values, indexes, nil
}

func joinIndexes(indexes []int) string {
	parts := make([]string, 0, len(indexes))
	for _, index := range indexes {
		parts = append(parts, strconv.Itoa(index))
	}
	return strings.Join(parts, " ")
}
