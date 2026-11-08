package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// registerConfigRoutes mounts the five routes of back/api/config.ts.
func registerConfigRoutes(router chi.Router, deps Deps) {
	router.Get("/api/configs/samples", func(w http.ResponseWriter, r *http.Request) {
		writeOK(w, deps.System.SampleFiles())
	})

	router.Get("/api/configs/files", func(w http.ResponseWriter, r *http.Request) {
		data, err := deps.System.ConfigFiles(r.Context())
		if err != nil {
			writeCronError(w, err)
			return
		}
		writeOK(w, data)
	})

	router.Get("/api/configs/detail", func(w http.ResponseWriter, r *http.Request) {
		reply, err := deps.System.ConfigDetail(r.Context(), r.URL.Query().Get("path"))
		if err != nil {
			writeCronError(w, err)
			return
		}
		writeReply(w, reply)
	})

	router.Post("/api/configs/save", func(w http.ResponseWriter, r *http.Request) {
		body, ok := readJSONObject(w, r)
		if !ok {
			return
		}
		name, ok := requiredString(w, body, "name")
		if !ok {
			return
		}
		content := ""
		if raw, exists := body["content"]; exists && raw != nil {
			text, ok := raw.(string)
			if !ok {
				writeValidationErrorKeys(w, []string{"content"}, `"content" must be a string`)
				return
			}
			content = text
		}
		reply, err := deps.System.SaveConfig(r.Context(), name, content)
		if err != nil {
			writeCronError(w, err)
			return
		}
		writeReply(w, reply)
	})

	router.Get("/api/configs/{file}", func(w http.ResponseWriter, r *http.Request) {
		writeReply(w, deps.System.ConfigsGone())
	})
}

// registerUpdateRoutes mounts the three routes of back/api/update.ts. They are
// aliases of PUT /api/system/reload with an explicit target.
func registerUpdateRoutes(router chi.Router, deps Deps) {
	router.Put("/api/update/reload", func(w http.ResponseWriter, r *http.Request) {
		writeReply(w, deps.System.Reload(""))
	})
	router.Put("/api/update/system", func(w http.ResponseWriter, r *http.Request) {
		writeReply(w, deps.System.Reload("system"))
	})
	router.Put("/api/update/data", func(w http.ResponseWriter, r *http.Request) {
		writeReply(w, deps.System.Reload("data"))
	})
}
