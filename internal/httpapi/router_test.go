package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"qinglong-go/internal/config"
	"qinglong-go/internal/keyv"
	"qinglong-go/internal/platform"
	"qinglong-go/internal/service"
	"qinglong-go/internal/store"
)

func newTestHandler(t *testing.T) http.Handler {
	t.Helper()
	return newTestHandlerWithConfig(t, &config.Config{
		RootPath:    `C:\qinglong`,
		UploadPath:  t.TempDir(),
		CORSOrigins: []string{"*"},
	})
}

// testEnv keeps the handler and its stores together so tests can inspect what
// was persisted.
type testEnv struct {
	handler http.Handler
	kv      *keyv.Store
	st      *store.Store
	cfg     *config.Config
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	return newTestEnvWithConfig(t, &config.Config{
		RootPath:    `C:\qinglong`,
		UploadPath:  t.TempDir(),
		CORSOrigins: []string{"*"},
	})
}

func newTestHandlerWithConfig(t *testing.T, cfg *config.Config) http.Handler {
	t.Helper()
	return newTestEnvWithConfig(t, cfg).handler
}

func newTestEnvWithConfig(t *testing.T, cfg *config.Config) *testEnv {
	t.Helper()

	if cfg.RootPath == "" {
		cfg.RootPath = `C:\qinglong`
	}
	if cfg.UploadPath == "" {
		cfg.UploadPath = t.TempDir()
	}
	if len(cfg.CORSOrigins) == 0 {
		cfg.CORSOrigins = []string{"*"}
	}
	if cfg.JWTSecret == "" {
		cfg.JWTSecret = "whyour-secret"
	}
	if cfg.MaxTokensPerPlatform == 0 {
		cfg.MaxTokensPerPlatform = 10
	}

	web := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<!doctype html><div id=\"root\"></div>")},
		"umi.js":     &fstest.MapFile{Data: []byte("console.log('bundle')")},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	dir := t.TempDir()
	cfg.DataPath = dir
	cfg.DBPath = filepath.Join(dir, "db")
	cfg.ConfigPath = filepath.Join(dir, "config")
	cfg.KeyvPath = filepath.Join(cfg.DBPath, "keyv.sqlite")
	if err := os.MkdirAll(cfg.DBPath, 0o755); err != nil {
		t.Fatalf("create db dir: %v", err)
	}

	st, err := store.Open(filepath.Join(cfg.DBPath, "database.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	kv, err := keyv.Open(cfg.KeyvPath)
	if err != nil {
		t.Fatalf("open keyv: %v", err)
	}
	t.Cleanup(func() { kv.Close() })

	users := service.NewUserService(cfg, st, kv, logger)
	if err := users.BootstrapAuthInfo(context.Background()); err != nil {
		t.Fatalf("bootstrap auth: %v", err)
	}

	handler := New(Deps{
		Config:    cfg,
		Store:     st,
		Keyv:      kv,
		User:      users,
		Logger:    logger,
		Web:       web,
		StartedAt: time.Now(),
		Build:     platform.BuildInfo{InstanceID: "test-instance", ParentPid: 1, Version: "2.24.0"},
	})
	return &testEnv{handler: handler, kv: kv, st: st, cfg: cfg}
}

// readAuthInfoRaw returns the decoded keyv envelope for authInfo, i.e. the
// {"value":...,"expires":null} document the Node panel reads.
func readAuthInfoRaw(t *testing.T, kv *keyv.Store) map[string]any {
	t.Helper()
	raw, err := kv.RawEntry(context.Background(), keyv.KeyAuthInfo)
	if err != nil {
		t.Fatalf("read authInfo: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("decode authInfo: %v", err)
	}
	if _, ok := doc["expires"]; !ok {
		t.Errorf("keyv envelope must contain an expires field: %s", raw)
	}
	return doc
}

func doRequest(handler http.Handler, method, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestHealthEndpoint(t *testing.T) {
	rec := doRequest(newTestHandler(t), http.MethodGet, "/api/health")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{`"code":200`, `"status":"ok"`, `"http":true`, `"instanceId":"test-instance"`} {
		if !strings.Contains(body, want) {
			t.Errorf("body %s missing %s", body, want)
		}
	}
}

func TestOpenAliasReachesAPI(t *testing.T) {
	// /open/health is not in the whitelist (only /api/health and /open/system
	// are), so the alias requires credentials before it can reach the handler.
	// Verified against the Node panel: GET /open/health -> 401 请先登录.
	rec := doRequest(newTestHandler(t), http.MethodGet, "/open/health")
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "请先登录") {
		t.Fatalf("/open/health = %d %s, want 401 请先登录", rec.Code, rec.Body.String())
	}

	handler := newTestHandler(t)
	initAdmin(t, handler)
	token := login(t, handler, "tester", "test1234")
	rec = apiCall(t, handler, http.MethodGet, "/open/health", "", bearer(token))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Fatalf("/open/health with token = %d %s", rec.Code, rec.Body.String())
	}
}

func TestStaticAndSPAFallback(t *testing.T) {
	handler := newTestHandler(t)

	cases := []struct {
		target      string
		status      int
		contentType string
		contains    string
	}{
		{"/", http.StatusOK, "text/html", "id=\"root\""},
		{"/cron", http.StatusOK, "text/html", "id=\"root\""},
		{"/umi.js", http.StatusOK, "application/javascript", "bundle"},
		{"/missing.js", http.StatusNotFound, "text/plain", "Not Found"},
	}
	for _, tc := range cases {
		rec := doRequest(handler, http.MethodGet, tc.target)
		if rec.Code != tc.status {
			t.Errorf("%s status = %d, want %d", tc.target, rec.Code, tc.status)
			continue
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, tc.contentType) {
			t.Errorf("%s content-type = %q, want %q", tc.target, ct, tc.contentType)
		}
		if !strings.Contains(rec.Body.String(), tc.contains) {
			t.Errorf("%s body = %q, want substring %q", tc.target, rec.Body.String(), tc.contains)
		}
	}
}

func TestAPI404AndCaseStrictness(t *testing.T) {
	handler := newTestHandler(t)

	// Auth runs before routing (express.ts:79 before the router), so an unknown
	// /api path without a token answers 401, not 404.
	rec := doRequest(handler, http.MethodGet, "/api/unknown")
	if rec.Code != http.StatusUnauthorized ||
		!strings.Contains(rec.Body.String(), "No authorization token was found") {
		t.Errorf("/api/unknown = %d %s, want 401 missing token", rec.Code, rec.Body.String())
	}

	rec = doRequest(handler, http.MethodGet, "/api/health/")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("/api/health/ = %d, want 401 (auth before strict routing)", rec.Code)
	}

	rec = doRequest(handler, http.MethodGet, "/API/health")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Invalid path format") {
		t.Errorf("/API/health = %d %s, want 400 Invalid path format", rec.Code, rec.Body.String())
	}
}

func TestCORSAndEnvJS(t *testing.T) {
	handler := newTestHandler(t)

	rec := doRequest(handler, http.MethodOptions, "/api/health")
	if rec.Code != http.StatusNoContent {
		t.Errorf("preflight status = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("allow-origin = %q, want *", got)
	}

	rec = doRequest(handler, http.MethodGet, "/api/env.js")
	if rec.Code != http.StatusOK {
		t.Fatalf("env.js status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("env.js content-type = %q", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `window.__ENV__QL_DIR="C:\\qinglong";`) {
		t.Errorf("env.js body = %q", body)
	}
	// pick() drops absent keys: no empty QlBaseUrl/DeployEnv assignment may be
	// emitted, otherwise the response is not byte-comparable with Node.
	if strings.Contains(body, "__ENV__QlBaseUrl") {
		t.Errorf("env.js must omit empty QlBaseUrl: %q", body)
	}
	if strings.Contains(body, "__ENV__DeployEnv") {
		t.Errorf("env.js must omit absent DeployEnv: %q", body)
	}
	if strings.HasSuffix(body, "\n") {
		t.Errorf("env.js must not end with a newline: %q", body)
	}
}

func TestEnvJSWithBaseURL(t *testing.T) {
	handler := newTestHandlerWithConfig(t, &config.Config{
		BaseURL:  "/ql",
		RootPath: `C:\ql`,
	})

	rec := doRequest(handler, http.MethodGet, "/ql/api/env.js")
	if rec.Code != http.StatusOK {
		t.Fatalf("env.js status = %d, want 200", rec.Code)
	}

	want := "window.__ENV__QlBaseUrl=\"/ql/\";\n" + `window.__ENV__QL_DIR="C:\\ql";`
	if got := rec.Body.String(); got != want {
		t.Errorf("env.js body = %q, want %q", got, want)
	}
}
