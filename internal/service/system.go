package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"qinglong-go/internal/config"
	"qinglong-go/internal/i18n"
	"qinglong-go/internal/platform"
	"qinglong-go/internal/store"
)

// SystemService owns the settings, storage, log and command endpoints of
// back/api/system.ts plus the retention policy of back/api/retention.ts.
//
// Node spreads the same work over SystemService, RetentionService and a few
// helpers; the Go side keeps them in one type because they all mutate the
// single systemConfig row.
type SystemService struct {
	cfg    *config.Config
	store  *store.Store
	user   *UserService
	crons  *CronService
	notify *NotifyService
	subs   *SubscriptionService
	tools  *platform.Toolchain
	logger *slog.Logger
	sock   *SockService

	// cleanup guards the log pruning ticker started by log-remove-frequency.
	cleanupMu   sync.Mutex
	cleanupStop chan struct{}

	// reload is installed by main so PUT /api/system/reload can ask the process
	// to stop the way Node's process.exit(0) does.
	reload func()
}

// NewSystemService wires the settings endpoints.
func NewSystemService(
	cfg *config.Config,
	st *store.Store,
	user *UserService,
	crons *CronService,
	notify *NotifyService,
	subs *SubscriptionService,
	tools *platform.Toolchain,
	logger *slog.Logger,
) *SystemService {
	if logger == nil {
		logger = slog.Default()
	}
	return &SystemService{
		cfg:    cfg,
		store:  st,
		user:   user,
		crons:  crons,
		notify: notify,
		subs:   subs,
		tools:  tools,
		logger: logger,
	}
}

// SetSock attaches the websocket hub after both services exist.
func (s *SystemService) SetSock(sock *SockService) { s.sock = sock }

// SetReloadHook installs the graceful shutdown callback used by /system/reload.
func (s *SystemService) SetReloadHook(hook func()) { s.reload = hook }

// Bootstrap replays the parts of initData.ts that belong to the settings: the
// systemConfig row, the language the task scripts see, and the log purge timer.
func (s *SystemService) Bootstrap(ctx context.Context) error {
	if err := s.store.EnsureAuthRowExists(ctx, store.AuthTypeSystemConfig); err != nil {
		return err
	}
	row, err := s.store.AuthRowByType(ctx, store.AuthTypeSystemConfig)
	if err != nil {
		return err
	}
	info := map[string]any{}
	if row != nil {
		info = decodeInfo(row.Info)
	}
	if lang, ok := info["lang"].(string); ok && strings.TrimSpace(lang) != "" {
		i18n.SetLang(lang)
	}
	// initData.ts writes lang_env.sh when it is missing so every task inherits
	// the panel language.
	if _, err := os.Stat(s.cfg.LangEnvFile); os.IsNotExist(err) {
		if writeErr := writeFileWithLock(s.cfg.LangEnvFile,
			"export QL_LANG="+shellQuote(i18n.Lang())+"\n"); writeErr != nil {
			s.logger.Warn("[boot] write lang_env.sh failed", "error", writeErr)
		}
	}
	if days := NormalizeRetentionDays(info["logRemoveFrequency"]); days > 0 {
		s.scheduleLogCleanup(days)
	}
	return nil
}

// nullableText keeps an empty column out of the JSON body, like a NULL column.
func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// ---------------------------------------------------------------------------
// system info / config
// ---------------------------------------------------------------------------

// InfoResponse is GET /api/system.
type InfoResponse struct {
	IsInitialized bool   `json:"isInitialized"`
	Version       string `json:"version"`
	PublishTime   int64  `json:"publishTime"`
	Branch        string `json:"branch"`
	ChangeLog     string `json:"changeLog"`
	ChangeLogLink string `json:"changeLogLink"`
}

// Info implements GET /api/system: the public bootstrap payload.
func (s *SystemService) Info(ctx context.Context) (*InfoResponse, error) {
	info, err := s.user.AuthInfo(ctx)
	if err != nil {
		return nil, err
	}
	doc := platform.ReadVersionDoc(s.cfg.RootPath)
	branch := strings.TrimSpace(os.Getenv("QL_BRANCH"))
	if branch == "" {
		branch = "master"
	}
	var publishTime int64
	if parsed, err := time.ParseInLocation("2006-01-02 15:04:05", doc.PublishTime, time.Local); err == nil {
		publishTime = parsed.Unix()
	}
	return &InfoResponse{
		IsInitialized: !info.IsDefault(),
		Version:       doc.Version,
		PublishTime:   publishTime,
		Branch:        branch,
		ChangeLog:     doc.ChangeLog,
		ChangeLogLink: doc.ChangeLogLink,
	}, nil
}

// SystemConfigResponse is GET /api/system/config. The layout reads
// info.panelTitle and info.lang from it.
type SystemConfigResponse struct {
	ID        int64          `json:"id"`
	IP        any            `json:"ip"`
	Type      string         `json:"type"`
	Info      map[string]any `json:"info"`
	CreatedAt *string        `json:"createdAt"`
	UpdatedAt *string        `json:"updatedAt"`
}

// Config implements GET /api/system/config.
func (s *SystemService) Config(ctx context.Context) (*SystemConfigResponse, error) {
	row, err := s.store.AuthRowByType(ctx, store.AuthTypeSystemConfig)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, &NotFoundError{Status: 500, Message: `System {"type":"systemConfig"} not found`}
	}
	info := decodeInfo(row.Info)
	if _, ok := info["timezone"]; !ok {
		info["timezone"] = "Asia/Shanghai"
	}
	return &SystemConfigResponse{
		ID:        row.ID,
		IP:        nullableText(row.IP),
		Type:      row.Type,
		Info:      info,
		CreatedAt: store.ISO8601(row.CreatedAt),
		UpdatedAt: store.ISO8601(row.UpdatedAt),
	}, nil
}

// InfoMap returns the raw systemConfig info document.
func (s *SystemService) InfoMap(ctx context.Context) (map[string]any, error) {
	row, err := s.store.AuthRowByType(ctx, store.AuthTypeSystemConfig)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, &NotFoundError{Status: 500, Message: `System {"type":"systemConfig"} not found`}
	}
	return decodeInfo(row.Info), nil
}

// updateInfo merges patch into the systemConfig info document and returns the
// merged map, like updateAuthDb({...oDoc, info:{...oDoc.info, ...info}}).
func (s *SystemService) updateInfo(ctx context.Context, patch map[string]any) (map[string]any, error) {
	info, err := s.InfoMap(ctx)
	if err != nil {
		return nil, err
	}
	for key, value := range patch {
		info[key] = value
	}
	encoded, err := json.Marshal(info)
	if err != nil {
		return nil, err
	}
	if err := s.store.UpsertAuthRow(ctx, store.AuthTypeSystemConfig, encoded); err != nil {
		return nil, err
	}
	return info, nil
}

func decodeInfo(raw json.RawMessage) map[string]any {
	info := map[string]any{}
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return info
	}
	if err := json.Unmarshal(raw, &info); err != nil {
		return map[string]any{}
	}
	return info
}

// ---------------------------------------------------------------------------
// scalar settings
// ---------------------------------------------------------------------------

// SetLogRemoveFrequency implements PUT /api/system/config/log-remove-frequency.
// A positive value starts the daily purge of task logs older than N days, the
// job `ql rmlog N` performs in Node.
func (s *SystemService) SetLogRemoveFrequency(ctx context.Context, frequency *int64, present bool) (*Reply, error) {
	patch := map[string]any{"logRemoveFrequency": nil}
	days := int64(0)
	if frequency != nil {
		patch["logRemoveFrequency"] = *frequency
		days = *frequency
	}
	if _, err := s.updateInfo(ctx, patch); err != nil {
		return nil, err
	}
	s.scheduleLogCleanup(days)
	// Node answers res.send(result) with `info` = req.body, not the whole row.
	// Node echoes req.body, so an explicit null still shows up as a key.
	echo := map[string]any{}
	if present {
		if frequency != nil {
			echo["logRemoveFrequency"] = *frequency
		} else {
			echo["logRemoveFrequency"] = nil
		}
	}
	return &Reply{Code: 200, Data: echo}, nil
}

// SetCronConcurrency implements PUT /api/system/config/cron-concurrency. The
// scheduler reads the value back on every submission, so no restart is needed.
func (s *SystemService) SetCronConcurrency(ctx context.Context, concurrency *int64) (*Reply, error) {
	limit := int64(0)
	if concurrency != nil {
		limit = *concurrency
	}
	if limit < 0 || limit > 2147483647 {
		return nil, &NotFoundError{Status: 400, Message: i18n.T("参数错误")}
	}
	patch := map[string]any{"cronConcurrency": nil}
	echo := map[string]any{}
	if concurrency != nil {
		patch["cronConcurrency"] = limit
		echo["cronConcurrency"] = limit
	}
	if _, err := s.updateInfo(ctx, patch); err != nil {
		return nil, err
	}
	// Echo the request value, not the normalized one (Node returns `info`).
	return &Reply{Code: 200, Data: echo}, nil
}

// SetDependenceProxy implements PUT /api/system/config/dependence-proxy.
func (s *SystemService) SetDependenceProxy(ctx context.Context, proxy *string, present bool) (*Reply, error) {
	patch := map[string]any{"dependenceProxy": nil}
	if proxy != nil {
		patch["dependenceProxy"] = *proxy
	}
	if _, err := s.updateInfo(ctx, patch); err != nil {
		return nil, err
	}
	if proxy != nil && strings.TrimSpace(*proxy) != "" {
		content := "export http_proxy=" + shellQuote(*proxy) + "\nexport https_proxy=" + shellQuote(*proxy)
		if err := os.MkdirAll(s.cfg.ConfigPath, 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(s.cfg.DependenceProxyFile(), []byte(content), 0o644); err != nil {
			return nil, err
		}
	} else if err := os.Remove(s.cfg.DependenceProxyFile()); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	echo := map[string]any{}
	if present {
		if proxy != nil {
			echo["dependenceProxy"] = *proxy
		} else {
			echo["dependenceProxy"] = nil
		}
	}
	return &Reply{Code: 200, Data: echo}, nil
}

// SetPythonMirror implements PUT /api/system/config/python-mirror. It writes
// the same pip setting the Node panel does, through the interpreter the tasks
// use so the index actually applies.
func (s *SystemService) SetPythonMirror(ctx context.Context, mirror *string, present bool) (*Reply, error) {
	if !present {
		// back/api/system.ts only touches pip when the key is in the body.
		return &Reply{Code: 200, Data: map[string]any{}}, nil
	}
	patch := map[string]any{"pythonMirror": nil}
	if mirror != nil {
		patch["pythonMirror"] = *mirror
	}
	if _, err := s.updateInfo(ctx, patch); err != nil {
		return nil, err
	}
	python := s.tools.Path("python")
	if python == "" {
		return nil, &NotFoundError{Status: 500, Message: "python3: command not found"}
	}
	var args []string
	if mirror != nil && strings.TrimSpace(*mirror) != "" {
		args = []string{"-m", "pip", "config", "set", "global.index-url", *mirror}
	} else {
		args = []string{"-m", "pip", "config", "unset", "global.index-url"}
	}
	cmd := exec.Command(python, args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		return nil, &NotFoundError{Status: 500, Message: strings.TrimSpace(string(output))}
	}
	echo := map[string]any{}
	if mirror != nil {
		echo["pythonMirror"] = *mirror
	} else {
		echo["pythonMirror"] = nil
	}
	return &Reply{Code: 200, Data: echo}, nil
}

// SetTimezone implements PUT /api/system/config/timezone. The panel timezone
// drives the scheduler, so it is reloaded after the change.
func (s *SystemService) SetTimezone(ctx context.Context, timezone *string) (*Reply, error) {
	zone := "Asia/Shanghai"
	if timezone != nil && strings.TrimSpace(*timezone) != "" {
		zone = *timezone
	}
	if _, err := s.updateInfo(ctx, map[string]any{"timezone": zone}); err != nil {
		return nil, err
	}
	if !platform.SetSystemTimezone(zone) {
		return &Reply{Code: 400, Message: i18n.T("设置时区失败")}, nil
	}
	if err := s.crons.ReloadScheduler(ctx); err != nil {
		s.logger.Warn("[system] reload scheduler after timezone change failed", "error", err)
	}
	return &Reply{Code: 200, Data: map[string]any{"timezone": zone}}, nil
}

// SetLanguage implements PUT /api/system/config/lang. Besides the database it
// rewrites shell/preload/lang_env.sh so tasks see QL_LANG.
func (s *SystemService) SetLanguage(ctx context.Context, lang *string) (*Reply, error) {
	value := "zh"
	if lang != nil && *lang != "" {
		value = *lang
	}
	if _, err := s.updateInfo(ctx, map[string]any{"lang": value}); err != nil {
		return nil, err
	}
	if err := writeFileWithLock(s.cfg.LangEnvFile, "export QL_LANG="+shellQuote(value)+"\n"); err != nil {
		// Node logs the failure and still returns success.
		s.logger.Error("[system] write lang_env.sh failed", "error", err)
	}
	i18n.SetLang(value)
	return &Reply{Code: 200, Data: map[string]any{"lang": value}}, nil
}

// SetPanelTitle implements PUT /api/system/config/panel-title.
func (s *SystemService) SetPanelTitle(ctx context.Context, title *string) (*Reply, error) {
	value := ""
	if title != nil {
		value = strings.TrimSpace(*title)
	}
	if _, err := s.updateInfo(ctx, map[string]any{"panelTitle": value}); err != nil {
		return nil, err
	}
	return &Reply{Code: 200, Data: map[string]any{"panelTitle": value}}, nil
}

// SetGlobalSSHKey implements PUT /api/system/config/global-ssh-key.
func (s *SystemService) SetGlobalSSHKey(ctx context.Context, key *string) (*Reply, error) {
	patch := map[string]any{"globalSshKey": nil}
	if key != nil {
		patch["globalSshKey"] = *key
	}
	if _, err := s.updateInfo(ctx, patch); err != nil {
		return nil, err
	}
	if key != nil && strings.TrimSpace(*key) != "" {
		if err := s.subs.AddGlobalSSHKey(*key, "global"); err != nil {
			return nil, err
		}
	} else {
		s.subs.RemoveGlobalSSHKey("global")
	}
	// Node returns the updated row, not the echoed body.
	row, err := s.Config(ctx)
	if err != nil {
		return nil, err
	}
	return &Reply{Code: 200, Data: row}, nil
}

// CleanDependence implements PUT /api/system/config/dependence-clean: drop the
// download cache of one runtime and flag its installed rows as failed.
func (s *SystemService) CleanDependence(ctx context.Context, kind string) (*Reply, error) {
	if kind != "node" && kind != "python3" {
		return &Reply{Code: 400, Message: i18n.T("参数错误")}, nil
	}
	if err := os.RemoveAll(filepath.Join(s.cfg.DepCachePath, kind)); err != nil {
		return nil, err
	}
	depType := DepTypeNodejs
	if kind == "python3" {
		depType = DepTypePython3
	}
	if _, err := s.store.UpdateDependencesByTypeStatus(ctx, depType, DepStatusInstalled, DepStatusInstallFailed); err != nil {
		return nil, err
	}
	return &Reply{Code: 200}, nil
}

// Notify implements PUT /api/system/notify.
func (s *SystemService) Notify(ctx context.Context, title, content string) (*Reply, error) {
	return s.notify.Notify(ctx, NotificationRequest{Title: title, Content: content}), nil
}

// NotifyLog implements GET /api/system/notify-log: the newest 200 audit rows.
func (s *SystemService) NotifyLog(ctx context.Context) ([]map[string]any, error) {
	rows, err := s.store.AuthRowsByType(ctx, store.AuthTypeNotifyLog)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(rows))
	// Node orders by id DESC and limits to 200.
	for index := len(rows) - 1; index >= 0 && len(out) < 200; index-- {
		info := decodeInfo(rows[index].Info)
		info["id"] = rows[index].ID
		out = append(out, info)
	}
	return out, nil
}

// ResetAuth implements PUT /api/system/auth/reset.
func (s *SystemService) ResetAuth(ctx context.Context, payload ResetAuthPayload) (*Reply, error) {
	return s.user.ResetAuthInfo(ctx, payload)
}

// ResetAuthPayload is the validated body of PUT /api/system/auth/reset.
type ResetAuthPayload struct {
	Retries            *int64
	TwoFactorActivated *bool
	Password           *string
	Username           *string
}

// ---------------------------------------------------------------------------
// log pruning
// ---------------------------------------------------------------------------

// scheduleLogCleanup starts (or restarts) the daily purge of task logs older
// than days. Zero disables it, matching `if (info.logRemoveFrequency > 0)`.
func (s *SystemService) scheduleLogCleanup(days int64) {
	s.cleanupMu.Lock()
	defer s.cleanupMu.Unlock()
	if s.cleanupStop != nil {
		close(s.cleanupStop)
		s.cleanupStop = nil
	}
	if days <= 0 {
		return
	}
	stop := make(chan struct{})
	s.cleanupStop = stop
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if removed, err := s.PruneLogs(days); err != nil {
					s.logger.Warn("[system] prune task logs failed", "error", err)
				} else if removed > 0 {
					s.logger.Info("[system] pruned task logs", "files", removed, "days", days)
				}
			}
		}
	}()
}

// Stop stops the pruning ticker when the panel shuts down.
func (s *SystemService) Stop() {
	s.cleanupMu.Lock()
	defer s.cleanupMu.Unlock()
	if s.cleanupStop != nil {
		close(s.cleanupStop)
		s.cleanupStop = nil
	}
}
