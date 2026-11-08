package runner

import (
	"errors"
	"strings"
)

// Mode is the execution flavour selected by the task command, mirroring the
// branch in shell/otask.sh main().
type Mode int

const (
	// ModeNormal runs one process, optionally after the RandomDelay sleep.
	ModeNormal Mode = iota
	// ModeConcurrent splits the account env var and runs one process per
	// account, all at once (conc).
	ModeConcurrent
	// ModeDesignated runs one process with a subset of the accounts (desi).
	ModeDesignated
	// ModeElse runs the arguments as given (node x.js, python3 x.py, ...).
	ModeElse
)

func (m Mode) String() string {
	switch m {
	case ModeConcurrent:
		return "conc"
	case ModeDesignated:
		return "desi"
	case ModeElse:
		return "else"
	default:
		return "normal"
	}
}

// ErrUsage is returned when the command has no task arguments at all.
var ErrUsage = errors.New("task: missing script name")

// usageText is what `task` without arguments prints (shell/otask.sh usage()).
const usageText = `task命令运行本程序自动添加进crontab的脚本，需要输入脚本的绝对路径或去掉 "data/scripts/" 目录后的相对路径（定时任务中请写作相对路径），用法为：
1.task <file_name>                                             # 依次执行，如果设置了随机延迟，将随机延迟一定秒数
2.task <file_name> now                                         # 依次执行，无论是否设置了随机延迟，均立即运行，前台会输出日志，同时记录在日志文件中
3.task <file_name> conc <环境变量名称> <账号编号，空格分隔>(可选的)  # 并发执行，无论是否设置了随机延迟，均立即运行，前台不产生日志，直接记录在日志文件中，且可指定账号执行
4.task <file_name> desi <环境变量名称> <账号编号，空格分隔>         # 指定账号执行，无论是否设置了随机延迟，均立即运行`

// Plan is one parsed task command.
type Plan struct {
	Mode Mode

	// Program is the interpreter (node, python3, bash, ...). Empty means the
	// first argument is executed directly (run_else form such as `node x.js`).
	Program string
	// Script is the file handed to the interpreter, relative to WorkDir.
	Script string
	// Args are passed after the script.
	Args []string
	// WorkDir is the absolute working directory for the task.
	WorkDir string
	// FileParam is the script argument exactly as written in the command; the
	// log directory is derived from it (shell/otask.sh handle_log_path).
	FileParam string

	// EnvParam/NumParam carry the conc/desi account split.
	EnvParam string
	NumParam string

	// ShowLog is -l: print the task log path before running.
	ShowLog bool
	// TimeoutRaw is the -m value; it overrides CommandTimeoutTime.
	TimeoutRaw string

	// Env holds the leading NAME=value assignments of the command line
	// (real_time, no_tee, ID, log_name, task_before, ...).
	Env map[string]string
	// ArgsAll is every task argument after the assignments and options.
	ArgsAll []string

	// Failure is printed into the task log when the command cannot run at all
	// (no script name, missing account parameter); FailureCode is the exit code
	// the wrapper would report.
	Failure     string
	FailureCode int64
}

// taskScriptExts are the extensions shell/task.sh treats as task scripts.
var taskScriptExts = []string{".js", ".mjs", ".py", ".pyc", ".sh", ".ts"}

// allScriptExts also includes .go, which the Go runner executes with `go run`.
var allScriptExts = append(append([]string{}, taskScriptExts...), ".go")

func hasExt(name string, exts []string) bool {
	for _, ext := range exts {
		if strings.HasSuffix(name, ext) {
			return true
		}
	}
	return false
}

// isTaskWord reports whether word is the task wrapper entry point: `task`,
// `ql`, or a path to task.sh / ql.sh.
func isTaskWord(word string) bool {
	base := word
	if idx := strings.LastIndexAny(base, `/\`); idx >= 0 {
		base = base[idx+1:]
	}
	base = strings.TrimSuffix(base, ".sh")
	return base == "task" || base == "ql"
}

// isTaskScript reports whether name is a script the panel knows how to run,
// i.e. whether `task <name>` takes the script branch instead of run_else.
func isTaskScript(name string) bool { return hasExt(name, taskScriptExts) }

// ParseCommand turns one command line into a Plan. It never runs anything: the
// caller decides how to start the process.
func (r *Runner) ParseCommand(command string) (*Plan, error) {
	words, err := splitCommand(strings.TrimSpace(command))
	if err != nil {
		return nil, err
	}

	plan := &Plan{Env: map[string]string{}}

	// Leading NAME=value assignments become the task environment. The shell
	// consumes them before task.sh runs, so they are not task arguments.
	index := 0
	for index < len(words) {
		name, value, ok := isAssignment(words[index])
		if !ok {
			break
		}
		plan.Env[name] = value
		index++
	}
	args := words[index:]

	// The command in crontab.list starts with the task wrapper itself
	// (`task x.js`). The shell resolves that word; the Go runner has to drop
	// it so the remaining arguments look exactly like task.sh's "$@".
	if len(args) > 0 && isTaskWord(args[0]) {
		args = args[1:]
	}

	// getopts ":lm:" - only -l and -m are accepted, and only before the script.
	for len(args) > 0 {
		arg := args[0]
		if arg == "--" {
			args = args[1:]
			break
		}
		if len(arg) < 2 || arg[0] != '-' {
			break
		}
		switch {
		case arg == "-l":
			plan.ShowLog = true
			args = args[1:]
		case arg == "-m":
			if len(args) < 2 {
				// getopts reports a missing argument; the shell then keeps an
				// empty timeout, so the task simply runs without one.
				args = args[1:]
				continue
			}
			plan.TimeoutRaw = args[1]
			args = args[2:]
		case strings.HasPrefix(arg, "-m"):
			plan.TimeoutRaw = arg[2:]
			args = args[1:]
		default:
			// Unknown option: getopts with a leading colon stays silent and
			// stops, leaving the rest as task arguments.
			args = nil
		}
	}
	if len(args) == 0 {
		plan.Failure = "\n" + usageText + "\n"
		plan.FailureCode = 1
		return plan, nil
	}
	plan.ArgsAll = args

	// "--" separates the task parameters from the script parameters.
	taskArgs, scriptArgs := args, []string(nil)
	for i, arg := range args {
		if arg == "--" {
			taskArgs, scriptArgs = args[:i], args[i+1:]
			break
		}
	}

	if isTaskScript(taskArgs[0]) {
		switch {
		case len(taskArgs) == 1:
			plan.Mode = ModeNormal
		case taskArgs[1] == "now":
			plan.Mode = ModeNormal
		case taskArgs[1] == "conc":
			plan.Mode = ModeConcurrent
			if len(taskArgs) > 2 {
				plan.EnvParam = taskArgs[2]
			}
			if len(taskArgs) > 3 {
				plan.NumParam = strings.TrimSpace(strings.Join(taskArgs[3:], " "))
			}
		case taskArgs[1] == "desi":
			plan.Mode = ModeDesignated
			if len(taskArgs) > 2 {
				plan.EnvParam = taskArgs[2]
			}
			if len(taskArgs) > 3 {
				plan.NumParam = strings.TrimSpace(strings.Join(taskArgs[3:], " "))
			}
		default:
			plan.Mode = ModeElse
		}
	} else {
		plan.Mode = ModeElse
	}

	switch plan.Mode {
	case ModeNormal:
		plan.FileParam = taskArgs[0]
		plan.Program = r.interpreter(taskArgs[0])
		plan.Script = taskArgs[0]
		plan.Args = scriptArgs
	case ModeConcurrent, ModeDesignated:
		if plan.EnvParam == "" {
			plan.Failure = "\n" + missingEnvMessage(plan.Mode) + "\n"
			plan.FailureCode = 1
			return plan, nil
		}
		plan.FileParam = taskArgs[0]
		plan.Program = r.interpreter(taskArgs[0])
		plan.Script = taskArgs[0]
		plan.Args = scriptArgs
	case ModeElse:
		plan.FileParam = taskArgs[0]
		plan.Program = r.interpreter(taskArgs[0])
		plan.Script = taskArgs[0]
		plan.Args = taskArgs[1:]
	}

	plan.WorkDir, plan.Script = r.resolveWorkDir(plan.FileParam, plan.Script, plan.Env["work_dir"])
	return plan, nil
}

func missingEnvMessage(mode Mode) string {
	if mode == ModeConcurrent {
		return "缺少并发运行的环境变量参数"
	}
	return "缺少单独运行的参数 task xxx.js desi Test"
}

// interpreter maps a script name to the program that runs it, like
// define_program() in shell/task.sh. An empty result means "execute the file
// itself" (run_else form).
func (r *Runner) interpreter(name string) string {
	switch {
	case strings.HasSuffix(name, ".js"), strings.HasSuffix(name, ".mjs"):
		return r.node
	case strings.HasSuffix(name, ".ts"):
		return r.tsNode
	case strings.HasSuffix(name, ".py"), strings.HasSuffix(name, ".pyc"):
		return r.python
	case strings.HasSuffix(name, ".sh"):
		return r.bash
	case strings.HasSuffix(name, ".go"):
		return r.goBin
	default:
		return ""
	}
}

// resolveWorkDir ports enter_script_workdir(): an explicit work_dir wins, then
// a sub directory of the script path, and the script name is shortened to a
// path relative to the directory the task runs in.
func (r *Runner) resolveWorkDir(fileParam, script, workDir string) (string, string) {
	scripts := r.cfg.ScriptPath

	if workDir != "" {
		target := workDir
		if !isAbsPath(workDir) {
			target = joinPath(scripts, workDir)
		}
		if isDir(target) {
			if strings.Contains(fileParam, "/") {
				return target, baseName(fileParam)
			}
			return target, script
		}
		r.logger.Warn("[task] work_dir not found", "work_dir", target)
	}

	if strings.Contains(fileParam, "/") {
		scriptDir := fileParam[:strings.LastIndex(fileParam, "/")]
		candidate := scriptDir
		if !isAbsPath(candidate) {
			candidate = joinPath(scripts, scriptDir)
		}
		if isDir(candidate) {
			return candidate, baseName(fileParam)
		}
	}
	return scripts, script
}
