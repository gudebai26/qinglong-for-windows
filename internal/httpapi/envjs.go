package httpapi

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"

	"qinglong-go/internal/config"
)

// envJSHandler reproduces back/config/serverEnv.ts. The front-end index.html
// loads ./api/env.js before the bundle, so the response must be JavaScript and
// must not require authentication.
//
// serverEnv.ts uses pick(process.env, ['QlBaseUrl','DeployEnv','QL_DIR']), so a
// key that is absent (or empty) produces no line at all. Emitting an empty
// assignment would make the Go response differ byte for byte from Node's.
func envJSHandler(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		entries := []struct {
			key   string
			value string
		}{
			{"QlBaseUrl", baseURLWithSlash(cfg.BaseURL)},
			{"DeployEnv", os.Getenv("DeployEnv")},
			{"QL_DIR", cfg.RootPath},
		}

		lines := make([]string, 0, len(entries))
		for _, entry := range entries {
			if entry.value == "" {
				continue
			}
			lines = append(lines, envLine(entry.key, entry.value))
		}

		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(strings.Join(lines, "\n")))
	}
}

func envLine(key, value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		encoded = []byte(`""`)
	}
	return "window.__ENV__" + key + "=" + string(encoded) + ";"
}

// baseURLWithSlash normalizes the base URL the way serverEnv.ts does: leading
// and trailing slash, e.g. "/ql/".
func baseURLWithSlash(baseURL string) string {
	if baseURL == "" {
		return ""
	}
	return baseURL + "/"
}
