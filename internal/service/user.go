// Package service holds the panel's business logic. M2 implements the user and
// authentication domain from back/services/user.ts.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"qinglong-go/internal/auth"
	"qinglong-go/internal/config"
	"qinglong-go/internal/i18n"
	"qinglong-go/internal/keyv"
	"qinglong-go/internal/store"
)

// Login status values stored in login logs (back/data/system.ts LoginStatus).
const (
	loginStatusSuccess = 0
	loginStatusFail    = 1
)

// maxLoginLogs keeps the newest 100 entries, like getLoginLog().
const maxLoginLogs = 100

// maxNotifyLogs keeps the newest 200 audit rows.
const maxNotifyLogs = 200

// Reply is the business result a handler sends back. Node returns these with
// HTTP 200 even when Code is 4xx, so the HTTP status is decided by the handler.
type Reply struct {
	Code int
	Data any
	// MessageSet forces an empty message to be serialized. The 2FA challenge
	// response is {"code":420,"message":""}, which omitempty would collapse to
	// {"code":420}.
	Message    string
	MessageSet bool
}

// UserService implements the 17 user/auth endpoints.
type UserService struct {
	cfg      *config.Config
	store    *store.Store
	keyv     *keyv.Store
	logger   *slog.Logger
	throttle *auth.LoginThrottle

	// notifier delivers the login notice. It is attached after construction
	// because the notification service needs the same store, and a nil value
	// only means the audit row is written without a delivery attempt.
	notifier *NotifyService

	// authMu serializes account mutations. Node wraps every mutating method in
	// serializeAuthMutation so a login that read old credentials cannot race a
	// password reset that revokes its session.
	authMu sync.Mutex
}

// SetNotifier attaches the notification service used for login notices.
func (s *UserService) SetNotifier(notifier *NotifyService) {
	s.notifier = notifier
}

// NewUserService wires the service.
func NewUserService(cfg *config.Config, st *store.Store, kv *keyv.Store, logger *slog.Logger) *UserService {
	if logger == nil {
		logger = slog.Default()
	}
	return &UserService{
		cfg:      cfg,
		store:    st,
		keyv:     kv,
		logger:   logger,
		throttle: auth.NewLoginThrottle(),
	}
}

// Throttle exposes the login throttle for the system reset endpoint (M3).
func (s *UserService) Throttle() *auth.LoginThrottle { return s.throttle }

func nowMillis() int64 { return time.Now().UnixMilli() }

// BootstrapAuthInfo runs the auth part of initData.ts: ensure the authConfig and
// notification rows exist and mirror authConfig into keyv.sqlite, which is what
// the Node panel reads on every request.
func (s *UserService) BootstrapAuthInfo(ctx context.Context) error {
	defaultInfo := map[string]any{"username": "admin", "password": "admin"}
	if legacy, err := s.readLegacyAuthFile(); err == nil && legacy != nil {
		defaultInfo = legacy
	}
	if err := s.store.EnsureAuthRow(ctx, store.AuthTypeAuthConfig, defaultInfo); err != nil {
		return err
	}
	if err := s.store.EnsureAuthRowExists(ctx, store.AuthTypeNotification); err != nil {
		return err
	}

	info, err := s.AuthInfo(ctx)
	if err != nil {
		return err
	}
	// initData.ts only mirrors authConfig into keyv at boot; the Auths row is
	// left untouched so a Node-side edit is never overwritten by a Go restart.
	data, err := info.Bytes()
	if err != nil {
		return err
	}
	if err := s.keyv.Set(ctx, keyv.KeyAuthInfo, json.RawMessage(data)); err != nil {
		return err
	}

	if lang, err := s.keyv.Get(ctx, keyv.KeyLang); err == nil {
		var value string
		if json.Unmarshal(lang, &value) == nil {
			i18n.SetLang(value)
		}
	}
	return nil
}

// readLegacyAuthFile reads data/config/auth.json when it exists.
func (s *UserService) readLegacyAuthFile() (map[string]any, error) {
	content, err := os.ReadFile(filepath.Join(s.cfg.ConfigPath, "auth.json"))
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := json.Unmarshal(content, &doc); err != nil {
		return nil, err
	}
	if len(doc) == 0 {
		return nil, errors.New("empty auth.json")
	}
	return doc, nil
}

// AuthInfo returns the current auth document, preferring keyv like Node.
func (s *UserService) AuthInfo(ctx context.Context) (*auth.AuthInfo, error) {
	raw, err := s.keyv.Get(ctx, keyv.KeyAuthInfo)
	switch {
	case err == nil:
		info, parseErr := auth.ParseAuthInfo(raw)
		if parseErr == nil {
			return info, nil
		}
		s.logger.Warn("authInfo in keyv is unreadable, falling back to database", "error", parseErr)
	case errors.Is(err, keyv.ErrNotFound):
		// Fall through to the Auths row.
	default:
		return nil, err
	}

	row, err := s.store.AuthRowByType(ctx, store.AuthTypeAuthConfig)
	if err != nil {
		return nil, err
	}
	if row == nil || len(row.Info) == 0 {
		return auth.NewAuthInfo(nil), nil
	}
	return auth.ParseAuthInfo(row.Info)
}

// saveAuthInfo writes keyv first and then the Auths row, mirroring
// updateAuthInfo() so a crash between the two writes leaves the panel usable.
func (s *UserService) saveAuthInfo(ctx context.Context, info *auth.AuthInfo) error {
	data, err := info.Bytes()
	if err != nil {
		return err
	}
	if err := s.keyv.Set(ctx, keyv.KeyAuthInfo, json.RawMessage(data)); err != nil {
		return err
	}
	return s.store.UpsertAuthRow(ctx, store.AuthTypeAuthConfig, data)
}

// Login authenticates a username/password pair.
func (s *UserService) Login(ctx context.Context, username, password, ip, platform string) (*Reply, error) {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	return s.authenticate(ctx, username, password, ip, platform, true)
}

func (s *UserService) authenticate(
	ctx context.Context,
	username, password, ip, platform string,
	needTwoFactor bool,
) (*Reply, error) {
	content, err := s.AuthInfo(ctx)
	if err != nil {
		return nil, err
	}
	if content.IsDefault() {
		return &Reply{Code: 450, Message: i18n.T("请先初始化")}, nil
	}

	timestamp := nowMillis()
	address := auth.Address(ip)

	cUsername := content.String("username")
	cPassword := content.String("password")
	retries, _ := content.Int("retries")
	lastlogon, hasLastlogon := content.Int("lastlogon")
	lastip, hasLastip := content.Get("lastip")
	lastaddr, hasLastaddr := content.Get("lastaddr")
	platformOld, hasPlatform := content.Get("platform")
	twoFactorActivated := content.Bool("twoFactorActivated")

	if content.IsIPBlocked(ip) {
		s.insertLoginLog(ctx, timestamp, address, ip, platform, loginStatusFail)
		s.refreshLoginLog(ctx)
		return &Reply{Code: 403, Message: i18n.T("该 IP 已被列入黑名单")}, nil
	}

	if wait := s.throttle.RetryAfter(ip, timestamp); wait > 0 {
		return &Reply{
			Code:    410,
			Message: i18n.Tf("失败次数过多，请%s秒后重试", wait),
			Data:    wait,
		}, nil
	}

	passwordMatches := username == cUsername && auth.VerifyPassword(password, cPassword)

	if passwordMatches && twoFactorActivated && needTwoFactor {
		content.Set("isTwoFactorChecking", true)
		content.Set("twoFactorExpiresAt", timestamp+5*60*1000)
		if err := s.saveAuthInfo(ctx, content); err != nil {
			return nil, err
		}
		return &Reply{Code: 420, Message: "", MessageSet: true}, nil
	}

	if passwordMatches {
		s.throttle.Reset(ip)
		data := auth.RandomString(50, 100)
		ttl := 20 * 24 * time.Hour
		if twoFactorActivated {
			ttl = 60 * 24 * time.Hour
		}
		if override, ok := parseExpiresIn(s.cfg.JWTExpiresIn); ok {
			ttl = override
		}
		token, err := auth.Sign(s.cfg.JWTSecret, data, ttl)
		if err != nil {
			return nil, err
		}

		tokenInfo := auth.TokenInfo{
			Value:     token,
			Timestamp: timestamp,
			IP:        ip,
			Address:   address,
			Platform:  platform,
		}
		content.AddToken(platform, tokenInfo, s.cfg.MaxTokensPerPlatform, timestamp)

		storedPassword := cPassword
		if !auth.IsPasswordHash(cPassword) {
			if hashed, hashErr := auth.HashPassword(password); hashErr == nil {
				storedPassword = hashed
			}
		}
		content.Set("password", storedPassword)
		content.Set("token", token)
		content.Set("lastlogon", timestamp)
		content.Set("retries", 0)
		content.Set("lastip", ip)
		content.Set("lastaddr", address)
		content.Set("platform", platform)
		content.Set("isTwoFactorChecking", false)
		content.Set("twoFactorExpiresAt", 0)
		if err := s.saveAuthInfo(ctx, content); err != nil {
			return nil, err
		}

		s.notifyLoginAsync(ctx, timestamp, address, ip, platform, true)

		payload := map[string]any{"token": token}
		if hasLastip {
			payload["lastip"] = lastip
		}
		if hasLastaddr {
			payload["lastaddr"] = lastaddr
		}
		if hasLastlogon {
			payload["lastlogon"] = lastlogon
		}
		payload["retries"] = retries
		if hasPlatform {
			payload["platform"] = platformOld
		}

		s.insertLoginLog(ctx, timestamp, address, ip, platform, loginStatusSuccess)
		s.refreshLoginLog(ctx)
		return &Reply{Code: 200, Data: payload}, nil
	}

	failures := s.throttle.Fail(ip, timestamp)
	content.Set("retries", failures)
	content.Set("lastlogon", timestamp)
	content.Set("lastip", ip)
	content.Set("lastaddr", address)
	content.Set("platform", platform)
	if err := s.saveAuthInfo(ctx, content); err != nil {
		return nil, err
	}

	s.notifyLoginAsync(ctx, timestamp, address, ip, platform, false)
	s.insertLoginLog(ctx, timestamp, address, ip, platform, loginStatusFail)
	s.refreshLoginLog(ctx)

	if failures > 3 {
		wait := s.throttle.RetryAfter(ip, timestamp)
		return &Reply{
			Code:    410,
			Message: i18n.Tf("失败次数过多，请%s秒后重试", wait),
			Data:    wait,
		}, nil
	}
	return &Reply{Code: 400, Message: i18n.T("错误的用户名密码，请重试")}, nil
}

// Logout revokes the token of one platform.
func (s *UserService) Logout(ctx context.Context, platform, token string) (*Reply, error) {
	s.authMu.Lock()
	defer s.authMu.Unlock()

	if platform == "" || token == "" {
		s.logger.Warn("logout called with empty platform or token")
		return &Reply{Code: 200}, nil
	}
	info, err := s.AuthInfo(ctx)
	if err != nil {
		return nil, err
	}
	now := nowMillis()
	if !info.ContainsToken(platform, token) && info.String("token") != token {
		s.logger.Info("logout for a token that is no longer stored", "platform", platform)
		return &Reply{Code: 200}, nil
	}
	info.RemoveToken(platform, token, now)
	if info.String("token") == token {
		info.Set("token", "")
	}
	if err := s.saveAuthInfo(ctx, info); err != nil {
		return nil, err
	}
	return &Reply{Code: 200}, nil
}

// Init creates the administrator account (PUT /api/user/init).
func (s *UserService) Init(ctx context.Context, username, password string) (*Reply, error) {
	s.authMu.Lock()
	defer s.authMu.Unlock()

	info, err := s.AuthInfo(ctx)
	if err != nil {
		return nil, err
	}
	if !info.IsDefault() {
		return &Reply{Code: 450, Message: i18n.T("未知错误")}, nil
	}
	if password == "admin" {
		return &Reply{Code: 400, Message: i18n.T("密码不能设置为admin")}, nil
	}
	hashed, err := auth.HashPassword(password)
	if err != nil {
		return nil, err
	}
	info.Set("username", username)
	info.Set("password", hashed)
	info.ClearTokens()
	info.Set("isTwoFactorChecking", false)
	info.Set("twoFactorExpiresAt", 0)
	if err := s.saveAuthInfo(ctx, info); err != nil {
		return nil, err
	}
	return &Reply{Code: 200, Message: i18n.T("更新成功")}, nil
}

// UpdateUser changes the username and password and revokes every session.
func (s *UserService) UpdateUser(ctx context.Context, username, password string) (*Reply, error) {
	s.authMu.Lock()
	defer s.authMu.Unlock()

	if password == "admin" {
		return &Reply{Code: 400, Message: i18n.T("密码不能设置为admin")}, nil
	}
	info, err := s.AuthInfo(ctx)
	if err != nil {
		return nil, err
	}
	hashed, err := auth.HashPassword(password)
	if err != nil {
		return nil, err
	}
	info.Set("username", username)
	info.Set("password", hashed)
	info.ClearTokens()
	info.Set("isTwoFactorChecking", false)
	info.Set("twoFactorExpiresAt", 0)
	if err := s.saveAuthInfo(ctx, info); err != nil {
		return nil, err
	}
	return &Reply{Code: 200, Message: i18n.T("更新成功")}, nil
}

// UserInfo returns the profile fields GET /api/user exposes. Absent fields stay
// absent, exactly like JSON.stringify dropping undefined values.
func (s *UserService) UserInfo(ctx context.Context) (map[string]any, error) {
	info, err := s.AuthInfo(ctx)
	if err != nil {
		return nil, err
	}
	data := map[string]any{"username": info.String("username")}
	if avatar, ok := info.Get("avatar"); ok {
		data["avatar"] = avatar
	}
	if activated, ok := info.Get("twoFactorActivated"); ok {
		data["twoFactorActivated"] = activated
	}
	return data, nil
}

// TwoFactorInit generates a new TOTP secret and its otpauth URL.
func (s *UserService) TwoFactorInit(ctx context.Context) (*Reply, error) {
	s.authMu.Lock()
	defer s.authMu.Unlock()

	info, err := s.AuthInfo(ctx)
	if err != nil {
		return nil, err
	}
	if info.Bool("twoFactorActivated") {
		return nil, errors.New(i18n.T("请先关闭两步验证"))
	}
	secret, err := auth.GenerateTOTPSecret()
	if err != nil {
		return nil, err
	}
	url := auth.TOTPKeyURI(info.String("username"), "qinglong", secret)
	info.Set("twoFactorSecret", secret)
	if err := s.saveAuthInfo(ctx, info); err != nil {
		return nil, err
	}
	return &Reply{Code: 200, Data: map[string]any{"secret": secret, "url": url}}, nil
}

// TwoFactorActive enables 2FA after checking one valid code.
func (s *UserService) TwoFactorActive(ctx context.Context, code string) (*Reply, error) {
	s.authMu.Lock()
	defer s.authMu.Unlock()

	info, err := s.AuthInfo(ctx)
	if err != nil {
		return nil, err
	}
	valid := auth.VerifyTOTP(info.String("twoFactorSecret"), code)
	if valid {
		info.Set("twoFactorActivated", true)
		info.ClearTokens()
		info.Set("isTwoFactorChecking", false)
		info.Set("twoFactorExpiresAt", 0)
		if err := s.saveAuthInfo(ctx, info); err != nil {
			return nil, err
		}
	}
	return &Reply{Code: 200, Data: valid}, nil
}

// TwoFactorDeactivate turns 2FA off and revokes every session.
func (s *UserService) TwoFactorDeactivate(ctx context.Context) (*Reply, error) {
	s.authMu.Lock()
	defer s.authMu.Unlock()

	info, err := s.AuthInfo(ctx)
	if err != nil {
		return nil, err
	}
	info.Set("twoFactorActivated", false)
	info.Set("twoFactorSecret", "")
	info.ClearTokens()
	info.Set("isTwoFactorChecking", false)
	info.Set("twoFactorExpiresAt", 0)
	if err := s.saveAuthInfo(ctx, info); err != nil {
		return nil, err
	}
	return &Reply{Code: 200, Data: true}, nil
}

// TwoFactorLogin completes a login that was paused by the 420 challenge.
func (s *UserService) TwoFactorLogin(
	ctx context.Context,
	username, password, code, ip, platform string,
) (*Reply, error) {
	s.authMu.Lock()
	defer s.authMu.Unlock()

	info, err := s.AuthInfo(ctx)
	if err != nil {
		return nil, err
	}
	now := nowMillis()
	if s.throttle.RetryAfter(ip, now) > 0 {
		return &Reply{Code: 410, Message: i18n.T("失败次数过多，请稍后重试")}, nil
	}

	expiresAt, _ := info.Int("twoFactorExpiresAt")
	if !info.Bool("isTwoFactorChecking") ||
		!info.Bool("twoFactorActivated") ||
		expiresAt == 0 ||
		expiresAt <= now {
		return &Reply{Code: 450, Message: i18n.T("未知错误")}, nil
	}

	step := auth.TOTPStep()
	lastStep, _ := info.Int("lastTwoFactorStep")
	valid := username == info.String("username") &&
		auth.VerifyPassword(password, info.String("password")) &&
		lastStep != step &&
		auth.VerifyTOTP(info.String("twoFactorSecret"), code)

	if valid {
		info.Set("lastTwoFactorStep", step)
		if err := s.saveAuthInfo(ctx, info); err != nil {
			return nil, err
		}
		return s.authenticate(ctx, username, password, ip, platform, false)
	}

	address := auth.Address(ip)
	info.Set("retries", s.throttle.Fail(ip, now))
	info.Set("lastlogon", now)
	info.Set("lastip", ip)
	info.Set("lastaddr", address)
	info.Set("platform", platform)
	if err := s.saveAuthInfo(ctx, info); err != nil {
		return nil, err
	}
	return &Reply{Code: 430, Message: i18n.T("验证失败")}, nil
}

// LoginLog returns the newest 100 login log entries and prunes the rest.
func (s *UserService) LoginLog(ctx context.Context) ([]any, error) {
	return s.loginLog(ctx)
}

func (s *UserService) loginLog(ctx context.Context) ([]any, error) {
	rows, err := s.store.AuthRowsByType(ctx, store.AuthTypeLoginLog)
	if err != nil {
		return nil, err
	}
	type entry struct {
		id   int64
		info map[string]any
		ts   int64
	}
	entries := make([]entry, 0, len(rows))
	for _, row := range rows {
		if len(row.Info) == 0 {
			continue
		}
		var info map[string]any
		if err := json.Unmarshal(row.Info, &info); err != nil {
			continue
		}
		ts := int64(0)
		if value, ok := info["timestamp"]; ok {
			ts = toInt64(value)
		}
		entries = append(entries, entry{id: row.ID, info: info, ts: ts})
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].ts > entries[j].ts })

	if len(entries) > maxLoginLogs {
		ids := make([]int64, 0, len(entries)-maxLoginLogs)
		for _, item := range entries[maxLoginLogs:] {
			ids = append(ids, item.id)
		}
		if err := s.store.DeleteAuthRows(ctx, ids); err != nil {
			s.logger.Warn("prune login log failed", "error", err)
		}
		entries = entries[:maxLoginLogs]
	}

	out := make([]any, 0, len(entries))
	for _, item := range entries {
		out = append(out, item.info)
	}
	return out, nil
}

func (s *UserService) insertLoginLog(
	ctx context.Context,
	timestamp int64,
	address, ip, platform string,
	status int,
) {
	info, err := json.Marshal(map[string]any{
		"timestamp": timestamp,
		"address":   address,
		"ip":        ip,
		"platform":  platform,
		"status":    status,
	})
	if err != nil {
		s.logger.Warn("encode login log failed", "error", err)
		return
	}
	if _, err := s.store.InsertAuthRow(ctx, store.AuthTypeLoginLog, info); err != nil {
		s.logger.Warn("write login log failed", "error", err)
	}
}

func (s *UserService) refreshLoginLog(ctx context.Context) {
	if _, err := s.loginLog(ctx); err != nil {
		s.logger.Warn("refresh login log failed", "error", err)
	}
}

// IPBlacklist returns the normalized blacklist.
func (s *UserService) IPBlacklist(ctx context.Context) ([]string, error) {
	info, err := s.AuthInfo(ctx)
	if err != nil {
		return nil, err
	}
	return auth.UniqStrings(info.BlockedIPs()), nil
}

// BlockIP adds an address to the blacklist.
func (s *UserService) BlockIP(ctx context.Context, ip string) ([]string, error) {
	s.authMu.Lock()
	defer s.authMu.Unlock()

	info, err := s.AuthInfo(ctx)
	if err != nil {
		return nil, err
	}
	blocked := append(info.BlockedIPs(), auth.NormalizeIP(ip))
	blocked = auth.UniqStrings(blocked)
	info.SetStringSlice("blockedIps", blocked)
	if err := s.saveAuthInfo(ctx, info); err != nil {
		return nil, err
	}
	return blocked, nil
}

// UnblockIP removes an address from the blacklist.
func (s *UserService) UnblockIP(ctx context.Context, ip string) ([]string, error) {
	s.authMu.Lock()
	defer s.authMu.Unlock()

	info, err := s.AuthInfo(ctx)
	if err != nil {
		return nil, err
	}
	normalized := auth.NormalizeIP(ip)
	blocked := make([]string, 0, len(info.BlockedIPs()))
	for _, candidate := range info.BlockedIPs() {
		if candidate != "" && candidate != normalized {
			blocked = append(blocked, candidate)
		}
	}
	info.SetStringSlice("blockedIps", blocked)
	if err := s.saveAuthInfo(ctx, info); err != nil {
		return nil, err
	}
	return blocked, nil
}

// Notification returns the stored notification configuration ({} when unset).
func (s *UserService) Notification(ctx context.Context) (map[string]any, error) {
	row, err := s.store.AuthRowByType(ctx, store.AuthTypeNotification)
	if err != nil {
		return nil, err
	}
	if row == nil || len(row.Info) == 0 {
		return map[string]any{}, nil
	}
	var info map[string]any
	if err := json.Unmarshal(row.Info, &info); err != nil {
		return map[string]any{}, nil
	}
	return info, nil
}

// UpdateNotification validates the configuration with a test notification.
//
// M2 does not ship the channel senders yet: an empty type is accepted (the
// panel's "clear configuration" path), a configured type reports failure exactly
// like a broken configuration does in Node. M7 replaces the check with the real
// senders.
func (s *UserService) UpdateNotification(ctx context.Context, payload map[string]any) (*Reply, error) {
	s.authMu.Lock()
	defer s.authMu.Unlock()

	success := true
	notifyType := ""
	if raw, ok := payload["type"].(string); ok {
		notifyType = raw
	}
	if notifyType != "" {
		success = false
	}
	if !success {
		return &Reply{Code: 400, Message: i18n.T("通知发送失败，请检查参数")}, nil
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if err := s.store.UpsertAuthRow(ctx, store.AuthTypeNotification, data); err != nil {
		return nil, err
	}
	return &Reply{Code: 200, Data: map[string]any{"code": randomCode()}}, nil
}

// UpdateAvatar stores the uploaded file name on the account.
func (s *UserService) UpdateAvatar(ctx context.Context, filename string) (*Reply, error) {
	s.authMu.Lock()
	defer s.authMu.Unlock()

	info, err := s.AuthInfo(ctx)
	if err != nil {
		return nil, err
	}
	info.Set("avatar", filename)
	if err := s.saveAuthInfo(ctx, info); err != nil {
		return nil, err
	}
	return &Reply{Code: 200, Data: filename, Message: i18n.T("更新成功")}, nil
}

// notifyLoginAsync mirrors the un-awaited `notificationService.notify(...)` call
// in back/services/user.ts:171: the login response never waits for the channel
// attempt or its audit row, and a notification failure can never change the
// login result. WithoutCancel keeps the request values while dropping the
// cancellation that fires as soon as the response is written.
func (s *UserService) notifyLoginAsync(
	ctx context.Context,
	timestamp int64,
	address, ip, platform string,
	success bool,
) {
	base := context.WithoutCancel(ctx)
	go func() {
		runCtx, cancel := context.WithTimeout(base, 10*time.Second)
		defer cancel()
		s.notifyLogin(runCtx, timestamp, address, ip, platform, success)
	}()
}

// notifyLogin reproduces notificationService.notify(): the login notice is
// attempted and an audit row is always written.
func (s *UserService) notifyLogin(
	ctx context.Context,
	timestamp int64,
	address, ip, platform string,
	loginSucceeded bool,
) {
	title := i18n.T("登录通知")
	outcome := i18n.T("登录失败")
	if loginSucceeded {
		outcome = i18n.T("登录成功")
	}
	content := i18n.T("你于") +
		time.UnixMilli(timestamp).Format("2006-01-02 15:04:05") +
		i18n.T("在") + i18n.T(address) +
		" " + platform + i18n.T("端") +
		" " + outcome +
		i18n.T("，ip地址") + " " + ip

	// Notify delivers the notice and writes the audit row itself, using the same
	// channel configuration the panel shows. The old code hard-coded
	// `delivered := false`, so every login notice was recorded as failed even
	// when a channel was configured.
	if s.notifier != nil {
		s.notifier.Notify(ctx, NotificationRequest{Title: title, Content: content})
		return
	}
	// No notifier attached (tests): still record the attempt so the log never
	// silently loses it.
	s.persistNotifyLog(ctx, title, content, "", loginStatusFail)
}

func (s *UserService) persistNotifyLog(ctx context.Context, title, content, notifyType string, status int) {
	info, err := json.Marshal(map[string]any{
		"timestamp":  nowMillis(),
		"title":      truncate(title, 512),
		"content":    truncate(content, 8192),
		"status":     status,
		"notifyType": truncate(notifyType, 64),
		"source":     "system",
		"scriptName": "",
	})
	if err != nil {
		return
	}
	if _, err := s.store.InsertAuthRow(ctx, store.AuthTypeNotifyLog, info); err != nil {
		s.logger.Warn("write notify log failed", "error", err)
		return
	}
	if err := s.store.PruneAuthRows(ctx, store.AuthTypeNotifyLog, maxNotifyLogs); err != nil {
		s.logger.Warn("prune notify log failed", "error", err)
	}
}

func truncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func randomCode() string {
	return fmt.Sprintf("%06d", rand.Intn(1000000))
}

// parseExpiresIn understands the values jsonwebtoken accepts for JWT_EXPIRES_IN
// ("20d", "12h", "30m", "10s", "2w"); a bare number is read as seconds.
func parseExpiresIn(value string) (time.Duration, bool) {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return 0, false
	}
	unit := raw[len(raw)-1]
	numberPart := raw
	multiplier := time.Second
	switch unit {
	case 's':
		numberPart = raw[:len(raw)-1]
	case 'm':
		numberPart = raw[:len(raw)-1]
		multiplier = time.Minute
	case 'h':
		numberPart = raw[:len(raw)-1]
		multiplier = time.Hour
	case 'd':
		numberPart = raw[:len(raw)-1]
		multiplier = 24 * time.Hour
	case 'w':
		numberPart = raw[:len(raw)-1]
		multiplier = 7 * 24 * time.Hour
	}
	number, err := strconv.ParseFloat(strings.TrimSpace(numberPart), 64)
	if err != nil || number <= 0 {
		return 0, false
	}
	return time.Duration(number * float64(multiplier)), true
}

func toInt64(value any) int64 {
	switch typed := value.(type) {
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return parsed
		}
		if float, err := typed.Float64(); err == nil {
			return int64(float)
		}
	case float64:
		return int64(typed)
	case int64:
		return typed
	case int:
		return int64(typed)
	}
	return 0
}

// ResetAuthInfo implements PUT /api/system/auth/reset. It returns nil when the
// caller should answer with the plain 更新成功 message, which is what Node does
// when resetAuthInfo() resolves without a value.
func (s *UserService) ResetAuthInfo(ctx context.Context, payload ResetAuthPayload) (*Reply, error) {
	s.authMu.Lock()
	defer s.authMu.Unlock()

	if payload.Password != nil && *payload.Password == "admin" {
		return &Reply{Code: 400, Message: i18n.T("密码不能设置为admin")}, nil
	}
	info, err := s.AuthInfo(ctx)
	if err != nil {
		return nil, err
	}
	if payload.Password != nil {
		hashed, err := auth.HashPassword(*payload.Password)
		if err != nil {
			return nil, err
		}
		info.Set("password", hashed)
	}
	if payload.Username != nil {
		info.Set("username", *payload.Username)
	}
	if payload.Retries != nil {
		info.Set("retries", *payload.Retries)
	}
	if payload.TwoFactorActivated != nil {
		info.Set("twoFactorActivated", *payload.TwoFactorActivated)
	}
	if payload.Password != nil || payload.Username != nil || payload.TwoFactorActivated != nil {
		// A credential change revokes every session; the websocket loop closes
		// the sockets within a second (Node closes them synchronously).
		info.Set("token", "")
		info.Set("tokens", map[string]any{})
		info.Set("isTwoFactorChecking", false)
		info.Set("twoFactorExpiresAt", 0)
	}
	if err := s.saveAuthInfo(ctx, info); err != nil {
		return nil, err
	}
	clearedRetries := payload.Retries != nil && *payload.Retries == 0
	if clearedRetries || payload.Password != nil || payload.Username != nil {
		s.throttle.Clear()
	}
	return nil, nil
}
