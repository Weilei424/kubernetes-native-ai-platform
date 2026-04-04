// control-plane/internal/auth/middleware_test.go
package auth_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/auth"
)

type mockStore struct {
	tenantID string
	err      error
}

func (m *mockStore) FindToken(_ context.Context, _ string) (string, error) {
	return m.tenantID, m.err
}

func TestTokenAuth_NoHeader(t *testing.T) {
	store := &mockStore{err: fmt.Errorf("not found")}
	handler := auth.TokenAuth(store)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/anything", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestTokenAuth_InvalidToken(t *testing.T) {
	store := &mockStore{err: fmt.Errorf("token not found")}
	handler := auth.TokenAuth(store)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/anything", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestTokenAuth_ValidToken(t *testing.T) {
	store := &mockStore{tenantID: "tenant-abc"}
	var gotTenantID string
	handler := auth.TokenAuth(store)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTenantID = auth.TenantIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/anything", nil)
	req.Header.Set("Authorization", "Bearer valid-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if gotTenantID != "tenant-abc" {
		t.Fatalf("expected tenant-abc in context, got %q", gotTenantID)
	}
}
