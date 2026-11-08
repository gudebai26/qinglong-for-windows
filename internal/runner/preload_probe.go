package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"qinglong-go/internal/platform"
)

// preloadJSEntry is the file node --require loads.
func preloadJSEntry(dir string) string { return filepath.Join(dir, "sitecustomize.js") }

// preloadAvailable checks once whether Node can load the preload module. The
// hook needs @grpc/grpc-js (it backs the ql API); when a deployment has no
// node_modules the module is missing and every task would abort at start-up.
// In that case the runner keeps running tasks without the ql API instead of
// failing them, and says so once in the panel log.
//
// sitecustomize.js requires ./client.js at the top of the file, before it
// bails out on the missing argv[1], so a successful require here proves the
// whole ql API dependency chain (grpc-js, proto-loader, the proto file) loads.
func (r *Runner) preloadAvailable() bool {
	r.probeOnce.Do(func() {
		entry := preloadJSEntry(r.preloadDir)
		if _, err := os.Stat(entry); err != nil {
			r.probeOK = false
			return
		}
		if r.node == "" {
			r.probeOK = false
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		// -e leaves process.argv[1] undefined, so sitecustomize returns before
		// running the task hooks; only the module graph is loaded.
		cmd := exec.CommandContext(ctx, r.node, "-e", "require(process.env.QL_PRELOAD_PROBE)")
		cmd.Dir = r.cfg.RootPath
		env := newEnvList(os.Environ())
		env.Set("QL_PRELOAD_PROBE", entry)
		// client.js resolves its gRPC certificate directory from these.
		env.Set("QL_DIR", filepath.ToSlash(r.cfg.RootPath))
		env.Set("QL_DATA_DIR", filepath.ToSlash(r.cfg.DataPath))
		// The vendored ql API runtime lives in <root>/node_modules; Node finds
		// it by walking up from shell/preload, and NODE_PATH covers tasks that
		// run from another working directory.
		env.AppendPathList("NODE_PATH", append([]string{
			r.cfg.DepPath,
			filepath.Join(r.cfg.DepPath, "node_modules"),
			filepath.Join(r.cfg.RootPath, "node_modules"),
		}, r.pnpmNodePaths()...)...)
		cmd.Env = env.Slice()
		output, err := cmd.CombinedOutput()
		if err != nil {
			r.probeOK = false
			message := strings.TrimSpace(string(output))
			if len(message) > 400 {
				message = message[:400] + "..."
			}
			r.logger.Warn("[task] preload unavailable, tasks run without the ql API",
				"preload", entry, "error", err, "output", message)
			return
		}
		r.probeOK = true
	})
	return r.probeOK
}

// pythonPreloadAvailable checks once whether the Python hook can start without
// printing an error. The upstream sitecustomize.py dumps its environment into a
// hard-coded /tmp path and shells out through cmd.exe on Windows; when that
// fails it prints a traceback into every task log. The hook is skipped in that
// case (the task still runs, without QLAPI) so a broken platform does not
// pollute every log - Node behaves the same way there because its ':' joined
// PYTHONPATH never lets Python import the hook at all.
func (r *Runner) pythonPreloadAvailable() bool {
	r.pyProbeOnce.Do(func() {
		if r.python == "" {
			return
		}
		// An empty hook file keeps the probe from running user code.
		hook, err := os.CreateTemp("", "ql-py-probe-*.sh")
		if err != nil {
			return
		}
		hookPath := hook.Name()
		hook.Close()
		defer os.Remove(hookPath)

		env := newEnvList(os.Environ())
		env.Set("QL_DIR", filepath.ToSlash(r.cfg.RootPath))
		env.Set("QL_DATA_DIR", filepath.ToSlash(r.cfg.DataPath))
		env.Set("file_task_before", filepath.ToSlash(hookPath))
		env.Set("dir_scripts", filepath.ToSlash(r.cfg.ScriptPath))
		env.Set("task_before", "")
		env.Set("QL_LANG", r.cfg.GetDefault("QL_LANG", "zh"))
		env.PrependPathList("PYTHONPATH",
			filepath.ToSlash(r.preloadDir), filepath.ToSlash(r.cfg.ConfigPath))

		cmd := exec.Command(r.python, "-c", "pass")
		cmd.Dir = r.cfg.ScriptPath
		cmd.Env = env.Slice()
		output, err := cmd.CombinedOutput()
		text := string(output)
		if err != nil || strings.Contains(text, "run task before error") ||
			strings.Contains(text, "Traceback") || strings.Contains(text, "run builtin code error") {
			r.pyProbeOK = false
			message := strings.TrimSpace(text)
			if len(message) > 400 {
				message = message[:400] + "..."
			}
			r.logger.Warn("[task] python preload unavailable, tasks run without QLAPI",
				"preload", r.preloadDir, "error", err, "output", message)
			return
		}
		r.pyProbeOK = true
	})
	return r.pyProbeOK
}

// pnpmGlobal resolves the pnpm global module root once, so globally installed
// packages are importable from tasks (NODE_PATH). The probe is shared with the
// dependency page through platform.PnpmGlobalRoot, which also surfaces pnpm's
// "global bin directory is not in PATH" error instead of silently returning "".
func (r *Runner) pnpmNodePaths() []string {
	// The directory listing is recomputed on every call: pnpm 11+ creates a new
	// <hash>/node_modules project per `pnpm add -g`, so a dependency installed
	// while the panel is running is only resolvable if NODE_PATH is rebuilt.
	// PnpmGlobalRoot itself is cached; the walk is a few readdir calls.
	r.pnpmOnce.Do(func() {
		entries := platform.PnpmNodePathEntries()
		if len(entries) == 0 {
			if _, err := platform.PnpmGlobalRoot(); err != nil {
				r.logger.Warn("[task] pnpm global root unavailable, globally installed packages may not resolve",
					"error", err)
			}
		}
	})
	return platform.PnpmNodePathEntries()
}
