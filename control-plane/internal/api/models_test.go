// control-plane/internal/api/models_test.go
package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/api"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/events"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/jobs"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/models"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/testutil"
	"golang.org/x/crypto/bcrypt"
)

// mockModelsService satisfies the modelsService interface used by the handler.
type mockModelsService struct {
	registerResult   *models.ModelVersion
	registerErr      error
	getModelRecord   *models.ModelRecord
	getModelVersions []*models.ModelVersion
	getModelErr      error
	getVersionResult *models.ModelVersion
	getVersionErr    error
	promoteErr       error
	resolveResult    *models.ModelVersion
	resolveErr       error
}

func (m *mockModelsService) Register(ctx context.Context, tenantID string, req models.RegisterRequest) (*models.ModelVersion, error) {
	return m.registerResult, m.registerErr
}
func (m *mockModelsService) GetModel(ctx context.Context, name, tenantID string) (*models.ModelRecord, []*models.ModelVersion, error) {
	return m.getModelRecord, m.getModelVersions, m.getModelErr
}
func (m *mockModelsService) GetModelVersion(ctx context.Context, name string, version int, tenantID string) (*models.ModelVersion, error) {
	return m.getVersionResult, m.getVersionErr
}
func (m *mockModelsService) Promote(ctx context.Context, name string, version int, alias, tenantID string) error {
	return m.promoteErr
}
func (m *mockModelsService) ResolveAlias(ctx context.Context, name, alias, tenantID string) (*models.ModelVersion, error) {
	return m.resolveResult, m.resolveErr
}

func setupModelsAPITest(t *testing.T, svc api.ModelsService) (http.Handler, string) {
	t.Helper()
	pool := testutil.SetupDB(t)
	ctx := context.Background()

	var tenantID string
	pool.QueryRow(ctx, `INSERT INTO tenants (name) VALUES ('models-api-tenant') RETURNING id::text`).Scan(&tenantID)

	plaintext := "modelstoken-xxxx"
	prefix := plaintext[:8]
	hash, _ := bcrypt.GenerateFromPassword([]byte(plaintext), bcrypt.MinCost)
	pool.Exec(ctx, `INSERT INTO api_tokens (tenant_id, token_hash, token_prefix) VALUES ($1, $2, $3)`,
		tenantID, string(hash), prefix)

	store := jobs.NewPostgresJobStore(pool)
	pub := &events.NoOpPublisher{}
	handler := api.NewRouter(pool, store, pub, svc)
	return handler, plaintext
}

func TestModelsAPI_Register_HappyPath(t *testing.T) {
	svc := &mockModelsService{
		registerResult: &models.ModelVersion{
			ID: "ver-1", VersionNumber: 1,
			ArtifactURI: "runs:/mlflow-abc/model/", Status: "candidate",
		},
	}
	handler, token := setupModelsAPITest(t, svc)

	body := map[string]interface{}{
		"run_id":     "run-uuid-123",
		"model_name": "resnet50",
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/models", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestModelsAPI_Register_RunNotSucceeded(t *testing.T) {
	svc := &mockModelsService{registerErr: models.ErrRunNotSucceeded}
	handler, token := setupModelsAPITest(t, svc)

	body := map[string]interface{}{"run_id": "run-1", "model_name": "resnet50"}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/models", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestModelsAPI_Register_RunNotFound(t *testing.T) {
	svc := &mockModelsService{registerErr: models.ErrRunNotFound}
	handler, token := setupModelsAPITest(t, svc)

	body := map[string]interface{}{"run_id": "no-such-run", "model_name": "resnet50"}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/models", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestModelsAPI_GetModel_InfraError(t *testing.T) {
	svc := &mockModelsService{getModelErr: fmt.Errorf("db connection lost")}
	handler, token := setupModelsAPITest(t, svc)

	req := httptest.NewRequest(http.MethodGet, "/v1/models/resnet50", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for infra error, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestModelsAPI_GetModelVersion_InfraError(t *testing.T) {
	svc := &mockModelsService{getVersionErr: fmt.Errorf("db connection lost")}
	handler, token := setupModelsAPITest(t, svc)

	req := httptest.NewRequest(http.MethodGet, "/v1/models/resnet50/versions/1", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for infra error, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestModelsAPI_ResolveAlias_InfraError(t *testing.T) {
	svc := &mockModelsService{resolveErr: fmt.Errorf("db connection lost")}
	handler, token := setupModelsAPITest(t, svc)

	req := httptest.NewRequest(http.MethodGet, "/v1/models/resnet50/alias/production", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for infra error, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestModelsAPI_GetModel_HappyPath(t *testing.T) {
	svc := &mockModelsService{
		getModelRecord:   &models.ModelRecord{ID: "rec-1", Name: "resnet50"},
		getModelVersions: []*models.ModelVersion{{ID: "ver-1", VersionNumber: 1, Status: "candidate"}},
	}
	handler, token := setupModelsAPITest(t, svc)

	req := httptest.NewRequest(http.MethodGet, "/v1/models/resnet50", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestModelsAPI_GetModel_NotFound(t *testing.T) {
	svc := &mockModelsService{getModelErr: models.ErrModelNotFound}
	handler, token := setupModelsAPITest(t, svc)

	req := httptest.NewRequest(http.MethodGet, "/v1/models/no-such-model", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestModelsAPI_Promote_HappyPath(t *testing.T) {
	svc := &mockModelsService{}
	handler, token := setupModelsAPITest(t, svc)

	body := map[string]string{"alias": "production"}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/models/resnet50/versions/1/promote", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestModelsAPI_Promote_ArchivedRejected(t *testing.T) {
	svc := &mockModelsService{promoteErr: models.ErrVersionArchived}
	handler, token := setupModelsAPITest(t, svc)

	body := map[string]string{"alias": "production"}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/models/resnet50/versions/1/promote", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestModelsAPI_ResolveAlias_HappyPath(t *testing.T) {
	svc := &mockModelsService{
		resolveResult: &models.ModelVersion{ID: "ver-1", VersionNumber: 3, Status: "production"},
	}
	handler, token := setupModelsAPITest(t, svc)

	req := httptest.NewRequest(http.MethodGet, "/v1/models/resnet50/alias/production", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestModelsAPI_ResolveAlias_NotFound(t *testing.T) {
	svc := &mockModelsService{resolveErr: models.ErrAliasNotFound}
	handler, token := setupModelsAPITest(t, svc)

	req := httptest.NewRequest(http.MethodGet, "/v1/models/resnet50/alias/staging", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestModelsAPI_GetModelVersion_SourceRunTraceability(t *testing.T) {
	svc := &mockModelsService{
		getVersionResult: &models.ModelVersion{
			ID: "ver-1", VersionNumber: 2, Status: "staging",
			SourceRunID: "source-run-uuid", MLflowRunID: "mlflow-run-id",
		},
	}
	handler, token := setupModelsAPITest(t, svc)

	req := httptest.NewRequest(http.MethodGet, "/v1/models/resnet50/versions/2", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&resp)
	ver := resp["version"].(map[string]interface{})
	if ver["source_run_id"] != "source-run-uuid" {
		t.Errorf("expected source_run_id in response, got: %v", ver["source_run_id"])
	}
}

func TestModelsAPI_Unauthenticated(t *testing.T) {
	svc := &mockModelsService{}
	handler, _ := setupModelsAPITest(t, svc)

	req := httptest.NewRequest(http.MethodGet, "/v1/models/resnet50", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestModelsAPI_CrossTenantRejection(t *testing.T) {
	// Service returns model not found — simulates cross-tenant isolation.
	svc := &mockModelsService{getModelErr: models.ErrModelNotFound}
	handler, token := setupModelsAPITest(t, svc)

	req := httptest.NewRequest(http.MethodGet, "/v1/models/other-tenant-model", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for cross-tenant access, got %d", rec.Code)
	}
}

// --- Full-stack integration test helpers ---

type mockMLflowForIntegration struct {
	versionNum int
}

func (m *mockMLflowForIntegration) CreateRegisteredModel(ctx context.Context, name string) error { return nil }
func (m *mockMLflowForIntegration) CreateModelVersion(ctx context.Context, modelName, sourceURI, runID string) (int, string, error) {
	m.versionNum++
	return m.versionNum, sourceURI, nil
}
func (m *mockMLflowForIntegration) SetModelAlias(ctx context.Context, modelName, alias string, version int) error {
	return nil
}
func (m *mockMLflowForIntegration) DeleteModelAlias(ctx context.Context, modelName, alias string) error {
	return nil
}
func (m *mockMLflowForIntegration) GetModelVersionByAlias(ctx context.Context, modelName, alias string) (int, error) {
	return m.versionNum, nil
}

func setupModelsIntegrationTest(t *testing.T) (http.Handler, string, string, string) {
	t.Helper()
	pool := testutil.SetupDB(t)
	ctx := context.Background()

	var tenantID, projectID string
	pool.QueryRow(ctx, `INSERT INTO tenants (name) VALUES ('int-models-tenant') RETURNING id::text`).Scan(&tenantID)
	pool.QueryRow(ctx, `INSERT INTO projects (tenant_id, name) VALUES ($1, 'int-models-proj') RETURNING id::text`, tenantID).Scan(&projectID)

	plaintext := "int-models-tok"
	prefix := plaintext[:8]
	hash, _ := bcrypt.GenerateFromPassword([]byte(plaintext), bcrypt.MinCost)
	pool.Exec(ctx, `INSERT INTO api_tokens (tenant_id, token_hash, token_prefix) VALUES ($1, $2, $3)`, tenantID, string(hash), prefix)

	// Create a succeeded training run with mlflow_run_id set.
	jobStore := jobs.NewPostgresJobStore(pool)
	job := &jobs.TrainingJob{
		TenantID: tenantID, ProjectID: projectID, Name: "int-job",
		Status: "PENDING", Image: "img:1", Command: []string{"run"},
		Args: []string{}, Env: map[string]string{},
		NumWorkers: 1, WorkerCPU: "1", WorkerMemory: "1Gi",
		HeadCPU: "1", HeadMemory: "1Gi",
	}
	run := &jobs.TrainingRun{TenantID: tenantID, Status: "PENDING"}
	jobStore.CreateJobWithRun(ctx, job, run)
	jobStore.TransitionJobStatus(ctx, job.ID, "PENDING", "QUEUED", nil)
	jobStore.TransitionJobStatus(ctx, job.ID, "QUEUED", "RUNNING", nil)
	jobStore.TransitionJobStatus(ctx, job.ID, "RUNNING", "SUCCEEDED", nil)
	jobStore.SetMLflowRunID(ctx, job.ID, "mlflow-int-run")

	mc := &mockMLflowForIntegration{}
	modelStore := models.NewPostgresModelStore(pool)
	svc := models.NewService(modelStore, jobStore, mc)

	pub := &events.NoOpPublisher{}
	handler := api.NewRouter(pool, jobStore, pub, svc)
	return handler, plaintext, run.ID, tenantID
}

func TestModelsIntegration_RegisterThenGet(t *testing.T) {
	handler, token, runID, _ := setupModelsIntegrationTest(t)

	// Register model
	body := map[string]interface{}{"run_id": runID, "model_name": "resnet50-int"}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/models", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("register: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// GET model
	req2 := httptest.NewRequest(http.MethodGet, "/v1/models/resnet50-int", nil)
	req2.Header.Set("Authorization", "Bearer "+token)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("get model: expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}
	var resp map[string]interface{}
	json.NewDecoder(rec2.Body).Decode(&resp)
	versions := resp["versions"].([]interface{})
	if len(versions) != 1 {
		t.Fatalf("expected 1 version, got %d", len(versions))
	}
}

func TestModelsIntegration_RegisterThenPromoteThenResolve(t *testing.T) {
	handler, token, runID, _ := setupModelsIntegrationTest(t)

	// Register
	body := map[string]interface{}{"run_id": runID, "model_name": "resnet50-promote"}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/models", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("register: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// Promote to production
	promoteBody := map[string]string{"alias": "production"}
	pb, _ := json.Marshal(promoteBody)
	req2 := httptest.NewRequest(http.MethodPost, "/v1/models/resnet50-promote/versions/1/promote", bytes.NewReader(pb))
	req2.Header.Set("Authorization", "Bearer "+token)
	req2.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("promote: expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}

	// Resolve alias
	req3 := httptest.NewRequest(http.MethodGet, "/v1/models/resnet50-promote/alias/production", nil)
	req3.Header.Set("Authorization", "Bearer "+token)
	rec3 := httptest.NewRecorder()
	handler.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Fatalf("resolve alias: expected 200, got %d: %s", rec3.Code, rec3.Body.String())
	}
}

func TestModelsIntegration_SourceRunTraceability(t *testing.T) {
	handler, token, runID, _ := setupModelsIntegrationTest(t)

	// Register
	body := map[string]interface{}{"run_id": runID, "model_name": "resnet50-trace"}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/models", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("register: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// GET version — verify source_run_id traces back to the training run
	req2 := httptest.NewRequest(http.MethodGet, "/v1/models/resnet50-trace/versions/1", nil)
	req2.Header.Set("Authorization", "Bearer "+token)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("get version: expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}
	var resp map[string]interface{}
	json.NewDecoder(rec2.Body).Decode(&resp)
	ver := resp["version"].(map[string]interface{})
	if ver["source_run_id"] != runID {
		t.Errorf("expected source_run_id=%q, got %v", runID, ver["source_run_id"])
	}
}
