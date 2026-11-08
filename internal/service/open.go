package service

import (
	"context"
	"crypto/rand"
	"log/slog"
	"math/big"
	"strconv"
	"sync"
	"time"

	"qinglong-go/internal/config"
	"qinglong-go/internal/i18n"
	"qinglong-go/internal/keyv"
	"qinglong-go/internal/store"
)

// AppScope values accepted by the panel (back/data/open.ts). The scope check
// itself lives in the HTTP middleware so /open requests never reach a handler.
const (
	ScopeApps = "apps"
)

// appTokenTTL mirrors the 30 day lifetime in back/services/open.ts.
const appTokenTTL = 2592000

// AppResponse is the plain object Sequelize returns. Key order matches the
// model declaration so `JSON.stringify` output stays identical.
type AppResponse struct {
	ID           int64            `json:"id"`
	Name         string           `json:"name"`
	Scopes       []string         `json:"scopes"`
	ClientID     string           `json:"client_id"`
	ClientSecret string           `json:"client_secret"`
	Tokens       []store.AppToken `json:"tokens"`
	CreatedAt    string           `json:"createdAt"`
	UpdatedAt    string           `json:"updatedAt"`
}

// OpenService owns the Apps table and the keyv `apps` cache the middleware
// reads on every /open request.
type OpenService struct {
	cfg    *config.Config
	store  *store.Store
	keyv   *keyv.Store
	logger *slog.Logger

	// mu serializes database writes and cache publication, the job
	// withAppsLock does in Node.
	mu sync.Mutex
}

// NewOpenService wires the app registry.
func NewOpenService(cfg *config.Config, st *store.Store, kv *keyv.Store, logger *slog.Logger) *OpenService {
	if logger == nil {
		logger = slog.Default()
	}
	return &OpenService{cfg: cfg, store: st, keyv: kv, logger: logger}
}

// InitializeSystemApp creates the built-in `system` app on first boot and makes
// sure it keeps the dashboard scope an upgrade added (initData.ts:33).
func (s *OpenService) InitializeSystemApp(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	app, err := s.store.AppByName(ctx, "system")
	if err != nil {
		return err
	}
	switch {
	case app == nil:
		if _, err := s.store.InsertApp(ctx, store.App{
			Name:         "system",
			Scopes:       []string{"crons", "system", "dashboard"},
			ClientID:     randomString(12),
			ClientSecret: randomString(24),
			Tokens:       []store.AppToken{},
		}); err != nil {
			return err
		}
	case !containsString(app.Scopes, "dashboard"):
		scopes := append(append([]string{}, app.Scopes...), "dashboard")
		if err := s.store.UpdateApp(ctx, app.ID, store.AppUpdate{Scopes: &scopes}); err != nil {
			return err
		}
	}
	return s.publish(ctx)
}

// RefreshApps republishes the keyv cache without touching the table.
func (s *OpenService) RefreshApps(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.publish(ctx)
}

// List implements GET /api/apps: the system app is hidden and token values are
// never returned.
func (s *OpenService) List(ctx context.Context) ([]AppResponse, error) {
	apps, err := s.store.ListApps(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]AppResponse, 0, len(apps))
	for _, app := range apps {
		if app.Name == "system" {
			continue
		}
		response := toAppResponse(app)
		response.Tokens = []store.AppToken{}
		out = append(out, response)
	}
	return out, nil
}

// Create implements POST /api/apps.
func (s *OpenService) Create(ctx context.Context, name string, scopes []string) (*AppResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if scopes == nil {
		scopes = []string{}
	}
	app, err := s.store.InsertApp(ctx, store.App{
		Name:         name,
		Scopes:       scopes,
		ClientID:     randomString(12),
		ClientSecret: randomString(24),
		Tokens:       []store.AppToken{},
	})
	if err != nil {
		return nil, err
	}
	if err := s.publish(ctx); err != nil {
		return nil, err
	}
	response := toAppResponse(*app)
	response.Tokens = []store.AppToken{}
	return &response, nil
}

// Update implements PUT /api/apps. The system app can never be renamed or
// edited, and no app may take the reserved name.
func (s *OpenService) Update(ctx context.Context, id int64, name *string, scopes *[]string) (*AppResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	target, err := s.store.AppByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if target == nil {
		return nil, &NotFoundError{Status: 500, Message: "App {\"id\":" + itoa(id) + "} not found"}
	}
	if target.Name == "system" || (name != nil && *name == "system") {
		return nil, &NotFoundError{Status: 403, Message: i18n.T("暂无权限")}
	}
	if err := s.store.UpdateApp(ctx, id, store.AppUpdate{Name: name, Scopes: scopes}); err != nil {
		return nil, err
	}
	if err := s.publish(ctx); err != nil {
		return nil, err
	}
	updated, err := s.store.AppByID(ctx, id)
	if err != nil {
		return nil, err
	}
	response := toAppResponse(*updated)
	response.Tokens = []store.AppToken{}
	return &response, nil
}

// Remove implements DELETE /api/apps (the body is an array of ids).
func (s *OpenService) Remove(ctx context.Context, ids []int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	system, err := s.store.AnyAppByName(ctx, ids, "system")
	if err != nil {
		return err
	}
	if system {
		return &NotFoundError{Status: 403, Message: i18n.T("暂无权限")}
	}
	if _, err := s.store.DeleteApps(ctx, ids); err != nil {
		return err
	}
	return s.publish(ctx)
}

// ResetSecret implements PUT /api/apps/:id/reset-secret: a new secret and an
// empty token list, so every previously issued token stops working.
func (s *OpenService) ResetSecret(ctx context.Context, id int64) (*AppResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	target, err := s.store.AppByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if target == nil {
		// Node's updateDb returns undefined for an unknown row and the handler
		// answers a bare {code:200}; nothing is created.
		return nil, nil
	}
	if target.Name == "system" {
		return nil, &NotFoundError{Status: 403, Message: i18n.T("暂无权限")}
	}
	secret := randomString(24)
	empty := []store.AppToken{}
	if err := s.store.UpdateApp(ctx, id, store.AppUpdate{ClientSecret: &secret, Tokens: &empty}); err != nil {
		return nil, err
	}
	if err := s.publish(ctx); err != nil {
		return nil, err
	}
	updated, err := s.store.AppByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return ptrAppResponse(*updated), nil
}

// AuthToken implements GET /api/auth/token: a 30 day bearer token, reusing the
// fifth slot when an app already holds five live tokens.
func (s *OpenService) AuthToken(ctx context.Context, clientID, clientSecret string) (*Reply, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	app, err := s.store.AppByCredentials(ctx, clientID, clientSecret)
	if err != nil {
		return nil, err
	}
	if app == nil {
		return &Reply{Code: 400, Message: i18n.T("client_id 或 client_seret 有误")}, nil
	}

	now := time.Now().Unix()
	expiration := now + appTokenTTL
	token := newUUID()

	live := make([]store.AppToken, 0, len(app.Tokens))
	for _, item := range app.Tokens {
		if item.Expiration >= now {
			live = append(live, item)
		}
	}
	if len(live) >= 5 {
		token = live[4].Value
		live[4].Expiration = expiration
	} else {
		live = append(live, store.AppToken{Value: token, Expiration: expiration})
	}
	if err := s.store.UpdateApp(ctx, app.ID, store.AppUpdate{Tokens: &live}); err != nil {
		return nil, err
	}
	if err := s.publish(ctx); err != nil {
		return nil, err
	}
	return &Reply{Code: 200, Data: map[string]any{
		"token":      token,
		"token_type": "Bearer",
		"expiration": expiration,
	}}, nil
}

// publish writes the whole app table back to keyv, which the HTTP middleware
// reads for every /open request.
func (s *OpenService) publish(ctx context.Context) error {
	apps, err := s.store.ListApps(ctx)
	if err != nil {
		return err
	}
	payload := make([]AppResponse, 0, len(apps))
	for _, app := range apps {
		payload = append(payload, toAppResponse(app))
	}
	if err := s.keyv.Set(ctx, keyv.KeyApps, payload); err != nil {
		s.logger.Error("[open] publish apps cache failed", "error", err)
		return err
	}
	return nil
}

func toAppResponse(app store.App) AppResponse {
	scopes := app.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	tokens := app.Tokens
	if tokens == nil {
		tokens = []store.AppToken{}
	}
	return AppResponse{
		ID:           app.ID,
		Name:         app.Name,
		Scopes:       scopes,
		ClientID:     app.ClientID,
		ClientSecret: app.ClientSecret,
		Tokens:       tokens,
		// Sequelize serialises Dates as ISO strings; the row stores
		// "YYYY-MM-DD HH:mm:ss.SSS +00:00" and the panel must not leak that form.
		CreatedAt: isoOrEmpty(app.CreatedAt),
		UpdatedAt: isoOrEmpty(app.UpdatedAt),
	}
}

func isoOrEmpty(raw string) string {
	if value := store.ISO8601(raw); value != nil {
		return *value
	}
	return ""
}

func ptrAppResponse(app store.App) *AppResponse {
	response := toAppResponse(app)
	return &response
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// randomString mirrors createRandomString: at least one digit, lowercase,
// uppercase and symbol, then random characters, shuffled.
func randomString(length int) string {
	const (
		digits  = "0123456789"
		lower   = "abcdefghijklmnopqrstuvwxyz"
		upper   = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
		special = "-_"
	)
	alphabet := digits + lower + upper + special
	if length < 4 {
		length = 4
	}
	out := make([]byte, 0, length)
	out = append(out, pick(digits), pick(lower), pick(upper), pick(special))
	for len(out) < length {
		out = append(out, pick(alphabet))
	}
	for i := len(out) - 1; i > 0; i-- {
		j := randomInt(i + 1)
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}

func pick(alphabet string) byte {
	return alphabet[randomInt(len(alphabet))]
}

func randomInt(max int) int {
	if max <= 0 {
		return 0
	}
	value, err := rand.Int(rand.Reader, big.NewInt(int64(max)))
	if err != nil {
		return 0
	}
	return int(value.Int64())
}

func itoa(value int64) string { return strconv.FormatInt(value, 10) }
