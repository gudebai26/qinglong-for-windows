package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"qinglong-go/internal/auth"
	"qinglong-go/internal/config"
	"qinglong-go/internal/i18n"
	"qinglong-go/internal/keyv"
)

type contextKey string

const platformContextKey contextKey = "platform"

// platformFrom returns the UA-derived platform stored by the auth middleware.
func platformFrom(ctx context.Context) string {
	if value, ok := ctx.Value(platformContextKey).(string); ok {
		return value
	}
	return "desktop"
}

// authenticate implements back/loaders/express.ts:79-190 in the same order:
//
//	express-jwt (only /api paths outside the whitelist)
//	→ /open application tokens
//	→ whitelist bypass when no token is sent
//	→ "JWT signature + token present in authInfo" for everything else
//
// The middleware runs before routing on purpose: Node answers 401 for unknown
// /api paths instead of 404, and the frontend relies on that to detect an
// expired session.
func authenticate(deps Deps) func(http.Handler) http.Handler {
	whitelist := make(map[string]struct{}, len(config.APIWhiteList))
	for _, path := range config.APIWhiteList {
		whitelist[path] = struct{}{}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			platform := auth.Platform(r.UserAgent())
			r = r.WithContext(context.WithValue(r.Context(), platformContextKey, platform))

			lower := strings.ToLower(r.URL.Path)
			// "/api" and "/open" without a trailing slash are not protected:
			// express-jwt's unless regex only exempts paths that do not start
			// with "api/", and the custom middleware only looks at "/api/" and
			// "/open/" prefixes.
			isAPI := strings.HasPrefix(lower, "/api/")
			isOpen := strings.HasPrefix(lower, "/open/")
			if !isAPI && !isOpen {
				next.ServeHTTP(w, r)
				return
			}
			// Mounted before expressjwt in Node: the runtime env script and the
			// upload directory are public by design.
			if lower == "/api/env.js" || strings.HasPrefix(lower, "/api/static/") {
				next.ServeHTTP(w, r)
				return
			}

			header := r.Header.Get("Authorization")
			token := auth.TokenFromHeader(header)

			if isOpen && token != "" {
				allowed, authErr := openAppAllowed(r.Context(), deps, token, lower, r.Method)
				if authErr != nil {
					writeError(w, http.StatusUnauthorized, authErr.Error())
					return
				}
				if allowed {
					next.ServeHTTP(w, r)
					return
				}
			}

			_, whitelisted := whitelist[lower]
			if isAPI && !whitelisted {
				if _, err := auth.ParseAuthorization(header); err != nil {
					if errors.Is(err, auth.ErrNoToken) {
						writeError(w, http.StatusUnauthorized, "No authorization token was found")
					} else {
						writeError(w, http.StatusUnauthorized, "Format is Authorization: Bearer [token]")
					}
					return
				}
				claims, err := auth.Verify(deps.Config.JWTSecret, token)
				if err != nil {
					writeError(w, http.StatusUnauthorized, err.Error())
					return
				}
				info, err := deps.User.AuthInfo(r.Context())
				if err != nil {
					deps.Logger.Error("read authInfo failed", "error", err)
					writeError(w, http.StatusInternalServerError, "Internal Server Error")
					return
				}
				if !claims.HasExp || !info.HasToken(platform, token, time.Now().UnixMilli()) {
					writeError(w, http.StatusUnauthorized, i18n.T("Token 已失效"))
					return
				}
				next.ServeHTTP(w, r)
				return
			}

			if token == "" {
				if whitelisted {
					next.ServeHTTP(w, r)
					return
				}
				// Only /open reaches this point with an empty token (express-jwt
				// already rejected /api), and Node answers 请先登录 there.
				writeError(w, http.StatusUnauthorized, i18n.T("请先登录"))
				return
			}
			info, err := deps.User.AuthInfo(r.Context())
			if err != nil {
				deps.Logger.Error("read authInfo failed", "error", err)
				writeError(w, http.StatusInternalServerError, "Internal Server Error")
				return
			}
			if !validStoredToken(info, token, platform, deps.Config.JWTSecret) {
				writeError(w, http.StatusUnauthorized, i18n.T("Token 已失效"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// validStoredToken mirrors isValidToken(): the JWT must verify and the token
// must still be present in authInfo for this platform.
func validStoredToken(info *auth.AuthInfo, token, platform, secret string) bool {
	if info == nil || token == "" {
		return false
	}
	claims, err := auth.Verify(secret, token)
	if err != nil || !claims.HasExp {
		return false
	}
	return info.HasToken(platform, token, time.Now().UnixMilli())
}

// initGuard mirrors the /user/init gate at express.ts:192-213.
func initGuard(deps Deps) func(http.Handler) http.Handler {
	guarded := map[string]struct{}{
		"/api/user/init":               {},
		"/api/user/notification/init":  {},
		"/open/user/init":              {},
		"/open/user/notification/init": {},
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := guarded[strings.ToLower(r.URL.Path)]; !ok {
				next.ServeHTTP(w, r)
				return
			}
			info, err := deps.User.AuthInfo(r.Context())
			if err != nil {
				deps.Logger.Error("read authInfo failed", "error", err)
				writeError(w, http.StatusInternalServerError, "Internal Server Error")
				return
			}
			if info != nil && !info.IsDefault() {
				writeJSON(w, http.StatusOK, envelope{Code: 450, Message: i18n.T("未知错误")})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// appDoc is the subset of an /open application document the gate needs.
type appDoc struct {
	Name   string     `json:"name"`
	Scopes []string   `json:"scopes"`
	Tokens []appToken `json:"tokens"`
}

type appToken struct {
	Value      string `json:"value"`
	Expiration int64  `json:"expiration"`
}

func (a appDoc) token(value string) (appToken, bool) {
	for _, candidate := range a.Tokens {
		if candidate.Value == value {
			return candidate, true
		}
	}
	return appToken{}, false
}

func (a appDoc) hasScope(scope string) bool {
	for _, candidate := range a.Scopes {
		if candidate == scope {
			return true
		}
	}
	return false
}

var readScopeNamespaces = map[string]struct{}{
	"crons":         {},
	"subscriptions": {},
	"envs":          {},
	"configs":       {},
	"scripts":       {},
	"logs":          {},
	"dependencies":  {},
	"dashboard":     {},
}

// openAppAllowed implements the application-token branch of express.ts:105-170.
// The returned error is a ready-to-send 401 message.
func openAppAllowed(
	ctx context.Context,
	deps Deps,
	token, lowerPath, method string,
) (bool, error) {
	raw, err := deps.Keyv.Get(ctx, keyv.KeyApps)
	if err != nil {
		return false, nil
	}
	var apps []appDoc
	if err := json.Unmarshal(raw, &apps); err != nil {
		return false, nil
	}

	key := openNamespace(lowerPath)
	for _, app := range apps {
		current, found := app.token(token)
		if !found {
			continue
		}
		isReadRequest := (method == http.MethodGet || method == http.MethodHead) &&
			!(key == "crons" && isCronsImport(lowerPath))
		if (key == "scripts" || key == "logs") && method == http.MethodPost && isDownload(lowerPath) {
			isReadRequest = true
		}

		_, readNamespace := readScopeNamespaces[key]
		hasNamespaceScope := app.hasScope(key) ||
			(readNamespace && app.hasScope(key+":read") && isReadRequest)
		if !hasNamespaceScope {
			return false, errors.New(i18n.T("暂无权限"))
		}

		appRouteAllowed := key != "system" ||
			app.Name == "system" ||
			((method == http.MethodGet || method == http.MethodHead) &&
				(lowerPath == "/open/system" || lowerPath == "/open/system/")) ||
			(method == http.MethodPut && lowerPath == "/open/system/notify")
		if !appRouteAllowed {
			return false, errors.New(i18n.T("暂无权限"))
		}

		if current.Expiration > 0 && current.Expiration < time.Now().Unix() {
			return false, errors.New(i18n.T("Token 已失效"))
		}
		return true, nil
	}
	return false, nil
}

func openNamespace(lowerPath string) string {
	trimmed := strings.TrimPrefix(lowerPath, "/open/")
	if idx := strings.IndexByte(trimmed, '/'); idx >= 0 {
		return trimmed[:idx]
	}
	return trimmed
}

func isCronsImport(lowerPath string) bool {
	return lowerPath == "/open/crons/import" || lowerPath == "/open/crons/import/"
}

func isDownload(lowerPath string) bool {
	return lowerPath == "/open/scripts/download" ||
		lowerPath == "/open/scripts/download/" ||
		lowerPath == "/open/logs/download" ||
		lowerPath == "/open/logs/download/"
}
