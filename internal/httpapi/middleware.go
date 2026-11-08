package httpapi

import (
	"bufio"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"qinglong-go/internal/config"
)

const (
	// maxBodyBytes matches body-parser's `limit: '50mb'` in back/loaders/express.ts.
	maxBodyBytes = 50 << 20
	// credentialBodyBytes is the tighter limit applied to login/init endpoints.
	credentialBodyBytes = 16 << 10
)

// credentialPaths are the only routes mounted with the 16kb body parser. Node
// builds the list from both prefixes (express.ts:62-71), so the /open aliases
// share the tighter limit.
var credentialPaths = map[string]struct{}{
	"/api/user/login":             {},
	"/api/user/init":              {},
	"/api/user/two-factor/login":  {},
	"/open/user/login":            {},
	"/open/user/init":             {},
	"/open/user/two-factor/login": {},
}

// responseState remembers whether the handler already started the response so
// the panic recovery middleware does not append a second status code.
type responseState struct {
	http.ResponseWriter
	status int
}

func (w *responseState) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
		// Forward only the first call: a second WriteHeader reaches net/http and
		// logs "superfluous response.WriteHeader", which must never appear.
		w.ResponseWriter.WriteHeader(status)
	}
}

func (w *responseState) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(p)
}

func (w *responseState) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *responseState) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Hijack lets the raw SockJS websocket transport upgrade the connection. The
// websocket library type-asserts http.Hijacker on the writer it received; the
// middleware must forward that ability or every /api/ws/.../websocket request
// fails with a 500 that the browser client silently masks by falling back to
// xhr-streaming.
func (w *responseState) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hijacker, ok := w.ResponseWriter.(http.Hijacker); ok {
		return hijacker.Hijack()
	}
	return nil, nil, fmt.Errorf("http.Hijacker is not supported by the response writer")
}

func recoverer(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			state := &responseState{ResponseWriter: w}
			defer func() {
				recovered := recover()
				if recovered == nil {
					return
				}
				// Let net/http abort the connection as usual.
				if recovered == http.ErrAbortHandler {
					panic(recovered)
				}
				logger.Error("panic recovered",
					"error", fmt.Sprint(recovered),
					"method", r.Method,
					"path", r.URL.Path,
					"stack", string(debug.Stack()),
				)
				if state.status == 0 {
					writeError(state, http.StatusInternalServerError, "Internal Server Error")
				}
			}()
			next.ServeHTTP(state, r)
		})
	}
}

// requestLogger records method, path, status and latency. Successful requests
// stay at debug level so a busy panel does not flood data/syslog with 200s;
// 4xx/5xx are always recorded and slow requests keep the Node warning.
func requestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			state, ok := w.(*responseState)
			if !ok {
				state = &responseState{ResponseWriter: w}
			}
			next.ServeHTTP(state, r)

			status := state.status
			if status == 0 {
				status = http.StatusOK
			}
			duration := time.Since(start)
			attrs := []any{
				"method", r.Method,
				"path", r.URL.Path,
				"status", status,
				"duration", duration.Round(time.Millisecond).String(),
			}
			switch {
			case status >= http.StatusInternalServerError:
				logger.Error("request", attrs...)
			case status >= http.StatusBadRequest:
				logger.Warn("request", attrs...)
			case duration > time.Second:
				// Mirrors the "Slow request detected" warning in monitoring.ts.
				logger.Warn("slow request", attrs...)
			default:
				logger.Debug("request", attrs...)
			}
		})
	}
}

// cors replicates the permissive `cors()` middleware used by the Node panel:
// any origin, standard methods, and a 204 preflight response.
func cors(cfg *config.Config) func(http.Handler) http.Handler {
	allowed := cfg.CORSOrigins
	wildcard := len(allowed) == 0 || (len(allowed) == 1 && allowed[0] == "*")

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			switch {
			case wildcard:
				w.Header().Set("Access-Control-Allow-Origin", "*")
			case origin != "" && originAllowed(allowed, origin):
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Add("Vary", "Origin")
			}

			if r.Method == http.MethodOptions {
				w.Header().Set("Access-Control-Allow-Methods", "GET,HEAD,PUT,PATCH,POST,DELETE")
				if headers := r.Header.Get("Access-Control-Request-Headers"); headers != "" {
					w.Header().Set("Access-Control-Allow-Headers", headers)
					w.Header().Add("Vary", "Access-Control-Request-Headers")
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func originAllowed(allowed []string, origin string) bool {
	for _, candidate := range allowed {
		if candidate == "*" || strings.EqualFold(candidate, origin) {
			return true
		}
	}
	return false
}

// protectedPathCase mirrors back/middlewares/protectedPathCase.ts: a mixed-case
// path under /api or /open is rejected before routing so authentication can
// never be bypassed by casing tricks.
func protectedPathCase(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		lower := strings.ToLower(path)
		if path != lower && (strings.HasPrefix(lower, "/api/") || strings.HasPrefix(lower, "/open/")) {
			writeError(w, http.StatusBadRequest, "Invalid path format")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// baseURLRewrite implements express-urlrewrite's `${baseUrl}/*` -> `/$1`.
func baseURLRewrite(baseURL string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if baseURL == "" {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == baseURL {
				r.URL.Path = "/"
			} else if strings.HasPrefix(r.URL.Path, baseURL+"/") {
				r.URL.Path = strings.TrimPrefix(r.URL.Path, baseURL)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// openAliasRewrite implements rewrite('/open/*', '/api/$1'): every route that
// exists under /api is reachable under /open with the same handler.
func openAliasRewrite(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/open/") {
			r.URL.Path = "/api/" + strings.TrimPrefix(r.URL.Path, "/open/")
			if r.URL.RawPath != "" {
				r.URL.RawPath = ""
			}
		}
		next.ServeHTTP(w, r)
	})
}

// stripQueryParamT removes the cache-busting `t` parameter globally, matching
// httpApplication.ts so no handler ever sees it.
func stripQueryParamT(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if _, ok := query["t"]; ok {
			query.Del("t")
			r.URL.RawQuery = query.Encode()
		}
		next.ServeHTTP(w, r)
	})
}

// bodyLimit enforces body-parser's size limits before a handler can read the
// body. Oversized requests are rejected with the same 413 envelope Node emits.
func bodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := int64(maxBodyBytes)
		if _, ok := credentialPaths[r.URL.Path]; ok {
			limit = credentialBodyBytes
		}
		if r.ContentLength > limit {
			writeError(w, http.StatusRequestEntityTooLarge, "request entity too large")
			return
		}
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		next.ServeHTTP(w, r)
	})
}
