// Package service: this file ports back/services/dependence.ts.
package service

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"qinglong-go/internal/config"
	"qinglong-go/internal/i18n"
	"qinglong-go/internal/platform"
	"qinglong-go/internal/store"
)

// DependenceResponse is the plain-object shape Sequelize returns.
type DependenceResponse struct {
	ID        int64    `json:"id"`
	Name      *string  `json:"name"`
	Type      *int64   `json:"type"`
	Timestamp *string  `json:"timestamp"`
	Status    *int64   `json:"status"`
	Log       []string `json:"log"`
	Remark    *string  `json:"remark"`
	CreatedAt *string  `json:"createdAt"`
	UpdatedAt *string  `json:"updatedAt"`
}

// DependenceInput is one validated item of POST /api/dependencies.
type DependenceInput struct {
	Name   string
	Type   int64
	Remark string
}

// RefreshJob mirrors the job object of /api/dependencies/refresh.
type RefreshJob struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	State string `json:"state"`
}

// depJob is one queued install/uninstall. The queue is serial, like the
// PQueue({concurrency: 1}) in back/shared/pLimit.ts.
type depJob struct {
	id        int64
	isInstall bool
	force     bool
}

// DependenceService owns the dependency domain.
type DependenceService struct {
	cfg    *config.Config
	store  *store.Store
	tools  *platform.Toolchain
	logger *slog.Logger

	mu           sync.Mutex
	queue        []*depJob
	queued       map[int64]struct{}
	running      bool
	handles      map[int64]*execHandle
	logMu        sync.Mutex
	pipMu        sync.Mutex
	pipChecked   bool
	pipAvailable bool

	refreshMu sync.Mutex
	active    *RefreshJob
	jobs      map[string]*RefreshJob
	jobOrder  []string

	// sock streams install/uninstall output to the dependence page.
	sock *SockService
	// statusOf caches the in-flight status per row so appendLog can tag the
	// websocket message without a database read per chunk.
	statusOf sync.Map
}

// NewDependenceService wires the service.
func NewDependenceService(cfg *config.Config, st *store.Store, tools *platform.Toolchain, logger *slog.Logger) *DependenceService {
	if logger == nil {
		logger = slog.Default()
	}
	if tools == nil {
		tools = platform.NewToolchain()
	}
	return &DependenceService{
		cfg:     cfg,
		store:   st,
		tools:   tools,
		logger:  logger,
		queued:  map[int64]struct{}{},
		handles: map[int64]*execHandle{},
		jobs:    map[string]*RefreshJob{},
	}
}

// ToDependenceResponse converts a row into the API shape.
func ToDependenceResponse(row *store.Dependence) *DependenceResponse {
	if row == nil {
		return nil
	}
	return &DependenceResponse{
		ID:        row.ID,
		Name:      nullableString(row.Name),
		Type:      nullableInt(row.Type),
		Timestamp: nullableString(row.Timestamp),
		Status:    nullableInt(row.Status),
		Log:       store.DependenceLog(row.Log),
		Remark:    nullableString(row.Remark),
		CreatedAt: store.ISO8601(row.CreatedAt),
		UpdatedAt: store.ISO8601(row.UpdatedAt),
	}
}

func toDependenceDoc(row *store.Dependence) DependenceInput {
	doc := DependenceInput{Name: nullStringValue(row.Name), Remark: nullStringValue(row.Remark)}
	if row.Type.Valid {
		doc.Type = row.Type.Int64
	}
	return doc
}

// DependenceListParams mirrors the query string of GET /api/dependencies.
type DependenceListParams struct {
	SearchValue string
	Type        string
	Status      string
}

// List implements GET /api/dependencies.
func (s *DependenceService) List(ctx context.Context, params DependenceListParams) ([]DependenceResponse, error) {
	clauses := make([]string, 0, 3)
	args := make([]any, 0, 6)

	if params.Type != "" {
		switch params.Type {
		case "nodejs":
			clauses = append(clauses, "type = ?")
			args = append(args, DepTypeNodejs)
		case "python3":
			clauses = append(clauses, "type = ?")
			args = append(args, DepTypePython3)
		case "linux":
			clauses = append(clauses, "type = ?")
			args = append(args, DepTypeLinux)
		}
	}
	if params.Status != "" {
		values := make([]string, 0, 4)
		for _, item := range strings.Split(params.Status, ",") {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			values = append(values, "?")
			args = append(args, item)
		}
		if len(values) > 0 {
			clauses = append(clauses, "status IN ("+strings.Join(values, ",")+")")
		}
	}
	if params.SearchValue != "" {
		encoded := encodeURI(params.SearchValue)
		clauses = append(clauses, "(name LIKE ? OR name LIKE ?)")
		args = append(args, "%"+params.SearchValue+"%", "%"+encoded+"%")
	}

	rows, err := s.store.ListDependences(ctx, strings.Join(clauses, " AND "), args, store.DependenceDefaultOrder)
	if err != nil {
		return nil, err
	}
	data := make([]DependenceResponse, 0, len(rows))
	for i := range rows {
		data = append(data, *ToDependenceResponse(&rows[i]))
	}
	return data, nil
}

// Get implements GET /api/dependencies/:id.
func (s *DependenceService) Get(ctx context.Context, id int64) (*DependenceResponse, error) {
	row, err := s.store.DependenceByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, &NotFoundError{
			Status:  500,
			Message: fmt.Sprintf("Dependency {\"id\":%d} not found", id),
		}
	}
	return ToDependenceResponse(row), nil
}

// ensureNodeTooling reports why `pnpm add -g` cannot work, so the dependency
// page shows pnpm's own guidance instead of an install that fails seconds
// later with a stack trace in the log.
//
// When pnpm is simply missing but npm is present, it installs pnpm once. Asking
// the operator to open a terminal for `npm i -g pnpm` defeats the point of
// having a panel, and the machine already has everything needed.
func (s *DependenceService) ensureNodeTooling() error {
	if s.tools.Path("pnpm") == "" {
		if err := s.installPnpm(); err != nil {
			return &BusinessError{
				Code:    400,
				Message: i18n.T("未检测到 pnpm，自动安装失败：") + err.Error(),
			}
		}
	}
	if _, err := platform.PnpmGlobalRoot(); err != nil {
		return &BusinessError{Code: 400, Message: platform.PnpmSetupHint(err)}
	}
	return nil
}

// installPnpm runs `npm i -g pnpm` once. It takes tens of seconds, but only on
// a machine that has Node and no pnpm, and the dependency request that
// triggered it is already the slow path.
func (s *DependenceService) installPnpm() error {
	npm := s.tools.Path("npm")
	if npm == "" {
		return errors.New(i18n.T("未找到 npm，请先安装 Node.js"))
	}
	s.logger.Info("[dependence] pnpm missing, installing", "npm", npm)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, npm, "install", "-g", "pnpm")
	platform.PrepareCommand(cmd)
	output, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(output))
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return errors.New(i18n.T("安装超时（3 分钟）"))
		}
		if text == "" {
			text = err.Error()
		}
		return errors.New(text)
	}

	// The toolchain caches lookups, so pnpm would stay "missing" until restart.
	s.tools.Refresh()
	path := s.tools.Path("pnpm")
	if path == "" {
		return errors.New(i18n.T("安装完成但 pnpm 仍不在 PATH，请重开面板后再试"))
	}
	s.logger.Info("[dependence] pnpm installed", "path", path)
	return nil
}

// Create implements POST /api/dependencies.
func (s *DependenceService) Create(ctx context.Context, inputs []DependenceInput) ([]DependenceResponse, error) {
	rows := make([]*store.Dependence, 0, len(inputs))
	for _, input := range inputs {
		if _, err := s.getInstallCommand(input.Type, input.Name); err != nil {
			return nil, err
		}
		if input.Type == DepTypeNodejs {
			if err := s.ensureNodeTooling(); err != nil {
				return nil, err
			}
		}
		rows = append(rows, &store.Dependence{
			Name:      sql.NullString{String: strings.TrimSpace(input.Name), Valid: true},
			Type:      sql.NullInt64{Int64: input.Type, Valid: true},
			Timestamp: sql.NullString{String: dependenceTimestamp(time.Now()), Valid: true},
			Status:    sql.NullInt64{Int64: DepStatusQueued, Valid: true},
			Log:       sql.NullString{String: "[]", Valid: true},
			Remark:    sql.NullString{String: input.Remark, Valid: true},
		})
	}
	if err := s.store.InsertDependences(ctx, rows); err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	s.installOneByOne(ids, true, false)

	data := make([]DependenceResponse, 0, len(rows))
	for _, row := range rows {
		data = append(data, *ToDependenceResponse(row))
	}
	return data, nil
}

// Update implements PUT /api/dependencies.
func (s *DependenceService) Update(ctx context.Context, id int64, input DependenceInput) (*DependenceResponse, error) {
	row, err := s.store.DependenceByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, &NotFoundError{
			Status:  500,
			Message: fmt.Sprintf("Dependency {\"id\":%d} not found", id),
		}
	}
	if _, err := s.getInstallCommand(input.Type, input.Name); err != nil {
		return nil, err
	}
	if input.Type == DepTypeNodejs {
		if err := s.ensureNodeTooling(); err != nil {
			return nil, err
		}
	}
	if err := s.store.UpdateDependence(ctx, id, map[string]any{
		"name":   strings.TrimSpace(input.Name),
		"type":   input.Type,
		"remark": input.Remark,
		"status": DepStatusQueued,
	}); err != nil {
		return nil, err
	}
	s.installOneByOne([]int64{id}, true, false)
	updated, err := s.store.DependenceByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return ToDependenceResponse(updated), nil
}

// Remove implements DELETE /api/dependencies[/force].
func (s *DependenceService) Remove(ctx context.Context, ids []int64, force bool) ([]DependenceResponse, error) {
	rows, err := s.store.ListDependences(ctx, idClause(len(ids)), int64Args(ids), store.DependenceDefaultOrder)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		s.removeQueued(rows[i].ID)
	}

	installed := make([]int64, 0, len(rows))
	uninstalled := make([]int64, 0, len(rows))
	for i := range rows {
		row := &rows[i]
		doc := toDependenceDoc(row)
		isInstalled := row.Status.Valid && row.Status.Int64 == DepStatusInstalled
		// Older list refreshes marked unprobeable sources as failed although
		// their distributions may be present.
		if row.Status.Valid && row.Status.Int64 == DepStatusInstallFailed &&
			doc.Type == DepTypePython3 && IsPythonSourceDependency(doc.Name) {
			isInstalled = true
		}
		if isInstalled {
			installed = append(installed, row.ID)
		} else {
			uninstalled = append(uninstalled, row.ID)
		}
	}
	if err := s.store.DeleteDependences(ctx, uninstalled); err != nil {
		return nil, err
	}
	if len(installed) > 0 {
		if _, err := s.store.UpdateDependences(ctx, installed, map[string]any{
			"status": DepStatusQueued,
			"log":    "[]",
		}); err != nil {
			return nil, err
		}
		s.installOneByOne(installed, false, force)
	}

	data := make([]DependenceResponse, 0, len(rows))
	for i := range rows {
		data = append(data, *ToDependenceResponse(&rows[i]))
	}
	return data, nil
}

// ReInstall implements PUT /api/dependencies/reinstall.
func (s *DependenceService) ReInstall(ctx context.Context, ids []int64) ([]DependenceResponse, error) {
	if _, err := s.store.UpdateDependences(ctx, ids, map[string]any{
		"status": DepStatusQueued,
		"log":    "[]",
	}); err != nil {
		return nil, err
	}
	rows, err := s.store.ListDependences(ctx, idClause(len(ids)), int64Args(ids), store.DependenceDefaultOrder)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		s.removeQueued(rows[i].ID)
	}
	s.installOneByOne(ids, true, true)

	data := make([]DependenceResponse, 0, len(rows))
	for i := range rows {
		data = append(data, *ToDependenceResponse(&rows[i]))
	}
	return data, nil
}

// Cancel implements PUT /api/dependencies/cancel.
func (s *DependenceService) Cancel(ctx context.Context, ids []int64) error {
	rows, err := s.store.ListDependences(ctx, idClause(len(ids)), int64Args(ids), store.DependenceDefaultOrder)
	if err != nil {
		return err
	}
	for i := range rows {
		id := rows[i].ID
		s.removeQueued(id)
		if handle := s.runningHandle(id); handle != nil {
			handle.kill()
		}
	}
	_, err = s.store.UpdateDependences(ctx, ids, map[string]any{"status": DepStatusCancelled})
	return err
}

// ---------------------------------------------------------------------------
// queue
// ---------------------------------------------------------------------------

// installOneByOne queues the rows and makes sure a worker is draining them.
func (s *DependenceService) installOneByOne(ids []int64, isInstall, force bool) {
	s.mu.Lock()
	for _, id := range ids {
		s.queue = append(s.queue, &depJob{id: id, isInstall: isInstall, force: force})
		s.queued[id] = struct{}{}
	}
	start := !s.running && len(s.queue) > 0
	if start {
		s.running = true
	}
	s.mu.Unlock()
	if start {
		go s.drain()
	}
}

// drain processes the queue serially.
func (s *DependenceService) drain() {
	for {
		s.mu.Lock()
		if len(s.queue) == 0 {
			s.running = false
			s.mu.Unlock()
			return
		}
		job := s.queue[0]
		s.queue = s.queue[1:]
		delete(s.queued, job.id)
		s.mu.Unlock()
		s.process(job)
	}
}

// removeQueued ports removeQueuedDependency: a cancelled job never runs.
func (s *DependenceService) removeQueued(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.queued[id]; !ok {
		return
	}
	delete(s.queued, id)
	remaining := make([]*depJob, 0, len(s.queue))
	for _, job := range s.queue {
		if job.id == id {
			continue
		}
		remaining = append(remaining, job)
	}
	s.queue = remaining
}

func (s *DependenceService) setRunningHandle(id int64, handle *execHandle) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if handle == nil {
		delete(s.handles, id)
		return
	}
	s.handles[id] = handle
}

func (s *DependenceService) runningHandle(id int64) *execHandle {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.handles[id]
}

// appendLog ports updateLog: one JSON chunk per call, serialized.
func (s *DependenceService) appendLog(ctx context.Context, id int64, chunk string) {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	if err := s.store.AppendDependenceLog(ctx, id, chunk); err != nil {
		s.logger.Warn("[dependence] append log failed", "id", id, "error", err)
	}
	if s.sock == nil {
		return
	}
	status := int64(DepStatusInstalling)
	if cached, ok := s.statusOf.Load(id); ok {
		if value, ok := cached.(int64); ok {
			status = value
		}
	}
	s.sock.Send(SockMessage{
		Type:       dependenceSockType(status),
		Message:    chunk,
		References: []int64{id},
		Status:     status,
	})
}

func (s *DependenceService) setStatus(ctx context.Context, id, status int64) {
	s.statusOf.Store(id, status)
	if err := s.store.UpdateDependence(ctx, id, map[string]any{"status": status}); err != nil {
		s.logger.Warn("[dependence] update status failed", "id", id, "error", err)
	}
	if s.sock != nil {
		s.sock.Send(SockMessage{
			Type:       dependenceSockType(status),
			References: []int64{id},
			Status:     status,
		})
	}
}

// SetSock attaches the websocket hub used by the dependence page.
func (s *DependenceService) SetSock(sock *SockService) { s.sock = sock }

// dependenceSockType maps a row status onto the browser topic, exactly like the
// socketMessageType the Node service passes around.
func dependenceSockType(status int64) string {
	switch status {
	case DepStatusRemoving, DepStatusRemoved, DepStatusRemoveFailed:
		return "uninstallDependence"
	default:
		return "installDependence"
	}
}

// ---------------------------------------------------------------------------
// install / uninstall
// ---------------------------------------------------------------------------

// process ports installOrUninstallDependency for one queued row.
func (s *DependenceService) process(job *depJob) {
	ctx := context.Background()
	row, err := s.store.DependenceByID(ctx, job.id)
	if err != nil || row == nil {
		return
	}
	doc := toDependenceDoc(row)
	action := i18n.T("安装")
	if !job.isInstall {
		action = i18n.T("删除")
	}
	depName := strings.TrimSpace(doc.Name)

	if doc.Type == DepTypeNodejs {
		if err := s.ensureNodeTooling(); err != nil {
			status := DepStatusInstallFailed
			if !job.isInstall {
				status = DepStatusRemoveFailed
			}
			_, _ = s.store.UpdateDependences(ctx, []int64{job.id}, map[string]any{
				"status": status,
				"log":    dependencyLogJSON([]string{err.Error()}),
			})
			return
		}
	}

	execArgs, err := s.dependencyCommandArgs(doc.Type, depName, job.isInstall)
	if errors.Is(err, errNoInverse) {
		// Nothing to execute: the message explains why the distributions have to
		// be removed by name, and the row is still removed.
		endTime := time.Now()
		s.appendLog(ctx, job.id, i18n.T(
			"Remove installed Python distributions separately by package name.")+"\n\n")
		s.setStatus(ctx, job.id, DepStatusRemoved)
		s.logger.Info("[dependence] no inverse command",
			"id", job.id, "name", depName, "end", endTime)
		return
	}
	if err != nil {
		if !job.isInstall && job.force {
			_ = s.store.DeleteDependences(ctx, []int64{job.id})
			return
		}
		status := DepStatusInstallFailed
		if !job.isInstall {
			status = DepStatusRemoveFailed
		}
		if _, err := s.store.UpdateDependences(ctx, []int64{job.id}, map[string]any{
			"status": status,
			"log":    dependencyLogJSON([]string{err.Error()}),
		}); err != nil {
			s.logger.Warn("[dependence] record failure failed", "id", job.id, "error", err)
		}
		return
	}
	validated := strings.Join(quoteArgs(execArgs), " ")

	// Linux dependencies need a package manager; Windows has none.
	var linuxCommand linuxCommandSet
	if doc.Type == DepTypeLinux {
		commands := s.linuxCommands()
		if _, supported := s.detectOS(); !supported {
			startTime := time.Now()
			message := i18n.Tf("开始%s依赖 %s，开始时间 %s\n\n当前系统不支持\n\n依赖%s失败，结束时间 %s，耗时 %s 秒",
				action, depName, startTime.Format("2006-01-02 15:04:05"),
				action, startTime.Format("2006-01-02 15:04:05"),
				"0")
			s.setStatus(ctx, job.id, DepStatusInstallFailed)
			s.appendLog(ctx, job.id, message)
			return
		}
		linuxCommand = commands
	}

	status := DepStatusInstalling
	if !job.isInstall {
		status = DepStatusRemoving
	}
	s.setStatus(ctx, job.id, status)

	startTime := time.Now()
	s.appendLog(ctx, job.id, i18n.Tf("开始%s依赖 %s，开始时间 %s\n\n",
		action, depName, startTime.Format("2006-01-02 15:04:05")))

	// pip must resolve optional dependencies even when the base distribution is
	// already present, so extras always install.
	skipCheck := doc.Type == DepTypePython3 && strings.Contains(depName, "[")
	if job.isInstall && !job.force && !skipCheck {
		getCommand := s.getGetCommand(doc.Type, depName)
		if doc.Type == DepTypeLinux {
			getCommand = linuxCommand.info + " " + depName
		}
		probeName, version := splitDependencyVersion(doc.Type, depName)
		depInfo := normalizeDependencyInfo(s.runOutput(ctx, getCommand))
		matches := false
		switch doc.Type {
		case DepTypeNodejs:
			fields := strings.Split(depInfo, " ")
			matches = len(fields) > 0 && fields[0] == probeName
		case DepTypeLinux:
			matches = linuxCommand.check(strings.ToLower(depInfo))
		case DepTypePython3:
			matches = true
		}
		if depInfo != "" && matches && (version == "" || strings.Contains(depInfo, version)) {
			endTime := time.Now()
			message := i18n.Tf("检测到已经安装 %s\n\n%s\n\n跳过安装\n\n依赖%s成功，结束时间 %s，耗时 %s 秒",
				probeName, depInfo, action,
				endTime.Format("2006-01-02 15:04:05"),
				formatElapsed(endTime.Sub(startTime)))
			s.appendLog(ctx, job.id, message)
			s.setStatus(ctx, job.id, DepStatusInstalled)
			return
		}
	}

	// A mirror script has to be sourced, which only a shell can do. Everything
	// else runs as a plain argv process: pip and pnpm must work on a machine
	// without Git for Windows, and cmd.exe cannot parse the quoted POSIX form
	// (it answers "The filename, directory name, or volume label syntax is
	// incorrect").
	proxyFile := s.cfg.DependenceProxyFile()
	var cmd *exec.Cmd
	if info, statErr := os.Stat(proxyFile); statErr == nil && info.Mode().IsRegular() {
		bashPath := s.tools.Path("bash")
		if bashPath == "" {
			s.appendLog(ctx, job.id, i18n.T(
				"配置了依赖代理脚本（dependence-proxy.sh），执行它需要 bash。请安装 Git for Windows（https://git-scm.com/download/win）"))
			s.finishJob(ctx, job, action, startTime, -1)
			return
		}
		full := "source " + shellQuote(proxyFile) + " && " + validated
		file, shellArgs := platform.PosixShellCommand(full, bashPath)
		cmd = exec.CommandContext(ctx, file, shellArgs...)
	} else {
		cmd = exec.CommandContext(ctx, execArgs[0], execArgs[1:]...)
	}
	platform.PrepareCommand(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		s.finishJob(ctx, job, action, startTime, -1)
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		s.finishJob(ctx, job, action, startTime, -1)
		return
	}
	if err := cmd.Start(); err != nil {
		s.appendLog(ctx, job.id, err.Error())
		s.finishJob(ctx, job, action, startTime, -1)
		return
	}
	handle := &execHandle{cmd: cmd}
	s.setRunningHandle(job.id, handle)
	defer s.setRunningHandle(job.id, nil)

	var wg sync.WaitGroup
	stream := func(reader io.Reader) {
		defer wg.Done()
		buffer := make([]byte, 32*1024)
		for {
			read, readErr := reader.Read(buffer)
			if read > 0 {
				s.appendLog(ctx, job.id, string(buffer[:read]))
			}
			if readErr != nil {
				return
			}
		}
	}
	wg.Add(2)
	go stream(stdout)
	go stream(stderr)
	wg.Wait()

	code := 0
	if err := cmd.Wait(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			code = -1
		}
	}
	s.finishJob(ctx, job, action, startTime, code)
}

// dependencyCommand validates the row's command, exactly like the pre-check in
// installOrUninstallDependency.
func (s *DependenceService) dependencyCommand(depType int64, name string, isInstall bool) (string, error) {
	if isInstall {
		return s.getInstallCommand(depType, name)
	}
	return s.getUninstallCommand(depType, name)
}

// finishJob writes the closing message and the final status.
func (s *DependenceService) finishJob(ctx context.Context, job *depJob, action string, startTime time.Time, code int) {
	ctx2 := context.Background()
	endTime := time.Now()
	succeeded := code == 0
	resultText := i18n.T("成功")
	if !succeeded {
		resultText = i18n.T("失败")
	}
	message := "\n" + i18n.Tf("依赖%s%s，结束时间 %s，耗时 %s 秒",
		action, resultText, endTime.Format("2006-01-02 15:04:05"),
		formatElapsed(endTime.Sub(startTime)))
	s.appendLog(ctx2, job.id, message)

	status := DepStatusInstallFailed
	switch {
	case succeeded && job.isInstall:
		status = DepStatusInstalled
	case succeeded:
		status = DepStatusRemoved
	case !job.isInstall:
		status = DepStatusRemoveFailed
	}
	row, err := s.store.DependenceByID(ctx2, job.id)
	if err != nil || row == nil {
		return
	}
	// A cancelled row keeps its status.
	if row.Status.Valid && row.Status.Int64 == DepStatusCancelled {
		return
	}
	s.setStatus(ctx2, job.id, status)
	if (succeeded || job.force) && !job.isInstall {
		_ = s.store.DeleteDependences(ctx2, []int64{job.id})
	}
}

func formatElapsed(duration time.Duration) string {
	seconds := int64(duration / time.Second)
	if seconds < 0 {
		seconds = 0
	}
	return fmt.Sprintf("%d", seconds)
}

func dependencyLogJSON(values []string) string {
	encoded, err := json.Marshal(values)
	if err != nil {
		return "[]"
	}
	return string(encoded)
}

// dependenceTimestamp is `new Date().toString()` for the timestamp column.
func dependenceTimestamp(value time.Time) string {
	_, offset := value.Zone()
	sign := "+"
	if offset < 0 {
		sign = "-"
		offset = -offset
	}
	name, _ := value.Zone()
	return value.Format("Mon Jan 02 2006 15:04:05") +
		fmt.Sprintf(" GMT%s%02d%02d (%s)", sign, offset/3600, (offset%3600)/60, name)
}

// ---------------------------------------------------------------------------
// refresh
// ---------------------------------------------------------------------------

// StartRefresh implements POST /api/dependencies/refresh.
func (s *DependenceService) StartRefresh(kind string) (*RefreshJob, bool) {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	if s.active != nil {
		return s.active, s.active.Type == kind
	}
	job := &RefreshJob{ID: newJobID(), Type: kind, State: "running"}
	s.active = job
	s.jobs[job.ID] = job
	s.jobOrder = append(s.jobOrder, job.ID)
	for len(s.jobOrder) > 20 {
		delete(s.jobs, s.jobOrder[0])
		s.jobOrder = s.jobOrder[1:]
	}

	depType := DepTypeNodejs
	if kind == "python3" {
		depType = DepTypePython3
	}
	go func() {
		err := s.refreshInstalledStatuses(depType)
		s.refreshMu.Lock()
		if err != nil {
			s.logger.Error("[dependence] refresh failed", "error", err)
			job.State = "failed"
		} else {
			job.State = "completed"
		}
		if s.active == job {
			s.active = nil
		}
		s.refreshMu.Unlock()
	}()
	return job, true
}

// GetRefreshJob implements GET /api/dependencies/refresh/:id.
func (s *DependenceService) GetRefreshJob(id string) (*RefreshJob, bool) {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	job, ok := s.jobs[id]
	return job, ok
}

// GetActiveRefreshJob implements GET /api/dependencies/refresh/active.
func (s *DependenceService) GetActiveRefreshJob() *RefreshJob {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	if s.active != nil && s.active.State == "running" {
		return s.active
	}
	return nil
}

// refreshInstalledStatuses ports refreshInstalledStatuses: re-probe installed
// rows and demote the ones that disappeared.
func (s *DependenceService) refreshInstalledStatuses(depType int64) error {
	ctx := context.Background()
	rows, err := s.store.ListDependences(ctx,
		"status = ? AND type = ?", []any{DepStatusInstalled, depType}, store.DependenceDefaultOrder)
	if err != nil {
		return err
	}
	checkable := make([]store.Dependence, 0, len(rows))
	for _, row := range rows {
		doc := toDependenceDoc(&row)
		if doc.Type == DepTypePython3 && IsPythonSourceDependency(doc.Name) {
			continue
		}
		checkable = append(checkable, row)
	}

	semaphore := make(chan struct{}, 5)
	var wg sync.WaitGroup
	results := make([]bool, len(checkable))
	for index := range checkable {
		wg.Add(1)
		semaphore <- struct{}{}
		go func(position int) {
			defer wg.Done()
			defer func() { <-semaphore }()
			results[position] = s.isInstalled(ctx, &checkable[position])
		}(index)
	}
	wg.Wait()

	for index, installed := range results {
		if installed {
			continue
		}
		row := checkable[index]
		if err := s.store.UpdateDependence(ctx, row.ID, map[string]any{
			"status": DepStatusInstallFailed,
		}); err != nil {
			return err
		}
	}
	return nil
}

// isInstalled ports isDependenceInstalled.
func (s *DependenceService) isInstalled(ctx context.Context, row *store.Dependence) bool {
	doc := toDependenceDoc(row)
	depName, version := splitDependencyVersion(doc.Type, strings.TrimSpace(doc.Name))
	depInfo := normalizeDependencyInfo(s.runOutput(ctx, s.getGetCommand(doc.Type, depName)))
	matches := doc.Type == DepTypePython3
	if doc.Type == DepTypeNodejs {
		fields := strings.Split(depInfo, " ")
		matches = len(fields) > 0 && fields[0] == depName
	}
	return depInfo != "" && matches && (version == "" || strings.Contains(depInfo, version))
}

// newJobID returns a UUID v4, which is what the refresh routes validate.
func newJobID() string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return fmt.Sprintf("%08x-0000-4000-8000-%012x", time.Now().UnixNano(), time.Now().UnixNano())
	}
	buffer[6] = (buffer[6] & 0x0f) | 0x40
	buffer[8] = (buffer[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(buffer)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" +
		encoded[16:20] + "-" + encoded[20:32]
}
