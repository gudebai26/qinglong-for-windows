package httpapi

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"qinglong-go/internal/service"
)

// registerDashboardRoutes mounts the ten aggregation endpoints of
// back/api/dashboard.ts under /api/dashboard.
func registerDashboardRoutes(router chi.Router, deps Deps) {
	router.Post("/api/dashboard/record", func(w http.ResponseWriter, r *http.Request) {
		// The shell wrapper posts this without a validator, so a malformed body
		// is treated as an empty object instead of a 400.
		body := readLooseObject(r)
		writeJSON(w, http.StatusOK, deps.Dashboard.Record(r.Context(), body))
	})

	router.Get("/api/dashboard/overview", func(w http.ResponseWriter, r *http.Request) {
		data, err := deps.Dashboard.Overview(r.Context())
		if err != nil {
			writeCronError(w, err)
			return
		}
		writeOK(w, data)
	})

	router.Get("/api/dashboard/successes", func(w http.ResponseWriter, r *http.Request) {
		data, err := deps.Dashboard.TaskResults(r.Context(), true)
		if err != nil {
			writeCronError(w, err)
			return
		}
		writeOK(w, data)
	})

	router.Get("/api/dashboard/failures", func(w http.ResponseWriter, r *http.Request) {
		data, err := deps.Dashboard.TaskResults(r.Context(), false)
		if err != nil {
			writeCronError(w, err)
			return
		}
		writeOK(w, data)
	})

	router.Get("/api/dashboard/trend", func(w http.ResponseWriter, r *http.Request) {
		days := 7
		if raw := strings.TrimSpace(r.URL.Query().Get("days")); raw != "" {
			if parsed, err := strconv.Atoi(raw); err == nil && parsed != 0 {
				days = parsed
			}
		}
		data, err := deps.Dashboard.Trend(r.Context(), days)
		if err != nil {
			writeCronError(w, err)
			return
		}
		writeOK(w, data)
	})

	router.Get("/api/dashboard/top-time", func(w http.ResponseWriter, r *http.Request) {
		data, err := deps.Dashboard.TopTime(r.Context())
		if err != nil {
			writeCronError(w, err)
			return
		}
		writeOK(w, data)
	})

	router.Get("/api/dashboard/top-count", func(w http.ResponseWriter, r *http.Request) {
		data, err := deps.Dashboard.TopCount(r.Context())
		if err != nil {
			writeCronError(w, err)
			return
		}
		writeOK(w, data)
	})

	router.Get("/api/dashboard/runtime", func(w http.ResponseWriter, r *http.Request) {
		data, err := deps.Dashboard.Runtime(r.Context())
		if err != nil {
			writeCronError(w, err)
			return
		}
		writeOK(w, data)
	})

	router.Get("/api/dashboard/labels", func(w http.ResponseWriter, r *http.Request) {
		data, err := deps.Dashboard.Labels(r.Context())
		if err != nil {
			writeCronError(w, err)
			return
		}
		writeOK(w, data)
	})

	router.Get("/api/dashboard/system", func(w http.ResponseWriter, r *http.Request) {
		writeOK(w, deps.Dashboard.System())
	})
}

// readLooseObject decodes a JSON object and falls back to an empty map, which
// is how the dashboard handlers behave when they fail to validate input.
func readLooseObject(r *http.Request) map[string]any {
	raw, err := io.ReadAll(r.Body)
	if err != nil || len(strings.TrimSpace(string(raw))) == 0 {
		return map[string]any{}
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var payload any
	if err := decoder.Decode(&payload); err != nil {
		return map[string]any{}
	}
	object, ok := payload.(map[string]any)
	if !ok {
		return map[string]any{}
	}
	return object
}

// registerRetentionRoutes mounts the three storage-retention endpoints of
// back/api/retention.ts.
func registerRetentionRoutes(router chi.Router, deps Deps) {
	router.Put("/api/system/storage-retention/config", func(w http.ResponseWriter, r *http.Request) {
		body, ok := readJSONObject(w, r)
		if !ok {
			return
		}
		policy, ok := retentionPolicyBody(w, body)
		if !ok {
			return
		}
		data, err := deps.System.UpdatePolicy(r.Context(), policy)
		if err != nil {
			writeCronError(w, err)
			return
		}
		writeOK(w, data)
	})

	router.Post("/api/system/storage-retention/preview", func(w http.ResponseWriter, r *http.Request) {
		body, ok := readJSONObject(w, r)
		if !ok {
			return
		}
		request, ok := cleanupRequestBody(w, body, false)
		if !ok {
			return
		}
		data, err := deps.System.PreviewRetention(r.Context(), request)
		if err != nil {
			writeCronError(w, err)
			return
		}
		writeOK(w, data)
	})

	router.Post("/api/system/storage-retention/cleanup", func(w http.ResponseWriter, r *http.Request) {
		body, ok := readJSONObject(w, r)
		if !ok {
			return
		}
		request, ok := cleanupRequestBody(w, body, true)
		if !ok {
			return
		}
		data, err := deps.System.CleanupRetention(r.Context(), request)
		if err != nil {
			writeCronError(w, err)
			return
		}
		writeOK(w, data)
	})
}

// retentionPolicyBody enforces the two required day windows.
func retentionPolicyBody(w http.ResponseWriter, body map[string]any) (service.RetentionPolicy, bool) {
	policy := service.RetentionPolicy{}
	keys := make([]string, 0, 2)
	messages := make([]string, 0, 2)
	for _, field := range []struct {
		name string
		set  func(int64)
	}{
		{"runningInstanceRetentionDays", func(v int64) { policy.RunningInstanceRetentionDays = v }},
		{"cronStatRetentionDays", func(v int64) { policy.CronStatRetentionDays = v }},
	} {
		raw, exists := body[field.name]
		if !exists || raw == nil {
			keys = append(keys, field.name)
			messages = append(messages, `"`+field.name+`" is required`)
			continue
		}
		number, ok := floatValueOf(raw)
		if !ok {
			keys = append(keys, field.name)
			messages = append(messages, `"`+field.name+`" must be a number`)
			continue
		}
		if number < 0 || number > service.MaxRetentionDays {
			keys = append(keys, field.name)
			messages = append(messages, `"`+field.name+`" must be less than or equal to `+
				strconv.Itoa(service.MaxRetentionDays))
			continue
		}
		field.set(int64(number))
	}
	if len(keys) > 0 {
		// Joi aborts on the first failing key.
		writeValidationErrorKeys(w, keys[:1], messages[0])
		return policy, false
	}
	return policy, true
}

// cleanupRequestBody validates the preview/cleanup payload.
func cleanupRequestBody(w http.ResponseWriter, body map[string]any, requireConfirmation bool) (service.StorageCleanupRequest, bool) {
	policy, ok := retentionPolicyBody(w, body)
	if !ok {
		return service.StorageCleanupRequest{}, false
	}
	request := service.StorageCleanupRequest{RetentionPolicy: policy}

	if raw, exists := body["dependenceCacheTypes"]; exists && raw != nil {
		list, ok := raw.([]any)
		if !ok {
			writeValidationErrorKeys(w, []string{"dependenceCacheTypes"}, `"dependenceCacheTypes" must be an array`)
			return request, false
		}
		for index, item := range list {
			text, ok := item.(string)
			if !ok || (text != "node" && text != "python3") {
				writeValidationErrorKeys(w, []string{"dependenceCacheTypes." + strconv.Itoa(index)},
					`"dependenceCacheTypes[`+strconv.Itoa(index)+`]" must be one of [node, python3]`)
				return request, false
			}
			request.DependenceCacheTypes = append(request.DependenceCacheTypes, text)
		}
	}
	if raw, exists := body["compactDatabase"]; exists && raw != nil {
		flag, ok := raw.(bool)
		if !ok {
			writeValidationErrorKeys(w, []string{"compactDatabase"}, `"compactDatabase" must be a boolean`)
			return request, false
		}
		request.CompactDatabase = flag
	}
	if requireConfirmation {
		raw, exists := body["confirmation"]
		if !exists || raw == nil {
			writeValidationErrorKeys(w, []string{"confirmation"}, `"confirmation" is required`)
			return request, false
		}
		text, ok := raw.(string)
		if !ok || text != "CLEAN" {
			writeValidationErrorKeys(w, []string{"confirmation"}, `"confirmation" must be [CLEAN]`)
			return request, false
		}
	}
	return request, true
}

// registerClientIPRoutes mounts the three client-IP endpoints.
func registerClientIPRoutes(router chi.Router, deps Deps) {
	router.Get("/api/system/client-ip/config", func(w http.ResponseWriter, r *http.Request) {
		data, err := deps.System.ClientIPConfigFor(r.Context())
		if err != nil {
			writeCronError(w, err)
			return
		}
		writeOK(w, data)
	})

	router.Put("/api/system/client-ip/config", func(w http.ResponseWriter, r *http.Request) {
		body, ok := readJSONObject(w, r)
		if !ok {
			return
		}
		raw, exists := body["trustProxy"]
		if !exists || raw == nil {
			writeValidationErrorKeys(w, []string{"trustProxy"}, `"trustProxy" is required`)
			return
		}
		value, ok := raw.(string)
		if !ok {
			writeValidationErrorKeys(w, []string{"trustProxy"}, `"trustProxy" must be a string`)
			return
		}
		if len([]rune(value)) > 500 {
			writeValidationErrorKeys(w, []string{"trustProxy"},
				`"trustProxy" length must be less than or equal to 500 characters long`)
			return
		}
		data, reply := deps.System.UpdateTrustProxy(r.Context(), value)
		if reply != nil {
			// Node answers business failures with HTTP 200.
			writeReply(w, reply)
			return
		}
		writeOK(w, data)
	})

	router.Get("/api/system/client-ip/diagnose", func(w http.ResponseWriter, r *http.Request) {
		forwarded := make([]string, 0, 4)
		for _, header := range r.Header.Values("X-Forwarded-For") {
			for _, part := range strings.Split(header, ",") {
				trimmed := strings.TrimSpace(part)
				if trimmed != "" {
					forwarded = append(forwarded, trimmed)
				}
			}
		}
		remote := r.RemoteAddr
		if host, _, err := net.SplitHostPort(remote); err == nil {
			remote = host
		}
		data, reply := deps.System.DiagnoseClientIP(r.Context(), remote, forwarded)
		if reply != nil {
			writeReply(w, reply)
			return
		}
		writeOK(w, data)
	})
}
