// control-plane/internal/api/failure_test.go
package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/api"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/deployments"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/events"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/jobs"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/models"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/testutil"
)

// TestJobsAPI_QuotaExceeded verifies that submitting a job that would exceed the
// tenant's CPU quota is rejected with 422 and "quota" in the response body.
func TestJobsAPI_QuotaExceeded(t *testing.T) {
	pool := testutil.SetupDB(t)
	ctx := context.Background()

	// Tenant with a very tight quota: 1000 millicores (1 core) total.
	var tenantID, projectID string
	pool.QueryRow(ctx,
		`INSERT INTO tenants (name, cpu_quota, memory_quota) VALUES ('quota-fail-tenant', 1000, 1073741824) RETURNING id::text`,
	).Scan(&tenantID)
	pool.QueryRow(ctx,
		`INSERT INTO projects (tenant_id, name) VALUES ($1, 'quota-proj') RETURNING id::text`, tenantID,
	).Scan(&projectID)

	plaintext := "quotatoken-fail1"
	prefix := plaintext[:8]
	hash, _ := bcrypt.GenerateFromPassword([]byte(plaintext), bcrypt.MinCost)
	pool.Exec(ctx, `INSERT INTO api_tokens (tenant_id, token_hash, token_prefix) VALUES ($1, $2, $3)`,
		tenantID, string(hash), prefix)

	store := jobs.NewPostgresJobStore(pool)
	pub := &events.NoOpPublisher{}
	handler := api.NewRouter(pool, store, pub, nil, nil, nil)

	submitJob := func(cpu string) (int, string) {
		body := map[string]interface{}{
			"name": "quota-test-job", "project_id": projectID,
			"runtime": map[string]interface{}{
				"image": "img:1", "command": []string{"python", "train.py"},
				"args": []string{}, "env": map[string]string{},
			},
			"resources": map[string]interface{}{
				"num_workers": 1, "worker_cpu": cpu, "worker_memory": "512Mi",
				"head_cpu": "0", "head_memory": "0",
			},
		}
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/v1/jobs", bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+plaintext)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}

	// First job: 1 core = exactly the quota, should succeed.
	code, body := submitJob("1")
	if code != http.StatusAccepted {
		t.Fatalf("first job (within quota): expected 202, got %d: %s", code, body)
	}

	// Second job: would exceed the quota.
	code, body = submitJob("1")
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("second job (quota exceeded): expected 422, got %d: %s", code, body)
	}

	// Verify the response body mentions quota.
	var resp map[string]string
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if resp["error"] == "" {
		t.Errorf("expected error field in response body, got: %s", body)
	}
	if !strings.Contains(resp["error"], "quota") {
		t.Errorf("expected 'quota' in error message, got: %s", resp["error"])
	}

	// Verify no training_run was created by the rejected second job.
	var runCount int
	pool.QueryRow(ctx, `SELECT COUNT(*) FROM training_runs WHERE tenant_id = $1::uuid`, tenantID).Scan(&runCount)
	// The first job created 1 run. The second (quota-exceeded) should not have created any.
	if runCount != 1 {
		t.Errorf("expected 1 training_run (from first job), got %d", runCount)
	}
}

// TestDeploymentsAPI_InvalidModelVersion verifies that attempting to deploy a model
// version that is not in "production" status is rejected with 422 by the real service.
func TestDeploymentsAPI_InvalidModelVersion(t *testing.T) {
	pool := testutil.SetupDB(t)
	ctx := context.Background()

	// Build tenant, project, training run, model record and model version (candidate status).
	var tenantID, projectID string
	pool.QueryRow(ctx,
		`INSERT INTO tenants (name) VALUES ('dep-fail-tenant') RETURNING id::text`,
	).Scan(&tenantID)
	pool.QueryRow(ctx,
		`INSERT INTO projects (tenant_id, name) VALUES ($1, 'proj') RETURNING id::text`, tenantID,
	).Scan(&projectID)

	// Create a training job + run via the store so FKs are satisfied.
	jobStore := jobs.NewPostgresJobStore(pool)
	srcJob := &jobs.TrainingJob{
		TenantID: tenantID, ProjectID: projectID, Name: "dep-fail-src-job",
		Status: "PENDING", Image: "img:1", Command: []string{"run"},
		Args: []string{}, Env: map[string]string{},
		NumWorkers: 1, WorkerCPU: "1", WorkerMemory: "1Gi",
		HeadCPU: "1", HeadMemory: "1Gi",
	}
	srcRun := &jobs.TrainingRun{TenantID: tenantID, Status: "SUCCEEDED"}
	if err := jobStore.CreateJobWithRun(ctx, srcJob, srcRun); err != nil {
		t.Fatalf("CreateJobWithRun for model version source: %v", err)
	}
	runID := srcRun.ID

	var modelRecordID string
	pool.QueryRow(ctx,
		`INSERT INTO model_records (tenant_id, project_id, name, mlflow_registered_model_name) VALUES ($1, $2, 'test-model', 'test-model') RETURNING id::text`,
		tenantID, projectID,
	).Scan(&modelRecordID)

	if _, err := pool.Exec(ctx,
		`INSERT INTO model_versions (model_record_id, tenant_id, version_number, mlflow_run_id, source_run_id, artifact_uri, status)
		 VALUES ($1, $2, 1, 'mlflow-run-001', $3::uuid, 's3://bucket/model', 'candidate')`,
		modelRecordID, tenantID, runID,
	); err != nil {
		t.Fatalf("insert model_version: %v", err)
	}

	// Token for auth.
	plaintext := "depfailtoken-000"
	prefix := plaintext[:8]
	hash, _ := bcrypt.GenerateFromPassword([]byte(plaintext), bcrypt.MinCost)
	pool.Exec(ctx, `INSERT INTO api_tokens (tenant_id, token_hash, token_prefix) VALUES ($1, $2, $3)`,
		tenantID, string(hash), prefix)

	// Wire up real service backed by the test DB.
	modelStore := models.NewPostgresModelStore(pool)
	depStore := deployments.NewPostgresDeploymentStore(pool)
	depSvc := deployments.NewService(depStore, modelStore)

	store := jobs.NewPostgresJobStore(pool)
	pub := &events.NoOpPublisher{}
	handler := api.NewRouter(pool, store, pub, nil, depSvc, nil)

	body := map[string]interface{}{
		"model_name": "test-model", "model_version": 1,
		"name": "test-dep", "namespace": "default", "replicas": 1,
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/deployments", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+plaintext)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for candidate model version, got %d: %s", w.Code, w.Body.String())
	}

	// Verify no deployment record was created.
	var depCount int
	pool.QueryRow(ctx, `SELECT COUNT(*) FROM deployments WHERE tenant_id = $1::uuid`, tenantID).Scan(&depCount)
	if depCount != 0 {
		t.Errorf("expected 0 deployments after rejection, got %d", depCount)
	}
}

// TestRetryExhaustion verifies that a job stays FAILED permanently after max_retries
// are exhausted. max_retries defaults to 3. IncrementRetryCount is called on every
// FAILED event, so after 4 FAILED reports retry_count == 4 (> max_retries=3) and
// the 4th report leaves the job permanently FAILED with no re-queue.
func TestRetryExhaustion(t *testing.T) {
	pool := testutil.SetupDB(t)
	ctx := context.Background()

	var tenantID, projectID string
	pool.QueryRow(ctx, `INSERT INTO tenants (name) VALUES ('retry-exhaust-tenant') RETURNING id::text`).Scan(&tenantID)
	pool.QueryRow(ctx, `INSERT INTO projects (tenant_id, name) VALUES ($1, 'retry-proj') RETURNING id::text`, tenantID).Scan(&projectID)

	store := jobs.NewPostgresJobStore(pool)
	internalHandler := api.NewInternalRouter(store, &events.NoOpPublisher{}, nil, nil)

	// Create the job; CreateJobWithRun sets status=PENDING with max_retries=3 (DB default).
	job := &jobs.TrainingJob{
		TenantID: tenantID, ProjectID: projectID, Name: "exhaust-job",
		Status: "PENDING", Image: "img:1", Command: []string{"run"},
		Args: []string{}, Env: map[string]string{},
		NumWorkers: 1, WorkerCPU: "1", WorkerMemory: "1Gi",
		HeadCPU: "1", HeadMemory: "1Gi",
	}
	run := &jobs.TrainingRun{TenantID: tenantID, Status: "PENDING"}
	if err := store.CreateJobWithRun(ctx, job, run); err != nil {
		t.Fatalf("CreateJobWithRun: %v", err)
	}

	// Advance to QUEUED then RUNNING for the first attempt.
	if err := store.TransitionJobStatus(ctx, job.ID, "PENDING", "QUEUED", nil); err != nil {
		t.Fatalf("PENDING→QUEUED: %v", err)
	}
	if err := store.TransitionJobStatus(ctx, job.ID, "QUEUED", "RUNNING", nil); err != nil {
		t.Fatalf("QUEUED→RUNNING: %v", err)
	}

	// sendFailed posts a FAILED status update via the internal API.
	sendFailed := func() int {
		reason := "oom killed"
		body := map[string]interface{}{"status": "FAILED", "failure_reason": reason}
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPatch, "/internal/v1/jobs/"+job.ID+"/status", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		internalHandler.ServeHTTP(rec, req)
		return rec.Code
	}

	// Cycle through max_retries=3 auto-retries. After each auto-retry the handler
	// re-queues the job to QUEUED; we advance it back to RUNNING before the next
	// FAILED call so the state machine is valid.
	for i := 0; i < 3; i++ {
		code := sendFailed()
		if code != http.StatusOK {
			t.Fatalf("attempt %d FAILED: expected 200, got %d", i+1, code)
		}
		// Verify auto-retry re-queued the job.
		got, err := store.GetJob(ctx, job.ID, tenantID)
		if err != nil {
			t.Fatalf("attempt %d: GetJob: %v", i+1, err)
		}
		if got.Status != "QUEUED" {
			t.Fatalf("attempt %d: expected QUEUED after auto-retry, got %q", i+1, got.Status)
		}

		// Advance back to RUNNING for the next iteration (or final FAILED call).
		if err := store.TransitionJobStatus(ctx, job.ID, "QUEUED", "RUNNING", nil); err != nil {
			t.Fatalf("attempt %d: QUEUED→RUNNING: %v", i+1, err)
		}
	}

	// 4th FAILED: retry_count would reach 4 which exceeds max_retries=3, so no re-queue.
	code := sendFailed()
	if code != http.StatusOK {
		t.Fatalf("final FAILED: expected 200, got %d", code)
	}

	got, err := store.GetJob(ctx, job.ID, tenantID)
	if err != nil {
		t.Fatalf("final GetJob: %v", err)
	}
	if got.Status != "FAILED" {
		t.Errorf("expected FAILED after retry exhaustion, got %q", got.Status)
	}
	if got.RetryCount != 4 {
		t.Errorf("expected retry_count=4 after exhaustion, got %d", got.RetryCount)
	}
}
