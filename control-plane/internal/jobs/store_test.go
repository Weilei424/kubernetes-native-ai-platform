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
