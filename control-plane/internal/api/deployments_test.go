// control-plane/internal/api/deployments_test.go
package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	promtest "github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/api"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/deployments"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/events"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/jobs"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/observability"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/testutil"
	"golang.org/x/crypto/bcrypt"
)

type mockDeploymentsService struct {
	createResult   *deployments.Deployment
	createErr      error
	getResult      *deployments.Deployment
	getErr         error
	deleteErr      error
	rollbackResult *deployments.Deployment
	rollbackErr    error
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
func (m *mockDeploymentsService) Rollback(_ context.Context, _, _ string, _ int) (*deployments.Deployment, error) {
	return m.rollbackResult, m.rollbackErr
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
	handler := api.NewRouter(pool, store, pub, nil, svc, nil)
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

func TestDeploymentsAPI_Rollback_HappyPath(t *testing.T) {
	svc := &mockDeploymentsService{
		getResult:      &deployments.Deployment{ID: "dep-1", Status: "running"},
		rollbackResult: &deployments.Deployment{ID: "dep-1", Status: "pending"},
	}
	handler, token := setupDeploymentsAPITest(t, svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/deployments/dep-1/rollback", bytes.NewReader([]byte(`{"revision":1}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeploymentsAPI_Rollback_EmptyBody(t *testing.T) {
	svc := &mockDeploymentsService{
		getResult:      &deployments.Deployment{ID: "dep-1", Status: "running"},
		rollbackResult: &deployments.Deployment{ID: "dep-1", Status: "pending"},
	}
	handler, token := setupDeploymentsAPITest(t, svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/deployments/dep-1/rollback", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeploymentsAPI_Rollback_NotFound(t *testing.T) {
	svc := &mockDeploymentsService{rollbackErr: deployments.ErrDeploymentNotFound}
	handler, token := setupDeploymentsAPITest(t, svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/deployments/dep-1/rollback", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeploymentsAPI_Rollback_InvalidRevision(t *testing.T) {
	svc := &mockDeploymentsService{rollbackErr: deployments.ErrNoRevisionToRollback}
	handler, token := setupDeploymentsAPITest(t, svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/deployments/dep-1/rollback", bytes.NewReader([]byte(`{"revision":0}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeploymentsAPI_Delete_HappyPath(t *testing.T) {
	svc := &mockDeploymentsService{
		getResult: &deployments.Deployment{ID: "dep-1", Status: "running"},
	}
	handler, token := setupDeploymentsAPITest(t, svc)

	req := httptest.NewRequest(http.MethodDelete, "/v1/deployments/dep-1", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var body map[string]string
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["status"] != "deleting" {
		t.Errorf("expected status deleting in response body, got %q", body["status"])
	}
}

// TestDeploymentsAPI_Create_IncrementsGauge verifies that a successful POST /v1/deployments
// increments the deployment_count{status="pending"} gauge so the metric is accurate for
// deployments created after the startup snapshot.
func TestDeploymentsAPI_Create_IncrementsGauge(t *testing.T) {
	svc := &mockDeploymentsService{
		createResult: &deployments.Deployment{ID: "dep-g1", Name: "gauge-dep", Status: "pending"},
	}
	handler, token := setupDeploymentsAPITest(t, svc)

	before := promtest.ToFloat64(observability.DeploymentCount.WithLabelValues("pending"))

	body := map[string]interface{}{
		"model_name": "resnet50", "model_version": 1,
		"name": "gauge-dep", "namespace": "default", "replicas": 1,
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
	after := promtest.ToFloat64(observability.DeploymentCount.WithLabelValues("pending"))
	if after-before != 1 {
		t.Errorf("expected pending gauge delta +1 (before=%.0f after=%.0f)", before, after)
	}
}

// TestDeploymentsAPI_Delete_UpdatesGauge verifies that DELETE /v1/deployments/:id
// decrements the old-status gauge and increments deleting.
func TestDeploymentsAPI_Delete_UpdatesGauge(t *testing.T) {
	svc := &mockDeploymentsService{
		getResult: &deployments.Deployment{ID: "dep-g2", Status: "running"},
	}
	handler, token := setupDeploymentsAPITest(t, svc)

	runningBefore := promtest.ToFloat64(observability.DeploymentCount.WithLabelValues("running"))
	deletingBefore := promtest.ToFloat64(observability.DeploymentCount.WithLabelValues("deleting"))

	req := httptest.NewRequest(http.MethodDelete, "/v1/deployments/dep-g2", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if promtest.ToFloat64(observability.DeploymentCount.WithLabelValues("running"))-runningBefore != -1 {
		t.Error("expected running gauge to decrease by 1 on delete")
	}
	if promtest.ToFloat64(observability.DeploymentCount.WithLabelValues("deleting"))-deletingBefore != 1 {
		t.Error("expected deleting gauge to increase by 1 on delete")
	}
}

// TestDeploymentsAPI_Rollback_UpdatesGauge verifies that POST /v1/deployments/:id/rollback
// decrements the old-status gauge and increments pending.
func TestDeploymentsAPI_Rollback_UpdatesGauge(t *testing.T) {
	svc := &mockDeploymentsService{
		getResult:      &deployments.Deployment{ID: "dep-g3", Status: "running"},
		rollbackResult: &deployments.Deployment{ID: "dep-g3", Status: "pending"},
	}
	handler, token := setupDeploymentsAPITest(t, svc)

	runningBefore := promtest.ToFloat64(observability.DeploymentCount.WithLabelValues("running"))
	pendingBefore := promtest.ToFloat64(observability.DeploymentCount.WithLabelValues("pending"))

	req := httptest.NewRequest(http.MethodPost, "/v1/deployments/dep-g3/rollback", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if promtest.ToFloat64(observability.DeploymentCount.WithLabelValues("running"))-runningBefore != -1 {
		t.Error("expected running gauge to decrease by 1 on rollback")
	}
	if promtest.ToFloat64(observability.DeploymentCount.WithLabelValues("pending"))-pendingBefore != 1 {
		t.Error("expected pending gauge to increase by 1 on rollback")
	}
}

// TestDeploymentsAPI_Rollback_ClearsFailureReason verifies that a deployment that
// previously failed (carrying a failure_reason) has that field cleared after rollback,
// so a recovered deployment does not surface stale error metadata.
func TestDeploymentsAPI_Rollback_ClearsFailureReason(t *testing.T) {
	svc := &mockDeploymentsService{
		getResult: &deployments.Deployment{
			ID: "dep-rfr", Status: "failed",
			FailureReason: `init container "model-loader": artifact not found`,
		},
		rollbackResult: &deployments.Deployment{
			ID: "dep-rfr", Status: "pending", FailureReason: "",
		},
	}
	handler, token := setupDeploymentsAPITest(t, svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/deployments/dep-rfr/rollback", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var wrapper struct {
		Deployment deployments.Deployment `json:"deployment"`
	}
	if err := json.NewDecoder(w.Body).Decode(&wrapper); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	resp := wrapper.Deployment
	if resp.FailureReason != "" {
		t.Errorf("expected failure_reason to be empty after rollback, got %q", resp.FailureReason)
	}
	if resp.Status != "pending" {
		t.Errorf("expected status pending after rollback, got %q", resp.Status)
	}
}

// TestInternalStatus_DeletedNotInGauge verifies that the deleting→deleted transition
// decrements deleting but does NOT increment deleted, matching the startup snapshot
// which excludes deleted rows. This prevents the series from appearing at runtime
// then vanishing after process restart.
func TestInternalStatus_DeletedNotInGauge(t *testing.T) {
	pool := testutil.SetupDB(t)
	ctx := context.Background()

	var tenantID, projectID string
	pool.QueryRow(ctx, `INSERT INTO tenants (name) VALUES ('deleted-gauge-tenant') RETURNING id::text`).Scan(&tenantID)
	pool.QueryRow(ctx, `INSERT INTO projects (tenant_id, name) VALUES ($1, 'proj') RETURNING id::text`, tenantID).Scan(&projectID)

	jobStore := jobs.NewPostgresJobStore(pool)
	srcJob := &jobs.TrainingJob{
		TenantID: tenantID, ProjectID: projectID, Name: "del-gauge-src-job",
		Status: "PENDING", Image: "img:1", Command: []string{"run"},
		Args: []string{}, Env: map[string]string{},
		NumWorkers: 1, WorkerCPU: "1", WorkerMemory: "1Gi",
		HeadCPU: "1", HeadMemory: "1Gi",
	}
	srcRun := &jobs.TrainingRun{TenantID: tenantID, Status: "SUCCEEDED"}
	if err := jobStore.CreateJobWithRun(ctx, srcJob, srcRun); err != nil {
		t.Fatalf("CreateJobWithRun: %v", err)
	}

	modelStore := jobs.NewPostgresJobStore(pool) // reuse pool for model insert
	_ = modelStore
	var modelRecordID string
	pool.QueryRow(ctx,
		`INSERT INTO model_records (tenant_id, project_id, name, mlflow_registered_model_name) VALUES ($1, $2, 'del-model', 'del-model') RETURNING id::text`,
		tenantID, projectID,
	).Scan(&modelRecordID)
	var modelVersionID string
	pool.QueryRow(ctx,
		`INSERT INTO model_versions (model_record_id, tenant_id, version_number, mlflow_run_id, source_run_id, artifact_uri, status)
		 VALUES ($1, $2, 1, 'mlflow-del-1', $3::uuid, 's3://b/m', 'production') RETURNING id::text`,
		modelRecordID, tenantID, srcRun.ID,
	).Scan(&modelVersionID)

	depStore := deployments.NewPostgresDeploymentStore(pool)
	dep := &deployments.Deployment{
		TenantID: tenantID, ProjectID: projectID,
		ModelRecordID: modelRecordID, ModelVersionID: modelVersionID,
		Name: "del-gauge-dep", Namespace: "default",
		Status: "deleting", DesiredReplicas: 1,
	}
	// Insert directly at "deleting" status bypassing service validation.
	pool.QueryRow(ctx,
		`INSERT INTO deployments (tenant_id, project_id, model_record_id, model_version_id, name, namespace, status, desired_replicas)
		 VALUES ($1,$2,$3,$4,$5,$6,'deleting',1) RETURNING id::text`,
		tenantID, projectID, modelRecordID, modelVersionID, dep.Name, dep.Namespace,
	).Scan(&dep.ID)
	pool.Exec(ctx, `INSERT INTO deployment_revisions (deployment_id, revision_number, model_version_id, status) VALUES ($1::uuid, 1, $2::uuid, 'active')`, dep.ID, modelVersionID) //nolint:errcheck

	internalHandler := api.NewInternalRouter(jobStore, &events.NoOpPublisher{}, depStore, nil)

	deletingBefore := promtest.ToFloat64(observability.DeploymentCount.WithLabelValues("deleting"))
	deletedBefore := promtest.ToFloat64(observability.DeploymentCount.WithLabelValues("deleted"))

	body := map[string]interface{}{"status": "deleted"}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPatch, "/internal/v1/deployments/"+dep.ID+"/status", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	internalHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if promtest.ToFloat64(observability.DeploymentCount.WithLabelValues("deleting"))-deletingBefore != -1 {
		t.Error("expected deleting gauge to decrease by 1 on deleted transition")
	}
	if promtest.ToFloat64(observability.DeploymentCount.WithLabelValues("deleted"))-deletedBefore != 0 {
		t.Error("expected deleted gauge to remain unchanged (excluded from metric)")
	}
}
