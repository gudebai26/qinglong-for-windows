package httpapi

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"
)

// fieldKind is the subset of Joi rules the user endpoints use.
type fieldKind int

const (
	kindString fieldKind = iota
	kindIP
)

// fieldRule describes one validated body field.
type fieldRule struct {
	name     string
	kind     fieldKind
	required bool
	maxLen   int
}

// validationIssue mirrors celebrate's per-source detail object.
type validationIssue struct {
	Source  string   `json:"source"`
	Keys    []string `json:"keys"`
	Message string   `json:"message"`
}

// validationEnvelope is celebrate's error body. It is intentionally not the
// panel's {code,message} envelope: the frontend treats HTTP 400 from the
// validator differently from a business error.
type validationEnvelope struct {
	StatusCode int                        `json:"statusCode"`
	Error      string                     `json:"error"`
	Message    string                     `json:"message"`
	Validation map[string]validationIssue `json:"validation"`
}

// readJSONBody decodes a JSON object body. An empty body is treated as {} so the
// required-field messages match body-parser + Joi.
func readJSONBody(r *http.Request) (map[string]any, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return map[string]any{}, nil
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.UseNumber()
	var payload any
	if err := dec.Decode(&payload); err != nil {
		return nil, err
	}
	object, ok := payload.(map[string]any)
	if !ok {
		return nil, errNotAnObject
	}
	return object, nil
}

type bodyShapeError struct{}

func (bodyShapeError) Error() string { return "body must be an object" }

var errNotAnObject = bodyShapeError{}

// validateBody decodes the body and applies the rules in declaration order,
// producing celebrate's {"statusCode":400,...,"validation":{"body":{...}}}
// response. It returns ok=false when a response was already written.
func validateBody(w http.ResponseWriter, r *http.Request, rules []fieldRule) (map[string]any, bool) {
	body, err := readJSONBody(r)
	if err != nil {
		if err == errNotAnObject {
			writeValidationError(w, "value", `"value" must be of type object`)
			return nil, false
		}
		// body-parser turns malformed JSON into a 400 handled by the generic
		// error middleware, i.e. {code:400,message}.
		writeJSON(w, http.StatusBadRequest, envelope{
			Code:    http.StatusBadRequest,
			Message: "Unexpected token in JSON",
		})
		return nil, false
	}

	keys := make([]string, 0, 2)
	messages := make([]string, 0, 2)
	for _, rule := range rules {
		value, exists := body[rule.name]
		if !exists || value == nil {
			if rule.required {
				keys = append(keys, rule.name)
				messages = append(messages, `"`+rule.name+`" is required`)
			}
			continue
		}
		switch rule.kind {
		case kindString:
			text, ok := value.(string)
			if !ok {
				keys = append(keys, rule.name)
				messages = append(messages, `"`+rule.name+`" must be a string`)
				continue
			}
			if rule.maxLen > 0 && utf8.RuneCountInString(text) > rule.maxLen {
				keys = append(keys, rule.name)
				messages = append(messages, `"`+rule.name+`" must be less than or equal to `+
					strconv.Itoa(rule.maxLen)+` characters long`)
			}
		case kindIP:
			text, ok := value.(string)
			if !ok {
				keys = append(keys, rule.name)
				messages = append(messages, `"`+rule.name+`" must be a string`)
				continue
			}
			if !isIPAddress(text) {
				keys = append(keys, rule.name)
				messages = append(messages, `"`+rule.name+
					`" must be a valid ip address of one of the following versions [ipv4, ipv6] with a forbidden CIDR`)
			}
		}
	}
	if len(keys) > 0 {
		writeValidationErrorKeys(w, keys, strings.Join(messages, ". "))
		return nil, false
	}
	return body, true
}

func isIPAddress(value string) bool {
	if strings.Contains(value, "/") {
		return false
	}
	return net.ParseIP(value) != nil
}

func writeValidationError(w http.ResponseWriter, key, message string) {
	writeValidationErrorKeys(w, []string{key}, message)
}

func writeValidationErrorKeys(w http.ResponseWriter, keys []string, message string) {
	writeValidationErrorSource(w, "body", keys, message)
}

func writeValidationErrorSource(w http.ResponseWriter, source string, keys []string, message string) {
	writeJSON(w, http.StatusBadRequest, validationEnvelope{
		StatusCode: http.StatusBadRequest,
		Error:      "Bad Request",
		Message:    "Validation failed",
		Validation: map[string]validationIssue{
			source: {
				Source:  source,
				Keys:    keys,
				Message: message,
			},
		},
	})
}
