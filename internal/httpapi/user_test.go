package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"qinglong-go/internal/auth"
	"qinglong-go/internal/store"
)

// apiCall performs a JSON request and returns the recorder.
func apiCall(t *testing.T, handler http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.RemoteAddr = "127.0.0.1:54321"
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func decodeJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return payload
}

func bearer(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

// login logs in and returns the token.
func login(t *testing.T, handler http.Handler, username, password string) string {
	t.Helper()
	rec := apiCall(t, handler, http.MethodPost, "/api/user/login",
		`{"username":"`+username+`","password":"`+password+`"}`, nil)
	body := decodeJSON(t, rec)
	data, _ := body["data"].(map[string]any)
	token, _ := data["token"].(string)
	if token == "" {
		t.Fatalf("login failed: %d %s", rec.Code, rec.Body.String())
	}
	return token
}

func initAdmin(t *testing.T, handler http.Handler) {
	t.Helper()
	rec := apiCall(t, handler, http.MethodPut, "/api/user/init",
		`{"username":"tester","password":"test1234"}`, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"code":200`) {
		t.Fatalf("init failed: %d %s", rec.Code, rec.Body.String())
	}
}

func TestInitAndLoginFlow(t *testing.T) {
	handler := newTestHandler(t)

	rec := apiCall(t, handler, http.MethodPut, "/api/user/init",
		`{"username":"tester","password":"admin"}`, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"code":400`) ||
		!strings.Contains(rec.Body.String(), "密码不能设置为admin") {
		t.Fatalf("admin password = %d %s", rec.Code, rec.Body.String())
	}

	// Missing field: celebrate shape with HTTP 400, not the panel envelope.
	rec = apiCall(t, handler, http.MethodPut, "/api/user/init", `{"username":"tester"}`, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("validation status = %d", rec.Code)
	}
	body := decodeJSON(t, rec)
	if body["statusCode"] != float64(400) || body["error"] != "Bad Request" ||
		body["message"] != "Validation failed" {
		t.Errorf("validation body = %s", rec.Body.String())
	}
	validation, _ := body["validation"].(map[string]any)
	detail, _ := validation["body"].(map[string]any)
	if detail["source"] != "body" {
		t.Errorf("validation source = %v", detail["source"])
	}
	if detail["message"] != `"password" is required` {
		t.Errorf("validation message = %v", detail["message"])
	}

	initAdmin(t, handler)

	rec = apiCall(t, handler, http.MethodPut, "/api/user/init",
		`{"username":"tester","password":"test1234"}`, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"code":450`) {
		t.Fatalf("second init = %d %s", rec.Code, rec.Body.String())
	}

	rec = apiCall(t, handler, http.MethodPost, "/api/user/login",
		`{"username":"tester","password":"wrong"}`, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"code":400`) ||
		!strings.Contains(rec.Body.String(), "错误的用户名密码") {
		t.Fatalf("wrong password = %d %s", rec.Code, rec.Body.String())
	}

	token := login(t, handler, "tester", "test1234")
	claims, err := auth.Verify("whyour-secret", token)
	if err != nil {
		t.Fatalf("token must verify: %v", err)
	}
	if delta := claims.Exp - claims.Iat; delta != 20*24*3600 {
		t.Errorf("token ttl = %d, want 20 days", delta)
	}

	rec = apiCall(t, handler, http.MethodGet, "/api/user", "", bearer(token))
	if rec.Code != http.StatusOK || rec.Body.String() != `{"code":200,"data":{"username":"tester"}}` {
		t.Fatalf("GET /api/user = %d %s", rec.Code, rec.Body.String())
	}

	// Missing token uses express-jwt's message on /api, per the Node contract.
	rec = apiCall(t, handler, http.MethodGet, "/api/user", "", nil)
	if rec.Code != http.StatusUnauthorized ||
		rec.Body.String() != `{"code":401,"message":"No authorization token was found"}` {
		t.Fatalf("missing token = %d %s", rec.Code, rec.Body.String())
	}

	rec = apiCall(t, handler, http.MethodGet, "/api/user", "", bearer("abc.def.ghi"))
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "invalid token") {
		t.Fatalf("garbage token = %d %s", rec.Code, rec.Body.String())
	}

	rec = apiCall(t, handler, http.MethodPost, "/api/user/logout", "", bearer(token))
	if rec.Code != http.StatusOK || rec.Body.String() != `{"code":200}` {
		t.Fatalf("logout = %d %s", rec.Code, rec.Body.String())
	}

	rec = apiCall(t, handler, http.MethodGet, "/api/user", "", bearer(token))
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "Token 已失效") {
		t.Fatalf("revoked token = %d %s", rec.Code, rec.Body.String())
	}

	// A revoked token cannot be used to log in again either.
	rec = apiCall(t, handler, http.MethodPost, "/api/user/login",
		`{"username":"tester","password":"test1234"}`, bearer(token))
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "Token 已失效") {
		t.Fatalf("login with stale token = %d %s", rec.Code, rec.Body.String())
	}
}

func TestPasswordChangeRevokesSessions(t *testing.T) {
	handler := newTestHandler(t)
	initAdmin(t, handler)
	token := login(t, handler, "tester", "test1234")

	rec := apiCall(t, handler, http.MethodPut, "/api/user",
		`{"username":"tester","password":"admin"}`, bearer(token))
	if !strings.Contains(rec.Body.String(), `"code":400`) {
		t.Fatalf("admin password = %s", rec.Body.String())
	}

	rec = apiCall(t, handler, http.MethodPut, "/api/user",
		`{"username":"tester","password":"newpass1"}`, bearer(token))
	if rec.Code != http.StatusOK || rec.Body.String() != `{"code":200,"message":"更新成功"}` {
		t.Fatalf("update = %d %s", rec.Code, rec.Body.String())
	}

	rec = apiCall(t, handler, http.MethodGet, "/api/user", "", bearer(token))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("old token after password change = %d %s", rec.Code, rec.Body.String())
	}
	login(t, handler, "tester", "newpass1")
}

func TestTokenListFormatAndCap(t *testing.T) {
	env := newTestEnv(t)
	handler := env.handler
	initAdmin(t, handler)

	last := ""
	for i := 0; i < 12; i++ {
		last = login(t, handler, "tester", "test1234")
	}

	value := readAuthInfoRaw(t, env.kv)
	doc := value["value"].(map[string]any)
	if doc["token"] != last {
		t.Errorf("authInfo.token = %v, want the newest token", doc["token"])
	}
	tokens, _ := doc["tokens"].(map[string]any)
	desktop, _ := tokens["desktop"].([]any)
	if len(desktop) != 10 {
		t.Fatalf("desktop tokens = %d, want 10", len(desktop))
	}
	first, _ := desktop[0].(map[string]any)
	if first["value"] != last {
		t.Errorf("newest token must be unshifted to index 0")
	}
	for _, key := range []string{"value", "timestamp", "ip", "address", "platform"} {
		if _, ok := first[key]; !ok {
			t.Errorf("token entry missing %q: %v", key, first)
		}
	}
	if first["platform"] != "desktop" || first["ip"] != "127.0.0.1" {
		t.Errorf("token entry = %v", first)
	}
	if _, ok := first["expiration"]; ok {
		t.Errorf("expiration must stay absent for panel tokens")
	}
}

func TestMobilePlatformTokens(t *testing.T) {
	env := newTestEnv(t)
	handler := env.handler
	initAdmin(t, handler)

	rec := apiCall(t, handler, http.MethodPost, "/api/user/login",
		`{"username":"tester","password":"test1234"}`,
		map[string]string{"User-Agent": "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) Mobile/15E148"})
	body := decodeJSON(t, rec)
	data, _ := body["data"].(map[string]any)
	token, _ := data["token"].(string)
	if token == "" {
		t.Fatalf("mobile login = %s", rec.Body.String())
	}

	doc := readAuthInfoRaw(t, env.kv)["value"].(map[string]any)
	tokens, _ := doc["tokens"].(map[string]any)
	mobile, _ := tokens["mobile"].([]any)
	if len(mobile) != 1 {
		t.Fatalf("mobile tokens = %d", len(mobile))
	}
	entry, _ := mobile[0].(map[string]any)
	if entry["platform"] != "mobile" {
		t.Errorf("platform = %v", entry["platform"])
	}

	// The desktop session list must stay untouched.
	rec = apiCall(t, handler, http.MethodGet, "/api/user", "", bearer(token))
	if rec.Code != http.StatusOK {
		t.Fatalf("mobile token rejected: %d %s", rec.Code, rec.Body.String())
	}
}

func TestTwoFactorFlow(t *testing.T) {
	handler := newTestHandler(t)
	initAdmin(t, handler)
	token := login(t, handler, "tester", "test1234")

	rec := apiCall(t, handler, http.MethodGet, "/api/user/two-factor/init", "", bearer(token))
	body := decodeJSON(t, rec)
	data, _ := body["data"].(map[string]any)
	secret, _ := data["secret"].(string)
	url, _ := data["url"].(string)
	if secret == "" || len(secret) != 16 {
		t.Fatalf("secret = %q", secret)
	}
	wantURL := "otpauth://totp/qinglong:tester?secret=" + secret +
		"&period=30&digits=6&algorithm=SHA1&issuer=qinglong"
	if url != wantURL {
		t.Errorf("url = %q, want %q", url, wantURL)
	}

	code, err := auth.TOTPCodeAt(secret, time.Now())
	if err != nil {
		t.Fatalf("totp: %v", err)
	}
	rec = apiCall(t, handler, http.MethodPut, "/api/user/two-factor/active",
		`{"code":"`+code+`"}`, bearer(token))
	if rec.Body.String() != `{"code":200,"data":true}` {
		t.Fatalf("activate = %s", rec.Body.String())
	}

	// Activation revokes every session.
	rec = apiCall(t, handler, http.MethodGet, "/api/user", "", bearer(token))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("token after activation = %d", rec.Code)
	}

	rec = apiCall(t, handler, http.MethodPost, "/api/user/login",
		`{"username":"tester","password":"test1234"}`, nil)
	if rec.Body.String() != `{"code":420,"message":""}` {
		t.Fatalf("2FA challenge = %s", rec.Body.String())
	}

	code, _ = auth.TOTPCodeAt(secret, time.Now())
	rec = apiCall(t, handler, http.MethodPut, "/api/user/two-factor/login",
		`{"username":"tester","password":"test1234","code":"`+code+`"}`, nil)
	body = decodeJSON(t, rec)
	data, _ = body["data"].(map[string]any)
	twoFactorToken, _ := data["token"].(string)
	if twoFactorToken == "" {
		t.Fatalf("2FA login = %s", rec.Body.String())
	}
	claims, err := auth.Verify("whyour-secret", twoFactorToken)
	if err != nil {
		t.Fatalf("2FA token: %v", err)
	}
	if delta := claims.Exp - claims.Iat; delta != 60*24*3600 {
		t.Errorf("2FA token ttl = %d, want 60 days", delta)
	}

	// A second challenge is required before another 2FA login.
	rec = apiCall(t, handler, http.MethodPut, "/api/user/two-factor/login",
		`{"username":"tester","password":"test1234","code":"`+code+`"}`, nil)
	if !strings.Contains(rec.Body.String(), `"code":450`) {
		t.Fatalf("2FA replay = %s", rec.Body.String())
	}

	rec = apiCall(t, handler, http.MethodPut, "/api/user/two-factor/deactivate", "", bearer(twoFactorToken))
	if rec.Body.String() != `{"code":200,"data":true}` {
		t.Fatalf("deactivate = %s", rec.Body.String())
	}
}

func TestLoginThrottle(t *testing.T) {
	handler := newTestHandler(t)
	initAdmin(t, handler)

	for i := 0; i < 3; i++ {
		rec := apiCall(t, handler, http.MethodPost, "/api/user/login",
			`{"username":"tester","password":"nope"}`, nil)
		if !strings.Contains(rec.Body.String(), `"code":400`) {
			t.Fatalf("attempt %d = %s", i+1, rec.Body.String())
		}
	}
	rec := apiCall(t, handler, http.MethodPost, "/api/user/login",
		`{"username":"tester","password":"test1234"}`, nil)
	body := decodeJSON(t, rec)
	if body["code"] != float64(410) {
		t.Fatalf("throttled = %s", rec.Body.String())
	}
	if _, ok := body["data"].(float64); !ok {
		t.Errorf("410 must carry the remaining seconds in data: %s", rec.Body.String())
	}
	if !strings.Contains(body["message"].(string), "失败次数过多") {
		t.Errorf("message = %v", body["message"])
	}
}

func TestIPBlacklist(t *testing.T) {
	handler := newTestHandler(t)
	initAdmin(t, handler)
	token := login(t, handler, "tester", "test1234")

	rec := apiCall(t, handler, http.MethodPut, "/api/user/ip-blacklist", `{"ip":"not-an-ip"}`, bearer(token))
	if rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "must be a valid ip address") {
		t.Fatalf("invalid ip = %d %s", rec.Code, rec.Body.String())
	}

	rec = apiCall(t, handler, http.MethodPut, "/api/user/ip-blacklist", `{"ip":"10.1.2.3"}`, bearer(token))
	if rec.Body.String() != `{"code":200,"data":["10.1.2.3"],"message":"已加入 IP 黑名单"}` {
		t.Fatalf("block = %s", rec.Body.String())
	}

	rec = apiCall(t, handler, http.MethodGet, "/api/user/ip-blacklist", "", bearer(token))
	if rec.Body.String() != `{"code":200,"data":["10.1.2.3"]}` {
		t.Fatalf("list = %s", rec.Body.String())
	}

	rec = apiCall(t, handler, http.MethodDelete, "/api/user/ip-blacklist", `{"ip":"10.1.2.3"}`, bearer(token))
	if rec.Body.String() != `{"code":200,"data":[],"message":"已移出 IP 黑名单"}` {
		t.Fatalf("unblock = %s", rec.Body.String())
	}

	// A blocked address cannot log in, and the attempt is logged.
	apiCall(t, handler, http.MethodPut, "/api/user/ip-blacklist", `{"ip":"127.0.0.1"}`, bearer(token))
	rec = apiCall(t, handler, http.MethodPost, "/api/user/login",
		`{"username":"tester","password":"test1234"}`, nil)
	if !strings.Contains(rec.Body.String(), `"code":403`) {
		t.Fatalf("blocked login = %s", rec.Body.String())
	}
}

func TestOpenNamespace(t *testing.T) {
	handler := newTestHandler(t)

	rec := apiCall(t, handler, http.MethodGet, "/open/user", "", nil)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "请先登录") {
		t.Fatalf("/open/user = %d %s", rec.Code, rec.Body.String())
	}

	// Whitelisted alias reaches the same handler as /api/user/login.
	rec = apiCall(t, handler, http.MethodPost, "/open/user/login",
		`{"username":"admin","password":"admin"}`, nil)
	if !strings.Contains(rec.Body.String(), `"code":450`) {
		t.Fatalf("/open/user/login = %s", rec.Body.String())
	}

	// /api/system is whitelisted by path, for every method.
	rec = apiCall(t, handler, http.MethodPost, "/api/system", `{}`, nil)
	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("/api/system must not require a token: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAvatarUpload(t *testing.T) {
	handler := newTestHandler(t)
	initAdmin(t, handler)
	token := login(t, handler, "tester", "test1234")

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	header := make(map[string][]string)
	header["Content-Disposition"] = []string{`form-data; name="avatar"; filename="avatar.png"`}
	header["Content-Type"] = []string{"image/png"}
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatalf("form: %v", err)
	}
	// Minimal PNG signature; only the extension/mime pair is validated.
	part.Write([]byte("\x89PNG\r\n\x1a\n"))
	writer.Close()

	req := httptest.NewRequest(http.MethodPut, "/api/user/avatar", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	payload := decodeJSON(t, rec)
	data, _ := payload["data"].(string)
	if !strings.HasSuffix(data, ".png") || len(data) < 37 {
		t.Fatalf("avatar response = %s", rec.Body.String())
	}
	if payload["message"] != "更新成功" {
		t.Errorf("message = %v", payload["message"])
	}

	rec = apiCall(t, handler, http.MethodGet, "/api/user", "", bearer(token))
	if !strings.Contains(rec.Body.String(), `"avatar":"`+data+`"`) {
		t.Errorf("GET /api/user = %s", rec.Body.String())
	}
}

// TestLoginUnaffectedByNotificationFailure pins the fire-and-forget contract of
// back/services/user.ts:171: a broken notification channel must not change the
// login response, but the audit row still has to be written eventually.
func TestLoginUnaffectedByNotificationFailure(t *testing.T) {
	env := newTestEnv(t)
	handler := env.handler
	initAdmin(t, handler)

	// Simulate a channel the user saved earlier that no longer works.
	broken := []byte(`{"type":"email","smtpHost":"127.0.0.1","smtpPort":"1"}`)
	if err := env.st.UpsertAuthRow(context.Background(), store.AuthTypeNotification, broken); err != nil {
		t.Fatalf("seed notification config: %v", err)
	}

	start := time.Now()
	token := login(t, handler, "tester", "test1234")
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("login waited %s on the notification path", elapsed)
	}
	rec := apiCall(t, handler, http.MethodGet, "/api/user", "", bearer(token))
	if rec.Code != http.StatusOK {
		t.Fatalf("login result changed by notification failure: %d %s", rec.Code, rec.Body.String())
	}

	// The audit row is written asynchronously; poll briefly instead of sleeping.
	deadline := time.Now().Add(2 * time.Second)
	for {
		rows, err := env.st.AuthRowsByType(context.Background(), store.AuthTypeNotifyLog)
		if err != nil {
			t.Fatalf("read notify log: %v", err)
		}
		if len(rows) > 0 {
			var info map[string]any
			if err := json.Unmarshal(rows[len(rows)-1].Info, &info); err != nil {
				t.Fatalf("decode notify log: %v", err)
			}
			if info["notifyType"] != "email" {
				t.Errorf("notifyType = %v, want email", info["notifyType"])
			}
			if info["status"] != float64(1) {
				t.Errorf("status = %v, want 1 (failed attempt)", info["status"])
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("notification audit row was never written")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
