package httpapi

import (
	"encoding/json"
	"net/http"
)

// envelope is the response wrapper used by every JSON endpoint of the panel.
// The Node implementation sends {code, data} on success and {code, message} on
// failure; omitempty keeps the shapes identical.
type envelope struct {
	Code    int    `json:"code"`
	Data    any    `json:"data,omitempty"`
	Message string `json:"message,omitempty"`
}

// writeJSON writes an arbitrary payload with the given HTTP status.
func writeJSON(w http.ResponseWriter, status int, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, `{"code":500,"message":"Internal Server Error"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// writeOK mirrors res.send({code:200, data}).
func writeOK(w http.ResponseWriter, data any) {
	writeJSON(w, http.StatusOK, envelope{Code: http.StatusOK, Data: data})
}

// writeError mirrors the Node error handler: HTTP status equals body code.
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, envelope{Code: status, Message: message})
}

// writeText is used for the non-API surface where Express would return plain
// text (missing assets, non-GET requests outside /api).
func writeText(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(message))
}
