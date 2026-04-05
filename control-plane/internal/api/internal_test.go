// control-plane/internal/api/internal_test.go
package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/api"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/events"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/jobs"
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
	handler := api.NewInternalRouter(store, &events.NoOpPublisher{})
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

func TestInternalStatus_QueuedToFailed(t *testing.T) {
	handler, store, tenantID, projectID := setupInternalTest(t)
	ctx := context.Background()

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

	got, _ := store.GetJob(ctx, job.ID, tenantID)
	if got.Status != "FAILED" {
		t.Fatalf("expected FAILED, got %q", got.Status)
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
