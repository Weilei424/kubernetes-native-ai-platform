// control-plane/internal/api/internal_test.go
package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	promtest "github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/api"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/deployments"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/events"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/jobs"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/models"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/observability"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/testutil"
)

func setupInternalTest(t *testing.T) (http.Handler, jobs.Store, string, string) {
	t.Helper()
	pool := testutil.SetupDB(t)
	ctx := context.Background()

	var tenantID, projectID string
	pool.QueryRow(ctx, `INSERT INTO tenants (name) VALUES ('int-tenant') RETURNING id::text`).Scan(&tenantID)
	pool.QueryRow(ctx, `INSERT INTO projects (tenant_id, name) VALUES ($1, 'int-proj') RETURNING id::text`, tenantID).Scan(&projectID)

	store := jobs.NewPostgresJobStore(pool)
	handler := api.NewInternalRouter(store, &events.NoOpPublisher{}, nil, nil)
	return handler, store, tenantID, projectID
}

func TestInternalStatus_QueuedToRunning(t *testing.T) {
	handler, store, tenantID, projectID := setupInternalTest(t)
	ctx := context.Background()

	job := &jobs.TrainingJob{
		TenantID: tenantID, ProjectID: projectID, Name: "int-job",
		Status: "PENDING", Image: "img:1", Command: []string{"run"},
		Args: []string{}, Env: map[string]string{},
		NumWorkers: 1, WorkerCPU: "1", WorkerMemory: "1Gi",
		HeadCPU: "1", HeadMemory: "1Gi",
	}
	run := &jobs.TrainingRun{TenantID: tenantID, Status: "PENDING"}
	store.CreateJobWithRun(ctx, job, run)
	store.TransitionJobStatus(ctx, job.ID, "PENDING", "QUEUED", nil)

	body := map[string]interface{}{"status": "RUNNING"}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPatch, "/internal/v1/jobs/"+job.ID+"/status", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	got, _ := store.GetJob(ctx, job.ID, tenantID)
	if got.Status != "RUNNING" {
		t.Fatalf("expected RUNNING, got %q", got.Status)
	}
}

func TestInternalStatus_QueuedToFailed_AutoRetry(t *testing.T) {
	handler, store, tenantID, projectID := setupInternalTest(t)
	ctx := context.Background()

	// max_retries defaults to 3, so after the first FAILED the job should be re-queued.
	job := &jobs.TrainingJob{
		TenantID: tenantID, ProjectID: projectID, Name: "queued-fail-job",
		Status: "PENDING", Image: "img:1", Command: []string{"run"},
		Args: []string{}, Env: map[string]string{},
		NumWorkers: 1, WorkerCPU: "1", WorkerMemory: "1Gi",
		HeadCPU: "1", HeadMemory: "1Gi",
	}
	run := &jobs.TrainingRun{TenantID: tenantID, Status: "PENDING"}
	store.CreateJobWithRun(ctx, job, run)
	store.TransitionJobStatus(ctx, job.ID, "PENDING", "QUEUED", nil)

	reason := "image pull failed"
	body := map[string]interface{}{"status": "FAILED", "failure_reason": reason}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPatch, "/internal/v1/jobs/"+job.ID+"/status", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for QUEUED→FAILED, got %d: %s", rec.Code, rec.Body.String())
	}

	// With max_retries=3 and retry_count now 1, the job should have been re-queued automatically.
	got, _ := store.GetJob(ctx, job.ID, tenantID)
	if got.Status != "QUEUED" {
		t.Fatalf("expected QUEUED (auto-retry), got %q", got.Status)
	}
	if got.RetryCount != 1 {
		t.Fatalf("expected retry_count=1, got %d", got.RetryCount)
	}
}

func TestInternalStatus_SetsMLflowRunID(t *testing.T) {
	handler, store, tenantID, projectID := setupInternalTest(t)
	ctx := context.Background()

	job := &jobs.TrainingJob{
		TenantID: tenantID, ProjectID: projectID, Name: "mlflow-job",
		Status: "PENDING", Image: "img:1", Command: []string{"run"},
		Args: []string{}, Env: map[string]string{},
		NumWorkers: 1, WorkerCPU: "1", WorkerMemory: "1Gi",
		HeadCPU: "1", HeadMemory: "1Gi",
	}
	run := &jobs.TrainingRun{TenantID: tenantID, Status: "PENDING"}
	store.CreateJobWithRun(ctx, job, run)
	store.TransitionJobStatus(ctx, job.ID, "PENDING", "QUEUED", nil)
	store.TransitionJobStatus(ctx, job.ID, "QUEUED", "RUNNING", nil)

	mlflowID := "mlflow-abc123"
	body := map[string]interface{}{
		"status":        "SUCCEEDED",
		"mlflow_run_id": mlflowID,
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPatch, "/internal/v1/jobs/"+job.ID+"/status", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	got, err := store.GetRunByJobID(ctx, job.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if got.MLflowRunID == nil || *got.MLflowRunID != mlflowID {
		t.Fatalf("expected mlflow_run_id %q, got %v", mlflowID, got.MLflowRunID)
	}
}

func TestInternalStatus_RetryOnFailed(t *testing.T) {
	handler, store, tenantID, projectID := setupInternalTest(t)
	ctx := context.Background()

	// Create a job and advance it to RUNNING.
	job := &jobs.TrainingJob{
		TenantID: tenantID, ProjectID: projectID, Name: "retry-job",
		Status: "PENDING", Image: "img:1", Command: []string{"run"},
		Args: []string{}, Env: map[string]string{},
		NumWorkers: 1, WorkerCPU: "1", WorkerMemory: "1Gi",
		HeadCPU: "1", HeadMemory: "1Gi",
	}
	run := &jobs.TrainingRun{TenantID: tenantID, Status: "PENDING"}
	if err := store.CreateJobWithRun(ctx, job, run); err != nil {
		t.Fatalf("create job: %v", err)
	}
	if err := store.TransitionJobStatus(ctx, job.ID, "PENDING", "QUEUED", nil); err != nil {
		t.Fatalf("PENDING→QUEUED: %v", err)
	}
	if err := store.TransitionJobStatus(ctx, job.ID, "QUEUED", "RUNNING", nil); err != nil {
		t.Fatalf("QUEUED→RUNNING: %v", err)
	}

	// Send FAILED — max_retries=3, so retry_count will be 1 which is ≤ 3 → re-queue.
	reason := "oom killed"
	body := map[string]interface{}{"status": "FAILED", "failure_reason": reason}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPatch, "/internal/v1/jobs/"+job.ID+"/status", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	got, err := store.GetJob(ctx, job.ID, tenantID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if got.Status != "QUEUED" {
		t.Fatalf("expected QUEUED (re-queued for retry), got %q", got.Status)
	}
	if got.RetryCount != 1 {
		t.Fatalf("expected retry_count=1, got %d", got.RetryCount)
	}
}

// TestInternalStatus_EmitsRunDuration verifies that TrainingRunDuration is observed
// when a RUNNING job transitions to SUCCEEDED.
func TestInternalStatus_EmitsRunDuration(t *testing.T) {
	handler, store, tenantID, projectID := setupInternalTest(t)
	ctx := context.Background()

	job := &jobs.TrainingJob{
		TenantID: tenantID, ProjectID: projectID, Name: "duration-job",
		Status: "PENDING", Image: "img:1", Command: []string{"run"},
		Args: []string{}, Env: map[string]string{},
		NumWorkers: 1, WorkerCPU: "1", WorkerMemory: "1Gi",
		HeadCPU: "1", HeadMemory: "1Gi",
	}
	run := &jobs.TrainingRun{TenantID: tenantID, Status: "PENDING"}
	if err := store.CreateJobWithRun(ctx, job, run); err != nil {
		t.Fatalf("CreateJobWithRun: %v", err)
	}
	// Advance to RUNNING so started_at is set on the run.
	store.TransitionJobStatus(ctx, job.ID, "PENDING", "QUEUED", nil)   //nolint:errcheck
	store.TransitionJobStatus(ctx, job.ID, "QUEUED", "RUNNING", nil)   //nolint:errcheck

	// Capture sample count before the SUCCEEDED transition.
	beforeCount := histogramSampleCount(observability.TrainingRunDuration)

	body := map[string]interface{}{"status": "SUCCEEDED"}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPatch, "/internal/v1/jobs/"+job.ID+"/status", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	afterCount := histogramSampleCount(observability.TrainingRunDuration)
	if afterCount != beforeCount+1 {
		t.Errorf("expected TrainingRunDuration sample count to increase by 1 (before=%d, after=%d)",
			beforeCount, afterCount)
	}
}

// histogramSampleCount reads the current sample count from a prometheus Histogram.
func histogramSampleCount(h prometheus.Histogram) uint64 {
	var m prometheus.Metric
	ch := make(chan prometheus.Metric, 1)
	h.Collect(ch)
	m = <-ch
	var dto prometheusDTO
	if err := m.Write(&dto); err != nil {
		return 0
	}
	if dto.Histogram == nil {
		return 0
	}
	return dto.Histogram.GetSampleCount()
}

// prometheusDTO is the protobuf metric type alias used by the prometheus client.
type prometheusDTO = dto.Metric

// TestInternalStatus_DeploymentGaugeEmitted verifies that DeploymentCount gauge is
// decremented for the old status and incremented for the new status on a transition.
func TestInternalStatus_DeploymentGaugeEmitted(t *testing.T) {
	pool := testutil.SetupDB(t)
	ctx := context.Background()

	var tenantID, projectID string
	pool.QueryRow(ctx, `INSERT INTO tenants (name) VALUES ('gauge-tenant') RETURNING id::text`).Scan(&tenantID)
	pool.QueryRow(ctx, `INSERT INTO projects (tenant_id, name) VALUES ($1, 'gauge-proj') RETURNING id::text`, tenantID).Scan(&projectID)

	// Create a training job + run for FK requirements.
	jobStore := jobs.NewPostgresJobStore(pool)
	srcJob := &jobs.TrainingJob{
		TenantID: tenantID, ProjectID: projectID, Name: "gauge-src-job",
		Status: "PENDING", Image: "img:1", Command: []string{"run"},
		Args: []string{}, Env: map[string]string{},
		NumWorkers: 1, WorkerCPU: "1", WorkerMemory: "1Gi",
		HeadCPU: "1", HeadMemory: "1Gi",
	}
	srcRun := &jobs.TrainingRun{TenantID: tenantID, Status: "SUCCEEDED"}
	if err := jobStore.CreateJobWithRun(ctx, srcJob, srcRun); err != nil {
		t.Fatalf("CreateJobWithRun: %v", err)
	}

	// Create model record + production model version.
	modelStore := models.NewPostgresModelStore(pool)
	rec := &models.ModelRecord{
		TenantID: tenantID, ProjectID: projectID,
		Name: "gauge-model", MLflowRegisteredModelName: tenantID + "-gauge-model",
	}
	if err := modelStore.CreateOrGetModelRecord(ctx, rec); err != nil {
		t.Fatalf("CreateOrGetModelRecord: %v", err)
	}
	var modelVersionID string
	pool.QueryRow(ctx,
		`INSERT INTO model_versions (model_record_id, tenant_id, version_number, mlflow_run_id, source_run_id, artifact_uri, status)
		 VALUES ($1, $2, 1, 'mlflow-gauge-1', $3::uuid, 's3://bucket/model', 'production') RETURNING id::text`,
		rec.ID, tenantID, srcRun.ID,
	).Scan(&modelVersionID)

	// Create a deployment at "pending" status.
	depStore := deployments.NewPostgresDeploymentStore(pool)
	dep := &deployments.Deployment{
		TenantID: tenantID, ProjectID: projectID,
		ModelRecordID: rec.ID, ModelVersionID: modelVersionID,
		Name: "gauge-dep", Namespace: "default",
		Status: "pending", DesiredReplicas: 1,
	}
	if err := depStore.CreateDeployment(ctx, dep); err != nil {
		t.Fatalf("CreateDeployment: %v", err)
	}

	internalHandler := api.NewInternalRouter(jobStore, &events.NoOpPublisher{}, depStore, nil)

	// Snapshot gauge values before the transition.
	pendingBefore := promtest.ToFloat64(observability.DeploymentCount.WithLabelValues("pending"))
	provisioningBefore := promtest.ToFloat64(observability.DeploymentCount.WithLabelValues("provisioning"))

	body := map[string]interface{}{"status": "provisioning"}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPatch, "/internal/v1/deployments/"+dep.ID+"/status", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	internalHandler.ServeHTTP(rec2, req)

	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}

	pendingAfter := promtest.ToFloat64(observability.DeploymentCount.WithLabelValues("pending"))
	provisioningAfter := promtest.ToFloat64(observability.DeploymentCount.WithLabelValues("provisioning"))

	if pendingAfter-pendingBefore != -1 {
		t.Errorf("pending gauge: expected delta -1 (before=%.0f after=%.0f)", pendingBefore, pendingAfter)
	}
	if provisioningAfter-provisioningBefore != 1 {
		t.Errorf("provisioning gauge: expected delta +1 (before=%.0f after=%.0f)", provisioningBefore, provisioningAfter)
	}
}

func TestInternalStatus_InvalidTransition(t *testing.T) {
	handler, store, tenantID, projectID := setupInternalTest(t)
	ctx := context.Background()

	job := &jobs.TrainingJob{
		TenantID: tenantID, ProjectID: projectID, Name: "conflict-job",
		Status: "PENDING", Image: "img:1", Command: []string{"run"},
		Args: []string{}, Env: map[string]string{},
		NumWorkers: 1, WorkerCPU: "1", WorkerMemory: "1Gi",
		HeadCPU: "1", HeadMemory: "1Gi",
	}
	run := &jobs.TrainingRun{TenantID: tenantID, Status: "PENDING"}
	store.CreateJobWithRun(ctx, job, run)

	// PENDING → SUCCEEDED is invalid
	body := map[string]interface{}{"status": "SUCCEEDED"}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPatch, "/internal/v1/jobs/"+job.ID+"/status", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", rec.Code)
	}
}
