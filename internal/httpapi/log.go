package httpapi

import (
	"mime"
	"net/http"
	"path/filepath"

	"github.com/go-chi/chi/v5"

	"qinglong-go/internal/i18n"
	"qinglong-go/internal/service"
)

// registerLogRoutes mounts the 5 routes of back/api/log.ts. They share the log
// chunk reader with /api/crons/:id/log, so the six-field response stays aligned.
func registerLogRoutes(router chi.Router, deps Deps) {
	cron := deps.Cron

	router.Get("/api/logs", func(w http.ResponseWriter, r *http.Request) {
		data, err := cron.ReadDirs(r.Context(), deps.Config.LogPath)
		if err != nil {
			writeCronError(w, err)
			return
		}
		writeOK(w, data)
	})

	router.Get("/api/logs/detail", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		file := query.Get("file")
		if file == "" {
			// Joi distinguishes a missing key ("is required") from an empty
			// value ("is not allowed to be empty"); the query always has the key.
			if _, present := query["file"]; present {
				writeValidationErrorSource(w, "query", []string{"file"},
					`"file" is not allowed to be empty`)
			} else {
				writeValidationErrorSource(w, "query", []string{"file"}, `"file" is required`)
			}
			return
		}
		path := query.Get("path")
		offset, ok := queryIntParam(w, r, "offset", 0, 0)
		if !ok {
			return
		}
		limit, ok := queryIntParam(w, r, "limit", 1, service.MaxLogChunkBytes)
		if !ok {
			return
		}
		tailValue, ok := queryBoolParam(w, r, "tail")
		if !ok {
			return
		}
		tail := tailValue != nil && *tailValue

		result, ok, err := cron.LogDetail(r.Context(), path, file, offset, limit, tail)
		if err != nil {
			writeCronError(w, err)
			return
		}
		if !ok {
			writeReply(w, &service.Reply{Code: 403, Message: i18n.T("暂无权限")})
			return
		}
		body := map[string]any{
			"code":       http.StatusOK,
			"data":       result.Content,
			"offset":     result.Offset,
			"nextOffset": result.NextOffset,
			"total":      result.Total,
			"truncated":  result.Truncated,
		}
		if result.Status != "" {
			body["logStatus"] = result.Status
		}
		writeJSON(w, http.StatusOK, body)
	})

	router.Get("/api/logs/{file}", func(w http.ResponseWriter, r *http.Request) {
		writeReply(w, &service.Reply{
			Code:    410,
			Message: i18n.T("接口已下线，请使用 /logs/detail 接口"),
		})
	})

	router.Delete("/api/logs", func(w http.ResponseWriter, r *http.Request) {
		body, ok := validateBody(w, r, nil)
		if !ok {
			return
		}
		filename, _ := body["filename"].(string)
		if filename == "" {
			if _, present := body["filename"]; present {
				writeValidationErrorKeys(w, []string{"filename"},
					`"filename" is not allowed to be empty`)
			} else {
				writeValidationErrorKeys(w, []string{"filename"}, `"filename" is required`)
			}
			return
		}
		path, _ := body["path"].(string)
		removed, err := cron.RemoveLog(r.Context(), path, filename)
		if err != nil {
			writeCronError(w, err)
			return
		}
		if !removed {
			writeReply(w, &service.Reply{Code: 403, Message: i18n.T("暂无权限")})
			return
		}
		writeReply(w, &service.Reply{Code: 200})
	})

	router.Post("/api/logs/download", func(w http.ResponseWriter, r *http.Request) {
		body, ok := validateBody(w, r, nil)
		if !ok {
			return
		}
		filename, _ := body["filename"].(string)
		if filename == "" {
			if _, present := body["filename"]; present {
				writeValidationErrorKeys(w, []string{"filename"},
					`"filename" is not allowed to be empty`)
			} else {
				writeValidationErrorKeys(w, []string{"filename"}, `"filename" is required`)
			}
			return
		}
		path, _ := body["path"].(string)
		target, allowed := cron.CheckLogFilePath(path, filename)
		if !allowed {
			writeReply(w, &service.Reply{Code: 403, Message: i18n.T("暂无权限")})
			return
		}
		disposition := mime.FormatMediaType("attachment", map[string]string{
			"filename": filepath.Base(filename),
		})
		if disposition != "" {
			w.Header().Set("Content-Disposition", disposition)
		}
		http.ServeFile(w, r, target)
	})
}
