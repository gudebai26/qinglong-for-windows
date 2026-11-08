// Package runner executes one task the way shell/task.sh + shell/otask.sh do:
// parse the command, resolve the interpreter, assemble the environment, write
// the log header/footer, enforce CommandTimeoutTime and kill the whole process
// tree on timeout or stop.
//
// Tasks are always started directly (node/python/bash/...), never through a
// shell, so quoting in a task command cannot turn into code execution.
package runner

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"qinglong-go/internal/config"
	"qinglong-go/internal/i18n"
	"qinglong-go/internal/platform"
	"qinglong-go/internal/preload"
)

// Request is one task to run.
type Request struct {
	// Command is the full command line, including the leading NAME=value
	// assignments the panel puts in front of it.
	Command string
	// CronID is the task id, used to stop a running execution.
	CronID int64
	// Sink receives the task output. The service passes the opened log file.
	Sink io.Writer
	// Console also receives the output when the command tees (interactive
	// runs). nil means os.Stdout.
	Console io.Writer
}

// Result is the outcome of one run.
type Result struct {
	ExitCode int64
	Duration time.Duration
	Stopped  bool
	TimedOut bool
}

// Runner executes tasks. It is safe for concurrent use.
type Runner struct {
	cfg    *config.Config
	logger *slog.Logger

	node   string
	tsNode string
	python string
	bash   string
	goBin  string

	preloadDir string

	pnpmOnce  sync.Once
	pnpmPath  string
	pnpmPaths []string

	probeOnce sync.Once
	probeOK   bool

	pyProbeOnce sync.Once
	pyProbeOK   bool

	mu      sync.Mutex
	running map[int64]*Execution
}

// New resolves the interpreters once and prepares the preload directory.
func New(cfg *config.Config, logger *slog.Logger) *Runner {
	if logger == nil {
		logger = slog.Default()
	}
	r := &Runner{
		cfg:     cfg,
		logger:  logger,
		node:    lookPath("node"),
		tsNode:  lookPath("ts-node-transpile-only", "ts-node"),
		bash:    platform.BashPath(),
		goBin:   lookPath("go"),
		running: map[int64]*Execution{},
	}
	r.python = lookPath("python3")
	if r.python == "" {
		r.python = lookPath("python")
	}
	r.preloadDir = filepath.Join(cfg.RootPath, "shell", "preload")
	if err := preload.Materialize(r.preloadDir); err != nil {
		logger.Warn("[task] materialize preload failed", "error", err)
	}
	return r
}

// lookPath returns the first program found on PATH, or "" when none exists.
func lookPath(names ...string) string {
	for _, name := range names {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	return ""
}

// PreloadDir is where the Node/Python hooks live.
func (r *Runner) PreloadDir() string { return r.preloadDir }

// Bootstrap creates the files a task expects to exist: the hook scripts the
// preload requires, the dependency directory and the preload itself. Node's
// back/loaders/initFile.ts creates the same files on first boot.
func (r *Runner) Bootstrap() error {
	if err := os.MkdirAll(r.cfg.ConfigPath, 0o755); err != nil {
		return err
	}
	files := map[string]string{
		"task_before.sh": "#!/usr/bin/env bash\n",
		"task_after.sh":  "#!/usr/bin/env bash\n",
		"task_before.js": "// The JavaScript code that executes before the JavaScript task execution will execute.",
		"task_before.py": "# The Python code that executes before the Python task execution will execute.",
	}
	for name, content := range files {
		path := filepath.Join(r.cfg.ConfigPath, name)
		if _, err := os.Stat(path); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return fmt.Errorf("create %s: %w", path, err)
		}
	}
	if err := os.MkdirAll(r.cfg.DepPath, 0o755); err != nil {
		return fmt.Errorf("create dependency directory: %w", err)
	}
	if err := preload.MaterializeProto(r.cfg.RootPath); err != nil {
		return err
	}
	return preload.Materialize(r.preloadDir)
}

// Execution is one running task.
type Execution struct {
	runner     *Runner
	plan       *Plan
	log        logTarget
	sink       io.Writer
	consoleOut io.Writer

	startedAt time.Time
	pid       int
	cmds      []*exec.Cmd

	// hookExports holds the variables the task_before hook exported; the task
	// inherits them like a sourced hook in the shell wrapper.
	hookExports map[string]string

	// conc runs write to one file per account and are concatenated afterwards.
	childFiles []*os.File
	childLogs  []string

	stopped  atomic.Bool
	timedOut atomic.Bool
	finished atomic.Bool

	// failureCode is set by Start when the command could not be started at
	// all (bad timeout, missing interpreter, invalid parameters).
	failureCode int64

	// result is kept so a second Wait returns the same outcome.
	result Result
	done   chan struct{}
}

// LogPath is the log file, relative to data/log, that the run writes.
func (e *Execution) LogPath() string { return e.log.Path }

// Pid is the process id of the task (the first child for conc runs).
func (e *Execution) Pid() int { return e.pid }

// SetSink replaces the output writer before Start. Script debug runs resolve
// their log path from the parsed command, so the service opens the file after
// Prepare and installs it here.
func (e *Execution) SetSink(w io.Writer) {
	if w != nil {
		e.sink = w
	}
}

// Plan exposes the parsed command, for logging and tests.
func (e *Execution) Plan() *Plan { return e.plan }

// Prepare parses the command and resolves the log destination. A command that
// cannot run (unknown mode, missing account parameter, malformed timeout) is
// reported by Start and Wait as a failed task, exactly like the shell wrapper,
// so the panel still records a log and an exit code.
func (r *Runner) Prepare(req Request) (*Execution, error) {
	plan, err := r.ParseCommand(req.Command)
	if err != nil {
		return nil, err
	}

	execution := &Execution{
		runner:     r,
		plan:       plan,
		log:        r.resolveLog(plan, time.Now()),
		sink:       req.Sink,
		consoleOut: req.Console,
		done:       make(chan struct{}),
	}
	if execution.sink == nil {
		execution.sink = io.Discard
	}
	return execution, nil
}

// Start writes the log header, applies the random delay, runs the before hook
// and spawns the task. A task that cannot start is reported in the log and
// finished by Wait with the matching exit code.
func (e *Execution) Start() error {
	r := e.runner
	e.startedAt = time.Now()

	if e.plan.ShowLog {
		r.logger.Debug("[task] log path", "path", e.log.Path)
	}

	// ## 开始执行... <time> followed by a blank line, byte for byte the output
	// of `t '## 开始执行... %s\n'` in shell/otask.sh.
	fmt.Fprint(e.sink, i18n.Tf("## 开始执行... %s\n\n", e.startedAt.Format("2006-01-02 15:04:05")))

	if e.plan.Failure != "" {
		fmt.Fprint(e.sink, e.plan.Failure)
		e.failureCode = e.plan.FailureCode
		return nil
	}

	timeoutRaw := e.plan.TimeoutRaw
	if timeoutRaw == "" {
		timeoutRaw = r.cfg.Get("CommandTimeoutTime")
	}
	if _, err := ParseTimeout(timeoutRaw); err != nil {
		// shell/task-timeout.sh prints this on stderr and returns 125.
		fmt.Fprintf(e.sink, "Invalid task timeout: %s\n", timeoutRaw)
		e.failureCode = invalidTimeoutExitCode
		return nil
	}

	if delay, message := r.randomDelay(e.plan, e.startedAt); delay > 0 {
		io.WriteString(e.sink, message)
		r.logger.Info("[task] random delay", "seconds", int(delay.Seconds()), "cron_id", e.plan.Env["ID"])
		time.Sleep(delay)
	}

	e.runBeforeHook()

	if err := e.spawn(); err != nil {
		fmt.Fprintf(e.sink, "%s\n", err)
		e.failureCode = 1
		if errors.Is(err, exec.ErrNotFound) || strings.Contains(err.Error(), "executable file not found") {
			e.failureCode = 127
		}
		return nil
	}

	if e.plan.Env["ID"] != "" && e.plan.Env["ID"] != "0" {
		if id := numericID(e.plan.Env["ID"]); id > 0 {
			r.mu.Lock()
			r.running[id] = e
			r.mu.Unlock()
		}
	}
	return nil
}

// spawn starts the process(es) for the plan.
func (e *Execution) spawn() error {
	plan := e.plan

	switch plan.Mode {
	case ModeConcurrent:
		return e.spawnConcurrent()
	case ModeDesignated:
		return e.spawnDesignated()
	}

	env := e.taskEnv(nil)
	cmd, err := e.command(plan.Program, plan.Script, plan.Args, env)
	if err != nil {
		return err
	}
	cmd.Stdout = e.stdout()
	cmd.Stderr = e.stdout()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start task: %w", err)
	}
	e.cmds = []*exec.Cmd{cmd}
	e.pid = cmd.Process.Pid
	return nil
}

// spawnConcurrent runs one process per account. Each child writes to its own
// log file so the outputs do not interleave, then the files are appended to the
// task log in order, exactly like run_concurrent().
func (e *Execution) spawnConcurrent() error {
	plan := e.plan
	values, indexes, err := e.runner.splitAccounts(plan)
	if err != nil {
		return err
	}
	if len(indexes) == 0 {
		fmt.Fprintf(e.sink, "\n环境变量 %s 没有可运行的账号\n\n", plan.EnvParam)
		return nil
	}

	dir := e.tempLogDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create concurrent log directory: %w", err)
	}
	started := time.Now()
	files := make([]string, 0, len(indexes))
	cmds := make([]*exec.Cmd, 0, len(indexes))
	for position, index := range indexes {
		overrides := map[string]string{
			"envParam": plan.EnvParam,
			"numParam": itoa(index),
		}
		if !isJSPython(plan.Script) {
			overrides[plan.EnvParam] = values[index-1]
		}
		env := e.taskEnv(overrides)

		name := fmt.Sprintf("%s_%d.log", started.Format("2006-01-02-15-04-05"), position+1)
		path := filepath.Join(dir, name)
		file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return fmt.Errorf("create concurrent log: %w", err)
		}
		cmd, err := e.command(plan.Program, plan.Script, plan.Args, env)
		if err != nil {
			file.Close()
			return err
		}
		cmd.Stdout = file
		cmd.Stderr = file
		if err := cmd.Start(); err != nil {
			file.Close()
			return fmt.Errorf("start concurrent task: %w", err)
		}
		files = append(files, path)
		cmds = append(cmds, cmd)
		// The file is closed by Wait via cmd.Wait's completion; keep the handle
		// referenced so it lives until the child exits.
		e.childFiles = append(e.childFiles, file)
	}
	e.cmds = cmds
	e.pid = cmds[0].Process.Pid
	e.childLogs = files
	return nil
}

// spawnDesignated runs one process restricted to the selected accounts.
func (e *Execution) spawnDesignated() error {
	plan := e.plan
	values, indexes, err := e.runner.splitAccounts(plan)
	if err != nil {
		return err
	}
	if len(indexes) == 0 {
		fmt.Fprintf(e.sink, "\n环境变量 %s 没有可运行的账号\n\n", plan.EnvParam)
		return nil
	}

	overrides := map[string]string{
		"envParam": plan.EnvParam,
		"numParam": joinIndexes(indexes),
	}
	if !isJSPython(plan.Script) {
		selected := make([]string, 0, len(indexes))
		for _, index := range indexes {
			selected = append(selected, values[index-1])
		}
		overrides[plan.EnvParam] = strings.Join(selected, "&")
	}
	env := e.taskEnv(overrides)

	cmd, err := e.command(plan.Program, plan.Script, plan.Args, env)
	if err != nil {
		return err
	}
	cmd.Stdout = e.stdout()
	cmd.Stderr = e.stdout()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start task: %w", err)
	}
	e.cmds = []*exec.Cmd{cmd}
	e.pid = cmd.Process.Pid
	return nil
}

// command builds one child process: the interpreter plus the script and its
// arguments, started directly with no shell in between.
func (e *Execution) command(program, script string, args []string, env []string) (*exec.Cmd, error) {
	var cmd *exec.Cmd
	switch {
	case program == "":
		// run_else form: the first word is the program itself.
		cmd = exec.Command(script, args...)
	case strings.HasSuffix(script, ".go"):
		cmd = exec.Command(program, append([]string{"run", script}, args...)...)
	case program == e.runner.bash && len(args) == 0:
		// The shell wrapper runs a task with `. "$@"`, so a script started
		// without arguments sees its own name in $1. Keep that observable
		// behaviour instead of silently changing what scripts read.
		cmd = exec.Command(program, script, script)
	default:
		cmd = exec.Command(program, append([]string{script}, args...)...)
	}
	cmd.Dir = e.plan.WorkDir
	cmd.Env = env
	platform.PrepareCommand(cmd)
	return cmd, nil
}

// stdout is where a task writes: the log sink, plus the console when the
// command tees (an interactive run without no_tee/real_time).
func (e *Execution) stdout() io.Writer {
	switch e.log.Mode {
	case logDiscard:
		return io.Discard
	case logTee:
		console := e.console()
		if console == nil {
			return e.sink
		}
		return io.MultiWriter(e.sink, console)
	default:
		return e.sink
	}
}

func (e *Execution) console() io.Writer {
	if e.consoleOut != nil {
		return e.consoleOut
	}
	return os.Stdout
}

// taskEnv merges the variables exported by the before hook with the per-run
// overrides and renders the child environment.
func (e *Execution) taskEnv(overrides map[string]string) []string {
	merged := make(map[string]string, len(overrides)+len(e.hookExports))
	for key, value := range e.hookExports {
		merged[key] = value
	}
	for key, value := range overrides {
		merged[key] = value
	}
	return e.runner.buildEnv(e.plan, merged)
}

// tempLogDir is where conc children write their own log files before they are
// concatenated into the task log.
func (e *Execution) tempLogDir() string {
	if e.log.Absolute != "" {
		return filepath.Dir(e.log.Absolute)
	}
	return os.TempDir()
}

// appendChildLogs copies the per-account logs into the task log in order and
// removes them, like the `cat` loop at the end of run_concurrent().
func (e *Execution) appendChildLogs() {
	for _, path := range e.childLogs {
		content, err := os.ReadFile(path)
		if err == nil && len(content) > 0 {
			_, _ = e.sink.Write(content)
		}
		_ = os.Remove(path)
	}
	e.childLogs = nil
}

// Wait blocks until the task ends, enforcing CommandTimeoutTime, and writes the
// footer. Calling it twice returns the first result instead of reaping the
// process again.
func (e *Execution) Wait() Result {
	select {
	case <-e.done:
		return e.result
	default:
	}

	code := e.failureCode
	timedOut := false
	if code == 0 {
		timeoutRaw := e.plan.TimeoutRaw
		if timeoutRaw == "" {
			timeoutRaw = e.runner.cfg.Get("CommandTimeoutTime")
		}
		timeout, err := ParseTimeout(timeoutRaw)
		if err != nil {
			code = invalidTimeoutExitCode
		} else {
			code, timedOut = e.waitChildren(timeout)
		}
	}
	result := Result{
		ExitCode: code,
		Duration: time.Since(e.startedAt),
		TimedOut: timedOut,
		Stopped:  e.stopped.Load(),
	}
	if result.TimedOut {
		result.ExitCode = timeoutExitCode
	} else if result.Stopped {
		result.ExitCode = stopExitCode
	}
	// A stopped task skips the after hook, like the signal handler in
	// shell/otask.sh which jumps straight to handle_task_end.
	if !result.Stopped {
		e.runAfterHook()
	}
	e.finished.Store(true)
	e.writeFooter(result)

	e.result = result
	close(e.done)
	e.runner.mu.Lock()
	if id := numericID(e.plan.Env["ID"]); id > 0 {
		if current, ok := e.runner.running[id]; ok && current == e {
			delete(e.runner.running, id)
		}
	}
	e.runner.mu.Unlock()
	return result
}

// waitChildren waits for every child, escalating to SIGTERM then SIGKILL when
// the timeout expires or the task is stopped.
func (e *Execution) waitChildren(timeout time.Duration) (int64, bool) {
	if len(e.cmds) == 0 {
		return 0, false
	}

	type outcome struct {
		index int
		code  int64
	}
	results := make(chan outcome, len(e.cmds))
	for index, cmd := range e.cmds {
		go func(index int, cmd *exec.Cmd) {
			results <- outcome{index: index, code: exitCodeOf(cmd.Wait())}
		}(index, cmd)
	}

	remaining := len(e.cmds)
	codes := make([]int64, len(e.cmds))
	finish := func() {
		for _, file := range e.childFiles {
			file.Close()
		}
		e.appendChildLogs()
	}

	if timeout <= 0 {
		for remaining > 0 {
			result := <-results
			codes[result.index] = result.code
			remaining--
		}
		finish()
		return lastFailure(codes), false
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for remaining > 0 {
		select {
		case result := <-results:
			codes[result.index] = result.code
			remaining--
		case <-timer.C:
			timer.Stop()
			e.timedOut.Store(true)
			e.terminate()
			for remaining > 0 {
				result := <-results
				codes[result.index] = result.code
				remaining--
			}
			finish()
			return timeoutExitCode, true
		}
	}
	finish()
	return lastFailure(codes), false
}

// terminate asks the whole process tree to exit and kills it after the grace
// period, mirroring shell/task-timeout.sh.
func (e *Execution) terminate() {
	for _, cmd := range e.cmds {
		if cmd.Process == nil {
			continue
		}
		pid := cmd.Process.Pid
		_ = platform.TerminateTree(pid)
		go func(pid int) {
			time.Sleep(killGrace)
			_ = platform.KillProcessTree(pid)
		}(pid)
	}
}

// Stop ends the task because the panel asked for it. The waiting goroutine
// writes the "已停止" footer and reports exit code 143.
func (e *Execution) Stop() bool {
	if e.finished.Load() || !e.stopped.CompareAndSwap(false, true) {
		return false
	}
	e.terminate()
	return true
}

// Stop ends the task with the given id. It returns false when the task is not
// running in this process.
func (r *Runner) Stop(cronID int64) bool {
	r.mu.Lock()
	execution := r.running[cronID]
	r.mu.Unlock()
	if execution == nil {
		return false
	}
	return execution.Stop()
}

// Abort ends a task the panel decided not to keep (a lost claim, a mutation
// during startup) and reaps the process so nothing is left behind.
func (e *Execution) Abort() {
	e.Stop()
	_ = e.Wait()
}

// Running reports whether the task is currently executing.
func (r *Runner) Running(cronID int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.running[cronID]
	return ok
}

// writeFooter prints the closing line in the exact shape handle_task_end uses.
func (e *Execution) writeFooter(result Result) {
	endTime := time.Now().Format("2006-01-02 15:04:05")
	elapsed := int64(result.Duration.Seconds())
	if elapsed == 0 {
		elapsed = 1
	}
	const tail = "　　　　　" // five ideographic spaces, as in shell/otask.sh
	switch {
	case result.Stopped:
		fmt.Fprint(e.sink, i18n.Tf("\n## 已停止 🛑... %s  耗时 %s 秒%s", endTime, elapsed, tail)+"\n")
	case result.ExitCode == 0:
		fmt.Fprint(e.sink, i18n.Tf("\n## 完成 ✅... %s  耗时 %s 秒%s", endTime, elapsed, tail)+"\n")
	default:
		fmt.Fprint(e.sink, i18n.Tf("\n## 失败 ❌(退出码 %s)... %s  耗时 %s 秒%s", result.ExitCode, endTime, elapsed, tail)+"\n")
	}
}

func exitCodeOf(err error) int64 {
	if err == nil {
		return 0
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return int64(exitError.ExitCode())
	}
	return 1
}

// lastFailure mirrors bash `wait`: the last non-zero status wins, otherwise 0.
func lastFailure(codes []int64) int64 {
	code := int64(0)
	for _, value := range codes {
		if value != 0 {
			code = value
		}
	}
	return code
}

func isJSPython(name string) bool {
	return strings.HasSuffix(name, ".js") || strings.HasSuffix(name, ".mjs") ||
		strings.HasSuffix(name, ".ts") || strings.HasSuffix(name, ".py") ||
		strings.HasSuffix(name, ".pyc")
}
