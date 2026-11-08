package httpapi

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRecovererReturnsUnifiedError(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := recoverer(logger)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/anything", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, `"code":500`) || !strings.Contains(body, `"message"`) {
		t.Fatalf("body = %s, want {code,message} envelope", body)
	}
}

func TestBodyLimitRejectsOversizedCredentialRequest(t *testing.T) {
	handler := bodyLimit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not run for oversized bodies")
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/user/login", strings.NewReader(strings.Repeat("a", credentialBodyBytes+1)))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"code":413`) {
		t.Fatalf("body = %s, want 413 envelope", rec.Body.String())
	}
}

func TestStripQueryParamT(t *testing.T) {
	handler := stripQueryParamT(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.URL.Query()["t"]; ok {
			t.Error("t must be removed from the query")
		}
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/crons?page=1&t=1700000000", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
}

// Successful requests must stay out of data/syslog at the default info level;
// only failures (and slow calls) deserve a line.
func TestAccessLogLevels(t *testing.T) {
	run := func(status int) string {
		var buf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
		handler := requestLogger(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
		}))
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/crons", nil))
		return buf.String()
	}

	if line := run(http.StatusOK); !strings.Contains(line, "level=DEBUG") || !strings.Contains(line, "status=200") {
		t.Errorf("200 request logged at %q, want DEBUG", line)
	}
	if line := run(http.StatusNotFound); !strings.Contains(line, "level=WARN") || !strings.Contains(line, "status=404") {
		t.Errorf("404 request logged at %q, want WARN", line)
	}
	if line := run(http.StatusInternalServerError); !strings.Contains(line, "level=ERROR") {
		t.Errorf("500 request logged at %q, want ERROR", line)
	}
}
