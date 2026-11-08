package runner

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"qinglong-go/internal/config"
	"qinglong-go/internal/platform"
)

// newExecRoot builds a throw-away panel root with the directories and hook
// files a task expects.
func newExecRoot(t *testing.T, values map[string]string) (*Runner, *config.Config) {
	t.Helper()
	root := t.TempDir()
	cfg := testConfig(t, root, values)
	for _, dir := range []string{cfg.ConfigPath, cfg.ScriptPath, cfg.LogPath} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	r := New(cfg, nil)
	if err := r.Bootstrap(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	return r, cfg
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// runTask executes one command and returns its result plus the captured log.
func runTask(t *testing.T, r *Runner, command string) (Result, string) {
	t.Helper()
	sink, err := os.CreateTemp(t.TempDir(), "task-*.log")
	if err != nil {
		t.Fatalf("create sink: %v", err)
	}
	defer sink.Close()

	execution, err := r.Prepare(Request{Command: command, Sink: sink})
	if err != nil {
		t.Fatalf("prepare %q: %v", command, err)
	}
	if err := execution.Start(); err != nil {
		t.Fatalf("start %q: %v", command, err)
	}
	result := execution.Wait()

	content, err := os.ReadFile(sink.Name())
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	return result, string(content)
}

func requireProgram(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s is not installed", name)
	}
}

func assertLogShape(t *testing.T, log string) {
	t.Helper()
	if !strings.HasPrefix(log, "## 开始执行... ") {
		t.Fatalf("missing header: %q", firstLine(log))
	}
	// The header is followed by a blank line (t '...\n' + the trailing newline
	// of the t() helper).
	header := strings.SplitN(log, "\n", 3)
	if len(header) < 3 || header[1] != "" {
		t.Fatalf("header is not followed by a blank line: %q", log[:min(len(log), 80)])
	}
	if !strings.HasSuffix(log, "秒　　　　　\n") {
		t.Fatalf("footer has no trailing ideographic spaces: %q", tail(log, 80))
	}
}

func TestExecJavaScript(t *testing.T) {
	requireProgram(t, "node")
	r, cfg := newExecRoot(t, nil)
	writeFile(t, filepath.Join(cfg.ScriptPath, "demo.js"),
		"console.log('js ok', process.argv.slice(2).join(','));\n")

	result, log := runTask(t, r, "task demo.js -- --flag 1")
	if result.ExitCode != 0 {
		t.Fatalf("exit code = %d, log:\n%s", result.ExitCode, log)
	}
	if !strings.Contains(log, "js ok --flag,1") {
		t.Fatalf("script output missing:\n%s", log)
	}
	if !strings.Contains(log, "## 完成 ✅... ") {
		t.Fatalf("missing success footer:\n%s", log)
	}
	assertLogShape(t, log)
}

func TestExecPython(t *testing.T) {
	requireProgram(t, "python3")
	r, cfg := newExecRoot(t, nil)
	writeFile(t, filepath.Join(cfg.ScriptPath, "demo.py"), "import sys\nprint('py ok', ' '.join(sys.argv[1:]))\n")

	result, log := runTask(t, r, "task demo.py -- a b")
	if result.ExitCode != 0 {
		t.Fatalf("exit code = %d, log:\n%s", result.ExitCode, log)
	}
	if !strings.Contains(log, "py ok a b") {
		t.Fatalf("script output missing:\n%s", log)
	}
	assertLogShape(t, log)
}

func TestExecShell(t *testing.T) {
	requireProgram(t, "bash")
	r, cfg := newExecRoot(t, nil)
	writeFile(t, filepath.Join(cfg.ScriptPath, "demo.sh"), "echo \"sh ok $1\"\n")

	result, log := runTask(t, r, "task demo.sh -- hello")
	if result.ExitCode != 0 {
		t.Fatalf("exit code = %d, log:\n%s", result.ExitCode, log)
	}
	if !strings.Contains(log, "sh ok hello") {
		t.Fatalf("script output missing:\n%s", log)
	}
	assertLogShape(t, log)
}

// run_else form: the first word is the program, not a script extension.
func TestExecRunElse(t *testing.T) {
	requireProgram(t, "node")
	r, cfg := newExecRoot(t, nil)
	writeFile(t, filepath.Join(cfg.ScriptPath, "demo.js"), "console.log('run_else ok')\n")

	result, log := runTask(t, r, "task node demo.js")
	if result.ExitCode != 0 {
		t.Fatalf("exit code = %d, log:\n%s", result.ExitCode, log)
	}
	if !strings.Contains(log, "run_else ok") {
		t.Fatalf("script output missing:\n%s", log)
	}
}

func TestExecTimeout(t *testing.T) {
	requireProgram(t, "node")
	r, cfg := newExecRoot(t, map[string]string{"CommandTimeoutTime": "1s"})
	writeFile(t, filepath.Join(cfg.ScriptPath, "slow.js"), "setTimeout(() => {}, 30000);\n")

	start := time.Now()
	result, log := runTask(t, r, "task slow.js")
	elapsed := time.Since(start)

	if result.ExitCode != 124 {
		t.Fatalf("exit code = %d, want 124, log:\n%s", result.ExitCode, log)
	}
	if !result.TimedOut {
		t.Fatal("result is not marked as timed out")
	}
	if elapsed > 10*time.Second {
		t.Fatalf("timeout took %s", elapsed)
	}
	if !strings.Contains(log, "## 失败 ❌(退出码 124)... ") {
		t.Fatalf("missing failure footer:\n%s", log)
	}
}

func TestExecInvalidTimeout(t *testing.T) {
	r, cfg := newExecRoot(t, nil)
	writeFile(t, filepath.Join(cfg.ScriptPath, "demo.js"), "console.log('never')\n")

	if result, log := runTask(t, r, "task demo.js"); result.ExitCode != 0 {
		t.Fatalf("sanity check failed: %d\n%s", result.ExitCode, log)
	}

	r2, cfg2 := newExecRoot(t, map[string]string{"CommandTimeoutTime": "abc"})
	writeFile(t, filepath.Join(cfg2.ScriptPath, "demo.js"), "console.log('never')\n")
	result2, log2 := runTask(t, r2, "task demo.js")
	if result2.ExitCode != 125 {
		t.Fatalf("exit code = %d, want 125, log:\n%s", result2.ExitCode, log2)
	}
	if !strings.Contains(log2, "Invalid task timeout: abc") {
		t.Fatalf("missing timeout error:\n%s", log2)
	}
	if !strings.Contains(log2, "## 失败 ❌(退出码 125)... ") {
		t.Fatalf("missing failure footer:\n%s", log2)
	}
	if strings.Contains(log2, "never") {
		t.Fatalf("script must not run with an invalid timeout:\n%s", log2)
	}
}

func TestExecRandomDelayApplied(t *testing.T) {
	requireProgram(t, "node")
	r, cfg := newExecRoot(t, map[string]string{
		"RandomDelay":               "2",
		"RandomDelayFileExtensions": "js",
		"RandomDelayIgnoredMinutes": "",
	})
	writeFile(t, filepath.Join(cfg.ScriptPath, "demo.js"), "console.log('delayed')\n")

	start := time.Now()
	result, log := runTask(t, r, "task demo.js")
	elapsed := time.Since(start)

	if result.ExitCode != 0 {
		t.Fatalf("exit code = %d, log:\n%s", result.ExitCode, log)
	}
	if !strings.Contains(log, "任务随机延迟 ") {
		t.Fatalf("missing delay message:\n%s", log)
	}
	if elapsed < 1*time.Second {
		t.Fatalf("task did not wait for the delay: %s", elapsed)
	}
	// The delay message comes after the header and before the script output.
	if strings.Index(log, "任务随机延迟") < strings.Index(log, "## 开始执行") {
		t.Fatalf("delay message is before the header:\n%s", log)
	}
}

// The first two log lines must be byte for byte the shell wrapper's output:
// `t '## 开始执行... %s\n'` followed by `t '任务随机延迟 %s 秒，将于 %s 开始，
// 配置文件参数 RandomDelay 置空可取消延迟\n'`. This is the only place the panel
// explains why a task with a long RandomDelay looks idle, so the wording and the
// blank lines matter.
func TestExecRandomDelayLogFormat(t *testing.T) {
	requireProgram(t, "node")
	r, cfg := newExecRoot(t, map[string]string{
		"RandomDelay":               "2",
		"RandomDelayFileExtensions": "",
		"RandomDelayIgnoredMinutes": "",
	})
	writeFile(t, filepath.Join(cfg.ScriptPath, "demo.js"), "console.log('body')\n")

	_, log := runTask(t, r, "task demo.js")
	pattern := regexp.MustCompile(`^## 开始执行\.\.\. \d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\n\n` +
		`任务随机延迟 [12] 秒，将于 \d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} 开始，配置文件参数 RandomDelay 置空可取消延迟\n\n` +
		`body\n`)
	if !pattern.MatchString(log) {
		t.Fatalf("log header does not match the shell wrapper://n%q", log)
	}
}

func TestExecRandomDelaySkippedForManualRun(t *testing.T) {
	requireProgram(t, "node")
	r, cfg := newExecRoot(t, map[string]string{
		"RandomDelay":               "5",
		"RandomDelayFileExtensions": "js",
		"RandomDelayIgnoredMinutes": "",
	})
	writeFile(t, filepath.Join(cfg.ScriptPath, "demo.js"), "console.log('no delay')\n")

	start := time.Now()
	result, log := runTask(t, r, "real_time=true no_delay=true task demo.js")
	if result.ExitCode != 0 {
		t.Fatalf("exit code = %d, log:\n%s", result.ExitCode, log)
	}
	if strings.Contains(log, "任务随机延迟") {
		t.Fatalf("manual run must not be delayed:\n%s", log)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("manual run took %s", elapsed)
	}
}

func TestExecHooksShell(t *testing.T) {
	requireProgram(t, "bash")
	r, cfg := newExecRoot(t, nil)
	writeFile(t, filepath.Join(cfg.ScriptPath, "demo.sh"), "echo \"hook=$HOOK_VALUE\"\n")

	result, log := runTask(t, r,
		"task_before='export HOOK_VALUE=from-before' task_after='echo after-ran' task demo.sh")
	if result.ExitCode != 0 {
		t.Fatalf("exit code = %d, log:\n%s", result.ExitCode, log)
	}
	if !strings.Contains(log, "执行前置命令") || !strings.Contains(log, "执行前置命令结束") {
		t.Fatalf("before hook messages missing:\n%s", log)
	}
	if !strings.Contains(log, "hook=from-before") {
		t.Fatalf("before hook variables were not exported to the task:\n%s", log)
	}
	if !strings.Contains(log, "执行后置命令") || !strings.Contains(log, "after-ran") {
		t.Fatalf("after hook missing:\n%s", log)
	}
}

func TestExecHooksJavaScript(t *testing.T) {
	requireProgram(t, "node")
	r, cfg := newExecRoot(t, nil)
	if !r.preloadAvailable() {
		t.Skip("node preload unavailable (@grpc/grpc-js missing)")
	}
	writeFile(t, filepath.Join(cfg.ScriptPath, "demo.js"), "console.log('hook=' + process.env.HOOK_VALUE)\n")
	writeFile(t, filepath.Join(cfg.ConfigPath, "task_before.js"), "global.__beforeRan = true\n")

	result, log := runTask(t, r, "task_before='export HOOK_VALUE=from-before' task demo.js")
	if result.ExitCode != 0 {
		t.Fatalf("exit code = %d, log:\n%s", result.ExitCode, log)
	}
	if !strings.Contains(log, "hook=from-before") {
		t.Fatalf("preload did not apply task_before:\n%s", log)
	}
}

func TestExecStop(t *testing.T) {
	requireProgram(t, "node")
	r, cfg := newExecRoot(t, nil)
	writeFile(t, filepath.Join(cfg.ScriptPath, "slow.js"), "setTimeout(() => {}, 30000);\n")

	sink, err := os.CreateTemp(t.TempDir(), "task-*.log")
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()

	execution, err := r.Prepare(Request{Command: "ID=42 task slow.js", CronID: 42, Sink: sink})
	if err != nil {
		t.Fatal(err)
	}
	if err := execution.Start(); err != nil {
		t.Fatal(err)
	}
	pid := execution.Pid()
	if pid <= 0 {
		t.Fatal("task has no pid")
	}
	time.Sleep(300 * time.Millisecond)
	if !r.Running(42) {
		t.Fatal("task is not registered as running")
	}
	if !r.Stop(42) {
		t.Fatal("stop did not find the task")
	}
	result := execution.Wait()
	if result.ExitCode != 143 {
		t.Fatalf("exit code = %d, want 143", result.ExitCode)
	}
	if !result.Stopped {
		t.Fatal("result is not marked as stopped")
	}
	if platform.ProcessAlive(pid) {
		t.Fatalf("pid %d survived the stop", pid)
	}
	content, _ := os.ReadFile(sink.Name())
	if !strings.Contains(string(content), "## 已停止 🛑... ") {
		t.Fatalf("missing stop footer:\n%s", content)
	}
	if r.Running(42) {
		t.Fatal("task is still registered after the stop")
	}
}

// conc runs one process per account; shell tasks get the account value directly.
func TestExecConcurrentAccounts(t *testing.T) {
	requireProgram(t, "bash")
	r, cfg := newExecRoot(t, nil)
	// share.sh sets file_env=$dir_preload/env.sh, so the environment page
	// writes <root>/shell/preload/env.sh and the runner reads it there.
	writeFile(t, filepath.Join(cfg.RootPath, "shell", "preload", "env.sh"),
		"export ACCOUNTS=\"first&second\"\n")
	writeFile(t, filepath.Join(cfg.ScriptPath, "demo.sh"), "echo \"account=$ACCOUNTS\"\n")

	result, log := runTask(t, r, "task demo.sh conc ACCOUNTS")
	if result.ExitCode != 0 {
		t.Fatalf("exit code = %d, log:\n%s", result.ExitCode, log)
	}
	if !strings.Contains(log, "account=first") || !strings.Contains(log, "account=second") {
		t.Fatalf("both accounts should run:\n%s", log)
	}
	if strings.Count(log, "## 开始执行") != 1 {
		t.Fatalf("conc writes a single header:\n%s", log)
	}
}

func TestExecDesignatedAccounts(t *testing.T) {
	requireProgram(t, "bash")
	r, cfg := newExecRoot(t, nil)
	writeFile(t, filepath.Join(cfg.RootPath, "shell", "preload", "env.sh"),
		"export ACCOUNTS=\"first&second&third\"\n")
	writeFile(t, filepath.Join(cfg.ScriptPath, "demo.sh"), "echo \"account=$ACCOUNTS\"\n")

	result, log := runTask(t, r, "task demo.sh desi ACCOUNTS 1 3")
	if result.ExitCode != 0 {
		t.Fatalf("exit code = %d, log:\n%s", result.ExitCode, log)
	}
	if !strings.Contains(log, "account=first&third") {
		t.Fatalf("designated accounts not applied:\n%s", log)
	}
}

// TestManualRun is a development harness, not a unit test: it runs one task
// with the real panel configuration (QL_DIR / QL_DATA_DIR) and prints the log
// path, so the output can be diffed against the Node panel.
//
//	QL_M5_RUN="real_log_path=m5/body-js.log task body.js" go test ./internal/runner -run TestManualRun -v
func TestManualRun(t *testing.T) {
	command := os.Getenv("QL_M5_RUN")
	if command == "" {
		t.Skip("set QL_M5_RUN to execute a task")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	r := New(cfg, nil)
	if err := r.Bootstrap(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	plan, err := r.ParseCommand(command)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	target := r.resolveLog(plan, time.Now())
	if err := target.prepareLogDir(); err != nil {
		t.Fatalf("prepare log: %v", err)
	}
	file, err := target.open()
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	defer file.Close()

	execution, err := r.Prepare(Request{Command: command, CronID: 0, Sink: file})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if err := execution.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	result := execution.Wait()
	t.Logf("M5LOG=%s M5EXIT=%d", target.Absolute, result.ExitCode)
}

func firstLine(value string) string {
	if index := strings.IndexByte(value, '\n'); index >= 0 {
		return value[:index]
	}
	return value
}

func tail(value string, size int) string {
	if len(value) <= size {
		return value
	}
	return value[len(value)-size:]
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
