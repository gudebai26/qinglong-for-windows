package runner

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"qinglong-go/internal/i18n"
	"qinglong-go/internal/platform"
)

// hookSpec is one of the two hook phases.
type hookSpec struct {
	file    string
	body    string
	before  string
	after   string
	workDir string
}

// runBeforeHook executes data/config/task_before.sh plus the task's own
// task_before body for tasks the preload does not cover (shell and other
// commands). Variables the hook exports are merged into the task environment,
// because in the shell wrapper the hook runs in the same process as the task.
func (e *Execution) runBeforeHook() {
	if isJSPython(e.plan.Script) {
		// The Node/Python preload sources the same file inside the task
		// process, so the hook runs there and not here.
		return
	}
	exports := e.runHook(hookSpec{
		file: filepath.Join(e.runner.cfg.ConfigPath, "task_before.sh"),
		body: e.plan.Env["task_before"],
		// t '执行前置命令\n' and t '\n执行前置命令结束\n' each gain one newline.
		before:  i18n.T("执行前置命令\n") + "\n",
		after:   i18n.T("\n执行前置命令结束\n") + "\n",
		workDir: e.runner.cfg.RootPath,
	})
	if len(exports) > 0 {
		e.hookExports = exports
	}
}

// runAfterHook executes data/config/task_after.sh plus the task's task_after
// body. It runs for every task type, exactly like the wrapper's unconditional
// run_task_after call.
func (e *Execution) runAfterHook() {
	e.runHook(hookSpec{
		file:    filepath.Join(e.runner.cfg.ConfigPath, "task_after.sh"),
		body:    e.plan.Env["task_after"],
		before:  i18n.T("\n执行后置命令\n") + "\n",
		after:   i18n.T("\n执行后置命令结束") + "\n",
		workDir: e.plan.WorkDir,
	})
}

// runHook sources the hook file, evaluates the task body in the same shell and
// returns the environment the hook left behind.
func (e *Execution) runHook(spec hookSpec) map[string]string {
	// Retry the lookup instead of trusting the boot-time result: an operator who
	// installs Git for Windows while the panel is running should not have to
	// restart it before the hooks start working.
	bash := e.runner.bash
	if bash == "" {
		bash = platform.BashPath()
	}
	if bash == "" {
		// Silent in the task log: a machine without Git still runs JS and Python
		// tasks fine, and repeating three lines of install advice in every log
		// buries the script's own output. The boot log already reports the
		// missing tool once, which is where an operator looks.
		e.runner.logger.Debug("[task] bash not found, skipping task hook",
			"hook", filepath.Base(spec.file), "cron_id", e.plan.Env["ID"])
		return nil
	}

	env := e.runner.baseEnv(e.plan)
	env.Set("QL_HOOK_FILE", spec.file)
	env.Set("QL_HOOK_BODY", strings.TrimSuffix(spec.body, ";"))

	envFile := ""
	if spec.body != "" {
		file, err := os.CreateTemp("", "ql-hook-env-*.list")
		if err == nil {
			envFile = file.Name()
			file.Close()
			defer os.Remove(envFile)
			env.Set("QL_HOOK_ENV", envFile)
		}
	}

	script := `. "$QL_HOOK_FILE" "$@"`
	if spec.body != "" {
		script += "\neval \"$QL_HOOK_BODY\""
	}
	if envFile != "" {
		script += "\nenv -0 > \"$QL_HOOK_ENV\" 2>/dev/null || true"
	}

	if spec.body != "" {
		out := e.stdout()
		fmt.Fprint(out, spec.before)
	}

	cmd := exec.Command(bash, append([]string{"-c", script, "--"}, e.plan.ArgsAll...)...)
	cmd.Dir = spec.workDir
	cmd.Env = env.Slice()
	cmd.Stdout = e.stdout()
	cmd.Stderr = e.stdout()
	platform.PrepareCommand(cmd)
	runErr := cmd.Run()

	if spec.body != "" {
		fmt.Fprint(e.stdout(), spec.after)
	}
	if runErr != nil {
		e.runner.logger.Warn("[task] task hook failed",
			"hook", filepath.Base(spec.file), "cron_id", e.plan.Env["ID"], "error", runErr)
	}
	if envFile == "" {
		return nil
	}
	return readEnvFile(envFile)
}

// readEnvFile parses the NUL separated `env -0` dump of the hook process.
func readEnvFile(path string) map[string]string {
	content, err := os.ReadFile(path)
	if err != nil || len(content) == 0 {
		return nil
	}
	result := map[string]string{}
	for _, entry := range bytes.Split(content, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		key, value, ok := strings.Cut(string(entry), "=")
		if !ok || key == "" {
			continue
		}
		result[key] = value
	}
	return result
}
