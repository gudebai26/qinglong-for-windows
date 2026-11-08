package httpapi

import (
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"qinglong-go/internal/config"
	"qinglong-go/internal/keyv"
	"qinglong-go/internal/platform"
	"qinglong-go/internal/service"
	"qinglong-go/internal/store"
)

// Deps groups everything the HTTP layer needs. Business handlers added in
// later milestones receive the same struct.
type Deps struct {
	Config  *config.Config
	Store   *store.Store
	Keyv    *keyv.Store
	User    *service.UserService
	Cron    *service.CronService
	Scripts *service.ScriptService
	Envs    *service.EnvService
	// Subscriptions and Dependences own the M7 pages.
	Subscriptions *service.SubscriptionService
	Dependences   *service.DependenceService
	// Open owns the app tokens and the /open scope checks.
	Open *service.OpenService
	// System owns the settings, retention and upgrade endpoints.
	System *service.SystemService
	// Notify owns the notification channels and the settings-page test send.
	Notify *service.NotifyService
	// Dashboard owns the ten aggregation endpoints.
	Dashboard *service.DashboardService
	// Sock fans task events out to the browser.
	Sock      *service.SockService
	Logger    *slog.Logger
	Web       fs.FS
	StartedAt time.Time
	Build     platform.BuildInfo
}

// New builds the full router: middleware chain, API surface implemented so far,
// static uploads, embedded front-end and the SPA fallback.
//
// The middleware chain is assembled by hand instead of with chi.Use because the
// auth stage has to sit between the public endpoints (env.js, /api/static) and
// the API routes, and chi only allows Use() before the first route.
func New(deps Deps) http.Handler {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	if deps.StartedAt.IsZero() {
		deps.StartedAt = time.Now()
	}

	mux := chi.NewRouter()

	// Public endpoints from the API inventory. They are mounted before the auth
	// chain on purpose: Node mounts env.js and the upload directory before
	// expressjwt (express.ts:51-60), so they never require a token.
	health := healthHandler(deps)
	envJS := envJSHandler(deps.Config)
	mux.Get("/api/health", health)
	mux.Head("/api/health", health)
	mux.Get("/api/env.js", envJS)
	mux.Head("/api/env.js", envJS)
	mux.Handle("/api/static/*", uploadsHandler(deps.Config.UploadPath))

	registerUserRoutes(mux, deps)
	registerCronRoutes(mux, deps)
	registerLogRoutes(mux, deps)
	registerScriptRoutes(mux, deps)
	registerEnvRoutes(mux, deps)
	registerSubscriptionRoutes(mux, deps)
	registerDependenceRoutes(mux, deps)
	registerOpenRoutes(mux, deps)
	registerSystemSettingRoutes(mux, deps)
	registerDashboardRoutes(mux, deps)
	registerRetentionRoutes(mux, deps)
	registerClientIPRoutes(mux, deps)
	registerConfigRoutes(mux, deps)
	registerUpdateRoutes(mux, deps)

	// Everything else is either the embedded front-end or a 404 for /api.
	static := staticHandler(deps)
	mux.Handle("/", static)
	mux.Handle("/*", static)

	// Chain order mirrors back/loaders/express.ts:
	// cors -> protectedPathCase -> baseUrl -> body limits -> jwt -> init guard
	// -> /open alias -> routes.
	var handler http.Handler = mux
	handler = openAliasRewrite(handler)
	handler = initGuard(deps)(handler)
	handler = authenticate(deps)(handler)
	handler = bodyLimit(handler)
	handler = stripQueryParamT(handler)
	handler = baseURLRewrite(deps.Config.BaseURL)(handler)
	handler = protectedPathCase(handler)
	handler = cors(deps.Config)(handler)
	// The websocket endpoint authenticates with a query token and must be
	// reached without the JWT middleware, exactly like Node installs sockjs on
	// the raw HTTP server before express.
	handler = wsHandler(deps)(handler)
	// gzip sits inside the websocket interception and outside the routes: every
	// API/static response is eligible, but /api/ws and the streaming/binary
	// endpoints never see it.
	handler = compress(handler)
	handler = requestLogger(deps.Logger)(handler)
	handler = recoverer(deps.Logger)(handler)

	return handler
}
