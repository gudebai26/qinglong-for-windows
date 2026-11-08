// Command qinglong runs the panel: it embeds the React build, serves the API
// and (from M4 on) owns the scheduler. M1 only wires configuration, storage,
// logging, static hosting and the health endpoint.
package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	// Windows has no zoneinfo database; embedding it keeps time.LoadLocation
	// working for the panel timezone on every platform.
	_ "time/tzdata"

	qinglong "qinglong-go"
	"qinglong-go/internal/config"
	"qinglong-go/internal/grpcapi"
	"qinglong-go/internal/httpapi"
	"qinglong-go/internal/i18n"
	"qinglong-go/internal/keyv"
	"qinglong-go/internal/logging"
	"qinglong-go/internal/nodemodules"
	"qinglong-go/internal/platform"
	"qinglong-go/internal/preload"
	"qinglong-go/internal/scheduler"
	"qinglong-go/internal/service"
	"qinglong-go/internal/store"
)

// consoleCodePage records the code page that was active before the switch, so
// the boot log can show what the console started with.
var consoleCodePage uint32

func main() {
	// cmd.exe starts in the OEM code page (936 on Chinese Windows) while Go
	// writes UTF-8 bytes, so every Chinese log line would be mojibake without
	// this switch. Restore before exiting: start.bat prints GBK text after the
	// panel stops.
	restoreConsole, previousCP := platform.EnableUTF8Console()
	consoleCodePage = previousCP
	code := 0
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		code = 1
	}
	restoreConsole()
	os.Exit(code)
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if err := cfg.EnsureDirs(); err != nil {
		return err
	}
	// config.sh is the panel's settings file; shell scripts source it, so a
	// fresh data directory gets the sample before any task can run.
	if sample, err := preload.ConfigSample(); err == nil {
		if err := cfg.EnsureConfigFile(sample); err != nil {
			return fmt.Errorf("create config.sh: %w", err)
		}
	}

	logger, err := logging.New(logging.Options{
		Dir:        cfg.SyslogPath,
		Level:      logging.ParseLevel(cfg.LogLevel),
		MaxBytes:   20 << 20,
		MaxAgeDays: 7,
		// Traditional cmd.exe cannot render emoji, so the startup narration
		// falls back to ASCII there and keeps the icons in Windows Terminal.
		ASCIIOnly: !platform.ConsoleSupportsEmoji(),
	})
	if err != nil {
		return fmt.Errorf("init logger: %w", err)
	}
	defer logger.Close()
	slog.SetDefault(logger.Logger)
	log := logger.Logger
	// Visible with log_level: debug. A code page other than 65001 after the
	// switch means the console refused UTF-8 and Chinese output will be
	// mojibake.
	log.Debug("[boot] console ready",
		"code_page_before", consoleCodePage,
		"code_page_now", platform.ConsoleCodePage())

	// A self-contained distribution ships node/python/git under runtime/. Putting
	// them first on PATH is enough: the runner, the doctor and the dependency
	// installer all resolve tools through exec.LookPath.
	bundledDirs := platform.PrependBundledRuntime(cfg.RootPath)

	// A single-file distribution ships no version.yaml, so the compiled-in
	// manifest backs /api/health and the version endpoint.
	platform.SetEmbeddedVersion(qinglong.VersionYAML)
	build := platform.DetectBuild(cfg.RootPath)

	// Startup narration mirrors the container image: numbered steps, then a
	// SUCCESS line the operator cannot miss. Without it the boot ends on a
	// structured log line and it is impossible to tell whether the panel is up.
	logger.Banner(
		"青龙面板 QingLong "+build.Version+"  -  Windows 单机版",
		"程序目录  "+cfg.RootPath,
	)

	logger.Step("🚀", 1, "检测配置文件...")
	logger.Detail("配置文件", "%s", filepath.Join(cfg.ConfigPath, "config.sh"))
	logger.Detail("数据目录", "%s", cfg.DataPath)

	dbFile := filepath.Join(cfg.DBPath, "database.sqlite")
	st, err := store.Open(dbFile)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	schemaVersion, err := st.Migrate(ctx)
	if err != nil {
		cancel()
		return fmt.Errorf("migrate database: %w", err)
	}
	tables, err := st.TableNames(ctx)
	if err != nil {
		cancel()
		return fmt.Errorf("inspect database: %w", err)
	}
	missing, err := st.MissingTables(ctx)
	if err != nil {
		cancel()
		return fmt.Errorf("inspect database: %w", err)
	}
	if len(missing) > 0 {
		cancel()
		return fmt.Errorf("database is missing tables: %v", missing)
	}
	for _, table := range tables {
		if count, err := st.CountRows(ctx, table); err == nil {
			log.Debug("table ready", "name", table, "rows", count)
		}
	}
	cancel()
	logger.Step("📦", 2, "加载数据库...")
	logger.Detail("数据库", "%s（%d 张表，schema v%d）", st.Path(), len(tables), schemaVersion)

	webFS, err := fs.Sub(qinglong.WebFS, "web")
	if err != nil {
		return fmt.Errorf("open embedded web assets: %w", err)
	}

	kv, err := keyv.Open(cfg.KeyvPath)
	if err != nil {
		return fmt.Errorf("open keyv: %w", err)
	}
	defer kv.Close()

	users := service.NewUserService(cfg, st, kv, log)
	bootCtx, bootCancel := context.WithTimeout(context.Background(), 30*time.Second)
	err = users.BootstrapAuthInfo(bootCtx)
	bootCancel()
	if err != nil {
		return fmt.Errorf("bootstrap auth info: %w", err)
	}
	logger.Detail("登录凭据", "%s（语言 %s）", kv.Path(), i18n.Lang())

	crons := service.NewCronService(cfg, st, log)
	initCtx, initCancel := context.WithTimeout(context.Background(), 30*time.Second)
	err = crons.Bootstrap(initCtx)
	initCancel()
	if err != nil {
		return fmt.Errorf("bootstrap crontabs: %w", err)
	}

	// The scheduler reads the panel timezone and cronConcurrency from the same
	// systemConfig row the API serves, so both stay in sync.
	sched := scheduler.New(scheduler.Options{
		Store:       st,
		Executor:    crons,
		Logger:      log,
		Location:    crons.Timezone,
		Concurrency: crons.CronConcurrency,
	})
	crons.SetScheduler(sched)

	// Scripts and environment variables share the cron service: the script
	// debugger runs through the same runner, and the env page regenerates the
	// files the runner sources before every task.
	sock := service.NewSockService(log)
	scripts := service.NewScriptService(cfg, crons, log)
	scripts.SetSock(sock)
	envs := service.NewEnvService(cfg, st, log)
	notify := service.NewNotifyService(cfg, st, log)
	// Scripts build their notifier table from the environment; publish the
	// channel saved on the settings page so notify.send() works without the
	// operator mirroring it into config.sh.
	crons.SetNotificationEnv(func() map[string]string {
		return notify.ScriptEnv(context.Background())
	})
	// Login notices go through the same sender the notification page tests, so
	// the audit row reflects real delivery instead of a hard-coded failure.
	users.SetNotifier(notify)

	// External tools the subscription and dependency pages shell out to. The
	// doctor line is the only place a missing git/pnpm is visible before the
	// first request, so it is logged at boot instead of on failure.
	tools := platform.NewToolchain()
	logger.Step("🔍", 3, "检测运行环境...")
	if len(bundledDirs) > 0 {
		logger.Detail("内置运行时", "%s（优先于系统安装）",
			platform.BundledRuntimeRoot(cfg.RootPath))
	}
	for _, tool := range tools.Doctor() {
		switch {
		case tool.Found:
			logger.Detail(toolLabel(tool.Name), "%s  %s", toolVersion(tool.Name, tool.Version), tool.Path)
		case tool.Required:
			logger.DetailWarn(toolLabel(tool.Name),
				"未检测到，订阅与依赖相关功能会明确报错")
		}
	}
	dependences := service.NewDependenceService(cfg, st, tools, log)
	dependences.SetSock(sock)
	subscriptions := service.NewSubscriptionService(cfg, st, crons, notify, tools, log)
	subscriptions.SetSock(sock)
	opens := service.NewOpenService(cfg, st, kv, log)
	system := service.NewSystemService(cfg, st, users, crons, notify, subscriptions, tools, log)
	system.SetSock(sock)
	dashboard := service.NewDashboardService(cfg, st)
	bootSystemCtx, bootSystemCancel := context.WithTimeout(context.Background(), 30*time.Second)
	if err := system.Bootstrap(bootSystemCtx); err != nil {
		bootSystemCancel()
		return fmt.Errorf("bootstrap system settings: %w", err)
	}
	bootSystemCancel()
	logger.Detail("系统设置", "语言 %s", i18n.Lang())

	// Rewrite env.sh/env.js/env.py from the database before anything can run a
	// task: an upgraded data/ directory otherwise keeps files written by an
	// older (possibly buggy) version. Node does this in initData as well.
	envCtx, envCancel := context.WithTimeout(context.Background(), 30*time.Second)
	err = envs.Bootstrap(envCtx)
	envCancel()
	if err != nil {
		return fmt.Errorf("bootstrap environment files: %w", err)
	}

	// The ql API a task calls through shell/preload/client.js. It is a separate
	// listener from the panel, so a bind failure only costs the scripts their
	// QLAPI object; the panel itself keeps serving.
	//
	// The vendored Node runtime is materialized first: client.js requires
	// @grpc/grpc-js at the top of the file, and the runner probe that decides
	// whether to inject the preload looks for it under <QL_DIR>/node_modules.
	// Extracting here covers both - no request and no task can run before this
	// point, and a failure only degrades the ql API (warning, not fatal).
	materializeNodeModules(cfg, logger)

	grpcPort := cfg.EnvInt("GRPC_PORT", grpcapi.DefaultPort)
	grpcHost := cfg.EnvDefault("BIND_HOST_GRPC", grpcapi.DefaultBindHost)
	grpcServer := grpcapi.NewServer(grpcapi.Options{
		Port:     grpcPort,
		BindHost: grpcHost,
		CertDir:  filepath.Join(cfg.ConfigPath, "grpc"),
		Logger:   log,
		Services: grpcapi.Services{Envs: envs, Crons: crons, Notify: notify},
	})
	logger.Step("⚙️", 4, "启动后端服务...")
	if err := grpcServer.Start(context.Background()); err != nil {
		logger.DetailWarn("gRPC", "启动失败：%v（ql API 不可用，面板仍可正常使用）", err)
	} else {
		logger.Detail("gRPC", "%s:%d（mTLS）", grpcHost, grpcPort)
	}

	// Replay initTask.ts: apply the SSH configuration and register every enabled
	// subscription before the first request can list them.
	// The built-in `system` app must exist before the first /open request: the
	// shell scripts authenticate with its token.
	openCtx, openCancel := context.WithTimeout(context.Background(), 30*time.Second)
	err = opens.InitializeSystemApp(openCtx)
	openCancel()
	if err != nil {
		return fmt.Errorf("initialize system app: %w", err)
	}

	subCtx, subCancel := context.WithTimeout(context.Background(), 30*time.Second)
	err = subscriptions.Bootstrap(subCtx)
	subCancel()
	if err != nil {
		return fmt.Errorf("bootstrap subscriptions: %w", err)
	}

	handler := httpapi.New(httpapi.Deps{
		Config:        cfg,
		Store:         st,
		Keyv:          kv,
		User:          users,
		Cron:          crons,
		Scripts:       scripts,
		Envs:          envs,
		Subscriptions: subscriptions,
		Dependences:   dependences,
		Open:          opens,
		System:        system,
		Notify:        notify,
		Dashboard:     dashboard,
		Sock:          sock,
		Logger:        log,
		Web:           webFS,
		StartedAt:     time.Now(),
		Build:         build,
	})

	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 15 * time.Second,
	}

	// PUT /api/system/reload asks the process to stop; the operator starts it
	// again with start.bat, matching `ql reload` + exit(0).
	reloadRequested := make(chan struct{}, 1)
	system.SetReloadHook(func() {
		select {
		case reloadRequested <- struct{}{}:
		default:
		}
	})

	// Bind explicitly instead of ListenAndServe: only a successful bind proves
	// the panel is reachable, and the SUCCESS line below must not lie.
	logger.Step("🌐", 5, "启动 Web 服务...")
	listener, err := net.Listen("tcp", cfg.ListenAddr())
	if err != nil {
		return fmt.Errorf("监听 %s 失败: %w", cfg.ListenAddr(), err)
	}
	logger.Detail("监听地址", "%s", displayListenAddr(cfg))

	serverErr := make(chan error, 1)
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
			return
		}
		serverErr <- nil
	}()

	// Start the scheduler after the listener: a @boot task may call back into
	// the panel (M5 runner), and Node boots it the same way.
	logger.Step("⏳", 6, "等待面板就绪...")
	startCtx, startCancel := context.WithTimeout(context.Background(), 30*time.Second)
	if err := sched.Start(startCtx); err != nil {
		logger.DetailWarn("调度器", "启动失败：%v", err)
	}
	startCancel()

	logger.Success("🎉", "青龙面板启动成功!")
	logger.Success("🌐", "访问地址  %s", accessURL(cfg))
	logger.Detail("停止服务", "按 Ctrl+C，或直接关闭本窗口")

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)

	select {
	case <-reloadRequested:
		logger.Detail("正在停止", "收到重载请求")
		// Drop the long-lived websocket sessions first: otherwise Shutdown
		// waits for them until the timeout and the port stays held after the
		// front-end already reported success.
		sock.CloseAll()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer shutdownCancel()
		_ = server.Shutdown(shutdownCtx)
		grpcServer.Stop(5 * time.Second)
		sched.Stop()
		system.Stop()
		subscriptions.Shutdown(5 * time.Second)
		crons.Shutdown(5 * time.Second)
		logger.Success("👋", "服务已停止，下次双击 start.bat 重新启动")
		return nil
	case err := <-serverErr:
		if err != nil {
			return fmt.Errorf("http server: %w", err)
		}
		return nil
	case sig := <-signals:
		logger.Detail("正在停止", "收到 %s", sig.String())
		// Same as the reload path: a Ctrl+C must not hang on the websocket
		// sessions for the full shutdown timeout during day-to-day debugging.
		sock.CloseAll()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer shutdownCancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Error("[boot] http shutdown failed", "error", err)
		}
		// Drain the ql API before the task runner goes away: an RPC in flight
		// may still be queueing a task.
		grpcServer.Stop(5 * time.Second)
		// Node lets running tasks die with the worker; the Go panel ends them
		// explicitly so a stopped panel never leaves orphan processes.
		sched.Stop()
		system.Stop()
		subscriptions.Shutdown(5 * time.Second)
		crons.Shutdown(5 * time.Second)
		logger.Success("👋", "服务已停止，感谢使用青龙面板")
		return nil
	}
}

// toolLabel renders a doctor entry the way an operator reads it, without
// touching the lowercase names the API and the logs use.
func toolLabel(name string) string {
	switch name {
	case "node":
		return "Node.js"
	case "git":
		return "Git"
	case "python":
		return "Python"
	case "bash":
		return "Bash"
	default:
		return name
	}
}

// toolVersion trims the prefixes some tools repeat in --version output, so the
// column reads "Python  3.13.14" instead of "Python  Python 3.13.14".
func toolVersion(name, version string) string {
	switch name {
	case "python":
		return strings.TrimPrefix(version, "Python ")
	case "pip":
		// "pip 25.0.1 from C:\... (python 3.12)" -> "25.0.1"
		if fields := strings.Fields(version); len(fields) >= 2 && fields[0] == "pip" {
			return fields[1]
		}
	case "git":
		return strings.TrimPrefix(version, "git version ")
	case "bash":
		// "GNU bash, version 5.3.15(1)-release (x86_64-pc-cygwin)" -> "5.3.15"
		if i := strings.Index(version, "version "); i >= 0 {
			rest := version[i+len("version "):]
			if j := strings.IndexAny(rest, "( "); j > 0 {
				return rest[:j]
			}
		}
	}
	return version
}

// displayListenAddr turns ":5700" into "0.0.0.0:5700" so the line states which
// interfaces the panel accepted connections on.
func displayListenAddr(cfg *config.Config) string {
	switch cfg.BindHost {
	case "", "::", "0.0.0.0", "*":
		return "0.0.0.0:" + strconv.Itoa(cfg.Port)
	default:
		return net.JoinHostPort(cfg.BindHost, strconv.Itoa(cfg.Port))
	}
}

// accessURL is the address to paste into a browser. A panel mounted under a
// base path keeps the trailing slash, exactly like the container image.
func accessURL(cfg *config.Config) string {
	url := "http://localhost:" + strconv.Itoa(cfg.Port)
	if cfg.BaseURL != "" {
		url += cfg.BaseURL + "/"
	}
	return url
}

// materializeNodeModules copies the embedded ql API runtime into// <QL_DIR>/node_modules. Existing files are never overwritten: a deployment
// upgraded from the Node version already has its own (much larger) dependency
// tree, and only the packages the panel ships are added when missing.
//
// Every failure is downgraded to a warning. The panel must still start so the
// operator can reach the UI and the logs; the cost is that JavaScript tasks run
// without the ql API, exactly as when Node itself has no node_modules.
func materializeNodeModules(cfg *config.Config, logger *logging.Logger) {
	sub, err := fs.Sub(qinglong.NodeModulesFS, nodemodules.DirName)
	if err != nil {
		logger.DetailWarn("ql API", "内置运行时不可用，JavaScript 任务将没有 ql 对象")
		return
	}

	started := time.Now()
	result, err := nodemodules.Ensure(sub, cfg.RootPath)
	if err != nil {
		logger.DetailWarn("ql API",
			"运行时解压不完整（已写入 %d 个文件），JavaScript 任务可能没有 ql 对象",
			result.Extracted)
		return
	}
	if result.UpToDate {
		logger.Detail("ql API", "%d 个文件（已是最新）", result.Skipped)
		return
	}
	logger.Detail("ql API",
		"%d 个文件（新增 %d，保留 %d，耗时 %s）",
		result.Extracted+result.Skipped, result.Extracted, result.Skipped,
		time.Since(started).Round(time.Millisecond))
}
