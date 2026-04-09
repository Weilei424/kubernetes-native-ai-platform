package observability_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/observability"
)

func TestFromContext_ReturnsLoggerWithRequestID(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(observability.RequestLogger(logger))
	r.Get("/test", func(w http.ResponseWriter, r *http.Request) {
		l := observability.FromContext(r.Context())
		l.Info("hello", "key", "val")
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	var entry map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		if err := json.Unmarshal(line, &entry); err == nil {
			if entry["msg"] == "hello" {
				break
			}
		}
	}
	if entry["msg"] != "hello" {
		t.Fatal("hello log line not found")
	}
	if entry["request_id"] == nil || entry["request_id"] == "" {
		t.Errorf("expected request_id in log entry, got: %v", entry)
	}
}

func TestFromContext_FallsBackToDefault(t *testing.T) {
	l := observability.FromContext(context.Background())
	if l == nil {
		t.Fatal("expected non-nil logger from empty context")
	}
}
