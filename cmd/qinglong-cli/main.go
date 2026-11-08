// Command qinglong-cli runs panel tasks from a terminal, replacing the
// shell/task.sh entry point the Node version shipped.
//
// The Node panel only touched the database when the command carried an ID
// (shell/share.sh:408 `if [[ $ID ]]`), so `task xxx.js` was a plain run while
// `ID=3 task xxx.js` updated the task row. This command keeps that split: it
// never writes the database, which is what makes it safe for debugging.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	qinglong "qinglong-go"
	"qinglong-go/internal/config"
	"qinglong-go/internal/logging"
	"qinglong-go/internal/platform"
	"qinglong-go/internal/runner"
)

func main() {
	// Same console handling as the panel: cmd.exe starts in the OEM code page
	// while Go writes UTF-8, so Chinese output would be mojibake.
	restoreConsole, _ := platform.EnableUTF8Console()
	code := run(os.Args[1:])
	restoreConsole()
	os.Exit(code)
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}
	switch args[0] {
	case "task":
		return cmdTask(args[1:])
	case "list", "ls":
		return cmdList()
	case "doctor":
		return cmdDoctor()
	case "version", "-v", "--version":
		return cmdVersion()
	case "help", "-h", "--help":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "未知命令: %s\n\n", args[0])
		usage()
		return 2
	}
}

func usage() {
	fmt.Print(`青龙面板命令行 2.24.0

用法:
  qinglong-CLI.exe task [-m 秒] <脚本> [参数...]   执行脚本
  qinglong-CLI.exe list                            列出 data\scripts 下的脚本
  qinglong-CLI.exe doctor                          检查运行环境
  qinglong-CLI.exe version                         显示版本

示例:
  qinglong-CLI.exe task demo.js
  qinglong-CLI.exe task -m 60 demo.py 参数1
  qinglong-CLI.exe task "子目录/a.js"

说明:
  命令行执行不会更新面板里的任务状态（与 Node 版不带 ID 时一致）。
  输出同时写终端和 data\log\<脚本名>\<时间戳>.log。
`)
}

// load prepares the configuration and a logger without starting the panel.
func load() (*config.Config, *logging.Logger, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, fmt.Errorf("读取配置失败: %w", err)
	}
	if err := cfg.EnsureDirs(); err != nil {
		return nil, nil, err
	}
	// Prefer the interpreters shipped next to the exe, exactly like the panel.
	platform.PrependBundledRuntime(cfg.RootPath)
	logger, err := logging.New(logging.Options{
		Dir:        cfg.SyslogPath,
		Level:      logging.LevelError, // task output owns the terminal
		MaxBytes:   20 << 20,
		MaxAgeDays: 7,
		ASCIIOnly:  !platform.ConsoleSupportsEmoji(),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("初始化日志失败: %w", err)
	}
	return cfg, logger, nil
}

func cmdTask(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "用法: qinglong-CLI.exe task [-m 秒] <脚本> [参数...]")
		return 2
	}

	cfg, logger, err := load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer logger.Close()

	r := runner.New(cfg, logger.Logger)
	if err := r.Bootstrap(); err != nil {
		fmt.Fprintf(os.Stderr, "初始化执行器失败: %v\n", err)
		return 1
	}

	// The runner parses the same command shape the panel stores, so the
	// arguments are joined verbatim and a path with spaces keeps its quotes.
	command := "task " + strings.Join(args, " ")
	exec, err := r.Prepare(runner.Request{Command: command, Console: os.Stdout})
	if err != nil {
		fmt.Fprintf(os.Stderr, "解析命令失败: %v\n", err)
		return 1
	}

	// The runner computes the log path relative to data/log but never opens it:
	// the panel service owns the file. Here the CLI does, so output reaches both
	// the terminal and data/log like the Node version did.
	logPath := filepath.Join(cfg.LogPath, filepath.FromSlash(exec.LogPath()))
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err == nil {
		if file, ferr := os.Create(logPath); ferr == nil {
			exec.SetSink(file)
			defer file.Close()
			fmt.Printf("日志: %s\n\n", logPath)
		}
	}

	if err := exec.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "启动任务失败: %v\n", err)
		return 1
	}
	result := exec.Wait()
	if result.TimedOut {
		fmt.Fprintln(os.Stderr, "\n任务超时，已结束")
	}
	return int(result.ExitCode)
}

func cmdList() int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	root := cfg.ScriptPath
	var files []string
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (name == "node_modules" || name == ".git" || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "读取脚本目录失败: %v\n", err)
		return 1
	}
	if len(files) == 0 {
		fmt.Printf("脚本目录为空: %s\n", root)
		return 0
	}
	sort.Strings(files)
	fmt.Printf("脚本目录: %s\n\n", root)
	for _, file := range files {
		fmt.Println("  " + file)
	}
	fmt.Printf("\n共 %d 个文件\n", len(files))
	return 0
}

func cmdDoctor() int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("青龙面板环境检查")
	fmt.Println(strings.Repeat("-", 40))
	fmt.Printf("  [OK] 程序目录   %s\n", cfg.RootPath)
	fmt.Printf("  [OK] 数据目录   %s\n", cfg.DataPath)

	tools := platform.NewToolchain()
	for _, tool := range tools.Doctor() {
		mark := "[--]"
		value := "未检测到"
		if tool.Found {
			mark = "[OK]"
			value = strings.TrimSpace(tool.Version + "  " + tool.Path)
			if strings.TrimSpace(tool.Version) == "" {
				value = tool.Path
			}
		}
		label := tool.Name
		if len(label) < 8 {
			label += strings.Repeat(" ", 8-len(label))
		}
		fmt.Printf("  %s %s%s\n", mark, label, value)
	}
	return 0
}

func cmdVersion() int {
	platform.SetEmbeddedVersion(qinglong.VersionYAML)
	cfg, err := config.Load()
	root := ""
	if err == nil {
		root = cfg.RootPath
	}
	build := platform.DetectBuild(root)
	fmt.Printf("青龙面板 QingLong %s（Go 版 · 单文件）\n", build.Version)
	return 0
}
