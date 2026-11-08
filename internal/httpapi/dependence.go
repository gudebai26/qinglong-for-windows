package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"qinglong-go/internal/service"
)

// registerDependenceRoutes mounts the 11 routes of back/api/dependence.ts.
func registerDependenceRoutes(router chi.Router, deps Deps) {
	dependencies := deps.Dependences

	router.Get("/api/dependencies", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		data, err := dependencies.List(r.Context(), service.DependenceListParams{
			SearchValue: query.Get("searchValue"),
			Type:        query.Get("type"),
			Status:      query.Get("status"),
		})
		if err != nil {
			writeSubscriptionError(w, err)
			return
		}
		writeOK(w, data)
	})

	router.Post("/api/dependencies/refresh", func(w http.ResponseWriter, r *http.Request) {
		body, ok := validateBody(w, r, nil)
		if !ok {
			return
		}
		kind, _ := body["type"].(string)
		if kind != "nodejs" && kind != "python3" {
			writeValidationErrorKeys(w, []string{"type"}, `"type" must be one of [nodejs, python3]`)
			return
		}
		job, accepted := dependencies.StartRefresh(kind)
		if !accepted {
			writeJSON(w, http.StatusConflict, envelope{
				Code:    http.StatusConflict,
				Message: "Another dependency refresh is already running",
			})
			return
		}
		writeOK(w, job)
	})

	router.Get("/api/dependencies/refresh/active", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		job := dependencies.GetActiveRefreshJob()
		if job == nil {
			writeJSON(w, http.StatusOK, map[string]any{"code": 200, "data": nil})
			return
		}
		writeOK(w, job)
	})

	router.Get("/api/dependencies/refresh/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if !isUUID(id) {
			writeValidationErrorSource(w, "params", []string{"id"}, `"id" must be a valid GUID`)
			return
		}
		job, ok := dependencies.GetRefreshJob(id)
		if !ok {
			writeJSON(w, http.StatusNotFound, envelope{
				Code:    http.StatusNotFound,
				Message: "Dependency refresh job not found",
			})
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeOK(w, job)
	})

	router.Post("/api/dependencies", func(w http.ResponseWriter, r *http.Request) {
		inputs, ok := parseDependenceArray(w, r)
		if !ok {
			return
		}
		data, err := dependencies.Create(r.Context(), inputs)
		if err != nil {
			writeSubscriptionError(w, err)
			return
		}
		writeOK(w, data)
	})

	router.Put("/api/dependencies", func(w http.ResponseWriter, r *http.Request) {
		body, ok := validateBody(w, r, nil)
		if !ok {
			return
		}
		keys := make([]string, 0, 3)
		messages := make([]string, 0, 3)
		name, nameOK := body["name"].(string)
		if !nameOK || name == "" {
			keys = append(keys, "name")
			messages = append(messages, `"name" is required`)
		}
		id, idOK := asNumber(body["id"])
		if !idOK {
			keys = append(keys, "id")
			messages = append(messages, `"id" is required`)
		}
		depType, typeOK := asNumber(body["type"])
		if !typeOK {
			keys = append(keys, "type")
			messages = append(messages, `"type" is required`)
		}
		remark := ""
		if raw, exists := body["remark"]; exists && raw != nil {
			text, ok := raw.(string)
			if !ok {
				keys = append(keys, "remark")
				messages = append(messages, `"remark" must be a string`)
			}
			remark = text
		}
		if len(keys) > 0 {
			writeValidationErrorKeys(w, keys, strings.Join(messages, ". "))
			return
		}
		data, err := dependencies.Update(r.Context(), id, service.DependenceInput{
			Name:   name,
			Type:   depType,
			Remark: remark,
		})
		if err != nil {
			writeSubscriptionError(w, err)
			return
		}
		writeOK(w, data)
	})

	router.Delete("/api/dependencies", func(w http.ResponseWriter, r *http.Request) {
		ids, ok := decodeIDArray(w, r)
		if !ok {
			return
		}
		data, err := dependencies.Remove(r.Context(), ids, false)
		if err != nil {
			writeSubscriptionError(w, err)
			return
		}
		writeOK(w, data)
	})

	router.Delete("/api/dependencies/force", func(w http.ResponseWriter, r *http.Request) {
		ids, ok := decodeIDArray(w, r)
		if !ok {
			return
		}
		data, err := dependencies.Remove(r.Context(), ids, true)
		if err != nil {
			writeSubscriptionError(w, err)
			return
		}
		writeOK(w, data)
	})

	router.Put("/api/dependencies/reinstall", func(w http.ResponseWriter, r *http.Request) {
		ids, ok := decodeIDArray(w, r)
		if !ok {
			return
		}
		data, err := dependencies.ReInstall(r.Context(), ids)
		if err != nil {
			writeSubscriptionError(w, err)
			return
		}
		writeOK(w, data)
	})

	router.Put("/api/dependencies/cancel", func(w http.ResponseWriter, r *http.Request) {
		ids, ok := decodeIDArray(w, r)
		if !ok {
			return
		}
		if err := dependencies.Cancel(r.Context(), ids); err != nil {
			writeSubscriptionError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, envelope{Code: 200})
	})

	router.Get("/api/dependencies/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := idParam(w, r)
		if !ok {
			return
		}
		data, err := dependencies.Get(r.Context(), id)
		if err != nil {
			writeSubscriptionError(w, err)
			return
		}
		writeOK(w, data)
	})
}

// parseDependenceArray validates the bulk create body.
func parseDependenceArray(w http.ResponseWriter, r *http.Request) ([]service.DependenceInput, bool) {
	payload, err := readJSONAny(r)
	if err != nil {
		writeValidationErrorSource(w, "body", []string{""}, `"value" must be an array`)
		return nil, false
	}
	list, ok := payload.([]any)
	if !ok {
		writeValidationErrorSource(w, "body", []string{""}, `"value" must be an array`)
		return nil, false
	}
	inputs := make([]service.DependenceInput, 0, len(list))
	for index, item := range list {
		// celebrate reports array item failures with a "0.type" key and a
		// "[0].type" message.
		key := strconv.Itoa(index) + "."
		label := "[" + strconv.Itoa(index) + "]."
		object, ok := item.(map[string]any)
		if !ok {
			writeValidationErrorSource(w, "body", []string{strconv.Itoa(index)}, `"value" must be an object`)
			return nil, false
		}
		name, nameOK := object["name"].(string)
		if !nameOK || name == "" {
			writeValidationErrorSource(w, "body", []string{key + "name"}, `"`+label+`name" is required`)
			return nil, false
		}
		depType, typeOK := asNumber(object["type"])
		if !typeOK {
			writeValidationErrorSource(w, "body", []string{key + "type"}, `"`+label+`type" is required`)
			return nil, false
		}
		remark := ""
		if raw, exists := object["remark"]; exists && raw != nil {
			text, ok := raw.(string)
			if !ok {
				writeValidationErrorSource(w, "body", []string{key + "remark"}, `"`+label+`remark" must be a string`)
				return nil, false
			}
			remark = text
		}
		inputs = append(inputs, service.DependenceInput{Name: name, Type: depType, Remark: remark})
	}
	return inputs, true
}

// isUUID matches Joi.string().uuid() closely enough for the refresh route.
func isUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, r := range value {
		switch index {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
				return false
			}
		}
	}
	return true
}
