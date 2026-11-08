package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"qinglong-go/internal/service"
)

// registerSubscriptionRoutes mounts the 12 routes of back/api/subscription.ts.
func registerSubscriptionRoutes(router chi.Router, deps Deps) {
	subs := deps.Subscriptions

	router.Get("/api/subscriptions", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		// Node treats an empty `ids` as "no filter" (`ids ? ... : undefined`).
		_, idsSet := query["ids"]
		idsSet = idsSet && query.Get("ids") != ""
		data, err := subs.List(r.Context(), service.SubscriptionListParams{
			SearchValue: query.Get("searchValue"),
			IDs:         query.Get("ids"),
			IDsSet:      idsSet,
		})
		if err != nil {
			writeSubscriptionError(w, err)
			return
		}
		writeOK(w, data)
	})

	router.Post("/api/subscriptions", func(w http.ResponseWriter, r *http.Request) {
		input, ok := parseSubscriptionInput(w, r, false)
		if !ok {
			return
		}
		if !service.ValidateSubscriptionSchedule(input.Schedule) {
			writeJSON(w, http.StatusOK, envelope{Code: 400, Message: "param schedule error"})
			return
		}
		data, err := subs.Create(r.Context(), input)
		if err != nil {
			writeSubscriptionError(w, err)
			return
		}
		writeOK(w, data)
	})

	router.Put("/api/subscriptions", func(w http.ResponseWriter, r *http.Request) {
		input, ok := parseSubscriptionInput(w, r, true)
		if !ok {
			return
		}
		if !service.ValidateSubscriptionSchedule(input.Schedule) {
			writeJSON(w, http.StatusOK, envelope{Code: 400, Message: "param schedule error"})
			return
		}
		data, err := subs.Update(r.Context(), input)
		if err != nil {
			writeSubscriptionError(w, err)
			return
		}
		writeOK(w, data)
	})

	router.Put("/api/subscriptions/run", func(w http.ResponseWriter, r *http.Request) {
		ids, ok := decodeIDArray(w, r)
		if !ok {
			return
		}
		if err := subs.Run(r.Context(), ids); err != nil {
			writeSubscriptionError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, envelope{Code: 200})
	})

	router.Put("/api/subscriptions/stop", func(w http.ResponseWriter, r *http.Request) {
		ids, ok := decodeIDArray(w, r)
		if !ok {
			return
		}
		if err := subs.Stop(r.Context(), ids); err != nil {
			writeSubscriptionError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, envelope{Code: 200})
	})

	router.Put("/api/subscriptions/disable", func(w http.ResponseWriter, r *http.Request) {
		ids, ok := decodeIDArray(w, r)
		if !ok {
			return
		}
		if err := subs.SetDisabled(r.Context(), ids, true); err != nil {
			writeSubscriptionError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, envelope{Code: 200})
	})

	router.Put("/api/subscriptions/enable", func(w http.ResponseWriter, r *http.Request) {
		ids, ok := decodeIDArray(w, r)
		if !ok {
			return
		}
		if err := subs.SetDisabled(r.Context(), ids, false); err != nil {
			writeSubscriptionError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, envelope{Code: 200})
	})

	router.Put("/api/subscriptions/status", func(w http.ResponseWriter, r *http.Request) {
		body, ok := validateBody(w, r, nil)
		if !ok {
			return
		}
		// Joi.string() rejects an empty string, so an empty status/pid/log_path
		// is a validation error rather than a silent no-op.
		keys := make([]string, 0, 2)
		messages := make([]string, 0, 2)
		rawStatus, exists := body["status"]
		if !exists || rawStatus == nil {
			keys = append(keys, "status")
			messages = append(messages, `"status" is required`)
		} else if text, ok := rawStatus.(string); !ok {
			// back/api/subscription.ts declares status as Joi.string().
			keys = append(keys, "status")
			messages = append(messages, `"status" must be a string`)
		} else if text == "" {
			keys = append(keys, "status")
			messages = append(messages, `"status" is not allowed to be empty`)
		}
		for _, key := range []string{"pid", "log_path"} {
			raw, exists := body[key]
			if !exists || raw == nil {
				continue
			}
			if text, ok := raw.(string); ok && text == "" {
				keys = append(keys, key)
				messages = append(messages, `"`+key+`" is not allowed to be empty`)
			}
		}
		if len(keys) > 0 {
			writeValidationErrorKeys(w, keys, strings.Join(messages, ". "))
			return
		}

		input := &service.SubscriptionStatusInput{}
		if ids, ok := parseIDListField(body["ids"]); ok {
			input.IDs = ids
		}
		if text, ok := body["status"].(string); ok {
			if parsed, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64); err == nil {
				input.Status = parsed
			}
		}
		if raw, exists := body["pid"]; exists && raw != nil {
			switch typed := raw.(type) {
			case string:
				if parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64); err == nil {
					input.Pid = &parsed
				}
			default:
				if parsed, ok := asNumber(raw); ok {
					input.Pid = &parsed
				}
			}
		}
		if raw, exists := body["log_path"]; exists && raw != nil {
			if text, ok := raw.(string); ok {
				input.LogPath = text
			}
		}
		if value, ok := asNumber(body["last_running_time"]); ok {
			input.LastRunningTime = value
		}
		if value, ok := asNumber(body["last_execution_time"]); ok {
			input.LastExecutionTime = value
		}
		affected, err := subs.SetStatus(r.Context(), input)
		if err != nil {
			writeSubscriptionError(w, err)
			return
		}
		writeOK(w, []int64{affected})
	})

	router.Delete("/api/subscriptions", func(w http.ResponseWriter, r *http.Request) {
		ids, ok := decodeIDArray(w, r)
		if !ok {
			return
		}
		force := strings.EqualFold(r.URL.Query().Get("force"), "true")
		if err := subs.Remove(r.Context(), ids, force); err != nil {
			writeSubscriptionError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, envelope{Code: 200})
	})

	router.Get("/api/subscriptions/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := idParam(w, r)
		if !ok {
			return
		}
		data, err := subs.Get(r.Context(), id)
		if err != nil {
			writeSubscriptionError(w, err)
			return
		}
		writeOK(w, data)
	})

	router.Get("/api/subscriptions/{id}/log", func(w http.ResponseWriter, r *http.Request) {
		id, ok := idParam(w, r)
		if !ok {
			return
		}
		offset, ok := queryIntParam(w, r, "offset", 0, 0)
		if !ok {
			return
		}
		limit, ok := queryIntParam(w, r, "limit", 1, service.MaxLogChunkBytes)
		if !ok {
			return
		}
		tail := strings.EqualFold(r.URL.Query().Get("tail"), "true")
		result, err := subs.Log(r.Context(), id, offset, limit, tail)
		if err != nil {
			writeSubscriptionError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, subscriptionLogBody{
			Code:       http.StatusOK,
			Data:       result.Content,
			Content:    result.Content,
			Offset:     result.Offset,
			NextOffset: result.NextOffset,
			Total:      result.Total,
			Truncated:  result.Truncated,
		})
	})

	router.Get("/api/subscriptions/{id}/logs", func(w http.ResponseWriter, r *http.Request) {
		id, ok := idParam(w, r)
		if !ok {
			return
		}
		data, err := subs.Logs(r.Context(), id)
		if err != nil {
			writeSubscriptionError(w, err)
			return
		}
		writeOK(w, data)
	})
}

// subscriptionLogBody keeps the exact key order of
// `res.send({code, data, ...result})`: the spread re-adds `content` because
// the read result carries it alongside `data`.
type subscriptionLogBody struct {
	Code       int    `json:"code"`
	Data       string `json:"data"`
	Content    string `json:"content"`
	Offset     int64  `json:"offset"`
	NextOffset int64  `json:"nextOffset"`
	Total      int64  `json:"total"`
	Truncated  bool   `json:"truncated"`
}

// parseSubscriptionInput validates the create/update body. It follows the Joi
// schemas of back/api/subscription.ts closely enough that the same client
// payloads are accepted and the same ones are rejected.
func parseSubscriptionInput(w http.ResponseWriter, r *http.Request, requireID bool) (*service.SubscriptionInput, bool) {
	body, ok := validateBody(w, r, nil)
	if !ok {
		return nil, false
	}
	keys := make([]string, 0, 3)
	messages := make([]string, 0, 3)
	fail := func(key, message string) {
		keys = append(keys, key)
		messages = append(messages, message)
	}

	input := &service.SubscriptionInput{}
	readString := func(key string, required bool) string {
		raw, exists := body[key]
		if !exists || raw == nil {
			if required {
				fail(key, `"`+key+`" is required`)
			}
			return ""
		}
		text, ok := raw.(string)
		if !ok {
			fail(key, `"`+key+`" must be a string`)
			return ""
		}
		return text
	}
	readOptionalString := func(key string) *string {
		raw, exists := body[key]
		if !exists {
			return nil
		}
		if raw == nil {
			return nil
		}
		text, ok := raw.(string)
		if !ok {
			fail(key, `"`+key+`" must be a string`)
			return nil
		}
		return &text
	}
	readOptionalBool := func(key string) *bool {
		raw, exists := body[key]
		if !exists || raw == nil {
			return nil
		}
		value, ok := raw.(bool)
		if !ok {
			fail(key, `"`+key+`" must be a boolean`)
			return nil
		}
		return &value
	}

	input.Type = readString("type", true)
	input.URL = readString("url", true)
	input.Alias = readString("alias", true)
	input.ScheduleType = readString("schedule_type", !requireID)
	input.Schedule = readString("schedule", false)
	input.Name = readOptionalString("name")
	input.Whitelist = readOptionalString("whitelist")
	input.Blacklist = readOptionalString("blacklist")
	input.Branch = readOptionalString("branch")
	input.Dependences = readOptionalString("dependences")
	input.PullType = readOptionalString("pull_type")
	input.Extensions = readOptionalString("extensions")
	input.SubBefore = readOptionalString("sub_before")
	input.SubAfter = readOptionalString("sub_after")
	input.Proxy = readOptionalString("proxy")
	input.AutoAddCron = readOptionalBool("autoAddCron")
	input.AutoDelCron = readOptionalBool("autoDelCron")

	if raw, exists := body["pull_option"]; exists && raw != nil {
		object, ok := raw.(map[string]any)
		if !ok {
			fail("pull_option", `"pull_option" must be of type object`)
		} else {
			input.PullOption = object
		}
	}
	if raw, exists := body["interval_schedule"]; exists && raw != nil {
		object, ok := raw.(map[string]any)
		if !ok {
			fail("interval_schedule", `"interval_schedule" must be of type object`)
		} else {
			schedule := &service.IntervalSchedule{}
			if kind, ok := object["type"].(string); ok && kind != "" {
				schedule.Type = kind
			} else {
				fail("interval_schedule.type", `"interval_schedule.type" is required`)
			}
			value, valueOK := asNumber(object["value"])
			if !valueOK {
				fail("interval_schedule.value", `"interval_schedule.value" must be a number`)
			} else if value < 1 {
				fail("interval_schedule.value", `"interval_schedule.value" must be greater than or equal to 1`)
			} else {
				schedule.Value = value
			}
			input.IntervalSchedule = schedule
		}
	}

	if requireID {
		raw, exists := body["id"]
		if !exists || raw == nil {
			fail("id", `"id" is required`)
		} else if value, ok := asNumber(raw); ok {
			input.ID = value
		} else {
			fail("id", `"id" must be a number`)
		}
	}

	if len(keys) > 0 {
		// Joi runs with abortEarly, so celebrate reports only the first failing
		// key. The collection order above follows the schema order, therefore
		// the first entry is the one Node would name.
		writeValidationErrorKeys(w, keys[:1], messages[0])
		return nil, false
	}
	return input, true
}

// parseIDListField converts a JSON array of numbers.
func parseIDListField(raw any) ([]int64, bool) {
	if raw == nil {
		return nil, false
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, false
	}
	ids := make([]int64, 0, len(list))
	for _, item := range list {
		value, ok := asNumber(item)
		if !ok {
			return nil, false
		}
		ids = append(ids, value)
	}
	return ids, true
}

// writeSubscriptionError maps the service errors onto the Node responses.
func writeSubscriptionError(w http.ResponseWriter, err error) {
	switch typed := err.(type) {
	case *service.BusinessError:
		// Node answers business failures with HTTP 200 and a non-200 body code.
		writeJSON(w, http.StatusOK, envelope{Code: typed.Code, Message: typed.Message})
	case *service.ValidationError:
		writeValidationErrorKeys(w, typed.Keys, typed.Message)
	case *service.ConflictError:
		writeJSON(w, http.StatusConflict, envelope{Code: http.StatusConflict, Message: typed.Message})
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
