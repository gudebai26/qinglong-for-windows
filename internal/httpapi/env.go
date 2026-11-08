package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"qinglong-go/internal/i18n"
	"qinglong-go/internal/service"
)

// envNamePattern is the Joi pattern of POST /api/envs.
const envNamePattern = "/^[a-zA-Z_][0-9a-zA-Z_]*$/"

// registerEnvRoutes mounts the 14 routes of back/api/env.ts.
func registerEnvRoutes(router chi.Router, deps Deps) {
	envs := deps.Envs

	router.Get("/api/envs", func(w http.ResponseWriter, r *http.Request) {
		data, err := envs.List(r.Context(), r.URL.Query().Get("searchValue"))
		if err != nil {
			writeEnvError(w, err)
			return
		}
		writeOK(w, data)
	})

	router.Post("/api/envs", func(w http.ResponseWriter, r *http.Request) {
		payload, err := readJSONAny(r)
		if err != nil {
			writeValidationErrorSource(w, "body", []string{""}, `"value" must be an array`)
			return
		}
		list, ok := payload.([]any)
		if !ok {
			writeValidationErrorSource(w, "body", []string{""}, `"value" must be an array`)
			return
		}
		inputs := make([]service.EnvInput, 0, len(list))
		for index, item := range list {
			object, ok := item.(map[string]any)
			if !ok {
				writeValidationErrorSource(w, "body", []string{strconv.Itoa(index)},
					`"[`+strconv.Itoa(index)+`]" must be of type object`)
				return
			}
			prefix := strconv.Itoa(index) + "."
			value, ok := object["value"].(string)
			if !ok {
				if _, exists := object["value"]; !exists || object["value"] == nil {
					writeValidationErrorSource(w, "body", []string{prefix + "value"},
						`"[`+strconv.Itoa(index)+`].value" is required`)
				} else {
					writeValidationErrorSource(w, "body", []string{prefix + "value"},
						`"[`+strconv.Itoa(index)+`].value" must be a string`)
				}
				return
			}
			name, ok := object["name"].(string)
			if !ok {
				if _, exists := object["name"]; !exists || object["name"] == nil {
					writeValidationErrorSource(w, "body", []string{prefix + "name"},
						`"[`+strconv.Itoa(index)+`].name" is required`)
				} else {
					writeValidationErrorSource(w, "body", []string{prefix + "name"},
						`"[`+strconv.Itoa(index)+`].name" must be a string`)
				}
				return
			}
			if !service.IsEnvIdentifier(name) {
				writeValidationErrorSource(w, "body", []string{prefix + "name"},
					`"[`+strconv.Itoa(index)+`].name" with value "`+name+
						`" fails to match the required pattern: `+envNamePattern)
				return
			}
			input := service.EnvInput{Value: value, Name: name}
			if remarks, exists := object["remarks"]; exists && remarks != nil {
				text, ok := remarks.(string)
				if !ok {
					writeValidationErrorSource(w, "body", []string{prefix + "remarks"},
						`"[`+strconv.Itoa(index)+`].remarks" must be a string`)
					return
				}
				input.Remarks = &text
			}
			if labels, exists := object["labels"]; exists && labels != nil {
				items, ok := labels.([]any)
				if !ok {
					writeValidationErrorSource(w, "body", []string{prefix + "labels"},
						`"[`+strconv.Itoa(index)+`].labels" must be an array`)
					return
				}
				input.Labels = stringList(items)
				input.LabelsSet = true
			}
			inputs = append(inputs, input)
		}
		if len(inputs) == 0 {
			// Node answers this one with HTTP 200 and a 400 body code.
			writeJSON(w, http.StatusOK, envelope{Code: 400, Message: i18n.T("参数不正确")})
			return
		}
		data, err := envs.Create(r.Context(), inputs)
		if err != nil {
			writeEnvError(w, err)
			return
		}
		writeOK(w, data)
	})

	router.Put("/api/envs", func(w http.ResponseWriter, r *http.Request) {
		body, ok := validateBody(w, r, nil)
		if !ok {
			return
		}
		if !rejectUnknownKeys(w, body, envUpdateKeys) {
			return
		}
		value, ok := requireBodyString(w, body, "value")
		if !ok {
			return
		}
		name, ok := requireBodyString(w, body, "name")
		if !ok {
			return
		}
		id, ok := requireBodyNumber(w, body, "id")
		if !ok {
			return
		}
		input := service.EnvUpdateInput{ID: id, Value: value, Name: name}
		if remarks, exists := body["remarks"]; exists {
			if remarks == nil {
				input.RemarksSet = true
			} else if text, ok := remarks.(string); ok {
				input.Remarks = &text
				input.RemarksSet = true
			} else {
				writeValidationErrorKeys(w, []string{"remarks"}, `"remarks" must be a string`)
				return
			}
		}
		if labels, exists := body["labels"]; exists {
			if labels == nil {
				input.LabelsSet = true
			} else if items, ok := labels.([]any); ok {
				input.Labels = stringList(items)
				input.LabelsSet = true
			} else {
				writeValidationErrorKeys(w, []string{"labels"}, `"labels" must be an array`)
				return
			}
		}
		data, err := envs.Update(r.Context(), input)
		if err != nil {
			writeEnvError(w, err)
			return
		}
		writeOK(w, data)
	})

	router.Delete("/api/envs", func(w http.ResponseWriter, r *http.Request) {
		ids, ok := decodeIDArray(w, r)
		if !ok {
			return
		}
		if err := envs.Remove(r.Context(), ids); err != nil {
			writeEnvError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, envelope{Code: 200})
	})

	router.Put("/api/envs/{id}/move", func(w http.ResponseWriter, r *http.Request) {
		id, ok := idParam(w, r)
		if !ok {
			return
		}
		body, ok := validateBody(w, r, nil)
		if !ok {
			return
		}
		fromIndex, ok := requireBodyNumber(w, body, "fromIndex")
		if !ok {
			return
		}
		toIndex, ok := requireBodyNumber(w, body, "toIndex")
		if !ok {
			return
		}
		data, err := envs.Move(r.Context(), id, int(fromIndex), int(toIndex))
		if err != nil {
			writeEnvError(w, err)
			return
		}
		writeOK(w, data)
	})

	router.Put("/api/envs/disable", func(w http.ResponseWriter, r *http.Request) {
		ids, ok := decodeIDArray(w, r)
		if !ok {
			return
		}
		if err := envs.SetDisabled(r.Context(), ids, true); err != nil {
			writeEnvError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, envelope{Code: 200})
	})

	router.Put("/api/envs/enable", func(w http.ResponseWriter, r *http.Request) {
		ids, ok := decodeIDArray(w, r)
		if !ok {
			return
		}
		if err := envs.SetDisabled(r.Context(), ids, false); err != nil {
			writeEnvError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, envelope{Code: 200})
	})

	router.Put("/api/envs/name", func(w http.ResponseWriter, r *http.Request) {
		body, ok := validateBody(w, r, nil)
		if !ok {
			return
		}
		// back/api/env.ts leaves ids optional and passes undefined to Sequelize,
		// which answers 500 with this message. Reproduced for contract parity.
		if raw, exists := body["ids"]; !exists || raw == nil {
			writeJSON(w, http.StatusInternalServerError, envelope{
				Code:    http.StatusInternalServerError,
				Message: `WHERE parameter "id" has invalid "undefined" value`,
			})
			return
		}
		ids, ok := requireBodyIDArray(w, body, "ids", false)
		if !ok {
			return
		}
		name, ok := requireBodyString(w, body, "name")
		if !ok {
			return
		}
		if err := envs.UpdateNames(r.Context(), ids, name); err != nil {
			writeEnvError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, envelope{Code: 200})
	})

	router.Get("/api/envs/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := idParam(w, r)
		if !ok {
			return
		}
		data, err := envs.Get(r.Context(), id)
		if err != nil {
			writeEnvError(w, err)
			return
		}
		writeOK(w, data)
	})

	router.Put("/api/envs/pin", func(w http.ResponseWriter, r *http.Request) {
		ids, ok := decodeIDArray(w, r)
		if !ok {
			return
		}
		if err := envs.SetPinned(r.Context(), ids, true); err != nil {
			writeEnvError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, envelope{Code: 200})
	})

	router.Put("/api/envs/unpin", func(w http.ResponseWriter, r *http.Request) {
		ids, ok := decodeIDArray(w, r)
		if !ok {
			return
		}
		if err := envs.SetPinned(r.Context(), ids, false); err != nil {
			writeEnvError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, envelope{Code: 200})
	})

	router.Post("/api/envs/labels", func(w http.ResponseWriter, r *http.Request) {
		ids, labels, ok := decodeEnvLabelBody(w, r)
		if !ok {
			return
		}
		data, err := envs.AddLabels(r.Context(), ids, labels)
		if err != nil {
			writeEnvError(w, err)
			return
		}
		writeOK(w, data)
	})

	router.Delete("/api/envs/labels", func(w http.ResponseWriter, r *http.Request) {
		ids, labels, ok := decodeEnvLabelBody(w, r)
		if !ok {
			return
		}
		data, err := envs.RemoveLabels(r.Context(), ids, labels)
		if err != nil {
			writeEnvError(w, err)
			return
		}
		writeOK(w, data)
	})

	router.Post("/api/envs/upload", func(w http.ResponseWriter, r *http.Request) {
		_, upload, err := parseMultipart(r, "env", deps.Config.TmpPath, true, envUploadLimit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		var payload []byte
		if upload != nil {
			payload = upload.Data
		}
		var parsed any
		if err := json.Unmarshal(payload, &parsed); err != nil {
			// JSON.parse('') throws, and the route answers with 400.
			writeJSON(w, http.StatusBadRequest, envelope{Code: 400, Message: i18n.T("参数错误")})
			return
		}
		list, ok := parsed.([]any)
		if !ok {
			list = []any{parsed}
		}
		inputs := make([]service.EnvInput, 0, len(list))
		valid := len(list) > 0
		for _, item := range list {
			object, ok := item.(map[string]any)
			if !ok {
				valid = false
				break
			}
			name, nameOK := object["name"].(string)
			value, valueOK := object["value"].(string)
			if !nameOK || !service.IsEnvIdentifier(name) || !valueOK || value == "" {
				valid = false
				break
			}
			input := service.EnvInput{Name: name, Value: value}
			if remarks, ok := object["remarks"].(string); ok {
				input.Remarks = &remarks
			}
			if labels, ok := object["labels"].([]any); ok {
				input.Labels = stringList(labels)
				input.LabelsSet = true
			}
			inputs = append(inputs, input)
		}
		if !valid {
			writeJSON(w, http.StatusOK, envelope{
				Code:    400,
				Message: i18n.T("每条数据 name 或者 value 字段不能为空，参考导出文件格式"),
			})
			return
		}
		data, err := envs.Create(r.Context(), inputs)
		if err != nil {
			writeEnvError(w, err)
			return
		}
		writeOK(w, data)
	})
}

// envUpdateKeys is the Joi schema of PUT /api/envs (no .unknown(true)).
var envUpdateKeys = []string{"value", "name", "remarks", "id", "labels"}

func stringList(items []any) []string {
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

// requireBodyNumber validates a required numeric field (Joi.number()).
func requireBodyNumber(w http.ResponseWriter, body map[string]any, key string) (int64, bool) {
	value, exists := body[key]
	if !exists || value == nil {
		writeValidationErrorKeys(w, []string{key}, `"`+key+`" is required`)
		return 0, false
	}
	parsed, ok := asNumber(value)
	if !ok {
		writeValidationErrorKeys(w, []string{key}, `"`+key+`" must be a number`)
		return 0, false
	}
	return parsed, true
}

// requireBodyIDArray validates `ids: Joi.array().items(Joi.number())`.
func requireBodyIDArray(w http.ResponseWriter, body map[string]any, key string, minOne bool) ([]int64, bool) {
	value, exists := body[key]
	if !exists || value == nil {
		writeValidationErrorKeys(w, []string{key}, `"`+key+`" is required`)
		return nil, false
	}
	items, ok := value.([]any)
	if !ok {
		writeValidationErrorKeys(w, []string{key}, `"`+key+`" must be an array`)
		return nil, false
	}
	if minOne && len(items) == 0 {
		writeValidationErrorKeys(w, []string{key}, `"`+key+`" must contain at least 1 items`)
		return nil, false
	}
	ids := make([]int64, 0, len(items))
	for _, item := range items {
		parsed, ok := asNumber(item)
		if !ok {
			writeValidationErrorKeys(w, []string{key}, `"`+key+`" must be an array`)
			return nil, false
		}
		ids = append(ids, parsed)
	}
	return ids, true
}

// decodeEnvLabelBody validates {ids, labels} for the label endpoints.
func decodeEnvLabelBody(w http.ResponseWriter, r *http.Request) ([]int64, []string, bool) {
	body, ok := validateBody(w, r, nil)
	if !ok {
		return nil, nil, false
	}
	ids, ok := requireBodyIDArray(w, body, "ids", true)
	if !ok {
		return nil, nil, false
	}
	value, exists := body["labels"]
	if !exists || value == nil {
		writeValidationErrorKeys(w, []string{"labels"}, `"labels" is required`)
		return nil, nil, false
	}
	items, ok := value.([]any)
	if !ok {
		writeValidationErrorKeys(w, []string{"labels"}, `"labels" must be an array`)
		return nil, nil, false
	}
	if len(items) == 0 {
		writeValidationErrorKeys(w, []string{"labels"}, `"labels" must contain at least 1 items`)
		return nil, nil, false
	}
	labels := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok {
			writeValidationErrorKeys(w, []string{"labels"}, `"labels" must be an array`)
			return nil, nil, false
		}
		trimmed := strings.TrimSpace(text)
		if trimmed == "" {
			writeValidationErrorKeys(w, []string{"labels"}, `"labels[0]" is not allowed to be empty`)
			return nil, nil, false
		}
		labels = append(labels, trimmed)
	}
	return ids, labels, true
}

// writeEnvError maps service errors onto the Node response shapes.
func writeEnvError(w http.ResponseWriter, err error) {
	var statusErr *service.StatusError
	if errors.As(err, &statusErr) {
		writeJSON(w, statusErr.Status, envelope{Code: statusErr.Status, Message: statusErr.Message})
		return
	}
	writeError(w, http.StatusInternalServerError, err.Error())
}
