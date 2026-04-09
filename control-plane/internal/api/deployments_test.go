// control-plane/internal/api/deployments_test.go
package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/api"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/deployments"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/events"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/jobs"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/testutil"
	"golang.org/x/crypto/bcrypt"
)

type mockDeploymentsService struct {
	createResult *deployments.Deployment
	createErr    error
	getResult    *deployments.Deployment
	getErr       error
	deleteErr    error
}

func (m *mockDeploymentsService) Create(_ context.Context, _ string, _ deployments.CreateDeploymentRequest) (*deployments.Deployment, error) {
	return m.createResult, m.createErr
}
func (m *mockDeploymentsService) Get(_ context.Context, _, _ string) (*deployments.Deployment, error) {
	return m.getResult, m.getErr
}
func (m *mockDeploymentsService) Delete(_ context.Context, _, _ string) error {
	return m.deleteErr
}

func setupDeploymentsAPITest(t *testing.T, svc api.DeploymentsService) (http.Handler, string) {
	t.Helper()
	pool := testutil.SetupDB(t)
	ctx := context.Background()

	var tenantID string
	pool.QueryRow(ctx, `INSERT INTO tenants (name) VALUES ('dep-api-tenant') RETURNING id::text`).Scan(&tenantID)

	plaintext := "deptoken-xxxx1234"
	prefix := plaintext[:8]
	hash, _ := bcrypt.GenerateFromPassword([]byte(plaintext), bcrypt.MinCost)
	pool.Exec(ctx, `INSERT INTO api_tokens (tenant_id, token_hash, token_prefix) VALUES ($1, $2, $3)`,
		tenantID, string(hash), prefix)

	store := jobs.NewPostgresJobStore(pool)
	pub := &events.NoOpPublisher{}
	handler := api.NewRouter(pool, store, pub, nil, svc)
	return handler, plaintext
}

func TestDeploymentsAPI_Create_HappyPath(t *testing.T) {
	svc := &mockDeploymentsService{
		createResult: &deployments.Deployment{
			ID: "dep-1", Name: "resnet50-prod", Status: "pending",
			Namespace: "default", DesiredReplicas: 1,
		},
	}
	handler, token := setupDeploymentsAPITest(t, svc)

	body := map[string]interface{}{
		"model_name": "resnet50", "model_version": 1,
		"name": "resnet50-prod", "namespace": "default", "replicas": 1,
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/deployments", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeploymentsAPI_Create_ModelVersionNotProduction(t *testing.T) {
	svc := &mockDeploymentsService{createErr: deployments.ErrModelVersionNotProduction}
	handler, token := setupDeploymentsAPITest(t, svc)

	body := map[string]interface{}{
		"model_name": "resnet50", "model_version": 1,
		"name": "resnet50-dep", "namespace": "default", "replicas": 1,
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/deployments", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeploymentsAPI_Create_DuplicateName(t *testing.T) {
	svc := &mockDeploymentsService{createErr: deployments.ErrDuplicateDeploymentName}
	handler, token := setupDeploymentsAPITest(t, svc)

	body := map[string]interface{}{
		"model_name": "resnet50", "model_version": 1,
		"name": "resnet50-prod", "namespace": "default", "replicas": 1,
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/deployments", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeploymentsAPI_Create_ModelNotFound(t *testing.T) {
	svc := &mockDeploymentsService{createErr: deployments.ErrModelNotFound}
	handler, token := setupDeploymentsAPITest(t, svc)

	body := map[string]interface{}{
		"model_name": "missing", "model_version": 1,
		"name": "dep", "namespace": "default", "replicas": 1,
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/deployments", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeploymentsAPI_Create_Unauthorized(t *testing.T) {
	svc := &mockDeploymentsService{}
	handler, _ := setupDeploymentsAPITest(t, svc)

	body := map[string]interface{}{"model_name": "resnet50", "model_version": 1, "name": "dep"}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/deployments", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestDeploymentsAPI_Get_HappyPath(t *testing.T) {
	svc := &mockDeploymentsService{
		getResult: &deployments.Deployment{
			ID: "dep-1", Name: "resnet50-prod", Status: "running",
			ServingEndpoint: "triton-dep-1.default.svc.cluster.local:8000",
		},
	}
	handler, token := setupDeploymentsAPITest(t, svc)

	req := httptest.NewRequest(http.MethodGet, "/v1/deployments/dep-1", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeploymentsAPI_Get_NotFound(t *testing.T) {
	svc := &mockDeploymentsService{getErr: deployments.ErrDeploymentNotFound}
	handler, token := setupDeploymentsAPITest(t, svc)

	req := httptest.NewRequest(http.MethodGet, "/v1/deployments/nonexistent", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestDeploymentsAPI_Delete_HappyPath(t *testing.T) {
	svc := &mockDeploymentsService{}
	handler, token := setupDeploymentsAPITest(t, svc)

	req := httptest.NewRequest(http.MethodDelete, "/v1/deployments/dep-1", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}
