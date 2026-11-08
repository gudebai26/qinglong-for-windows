package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"

	"qinglong-go/internal/i18n"
	"qinglong-go/internal/service"
)

// registerSystemSettingRoutes mounts the 26 routes of back/api/system.ts. The
// public /api/system endpoint keeps its own handler from M3 because the layout
// calls it before login.
func registerSystemSettingRoutes(router chi.Router, deps Deps) {
	router.Get("/api/system", func(w http.ResponseWriter, r *http.Request) {
		data, err := deps.System.Info(r.Context())
		if err != nil {
			writeCronError(w, err)
			return
		}
		writeOK(w, data)
	})

	router.Get("/api/system/config", func(w http.ResponseWriter, r *http.Request) {
		data, err := deps.System.Config(r.Context())
		if err != nil {
			writeCronError(w, err)
			return
		}
		writeOK(w, data)
	})

	router.Put("/api/system/config/log-remove-frequency", func(w http.ResponseWriter, r *http.Request) {
		body, ok := readJSONObject(w, r)
		if !ok {
			return
		}
		value, ok := optionalInt(w, body, "logRemoveFrequency")
		if !ok {
			return
		}
		reply, err := deps.System.SetLogRemoveFrequency(r.Context(), value, hasKey(body, "logRemoveFrequency"))
		if err != nil {
			writeCronError(w, err)
			return
		}
		writeReply(w, reply)
	})

	router.Put("/api/system/config/cron-concurrency", func(w http.ResponseWriter, r *http.Request) {
		body, ok := readJSONObject(w, r)
		if !ok {
			return
		}
		value, ok := optionalInt(w, body, "cronConcurrency")
		if !ok {
			return
		}
		reply, err := deps.System.SetCronConcurrency(r.Context(), value)
		if err != nil {
			writeCronError(w, err)
			return
		}
		writeReply(w, reply)
	})

	router.Put("/api/system/config/dependence-proxy", func(w http.ResponseWriter, r *http.Request) {
		body, ok := readJSONObject(w, r)
		if !ok {
			return
		}
		value, ok := optionalNullableString(w, body, "dependenceProxy")
		if !ok {
			return
		}
		reply, err := deps.System.SetDependenceProxy(r.Context(), value, hasKey(body, "dependenceProxy"))
		if err != nil {
			writeCronError(w, err)
			return
		}
		writeReply(w, reply)
	})

	router.Put("/api/system/config/node-mirror", func(w http.ResponseWriter, r *http.Request) {
		body, ok := readJSONObject(w, r)
		if !ok {
			return
		}
		value, ok := optionalNullableString(w, body, "nodeMirror")
		if !ok {
			return
		}
		reply, err := deps.System.SetNodeMirror(r.Context(), value, hasKey(body, "nodeMirror"))
		if err != nil {
			writeCronError(w, err)
			return
		}
		if reply.Code != 200 {
			writeReply(w, reply)
			return
		}
		// Node sets QL-Task-Pid and ends the stream as soon as pnpm started;
		// progress arrives over the websocket.
		w.Header().Set("Content-Type", "application/octet-stream")
		if data, ok := reply.Data.(map[string]any); ok {
			if pid, ok := data["pid"].(int); ok {
				w.Header().Set("QL-Task-Pid", strconv.Itoa(pid))
			}
		}
		w.WriteHeader(http.StatusOK)
	})

	router.Put("/api/system/config/python-mirror", func(w http.ResponseWriter, r *http.Request) {
		body, ok := readJSONObject(w, r)
		if !ok {
			return
		}
		value, ok := optionalNullableString(w, body, "pythonMirror")
		if !ok {
			return
		}
		reply, err := deps.System.SetPythonMirror(r.Context(), value, hasKey(body, "pythonMirror"))
		if err != nil {
			writeCronError(w, err)
			return
		}
		writeReply(w, reply)
	})

	router.Put("/api/system/config/linux-mirror", func(w http.ResponseWriter, r *http.Request) {
		body, ok := readJSONObject(w, r)
		if !ok {
			return
		}
		value, ok := optionalNullableString(w, body, "linuxMirror")
		if !ok {
			return
		}
		reply, err := deps.System.SetLinuxMirror(r.Context(), value)
		if err != nil {
			writeCronError(w, err)
			return
		}
		writeReply(w, reply)
	})

	router.Put("/api/system/update-check", func(w http.ResponseWriter, r *http.Request) {
		writeReply(w, deps.System.CheckUpdate(r.Context()))
	})

	router.Put("/api/system/update", func(w http.ResponseWriter, r *http.Request) {
		writeReply(w, deps.System.UpdateSystem())
	})

	router.Get("/api/system/update-status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, deps.System.UpgradeStatus())
	})

	router.Put("/api/system/reload", func(w http.ResponseWriter, r *http.Request) {
		body, ok := readJSONObject(w, r)
		if !ok {
			return
		}
		value, ok := optionalNullableString(w, body, "type")
		if !ok {
			return
		}
		target := ""
		if value != nil {
			target = *value
		}
		writeReply(w, deps.System.Reload(target))
	})

	router.Put("/api/system/notify", func(w http.ResponseWriter, r *http.Request) {
		body, ok := readJSONObject(w, r)
		if !ok {
			return
		}
		title, ok := requiredString(w, body, "title")
		if !ok {
			return
		}
		content, ok := requiredString(w, body, "content")
		if !ok {
			return
		}
		reply, err := deps.System.Notify(r.Context(), title, content)
		if err != nil {
			writeCronError(w, err)
			return
		}
		writeReply(w, reply)
	})

	router.Put("/api/system/command-run", func(w http.ResponseWriter, r *http.Request) {
		body, ok := readJSONObject(w, r)
		if !ok {
			return
		}
		command, ok := requiredString(w, body, "command")
		if !ok {
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		// The runner writes its log header the moment the process starts, which
		// would flush the response before QL-Task-* could be set. Buffer the
		// first bytes until the headers are on the wire.
		flusher, _ := w.(http.Flusher)
		stream := &deferredWriter{out: w, flusher: flusher}
		execution, err := deps.System.StartCommand(r.Context(), command, stream)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.Header().Set("QL-Task-Pid", strconv.Itoa(execution.Pid))
		w.Header().Set("QL-Task-Log", execution.LogPath)
		w.WriteHeader(http.StatusOK)
		stream.Enable()
		execution.Wait()
	})

	router.Put("/api/system/command-stop", func(w http.ResponseWriter, r *http.Request) {
		body, ok := readJSONObject(w, r)
		if !ok {
			return
		}
		command, ok := optionalNullableString(w, body, "command")
		if !ok {
			return
		}
		pid, ok := optionalNumber(w, body, "pid")
		if !ok {
			return
		}
		value := ""
		if command != nil {
			value = *command
		}
		pidValue := int64(0)
		if pid != nil {
			pidValue = int64(*pid)
		}
		reply, err := deps.System.StopCommand(r.Context(), value, pidValue)
		if err != nil {
			writeCronError(w, err)
			return
		}
		writeReply(w, reply)
	})

	router.Put("/api/system/data/export", func(w http.ResponseWriter, r *http.Request) {
		body, ok := readJSONObject(w, r)
		if !ok {
			return
		}
		kinds, ok := stringArray(w, body, "type")
		if !ok {
			return
		}
		path, err := deps.System.ExportData(kinds)
		if err != nil {
			if business, ok := err.(*service.BusinessError); ok {
				writeJSON(w, http.StatusOK, envelope{Code: business.Code, Message: business.Message})
				return
			}
			writeJSON(w, http.StatusOK, envelope{Code: 400, Message: err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="data.tgz"`)
		file, err := os.Open(path)
		if err != nil {
			writeJSON(w, http.StatusOK, envelope{Code: 400, Message: err.Error()})
			return
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			writeJSON(w, http.StatusOK, envelope{Code: 400, Message: err.Error()})
			return
		}
		w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, file)
	})

	router.Put("/api/system/data/import", func(w http.ResponseWriter, r *http.Request) {
		if !isMultipart(r) {
			writeError(w, http.StatusBadRequest, "Multipart: Boundary not found")
			return
		}
		_, uploaded, err := parseMultipart(r, "data", deps.Config.TmpPath, false, 0)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if uploaded != nil {
			// multer stores the part as .tmp/data.tgz, which importData reads.
			if err := os.Rename(uploaded.Path, deps.Config.DataTgzFile); err != nil {
				if copyErr := copyFileContents(uploaded.Path, deps.Config.DataTgzFile); copyErr != nil {
					writeJSON(w, http.StatusOK, envelope{Code: 400, Message: copyErr.Error()})
					return
				}
				_ = os.Remove(uploaded.Path)
			}
		}
		data, err := deps.System.ImportData()
		if err != nil {
			writeJSON(w, http.StatusOK, envelope{Code: 400, Message: err.Error()})
			return
		}
		writeOK(w, data)
	})

	router.Get("/api/system/log", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		limit := int64(0)
		if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
			parsed, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || parsed < 1 || parsed > 1024*1024 {
				writeValidationErrorSource(w, "query", []string{"limit"},
					`"limit" must be a number`)
				return
			}
			limit = parsed
		}
		page, err := deps.System.PrepareSystemLog(query.Get("startTime"), query.Get("endTime"), limit)
		if err != nil {
			writeCronError(w, err)
			return
		}
		// Node streams raw bytes with no declared type; Go would sniff the body
		// and label it text/plain, which breaks the log download.
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.FormatInt(page.Length, 10))
		w.Header().Set("X-QL-Log-Total", strconv.FormatInt(page.Total, 10))
		truncated := "false"
		if page.Truncated {
			truncated = "true"
		}
		w.Header().Set("X-QL-Log-Truncated", truncated)
		w.WriteHeader(http.StatusOK)
		_ = page.CopyTo(w)
	})

	router.Get("/api/system/notify-log", func(w http.ResponseWriter, r *http.Request) {
		data, err := deps.System.NotifyLog(r.Context())
		if err != nil {
			writeCronError(w, err)
			return
		}
		writeOK(w, data)
	})

	router.Delete("/api/system/log", func(w http.ResponseWriter, r *http.Request) {
		if err := deps.System.DeleteSystemLog(); err != nil {
			writeCronError(w, err)
			return
		}
		// The Node handler answers a bare {code:200} without data.
		writeJSON(w, http.StatusOK, envelope{Code: http.StatusOK})
	})

	router.Put("/api/system/auth/reset", func(w http.ResponseWriter, r *http.Request) {
		body, ok := readJSONObject(w, r)
		if !ok {
			return
		}
		var payload service.ResetAuthPayload
		retries, ok := optionalInt(w, body, "retries")
		if !ok {
			return
		}
		payload.Retries = retries
		activated, ok := optionalBool(w, body, "twoFactorActivated")
		if !ok {
			return
		}
		payload.TwoFactorActivated = activated
		password, ok := optionalNullableString(w, body, "password")
		if !ok {
			return
		}
		payload.Password = password
		username, ok := optionalNullableString(w, body, "username")
		if !ok {
			return
		}
		payload.Username = username

		reply, err := deps.System.ResetAuth(r.Context(), payload)
		if err != nil {
			writeCronError(w, err)
			return
		}
		if reply != nil {
			writeReply(w, reply)
			return
		}
		writeJSON(w, http.StatusOK, envelope{Code: 200, Message: i18n.T("更新成功")})
	})

	router.Put("/api/system/config/timezone", func(w http.ResponseWriter, r *http.Request) {
		body, ok := readJSONObject(w, r)
		if !ok {
			return
		}
		value, ok := optionalNullableString(w, body, "timezone")
		if !ok {
			return
		}
		reply, err := deps.System.SetTimezone(r.Context(), value)
		if err != nil {
			writeCronError(w, err)
			return
		}
		writeReply(w, reply)
	})

	router.Put("/api/system/config/lang", func(w http.ResponseWriter, r *http.Request) {
		body, ok := readJSONObject(w, r)
		if !ok {
			return
		}
		value, ok := optionalNullableString(w, body, "lang")
		if !ok {
			return
		}
		reply, err := deps.System.SetLanguage(r.Context(), value)
		if err != nil {
			writeCronError(w, err)
			return
		}
		writeReply(w, reply)
	})

	router.Put("/api/system/config/panel-title", func(w http.ResponseWriter, r *http.Request) {
		body, ok := readJSONObject(w, r)
		if !ok {
			return
		}
		value, ok := optionalNullableString(w, body, "panelTitle")
		if !ok {
			return
		}
		if value != nil && len([]rune(*value)) > 100 {
			writeValidationErrorKeys(w, []string{"panelTitle"},
				`"panelTitle" length must be less than or equal to 100 characters long`)
			return
		}
		reply, err := deps.System.SetPanelTitle(r.Context(), value)
		if err != nil {
			writeCronError(w, err)
			return
		}
		writeReply(w, reply)
	})

	router.Put("/api/system/config/global-ssh-key", func(w http.ResponseWriter, r *http.Request) {
		body, ok := readJSONObject(w, r)
		if !ok {
			return
		}
		value, ok := optionalNullableString(w, body, "globalSshKey")
		if !ok {
			return
		}
		reply, err := deps.System.SetGlobalSSHKey(r.Context(), value)
		if err != nil {
			writeCronError(w, err)
			return
		}
		writeReply(w, reply)
	})

	router.Put("/api/system/config/dependence-clean", func(w http.ResponseWriter, r *http.Request) {
		body, ok := readJSONObject(w, r)
		if !ok {
			return
		}
		value, ok := optionalNullableString(w, body, "type")
		if !ok {
			return
		}
		kind := ""
		if value != nil {
			kind = *value
		}
		reply, err := deps.System.CleanDependence(r.Context(), kind)
		if err != nil {
			writeCronError(w, err)
			return
		}
		writeReply(w, reply)
	})
}

// deferredWriter holds the first bytes of a streamed response until the caller
// has written its headers.
type deferredWriter struct {
	mu      sync.Mutex
	out     io.Writer
	flusher http.Flusher
	buffer  bytes.Buffer
	enabled bool
}

func (w *deferredWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.enabled {
		w.buffer.Write(p)
		return len(p), nil
	}
	written, err := w.out.Write(p)
	if w.flusher != nil {
		w.flusher.Flush()
	}
	return written, err
}

// Enable flushes what the process already printed and switches to live output.
func (w *deferredWriter) Enable() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.enabled {
		return
	}
	w.enabled = true
	if w.buffer.Len() > 0 {
		_, _ = w.out.Write(w.buffer.Bytes())
		w.buffer.Reset()
	}
	if w.flusher != nil {
		w.flusher.Flush()
	}
}

// hasKey reports whether the JSON body carried the field at all, which is how
// Node tells an explicit null from an absent key.
func hasKey(body map[string]any, key string) bool {
	_, exists := body[key]
	return exists
}

// requiredString enforces `Joi.string().required()`.
func requiredString(w http.ResponseWriter, body map[string]any, key string) (string, bool) {
	value, exists := body[key]
	if !exists || value == nil {
		writeValidationErrorKeys(w, []string{key}, `"`+key+`" is required`)
		return "", false
	}
	text, ok := value.(string)
	if !ok {
		writeValidationErrorKeys(w, []string{key}, `"`+key+`" must be a string`)
		return "", false
	}
	return text, true
}

// optionalNumber enforces `Joi.number().allow(null)` and returns nil for null.
func optionalNumber(w http.ResponseWriter, body map[string]any, key string) (*float64, bool) {
	value, exists := body[key]
	if !exists || value == nil {
		return nil, true
	}
	number, ok := floatValueOf(value)
	if !ok {
		writeValidationErrorKeys(w, []string{key}, `"`+key+`" must be a number`)
		return nil, false
	}
	return &number, true
}

// optionalInt is optionalNumber restricted to integers (cron-concurrency, pid).
func optionalInt(w http.ResponseWriter, body map[string]any, key string) (*int64, bool) {
	number, ok := optionalNumber(w, body, key)
	if !ok || number == nil {
		return nil, ok
	}
	value := int64(*number)
	return &value, true
}

// optionalBool enforces `Joi.boolean().optional()`.
func optionalBool(w http.ResponseWriter, body map[string]any, key string) (*bool, bool) {
	value, exists := body[key]
	if !exists || value == nil {
		return nil, true
	}
	flag, ok := value.(bool)
	if !ok {
		// Joi converts the strings "true"/"false" and the numbers 0/1.
		switch typed := value.(type) {
		case string:
			if typed == "true" || typed == "false" {
				parsed := typed == "true"
				return &parsed, true
			}
		case json.Number:
			if typed.String() == "0" || typed.String() == "1" {
				parsed := typed.String() == "1"
				return &parsed, true
			}
		}
		writeValidationErrorKeys(w, []string{key}, `"`+key+`" must be a boolean`)
		return nil, false
	}
	return &flag, true
}

// optionalNullableString enforces `Joi.string().allow(”).allow(null)`.
func optionalNullableString(w http.ResponseWriter, body map[string]any, key string) (*string, bool) {
	value, exists := body[key]
	if !exists || value == nil {
		return nil, true
	}
	text, ok := value.(string)
	if !ok {
		writeValidationErrorKeys(w, []string{key}, `"`+key+`" must be a string`)
		return nil, false
	}
	return &text, true
}

// stringArray reads an optional array of strings without the scope rules.
func stringArray(w http.ResponseWriter, body map[string]any, key string) ([]string, bool) {
	value, exists := body[key]
	if !exists || value == nil {
		return []string{}, true
	}
	list, ok := value.([]any)
	if !ok {
		writeValidationErrorKeys(w, []string{key}, `"`+key+`" must be an array`)
		return nil, false
	}
	out := make([]string, 0, len(list))
	for index, item := range list {
		text, ok := item.(string)
		if !ok {
			writeValidationErrorKeys(w, []string{key + "." + strconv.Itoa(index)},
				`"`+key+"["+strconv.Itoa(index)+`]" must be a string`)
			return nil, false
		}
		out = append(out, text)
	}
	return out, true
}

// floatValueOf converts a decoded JSON number. Joi converts numeric strings by
// default, so "5" is accepted as 5 exactly like celebrate does.
func floatValueOf(value any) (float64, bool) {
	switch typed := value.(type) {
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	case float64:
		return typed, true
	case int64:
		return float64(typed), true
	case int:
		return float64(typed), true
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err != nil {
			return 0, false
		}
		return parsed, true
	}
	return 0, false
}

// copyFileContents is the fallback when the upload lives on another volume and
// cannot be renamed into place.
func copyFileContents(source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	out, err := os.Create(target)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
