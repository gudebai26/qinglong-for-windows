package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"qinglong-go/internal/i18n"
	"qinglong-go/internal/service"
)

// scriptUploadLimit is multer's memory limit for the env importer; script
// uploads use disk storage and are only bounded by the 50 MB body limit.
const envUploadLimit = 5 * 1024 * 1024

var (
	hexHashPattern = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
	uuidPattern    = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// registerScriptRoutes mounts the 13 routes of back/api/script.ts.
func registerScriptRoutes(router chi.Router, deps Deps) {
	scripts := deps.Scripts

	router.Get("/api/scripts", func(w http.ResponseWriter, r *http.Request) {
		data, err := scripts.List(r.URL.Query().Get("path"))
		if err != nil {
			writeScriptError(w, err)
			return
		}
		writeOK(w, data)
	})

	router.Get("/api/scripts/detail", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if _, ok := requireQueryString(w, query, "file"); !ok {
			return
		}
		content, err := scripts.GetFile(query.Get("path"), query.Get("file"))
		if err != nil {
			writeScriptError(w, err)
			return
		}
		writeOK(w, content)
	})

	router.Get("/api/scripts/history", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if _, ok := requireQueryString(w, query, "filename"); !ok {
			return
		}
		data, err := scripts.History().List(query.Get("path"), query.Get("filename"))
		if err != nil {
			writeScriptError(w, err)
			return
		}
		writeOK(w, data)
	})

	router.Get("/api/scripts/history/detail", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if _, ok := requireQueryString(w, query, "filename"); !ok {
			return
		}
		id, ok := requireQueryUUID(w, query, "id")
		if !ok {
			return
		}
		data, err := scripts.History().Detail(query.Get("path"), query.Get("filename"), id)
		if err != nil {
			writeScriptError(w, err)
			return
		}
		writeOK(w, data)
	})

	router.Put("/api/scripts/history/restore", func(w http.ResponseWriter, r *http.Request) {
		body, ok := validateBody(w, r, nil)
		if !ok {
			return
		}
		filename, ok := requireBodyString(w, body, "filename")
		if !ok {
			return
		}
		id, ok := requireBodyUUID(w, body, "id")
		if !ok {
			return
		}
		expectedHash, ok := requireBodyHash(w, body, "expectedHash", true)
		if !ok {
			return
		}
		data, err := scripts.Restore(bodyString(body, "path"), filename, id, expectedHash)
		if err != nil {
			writeScriptError(w, err)
			return
		}
		writeOK(w, data)
	})

	// Deprecated single-file endpoint: the panel keeps the 410 body so old
	// clients show the migration hint instead of a 404.
	router.Get("/api/scripts/{file}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, envelope{
			Code:    410,
			Message: i18n.T("接口已下线，请使用 /scripts/detail 接口"),
		})
	})

	router.Post("/api/scripts", func(w http.ResponseWriter, r *http.Request) {
		body, upload, ok := readScriptCreateBody(w, r, deps)
		if !ok {
			return
		}
		filename, ok := requireBodyString(w, body, "filename")
		if !ok {
			return
		}
		skipHistory := bodyBool(body, "skipHistory")
		expectedHash, ok := requireBodyHash(w, body, "expectedHash", skipHistory)
		if !ok {
			return
		}
		input := service.ScriptCreateInput{
			Filename:       filename,
			Path:           bodyString(body, "path"),
			Content:        bodyOptionalString(body, "content"),
			SkipHistory:    skipHistory,
			ExpectedHash:   expectedHash,
			OriginFilename: bodyString(body, "originFilename"),
			Directory:      bodyString(body, "directory"),
		}
		if upload != nil {
			input.UploadPath = upload.Path
			// multer names the destination from the form field, never the client
			// file name, so a script cannot be written outside the directory.
			defer func() {
				if upload.Path != "" {
					_ = os.Remove(upload.Path)
				}
			}()
		}
		data, err := scripts.Create(input)
		if err != nil {
			writeScriptError(w, err)
			return
		}
		if data == nil {
			writeJSON(w, http.StatusOK, envelope{Code: 200})
			return
		}
		writeOK(w, data)
	})

	router.Put("/api/scripts", func(w http.ResponseWriter, r *http.Request) {
		body, ok := validateBody(w, r, nil)
		if !ok {
			return
		}
		if !rejectUnknownKeys(w, body, scriptSaveKeys) {
			return
		}
		filename, ok := requireBodyString(w, body, "filename")
		if !ok {
			return
		}
		content, ok := requireBodyStringAllowEmpty(w, body, "content")
		if !ok {
			return
		}
		skipHistory := bodyBool(body, "skipHistory")
		expectedHash, ok := requireBodyHash(w, body, "expectedHash", skipHistory)
		if !ok {
			return
		}
		data, err := scripts.Save(bodyString(body, "path"), filename, content, service.SaveOptions{
			SkipHistory:  skipHistory,
			ExpectedHash: expectedHash,
		})
		if err != nil {
			writeScriptError(w, err)
			return
		}
		writeOK(w, data)
	})

	router.Delete("/api/scripts", func(w http.ResponseWriter, r *http.Request) {
		body, ok := validateBody(w, r, nil)
		if !ok {
			return
		}
		filename, ok := requireBodyString(w, body, "filename")
		if !ok {
			return
		}
		if err := scripts.Delete(bodyString(body, "path"), filename); err != nil {
			writeScriptError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, envelope{Code: 200})
	})

	router.Post("/api/scripts/download", func(w http.ResponseWriter, r *http.Request) {
		body, ok := validateBody(w, r, nil)
		if !ok {
			return
		}
		filename, ok := requireBodyString(w, body, "filename")
		if !ok {
			return
		}
		filePath, err := scripts.DownloadPath(bodyString(body, "path"), filename)
		if err != nil {
			writeScriptError(w, err)
			return
		}
		if err := writeDownload(w, filePath, filename); err != nil {
			// Express's send sets err.status = 404 for a missing file.
			writeError(w, http.StatusNotFound, err.Error())
		}
	})

	router.Put("/api/scripts/run", func(w http.ResponseWriter, r *http.Request) {
		body, ok := validateBody(w, r, nil)
		if !ok {
			return
		}
		filename, ok := requireBodyString(w, body, "filename")
		if !ok {
			return
		}
		pid, err := scripts.Run(bodyString(body, "path"), filename, bodyString(body, "content"))
		if err != nil {
			writeScriptError(w, err)
			return
		}
		writeOK(w, pid)
	})

	router.Put("/api/scripts/stop", func(w http.ResponseWriter, r *http.Request) {
		body, ok := validateBody(w, r, nil)
		if !ok {
			return
		}
		filename, ok := requireBodyString(w, body, "filename")
		if !ok {
			return
		}
		pid := bodyInt(body, "pid")
		if err := scripts.Stop(bodyString(body, "path"), filename, pid); err != nil {
			writeScriptError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, envelope{Code: 200})
	})

	router.Put("/api/scripts/rename", func(w http.ResponseWriter, r *http.Request) {
		body, ok := validateBody(w, r, nil)
		if !ok {
			return
		}
		filename, ok := requireBodyString(w, body, "filename")
		if !ok {
			return
		}
		newFilename, ok := requireBodyString(w, body, "newFilename")
		if !ok {
			return
		}
		if err := scripts.Rename(bodyString(body, "path"), filename, newFilename); err != nil {
			writeScriptError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, envelope{Code: 200})
	})
}

// scriptSaveKeys is the Joi schema of PUT /api/scripts. The schema has no
// .unknown(true), so extra keys are rejected like celebrate does.
var scriptSaveKeys = []string{"filename", "path", "content", "skipHistory", "expectedHash"}

// readScriptCreateBody accepts both the multipart form of the upload/new-file
// dialog and a JSON body.
func readScriptCreateBody(w http.ResponseWriter, r *http.Request, deps Deps) (map[string]any, *uploadedFile, bool) {
	if !isMultipart(r) {
		body, ok := validateBody(w, r, nil)
		return body, nil, ok
	}
	fields, upload, err := parseMultipart(r, "file", deps.Config.TmpPath, false, 0)
	if err != nil {
		if errors.Is(err, errFileTooLarge) {
			writeError(w, http.StatusInternalServerError, err.Error())
			return nil, nil, false
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return nil, nil, false
	}
	return fields, upload, true
}

// writeDownload mirrors res.download(): attachment disposition, mime type from
// the file extension and the byte length.
func writeDownload(w http.ResponseWriter, filePath, filename string) error {
	info, err := os.Stat(filePath)
	if err != nil {
		return fmt.Errorf("ENOENT: no such file or directory, stat '%s'", filePath)
	}
	if info.IsDir() {
		return fmt.Errorf("EISDIR: illegal operation on a directory, read")
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("ENOENT: no such file or directory, open '%s'", filePath)
	}
	w.Header().Set("Content-Type", expressContentType(filename))
	w.Header().Set("Content-Disposition", contentDisposition(filename))
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
	return nil
}

// expressContentType reproduces the mime-types table Express uses for
// res.download, including its charset suffix. Anything unknown is served as
// application/octet-stream, which is what Express does too (".py" and ".go"
// have no mapping in mime-db).
func expressContentType(filename string) string {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".js", ".mjs":
		return "application/javascript; charset=UTF-8"
	case ".json":
		return "application/json; charset=UTF-8"
	case ".sh":
		return "application/x-sh"
	case ".ts":
		return "video/mp2t"
	case ".txt", ".ini", ".conf", ".log":
		return "text/plain; charset=UTF-8"
	case ".md":
		return "text/markdown; charset=UTF-8"
	case ".html", ".htm":
		return "text/html; charset=UTF-8"
	case ".css":
		return "text/css; charset=UTF-8"
	case ".php":
		return "application/x-httpd-php"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".pdf":
		return "application/pdf"
	case ".zip":
		return "application/zip"
	case ".yml", ".yaml":
		return "text/yaml; charset=UTF-8"
	case ".csv":
		return "text/csv; charset=UTF-8"
	default:
		return "application/octet-stream"
	}
}

// contentDisposition ports the content-disposition module Express uses: the
// quoted ASCII fallback plus an RFC 5987 filename* for non-ASCII names.
func contentDisposition(filename string) string {
	fallback := make([]rune, 0, len(filename))
	nonASCII := false
	for _, r := range filename {
		if r < 0x20 || r > 0x7e {
			nonASCII = true
			fallback = append(fallback, '?')
			continue
		}
		if r == '"' || r == '\\' {
			fallback = append(fallback, '\\', r)
			continue
		}
		fallback = append(fallback, r)
	}
	disposition := `attachment; filename="` + string(fallback) + `"`
	if nonASCII {
		disposition += "; filename*=UTF-8''" + percentEncode(filename)
	}
	return disposition
}

func percentEncode(value string) string {
	var builder strings.Builder
	for _, b := range []byte(value) {
		switch {
		case b >= 'A' && b <= 'Z', b >= 'a' && b <= 'z', b >= '0' && b <= '9':
			builder.WriteByte(b)
		case b == '-' || b == '.' || b == '_' || b == '~':
			builder.WriteByte(b)
		default:
			fmt.Fprintf(&builder, "%%%02X", b)
		}
	}
	return builder.String()
}

// writeScriptError maps service errors onto the Node response shapes.
func writeScriptError(w http.ResponseWriter, err error) {
	var business *service.BusinessError
	if errors.As(err, &business) {
		// Node answers with HTTP 200 and a non-200 body code.
		writeJSON(w, http.StatusOK, envelope{Code: business.Code, Message: business.Message})
		return
	}
	var unavailable *service.HistoryUnavailableError
	if errors.As(err, &unavailable) {
		writeJSON(w, unavailable.Status, struct {
			Code               int    `json:"code"`
			Message            string `json:"message"`
			HistoryUnavailable bool   `json:"historyUnavailable"`
			CurrentHash        string `json:"currentHash"`
		}{
			Code:               unavailable.Status,
			Message:            unavailable.Message,
			HistoryUnavailable: true,
			CurrentHash:        unavailable.CurrentHash,
		})
		return
	}
	var historyErr *service.ScriptHistoryError
	if errors.As(err, &historyErr) {
		writeJSON(w, historyErr.Status, envelope{Code: historyErr.Status, Message: historyErr.Message})
		return
	}
	var statusErr *service.StatusError
	if errors.As(err, &statusErr) {
		writeJSON(w, statusErr.Status, envelope{Code: statusErr.Status, Message: statusErr.Message})
		return
	}
	writeError(w, http.StatusInternalServerError, err.Error())
}

// ---------------------------------------------------------------------------
// Body/query helpers shared by the script and env routes
// ---------------------------------------------------------------------------

func bodyString(body map[string]any, key string) string {
	value, ok := body[key]
	if !ok || value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func bodyOptionalString(body map[string]any, key string) *string {
	value, exists := body[key]
	if !exists || value == nil {
		return nil
	}
	text, ok := value.(string)
	if !ok {
		return nil
	}
	return &text
}

// bodyBool accepts JSON booleans and the "true"/"false" strings a multipart
// form produces, exactly like Joi.boolean().
func bodyBool(body map[string]any, key string) bool {
	value, ok := body[key]
	if !ok || value == nil {
		return false
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true")
	default:
		return false
	}
}

func bodyInt(body map[string]any, key string) int64 {
	value, ok := body[key]
	if !ok || value == nil {
		return 0
	}
	parsed, ok := asNumber(value)
	if !ok {
		return 0
	}
	return parsed
}

// requireBodyString validates a required string field that Joi does not mark
// with allow-empty:
// Joi reports an empty value as "is not allowed to be empty".
func requireBodyString(w http.ResponseWriter, body map[string]any, key string) (string, bool) {
	text, ok := requireBodyStringAllowEmpty(w, body, key)
	if !ok {
		return "", false
	}
	if text == "" {
		writeValidationErrorKeys(w, []string{key}, `"`+key+`" is not allowed to be empty`)
		return "", false
	}
	return text, true
}

// requireBodyStringAllowEmpty validates a required string field that accepts
// "" (content, path, remarks).
func requireBodyStringAllowEmpty(w http.ResponseWriter, body map[string]any, key string) (string, bool) {
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

func requireBodyUUID(w http.ResponseWriter, body map[string]any, key string) (string, bool) {
	text, ok := requireBodyString(w, body, key)
	if !ok {
		return "", false
	}
	if !uuidPattern.MatchString(text) {
		writeValidationErrorKeys(w, []string{key}, `"`+key+`" must be a valid GUID`)
		return "", false
	}
	return text, true
}

// requireBodyHash validates a SHA-256 hex digest; required decides whether a
// missing value is an error (Joi's `when('skipHistory', {is: true})`).
func requireBodyHash(w http.ResponseWriter, body map[string]any, key string, required bool) (string, bool) {
	value, exists := body[key]
	if !exists || value == nil {
		if !required {
			return "", true
		}
		writeValidationErrorKeys(w, []string{key}, `"`+key+`" is required`)
		return "", false
	}
	text, ok := value.(string)
	if !ok {
		writeValidationErrorKeys(w, []string{key}, `"`+key+`" must be a string`)
		return "", false
	}
	if !hexHashPattern.MatchString(text) {
		message := `"` + key + `" must only contain hexadecimal characters`
		if len(text) != 64 {
			message = `"` + key + `" length must be 64 characters long`
		}
		writeValidationErrorKeys(w, []string{key}, message)
		return "", false
	}
	return text, true
}

func requireQueryString(w http.ResponseWriter, query map[string][]string, key string) (string, bool) {
	values, exists := query[key]
	if !exists || len(values) == 0 {
		writeValidationErrorSource(w, "query", []string{key}, `"`+key+`" is required`)
		return "", false
	}
	if values[0] == "" {
		writeValidationErrorSource(w, "query", []string{key}, `"`+key+`" is not allowed to be empty`)
		return "", false
	}
	return values[0], true
}

func requireQueryUUID(w http.ResponseWriter, query map[string][]string, key string) (string, bool) {
	text, ok := requireQueryString(w, query, key)
	if !ok {
		return "", false
	}
	if !uuidPattern.MatchString(text) {
		writeValidationErrorSource(w, "query", []string{key}, `"`+key+`" must be a valid GUID`)
		return "", false
	}
	return text, true
}

// rejectUnknownKeys mirrors Joi's "is not allowed" error for schemas without
// .unknown(true).
func rejectUnknownKeys(w http.ResponseWriter, body map[string]any, allowed []string) bool {
	known := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		known[key] = struct{}{}
	}
	for key := range body {
		if _, ok := known[key]; ok {
			continue
		}
		writeValidationErrorKeys(w, []string{key}, `"`+key+`" is not allowed`)
		return false
	}
	return true
}

var _ = utf8.RuneCountInString
