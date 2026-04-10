// control-plane/internal/jobs/store_test.go
package jobs_test

import (
	"context"
	"testing"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/jobs"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/testutil"
)

func setupStoreTest(t *testing.T) (jobs.Store, context.Context, string, string) {
	t.Helper()
	pool := testutil.SetupDB(t)
	ctx := context.Background()

	var tenantID, projectID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO tenants (name) VALUES ('store-test-tenant') RETURNING id::text`,
	).Scan(&tenantID); err != nil {
		t.Fatalf("insert tenant: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO projects (tenant_id, name) VALUES ($1, 'store-test-project') RETURNING id::text`,
		tenantID,
	).Scan(&projectID); err != nil {
		t.Fatalf("insert project: %v", err)
	}

	store := jobs.NewPostgresJobStore(pool)
	return store, ctx, tenantID, projectID
}

func TestCreateJobWithRun(t *testing.T) {
	store, ctx, tenantID, projectID := setupStoreTest(t)

	job := &jobs.TrainingJob{
		TenantID: tenantID, ProjectID: projectID, Name: "test-job",
		Status: "PENDING", Image: "test:latest", Command: []string{"python", "train.py"},
		Args: []string{}, Env: map[string]string{},
		NumWorkers: 2, WorkerCPU: "2", WorkerMemory: "4Gi",
		HeadCPU: "1", HeadMemory: "2Gi",
	}
	run := &jobs.TrainingRun{TenantID: tenantID, Status: "PENDING"}

	if err := store.CreateJobWithRun(ctx, job, run); err != nil {
		t.Fatalf("CreateJobWithRun: %v", err)
	}
	if job.ID == "" {
		t.Fatal("expected job.ID to be set after create")
	}
	if run.ID == "" {
		t.Fatal("expected run.ID to be set after create")
	}
	if run.JobID != job.ID {
		t.Fatalf("run.JobID %q != job.ID %q", run.JobID, job.ID)
	}
}

func TestGetJob(t *testing.T) {
	store, ctx, tenantID, projectID := setupStoreTest(t)

	job := &jobs.TrainingJob{
		TenantID: tenantID, ProjectID: projectID, Name: "get-test-job",
		Status: "PENDING", Image: "img:1", Command: []string{"run"},
		Args: []string{}, Env: map[string]string{},
		NumWorkers: 1, WorkerCPU: "1", WorkerMemory: "1Gi",
		HeadCPU: "1", HeadMemory: "1Gi",
	}
	run := &jobs.TrainingRun{TenantID: tenantID, Status: "PENDING"}
	if err := store.CreateJobWithRun(ctx, job, run); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := store.GetJob(ctx, job.ID, tenantID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.Name != job.Name {
		t.Fatalf("name mismatch: %q vs %q", got.Name, job.Name)
	}
}

func TestTransitionJobStatus(t *testing.T) {
	store, ctx, tenantID, projectID := setupStoreTest(t)

	job := &jobs.TrainingJob{
		TenantID: tenantID, ProjectID: projectID, Name: "transition-job",
		Status: "PENDING", Image: "img:1", Command: []string{"run"},
		Args: []string{}, Env: map[string]string{},
		NumWorkers: 1, WorkerCPU: "1", WorkerMemory: "1Gi",
		HeadCPU: "1", HeadMemory: "1Gi",
	}
	run := &jobs.TrainingRun{TenantID: tenantID, Status: "PENDING"}
	if err := store.CreateJobWithRun(ctx, job, run); err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := store.TransitionJobStatus(ctx, job.ID, "PENDING", "QUEUED", nil); err != nil {
		t.Fatalf("TransitionJobStatus: %v", err)
	}

	got, _ := store.GetJob(ctx, job.ID, tenantID)
	if got.Status != "QUEUED" {
		t.Fatalf("expected QUEUED, got %q", got.Status)
	}
}

func TestTransitionJobStatus_InvalidTransition(t *testing.T) {
	store, ctx, tenantID, projectID := setupStoreTest(t)

	job := &jobs.TrainingJob{
		TenantID: tenantID, ProjectID: projectID, Name: "bad-transition-job",
		Status: "PENDING", Image: "img:1", Command: []string{"run"},
		Args: []string{}, Env: map[string]string{},
		NumWorkers: 1, WorkerCPU: "1", WorkerMemory: "1Gi",
		HeadCPU: "1", HeadMemory: "1Gi",
	}
	run := &jobs.TrainingRun{TenantID: tenantID, Status: "PENDING"}
	store.CreateJobWithRun(ctx, job, run)

	err := store.TransitionJobStatus(ctx, job.ID, "PENDING", "SUCCEEDED", nil)
	if err == nil {
		t.Fatal("expected error for PENDING → SUCCEEDED, got nil")
	}
}

// TestTransitionJobStatus_OnlyUpdatesLatestRun verifies that after a retry a second
// training_run is created and TransitionJobStatus updates only the newest run, leaving
// the historical FAILED run record intact.
func TestTransitionJobStatus_OnlyUpdatesLatestRun(t *testing.T) {
	store, ctx, tenantID, projectID := setupStoreTest(t)

	job := &jobs.TrainingJob{
		TenantID: tenantID, ProjectID: projectID, Name: "retry-isolation-job",
		Status: "PENDING", Image: "img:1", Command: []string{"run"},
		Args: []string{}, Env: map[string]string{},
		NumWorkers: 1, WorkerCPU: "1", WorkerMemory: "1Gi",
		HeadCPU: "1", HeadMemory: "1Gi",
	}
	firstRun := &jobs.TrainingRun{TenantID: tenantID, Status: "PENDING"}
	if err := store.CreateJobWithRun(ctx, job, firstRun); err != nil {
		t.Fatalf("CreateJobWithRun: %v", err)
	}

	// Advance job to FAILED (simulates first attempt failing).
	for _, t2 := range []struct{ from, to string }{
		{"PENDING", "QUEUED"}, {"QUEUED", "RUNNING"},
	} {
		if err := store.TransitionJobStatus(ctx, job.ID, t2.from, t2.to, nil); err != nil {
			t.Fatalf("transition %s→%s: %v", t2.from, t2.to, err)
		}
	}
	reason := "oom"
	if err := store.TransitionJobStatus(ctx, job.ID, "RUNNING", "FAILED", &reason); err != nil {
		t.Fatalf("RUNNING→FAILED: %v", err)
	}

	// Simulate retry: create a new run and re-queue the job.
	retryRun, err := store.CreateRetryRun(ctx, job.ID, tenantID)
	if err != nil {
		t.Fatalf("CreateRetryRun: %v", err)
	}
	if err := store.TransitionJobStatus(ctx, job.ID, "FAILED", "QUEUED", nil); err != nil {
		t.Fatalf("FAILED→QUEUED: %v", err)
	}
	if err := store.TransitionJobStatus(ctx, job.ID, "QUEUED", "RUNNING", nil); err != nil {
		t.Fatalf("QUEUED→RUNNING: %v", err)
	}

	// Now verify: firstRun must still be FAILED (historical record preserved).
	historical, err := store.GetRun(ctx, job.ID, firstRun.ID, tenantID)
	if err != nil {
		t.Fatalf("GetRun (historical): %v", err)
	}
	if historical.Status != "FAILED" {
		t.Errorf("expected historical run to remain FAILED, got %q", historical.Status)
	}

	// The retry run should be RUNNING.
	latest, err := store.GetRun(ctx, job.ID, retryRun.ID, tenantID)
	if err != nil {
		t.Fatalf("GetRun (retry): %v", err)
	}
	if latest.Status != "RUNNING" {
		t.Errorf("expected retry run to be RUNNING, got %q", latest.Status)
	}
}

// TestSetMLflowRunID_OnlyUpdatesLatestRun verifies that SetMLflowRunID targets only the
// most recent training_run for a job, not all historical runs.
func TestSetMLflowRunID_OnlyUpdatesLatestRun(t *testing.T) {
	store, ctx, tenantID, projectID := setupStoreTest(t)

	job := &jobs.TrainingJob{
		TenantID: tenantID, ProjectID: projectID, Name: "mlflow-isolation-job",
		Status: "PENDING", Image: "img:1", Command: []string{"run"},
		Args: []string{}, Env: map[string]string{},
		NumWorkers: 1, WorkerCPU: "1", WorkerMemory: "1Gi",
		HeadCPU: "1", HeadMemory: "1Gi",
	}
	firstRun := &jobs.TrainingRun{TenantID: tenantID, Status: "PENDING"}
	if err := store.CreateJobWithRun(ctx, job, firstRun); err != nil {
		t.Fatalf("CreateJobWithRun: %v", err)
	}

	// Fail the first run, then create a retry run.
	for _, t2 := range []struct{ from, to string }{
		{"PENDING", "QUEUED"}, {"QUEUED", "RUNNING"},
	} {
		if err := store.TransitionJobStatus(ctx, job.ID, t2.from, t2.to, nil); err != nil {
			t.Fatalf("%s→%s: %v", t2.from, t2.to, err)
		}
	}
	if err := store.TransitionJobStatus(ctx, job.ID, "RUNNING", "FAILED", nil); err != nil {
		t.Fatalf("RUNNING→FAILED: %v", err)
	}
	retryRun, err := store.CreateRetryRun(ctx, job.ID, tenantID)
	if err != nil {
		t.Fatalf("CreateRetryRun: %v", err)
	}

	// Set mlflow_run_id — should only update the retry run.
	if err := store.SetMLflowRunID(ctx, job.ID, "mlflow-xyz"); err != nil {
		t.Fatalf("SetMLflowRunID: %v", err)
	}

	// Historical run must NOT have an mlflow_run_id.
	historical, err := store.GetRun(ctx, job.ID, firstRun.ID, tenantID)
	if err != nil {
		t.Fatalf("GetRun (historical): %v", err)
	}
	if historical.MLflowRunID != nil {
		t.Errorf("expected historical run to have no mlflow_run_id, got %q", *historical.MLflowRunID)
	}

	// Retry run must have the mlflow_run_id.
	latest, err := store.GetRun(ctx, job.ID, retryRun.ID, tenantID)
	if err != nil {
		t.Fatalf("GetRun (retry): %v", err)
	}
	if latest.MLflowRunID == nil || *latest.MLflowRunID != "mlflow-xyz" {
		t.Errorf("expected retry run mlflow_run_id=%q, got %v", "mlflow-xyz", latest.MLflowRunID)
	}
}
