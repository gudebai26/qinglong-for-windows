package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"qinglong-go/internal/service"
)

// registerCronRoutes mounts the 27 routes of back/api/cron.ts.
func registerCronRoutes(router chi.Router, deps Deps) {
	cron := deps.Cron

	// ---- views -----------------------------------------------------------
	router.Get("/api/crons/views", func(w http.ResponseWriter, r *http.Request) {
		data, err := cron.ListCronViews(r.Context())
		if err != nil {
			writeCronError(w, err)
			return
		}
		writeOK(w, data)
	})

	router.Post("/api/crons/views", func(w http.ResponseWriter, r *http.Request) {
		body, ok := validateBody(w, r, nil)
		if !ok {
			return
		}
		input, err := cron.ParseCronViewInput(body, false)
		if err != nil {
			writeCronError(w, err)
			return
		}
		data, err := cron.CreateCronView(r.Context(), input)
		if err != nil {
			writeCronError(w, err)
			return
		}
		writeOK(w, data)
	})

	router.Put("/api/crons/views", func(w http.ResponseWriter, r *http.Request) {
		body, ok := validateBody(w, r, nil)
		if !ok {
			return
		}
		input, err := cron.ParseCronViewInput(body, true)
		if err != nil {
			writeCronError(w, err)
			return
		}
		data, err := cron.UpdateCronView(r.Context(), input)
		if err != nil {
			writeCronError(w, err)
			return
		}
		writeOK(w, data)
	})

	router.Delete("/api/crons/views", func(w http.ResponseWriter, r *http.Request) {
		ids, ok := decodeIDArray(w, r)
		if !ok {
			return
		}
		if err := cron.RemoveCronViews(r.Context(), ids); err != nil {
			writeCronError(w, err)
			return
		}
		writeReply(w, &service.Reply{Code: 200})
	})

	router.Put("/api/crons/views/move", func(w http.ResponseWriter, r *http.Request) {
		body, ok := validateBody(w, r, nil)
		if !ok {
			return
		}
		id, idOK := asNumber(body["id"])
		from, fromOK := asNumber(body["fromIndex"])
		to, toOK := asNumber(body["toIndex"])
		// Joi aborts on the first failing key (abortEarly default), so report
		// one at a time in schema order instead of listing all three.
		for _, item := range []struct {
			ok  bool
			key string
		}{{idOK, "id"}, {fromOK, "fromIndex"}, {toOK, "toIndex"}} {
			if item.ok {
				continue
			}
			if _, exists := body[item.key]; exists && body[item.key] != nil {
				writeValidationErrorKeys(w, []string{item.key}, `"`+item.key+`" must be a number`)
			} else {
				writeValidationErrorKeys(w, []string{item.key}, `"`+item.key+`" is required`)
			}
			return
		}
		data, err := cron.MoveCronView(r.Context(), id, int(from), int(to))
		if err != nil {
			writeCronError(w, err)
			return
		}
		writeOK(w, data)
	})

	router.Put("/api/crons/views/disable", func(w http.ResponseWriter, r *http.Request) {
		ids, ok := decodeIDArray(w, r)
		if !ok {
			return
		}
		if err := cron.SetCronViewsDisabled(r.Context(), ids, true); err != nil {
			writeCronError(w, err)
			return
		}
		writeReply(w, &service.Reply{Code: 200})
	})

	router.Put("/api/crons/views/enable", func(w http.ResponseWriter, r *http.Request) {
		ids, ok := decodeIDArray(w, r)
		if !ok {
			return
		}
		if err := cron.SetCronViewsDisabled(r.Context(), ids, false); err != nil {
			writeCronError(w, err)
			return
		}
		writeReply(w, &service.Reply{Code: 200})
	})

	// ---- tasks -----------------------------------------------------------
	router.Get("/api/crons", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		params := service.CronListParams{
			SearchValue: query.Get("searchValue"),
			Page:        queryIntDefault(query.Get("page")),
			Size:        queryIntDefault(query.Get("size")),
			Sorter:      query.Get("sorter"),
			Filters:     query.Get("filters"),
			QueryString: query.Get("queryString"),
		}
		data, err := cron.List(r.Context(), params)
		if err != nil {
			writeCronError(w, err)
			return
		}
		writeOK(w, data)
	})

	router.Get("/api/crons/detail", func(w http.ResponseWriter, r *http.Request) {
		data, err := cron.FindByLogPath(r.Context(), r.URL.Query().Get("log_path"))
		if err != nil {
			writeCronError(w, err)
			return
		}
		if data == nil {
			writeReply(w, &service.Reply{Code: 200})
			return
		}
		writeOK(w, data)
	})

	router.Post("/api/crons", func(w http.ResponseWriter, r *http.Request) {
		body, ok := validateBody(w, r, nil)
		if !ok {
			return
		}
		input, err := cron.ParseCrontabInput(body, false)
		if err != nil {
			writeCronError(w, err)
			return
		}
		data, err := cron.Create(r.Context(), input)
		if err != nil {
			writeCronError(w, err)
			return
		}
		writeOK(w, data)
	})

	router.Put("/api/crons/run", func(w http.ResponseWriter, r *http.Request) {
		ids, ok := decodeIDArray(w, r)
		if !ok {
			return
		}
		if err := cron.Run(r.Context(), ids); err != nil {
			writeCronError(w, err)
			return
		}
		writeReply(w, &service.Reply{Code: 200})
	})

	router.Put("/api/crons/stop", func(w http.ResponseWriter, r *http.Request) {
		ids, ok := decodeIDArray(w, r)
		if !ok {
			return
		}
		reply, err := cron.Stop(r.Context(), ids)
		if err != nil {
			writeCronError(w, err)
			return
		}
		writeReply(w, reply)
	})

	router.Delete("/api/crons/labels", func(w http.ResponseWriter, r *http.Request) {
		ids, labels, ok := decodeLabelBody(w, r)
		if !ok {
			return
		}
		if err := cron.RemoveLabels(r.Context(), ids, labels); err != nil {
			writeCronError(w, err)
			return
		}
		writeReply(w, &service.Reply{Code: 200})
	})

	router.Post("/api/crons/labels", func(w http.ResponseWriter, r *http.Request) {
		ids, labels, ok := decodeLabelBody(w, r)
		if !ok {
			return
		}
		if err := cron.AddLabels(r.Context(), ids, labels); err != nil {
			writeCronError(w, err)
			return
		}
		writeReply(w, &service.Reply{Code: 200})
	})

	router.Put("/api/crons/disable", func(w http.ResponseWriter, r *http.Request) {
		ids, ok := decodeIDArray(w, r)
		if !ok {
			return
		}
		if err := cron.SetDisabled(r.Context(), ids, true); err != nil {
			writeCronError(w, err)
			return
		}
		writeReply(w, &service.Reply{Code: 200})
	})

	router.Put("/api/crons/enable", func(w http.ResponseWriter, r *http.Request) {
		ids, ok := decodeIDArray(w, r)
		if !ok {
			return
		}
		if err := cron.SetDisabled(r.Context(), ids, false); err != nil {
			writeCronError(w, err)
			return
		}
		writeReply(w, &service.Reply{Code: 200})
	})

	router.Get("/api/crons/{id}/log", func(w http.ResponseWriter, r *http.Request) {
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
		tailValue, ok := queryBoolParam(w, r, "tail")
		if !ok {
			return
		}
		tail := tailValue != nil && *tailValue
		result, err := cron.Log(r.Context(), id, offset, limit, tail)
		if err != nil {
			writeCronError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, struct {
			Code       int    `json:"code"`
			Data       string `json:"data"`
			LogStatus  string `json:"logStatus"`
			Offset     int64  `json:"offset"`
			NextOffset int64  `json:"nextOffset"`
			Total      int64  `json:"total"`
			Truncated  bool   `json:"truncated"`
		}{
			Code:       http.StatusOK,
			Data:       result.Content,
			LogStatus:  result.Status,
			Offset:     result.Offset,
			NextOffset: result.NextOffset,
			Total:      result.Total,
			Truncated:  result.Truncated,
		})
	})

	router.Put("/api/crons", func(w http.ResponseWriter, r *http.Request) {
		body, ok := validateBody(w, r, nil)
		if !ok {
			return
		}
		input, err := cron.ParseCrontabInput(body, true)
		if err != nil {
			writeCronError(w, err)
			return
		}
		data, err := cron.Update(r.Context(), input)
		if err != nil {
			writeCronError(w, err)
			return
		}
		writeOK(w, data)
	})

	router.Delete("/api/crons", func(w http.ResponseWriter, r *http.Request) {
		ids, ok := decodeIDArray(w, r)
		if !ok {
			return
		}
		if err := cron.Remove(r.Context(), ids); err != nil {
			writeCronError(w, err)
			return
		}
		writeReply(w, &service.Reply{Code: 200})
	})

	router.Put("/api/crons/pin", func(w http.ResponseWriter, r *http.Request) {
		ids, ok := decodeIDArray(w, r)
		if !ok {
			return
		}
		if err := cron.SetPinned(r.Context(), ids, true); err != nil {
			writeCronError(w, err)
			return
		}
		writeReply(w, &service.Reply{Code: 200})
	})

	router.Put("/api/crons/unpin", func(w http.ResponseWriter, r *http.Request) {
		ids, ok := decodeIDArray(w, r)
		if !ok {
			return
		}
		if err := cron.SetPinned(r.Context(), ids, false); err != nil {
			writeCronError(w, err)
			return
		}
		writeReply(w, &service.Reply{Code: 200})
	})

	router.Get("/api/crons/import", func(w http.ResponseWriter, r *http.Request) {
		// Node returns before `crontab -l` finishes. Windows has no crontab
		// binary, so the best-effort import is a no-op there.
		go cron.ImportCrontab()
		writeReply(w, &service.Reply{Code: 200})
	})

	router.Get("/api/crons/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := idParam(w, r)
		if !ok {
			return
		}
		data, err := cron.Get(r.Context(), id)
		if err != nil {
			writeCronError(w, err)
			return
		}
		writeOK(w, data)
	})

	router.Put("/api/crons/status", func(w http.ResponseWriter, r *http.Request) {
		body, ok := validateBody(w, r, nil)
		if !ok {
			return
		}
		input, err := decodeStatusBody(body)
		if err != nil {
			writeCronError(w, err)
			return
		}
		if err := cron.SetStatus(r.Context(), input); err != nil {
			writeCronError(w, err)
			return
		}
		writeReply(w, &service.Reply{Code: 200})
	})

	router.Get("/api/crons/{id}/instances", func(w http.ResponseWriter, r *http.Request) {
		id, ok := idParam(w, r)
		if !ok {
			return
		}
		data, err := cron.Instances(r.Context(), id)
		if err != nil {
			writeCronError(w, err)
			return
		}
		writeOK(w, data)
	})

	router.Post("/api/crons/{id}/instances/{instanceId}/stop", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := idParam(w, r); !ok {
			return
		}
		instanceID, err := strconv.ParseInt(chi.URLParam(r, "instanceId"), 10, 64)
		if err != nil {
			writeValidationErrorSource(w, "params", []string{"instanceId"}, `"instanceId" must be a number`)
			return
		}
		reply, err := cron.StopInstance(r.Context(), instanceID)
		if err != nil {
			writeCronError(w, err)
			return
		}
		writeReply(w, reply)
	})

	router.Get("/api/crons/{id}/logs", func(w http.ResponseWriter, r *http.Request) {
		id, ok := idParam(w, r)
		if !ok {
			return
		}
		data, err := cron.Logs(r.Context(), id)
		if err != nil {
			writeCronError(w, err)
			return
		}
		writeOK(w, data)
	})
}

func writeCronError(w http.ResponseWriter, err error) {
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

func idParam(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeValidationErrorSource(w, "params", []string{"id"}, `"id" must be a number`)
		return 0, false
	}
	return id, true
}

func queryIntDefault(value string) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0
	}
	return parsed
}

// queryIntParam validates a numeric query parameter the way the Joi schemas of
// the log endpoints do. max <= 0 means "no upper bound".
func queryIntParam(w http.ResponseWriter, r *http.Request, key string, min, max int64) (*int64, bool) {
	values, exists := r.URL.Query()[key]
	if !exists || len(values) == 0 || strings.TrimSpace(values[0]) == "" {
		return nil, true
	}
	text := strings.TrimSpace(values[0])
	parsed, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		if _, floatErr := strconv.ParseFloat(text, 64); floatErr == nil {
			writeValidationErrorSource(w, "query", []string{key}, `"`+key+`" must be an integer`)
		} else {
			writeValidationErrorSource(w, "query", []string{key}, `"`+key+`" must be a number`)
		}
		return nil, false
	}
	if parsed < min {
		writeValidationErrorSource(w, "query", []string{key},
			`"`+key+`" must be greater than or equal to `+strconv.FormatInt(min, 10))
		return nil, false
	}
	if max > 0 && parsed > max {
		writeValidationErrorSource(w, "query", []string{key},
			`"`+key+`" must be less than or equal to `+strconv.FormatInt(max, 10))
		return nil, false
	}
	return &parsed, true
}

func queryBoolParam(w http.ResponseWriter, r *http.Request, key string) (*bool, bool) {
	values, exists := r.URL.Query()[key]
	if !exists || len(values) == 0 || strings.TrimSpace(values[0]) == "" {
		return nil, true
	}
	switch strings.ToLower(strings.TrimSpace(values[0])) {
	case "true", "1":
		value := true
		return &value, true
	case "false", "0":
		value := false
		return &value, true
	default:
		writeValidationErrorSource(w, "query", []string{key}, `"`+key+`" must be a boolean`)
		return nil, false
	}
}

func asNumber(value any) (int64, bool) {
	switch typed := value.(type) {
	case json.Number:
		parsed, err := typed.Int64()
		return parsed, err == nil
	case float64:
		return int64(typed), true
	case int64:
		return typed, true
	case int:
		return int64(typed), true
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func decodeIDArray(w http.ResponseWriter, r *http.Request) ([]int64, bool) {
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
	// Joi rejects an empty array with the generic "min" message; the panel
	// never sends one, but clients that do must get the same 400.
	if len(list) == 0 {
		writeValidationErrorSource(w, "body", []string{""},
			`"value" does not contain 1 required value(s)`)
		return nil, false
	}
	ids := make([]int64, 0, len(list))
	for index, item := range list {
		parsed, ok := asNumber(item)
		if !ok {
			// Joi reports the failing element path and quotes it: keys ["1"],
			// message `"[1]" must be a number`.
			writeValidationErrorSource(w, "body", []string{strconv.Itoa(index)},
				`"[`+strconv.Itoa(index)+`]" must be a number`)
			return nil, false
		}
		ids = append(ids, parsed)
	}
	return ids, true
}

func decodeLabelBody(w http.ResponseWriter, r *http.Request) ([]int64, []string, bool) {
	body, ok := validateBody(w, r, nil)
	if !ok {
		return nil, nil, false
	}
	var ids []int64
	if value, exists := body["ids"]; exists && value != nil {
		list, ok := value.([]any)
		if !ok {
			writeValidationErrorSource(w, "body", []string{"ids"}, `"ids" must be an array`)
			return nil, nil, false
		}
		for index, item := range list {
			parsed, ok := asNumber(item)
			if !ok {
				writeValidationErrorSource(w, "body", []string{"ids." + strconv.Itoa(index)},
					`"ids[`+strconv.Itoa(index)+`]" must be a number`)
				return nil, nil, false
			}
			ids = append(ids, parsed)
		}
	} else {
		// The Node schema leaves ids optional and passes undefined straight to
		// Sequelize, which answers 500 with this message. Reproduced so a client
		// (or a shell script) that inspects the body sees the same contract.
		writeJSON(w, http.StatusInternalServerError, envelope{
			Code:    http.StatusInternalServerError,
			Message: `WHERE parameter "id" has invalid "undefined" value`,
		})
		return nil, nil, false
	}
	var labels []string
	if value, exists := body["labels"]; exists && value != nil {
		list, ok := value.([]any)
		if !ok {
			writeValidationErrorSource(w, "body", []string{"labels"}, `"labels" must be an array`)
			return nil, nil, false
		}
		for _, item := range list {
			text, ok := item.(string)
			if !ok {
				writeValidationErrorSource(w, "body", []string{"labels"}, `"labels" must contain only strings`)
				return nil, nil, false
			}
			labels = append(labels, text)
		}
	}
	return ids, labels, true
}

func decodeStatusBody(body map[string]any) (*service.CronStatusInput, error) {
	input := &service.CronStatusInput{}
	idsValue, exists := body["ids"]
	if !exists || idsValue == nil {
		return nil, &service.ValidationError{Keys: []string{"ids"}, Message: `"ids" is required`}
	}
	list, ok := idsValue.([]any)
	if !ok {
		return nil, &service.ValidationError{Keys: []string{"ids"}, Message: `"ids" must be an array`}
	}
	for _, item := range list {
		parsed, ok := asNumber(item)
		if !ok {
			return nil, &service.ValidationError{Keys: []string{"ids"}, Message: `"ids" must contain only numbers`}
		}
		input.IDs = append(input.IDs, parsed)
	}

	statusValue, exists := body["status"]
	if !exists || statusValue == nil {
		return nil, &service.ValidationError{Keys: []string{"status"}, Message: `"status" is required`}
	}
	// back/api/cron.ts declares status (and pid) as Joi.string(): the shell
	// callbacks send "0"/"1", and a JSON number is a validation error.
	statusText, ok := statusValue.(string)
	if !ok {
		return nil, &service.ValidationError{Keys: []string{"status"}, Message: `"status" must be a string`}
	}
	status, err := strconv.ParseInt(strings.TrimSpace(statusText), 10, 64)
	if err != nil {
		return nil, &service.ValidationError{Keys: []string{"status"}, Message: `"status" must be a number`}
	}
	input.Status = status

	if value, exists := body["pid"]; exists && value != nil {
		pidText, ok := value.(string)
		if !ok {
			return nil, &service.ValidationError{Keys: []string{"pid"}, Message: `"pid" must be a string`}
		}
		pid, err := strconv.ParseInt(strings.TrimSpace(pidText), 10, 64)
		if err != nil {
			return nil, &service.ValidationError{Keys: []string{"pid"}, Message: `"pid" must be a number`}
		}
		input.Pid = &pid
	}
	if value, exists := body["log_path"]; exists && value != nil {
		text, ok := value.(string)
		if !ok {
			return nil, &service.ValidationError{Keys: []string{"log_path"}, Message: `"log_path" must be a string`}
		}
		input.LogPath = &text
	}
	if value, exists := body["last_running_time"]; exists && value != nil {
		parsed, ok := asNumber(value)
		if !ok {
			return nil, &service.ValidationError{Keys: []string{"last_running_time"}, Message: `"last_running_time" must be a number`}
		}
		input.LastRunningTime = parsed
	}
	if value, exists := body["last_execution_time"]; exists && value != nil {
		parsed, ok := asNumber(value)
		if !ok {
			return nil, &service.ValidationError{Keys: []string{"last_execution_time"}, Message: `"last_execution_time" must be a number`}
		}
		input.LastExecutionTime = parsed
	}
	if value, exists := body["exit_code"]; exists && value != nil {
		parsed, ok := asNumber(value)
		if !ok {
			return nil, &service.ValidationError{Keys: []string{"exit_code"}, Message: `"exit_code" must be a number`}
		}
		input.ExitCode = &parsed
	}
	return input, nil
}

func readJSONAny(r *http.Request) (any, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	var payload any
	if err := decoder.Decode(&payload); err != nil {
		return nil, err
	}
	return payload, nil
}
