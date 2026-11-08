package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"qinglong-go/internal/service"
)

// registerOpenRoutes mounts the six app/token endpoints of back/api/open.ts.
// The /open/* aliases reach the same handlers through the global rewrite.
func registerOpenRoutes(router chi.Router, deps Deps) {
	tokenLimiter := NewRateLimiter(100, 15*time.Minute, deps.Config.TrustProxy)

	router.Get("/api/apps", func(w http.ResponseWriter, r *http.Request) {
		data, err := deps.Open.List(r.Context())
		if err != nil {
			writeOpenError(w, err)
			return
		}
		writeOK(w, data)
	})

	router.Post("/api/apps", func(w http.ResponseWriter, r *http.Request) {
		body, ok := readJSONObject(w, r)
		if !ok {
			return
		}
		name, ok := optionalString(w, body, "name")
		if !ok {
			return
		}
		if name == "system" {
			writeValidationErrorKeys(w, []string{"name"}, `"name" contains an invalid value`)
			return
		}
		scopes, ok := stringArrayField(w, body, "scopes")
		if !ok {
			return
		}
		// Joi's array() rejects an empty list with the generic "min" message.
		if len(scopes) == 0 {
			writeValidationErrorKeys(w, []string{"scopes"},
				`"scopes" does not contain 1 required value(s)`)
			return
		}
		data, err := deps.Open.Create(r.Context(), name, scopes)
		if err != nil {
			writeOpenError(w, err)
			return
		}
		writeOK(w, data)
	})

	router.Put("/api/apps", func(w http.ResponseWriter, r *http.Request) {
		body, ok := readJSONObject(w, r)
		if !ok {
			return
		}
		// id is required; Joi rejects a missing or non-numeric id before the
		// handler runs.
		rawID, exists := body["id"]
		if !exists || rawID == nil {
			writeValidationErrorKeys(w, []string{"id"}, `"id" is required`)
			return
		}
		id, ok := numberValue(rawID)
		if !ok {
			writeValidationErrorKeys(w, []string{"id"}, `"id" must be a number`)
			return
		}
		name, ok := optionalString(w, body, "name")
		if !ok {
			return
		}
		var namePtr *string
		if _, exists := body["name"]; exists {
			namePtr = &name
		}
		scopes, ok := stringArrayField(w, body, "scopes")
		if !ok {
			return
		}
		var scopesPtr *[]string
		if _, exists := body["scopes"]; exists {
			scopesPtr = &scopes
		}
		data, err := deps.Open.Update(r.Context(), id, namePtr, scopesPtr)
		if err != nil {
			writeOpenError(w, err)
			return
		}
		writeOK(w, data)
	})

	router.Delete("/api/apps", func(w http.ResponseWriter, r *http.Request) {
		raw, ok := readJSONArray(w, r)
		if !ok {
			return
		}
		ids := make([]int64, 0, len(raw))
		for index, item := range raw {
			value, ok := numberValue(item)
			if !ok {
				writeValidationErrorKeys(w, []string{strconv.Itoa(index)}, `"`+"["+strconv.Itoa(index)+"]"+`" must be a number`)
				return
			}
			ids = append(ids, value)
		}
		if err := deps.Open.Remove(r.Context(), ids); err != nil {
			writeOpenError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, envelope{Code: http.StatusOK})
	})

	router.Put("/api/apps/{id}/reset-secret", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			writeValidationErrorKeys(w, []string{"id"}, `"id" must be a number`)
			return
		}
		data, err := deps.Open.ResetSecret(r.Context(), id)
		if err != nil {
			writeOpenError(w, err)
			return
		}
		if data == nil {
			writeReply(w, &service.Reply{Code: 200})
			return
		}
		writeOK(w, data)
	})

	router.With(tokenLimiter.Handler).Get("/api/auth/token", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		clientID := query.Get("client_id")
		clientSecret := query.Get("client_secret")
		// Joi reports the first missing required query parameter.
		if clientID == "" {
			writeValidationErrorSource(w, "query", []string{"client_id"}, `"client_id" is required`)
			return
		}
		if clientSecret == "" {
			writeValidationErrorSource(w, "query", []string{"client_secret"}, `"client_secret" is required`)
			return
		}
		result, err := deps.Open.AuthToken(r.Context(), clientID, clientSecret)
		if err != nil {
			writeOpenError(w, err)
			return
		}
		writeReply(w, result)
	})
}

// writeOpenError maps the service errors onto the Node responses. A thrown
// error with a status becomes that HTTP status (403 for the reserved app),
// while a returned {code,message} stays HTTP 200.
func writeOpenError(w http.ResponseWriter, err error) {
	switch typed := err.(type) {
	case *service.BusinessError:
		writeJSON(w, http.StatusOK, envelope{Code: typed.Code, Message: typed.Message})
	case *service.NotFoundError:
		status := typed.Status
		if status == 0 {
			status = http.StatusInternalServerError
		}
		writeJSON(w, status, envelope{Code: status, Message: typed.Message})
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

// readJSONObject decodes a body that must be a JSON object.
func readJSONObject(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	body, err := readJSONBody(r)
	if err != nil {
		if err == errNotAnObject {
			writeValidationError(w, "value", `"value" must be of type object`)
			return nil, false
		}
		writeJSON(w, http.StatusBadRequest, envelope{Code: http.StatusBadRequest, Message: "Unexpected token in JSON"})
		return nil, false
	}
	return body, true
}

// readJSONArray decodes a body that must be a JSON array (DELETE /api/apps).
func readJSONArray(w http.ResponseWriter, r *http.Request) ([]any, bool) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Unexpected token in JSON")
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var payload any
	if err := dec.Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, "Unexpected token in JSON")
		return nil, false
	}
	list, ok := payload.([]any)
	if !ok {
		// celebrate reports the root key as "" for a body-level array.
		writeValidationError(w, "", `"value" must be an array`)
		return nil, false
	}
	return list, true
}

// optionalString reads an optional string field, reporting Joi's type error.
func optionalString(w http.ResponseWriter, body map[string]any, key string) (string, bool) {
	value, exists := body[key]
	if !exists || value == nil {
		return "", true
	}
	text, ok := value.(string)
	if !ok {
		writeValidationErrorKeys(w, []string{key}, `"`+key+`" must be a string`)
		return "", false
	}
	return text, true
}

// stringArrayField reads an optional array of strings.
func stringArrayField(w http.ResponseWriter, body map[string]any, key string) ([]string, bool) {
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
		if text == "apps" {
			writeValidationErrorKeys(w, []string{key + "." + strconv.Itoa(index)},
				`"`+key+"["+strconv.Itoa(index)+`]" contains an invalid value`)
			return nil, false
		}
		out = append(out, text)
	}
	return out, true
}

// numberValue converts a decoded JSON number into an int64.
func numberValue(value any) (int64, bool) {
	switch typed := value.(type) {
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, false
		}
		return parsed, true
	case float64:
		return int64(typed), true
	case int64:
		return typed, true
	case int:
		return int64(typed), true
	}
	return 0, false
}
