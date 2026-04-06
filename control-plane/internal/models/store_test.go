// control-plane/internal/models/store_test.go
package models_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/jobs"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/models"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/testutil"
)

func setupStoreTest(t *testing.T) (models.Store, string, string, string) {
	t.Helper()
	pool := testutil.SetupDB(t)
	ctx := context.Background()

	var tenantID, projectID string
	pool.QueryRow(ctx, `INSERT INTO tenants (name, cpu_quota, memory_quota) VALUES ('model-tenant', 16000, 32000000000) RETURNING id::text`).Scan(&tenantID)
	pool.QueryRow(ctx, `INSERT INTO projects (tenant_id, name) VALUES ($1, 'model-proj') RETURNING id::text`, tenantID).Scan(&projectID)

	// Create a succeeded training run for source linkage.
	jobStore := jobs.NewPostgresJobStore(pool)
	job := &jobs.TrainingJob{
		TenantID: tenantID, ProjectID: projectID, Name: "src-job",
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
	jobStore.SetMLflowRunID(ctx, job.ID, "mlflow-run-xyz")

	store := models.NewPostgresModelStore(pool)
	return store, tenantID, projectID, run.ID
}

func TestCreateOrGetModelRecord_Create(t *testing.T) {
	store, tenantID, projectID, _ := setupStoreTest(t)
	ctx := context.Background()

	rec := &models.ModelRecord{
		TenantID:                  tenantID,
		ProjectID:                 projectID,
		Name:                      "resnet50",
		MLflowRegisteredModelName: tenantID + "-resnet50",
	}
	if err := store.CreateOrGetModelRecord(ctx, rec); err != nil {
		t.Fatalf("create model record: %v", err)
	}
	if rec.ID == "" {
		t.Fatal("expected ID to be set after create")
	}
}

func TestCreateOrGetModelRecord_Idempotent(t *testing.T) {
	store, tenantID, projectID, _ := setupStoreTest(t)
	ctx := context.Background()

	rec1 := &models.ModelRecord{
		TenantID: tenantID, ProjectID: projectID,
		Name: "bert", MLflowRegisteredModelName: tenantID + "-bert",
	}
	if err := store.CreateOrGetModelRecord(ctx, rec1); err != nil {
		t.Fatalf("first create: %v", err)
	}

	rec2 := &models.ModelRecord{
		TenantID: tenantID, ProjectID: projectID,
		Name: "bert", MLflowRegisteredModelName: tenantID + "-bert",
	}
	if err := store.CreateOrGetModelRecord(ctx, rec2); err != nil {
		t.Fatalf("second create (should be idempotent): %v", err)
	}
	if rec1.ID != rec2.ID {
		t.Fatalf("expected same ID on duplicate create, got %q vs %q", rec1.ID, rec2.ID)
	}
}

func TestCreateModelVersion(t *testing.T) {
	store, tenantID, projectID, runID := setupStoreTest(t)
	ctx := context.Background()

	rec := &models.ModelRecord{
		TenantID: tenantID, ProjectID: projectID,
		Name: "vit", MLflowRegisteredModelName: tenantID + "-vit",
	}
	store.CreateOrGetModelRecord(ctx, rec)

	ver := &models.ModelVersion{
		ModelRecordID: rec.ID,
		TenantID:      tenantID,
		VersionNumber: 1,
		MLflowRunID:   "mlflow-run-xyz",
		SourceRunID:   runID,
		ArtifactURI:   "runs:/mlflow-run-xyz/model/",
		Status:        "candidate",
	}
	if err := store.CreateModelVersion(ctx, ver); err != nil {
		t.Fatalf("create version: %v", err)
	}
	if ver.ID == "" {
		t.Fatal("expected ID to be set after create")
	}
}

func TestGetModelRecordByName_Found(t *testing.T) {
	store, tenantID, projectID, _ := setupStoreTest(t)
	ctx := context.Background()

	rec := &models.ModelRecord{
		TenantID: tenantID, ProjectID: projectID,
		Name: "gpt2", MLflowRegisteredModelName: tenantID + "-gpt2",
	}
	store.CreateOrGetModelRecord(ctx, rec)

	got, err := store.GetModelRecordByName(ctx, "gpt2", tenantID)
	if err != nil {
		t.Fatalf("get model record: %v", err)
	}
	if got.ID != rec.ID {
		t.Fatalf("expected ID %q, got %q", rec.ID, got.ID)
	}
}

func TestGetModelRecordByName_NotFound(t *testing.T) {
	store, tenantID, _, _ := setupStoreTest(t)
	ctx := context.Background()

	_, err := store.GetModelRecordByName(ctx, "does-not-exist", tenantID)
	if !errors.Is(err, models.ErrModelNotFound) {
		t.Fatalf("expected ErrModelNotFound, got %v", err)
	}
}

func TestListModelVersions(t *testing.T) {
	store, tenantID, projectID, runID := setupStoreTest(t)
	ctx := context.Background()

	rec := &models.ModelRecord{
		TenantID: tenantID, ProjectID: projectID,
		Name: "efficientnet", MLflowRegisteredModelName: tenantID + "-efficientnet",
	}
	store.CreateOrGetModelRecord(ctx, rec)

	for i := 1; i <= 3; i++ {
		v := &models.ModelVersion{
			ModelRecordID: rec.ID, TenantID: tenantID,
			VersionNumber: i, MLflowRunID: "mlflow-run-xyz",
			SourceRunID: runID, ArtifactURI: "runs:/mlflow-run-xyz/model/",
			Status: "candidate",
		}
		store.CreateModelVersion(ctx, v)
	}

	versions, err := store.ListModelVersions(ctx, rec.ID)
	if err != nil {
		t.Fatalf("list versions: %v", err)
	}
	if len(versions) != 3 {
		t.Fatalf("expected 3 versions, got %d", len(versions))
	}
}

func TestGetModelVersionByNumber(t *testing.T) {
	store, tenantID, projectID, runID := setupStoreTest(t)
	ctx := context.Background()

	rec := &models.ModelRecord{
		TenantID: tenantID, ProjectID: projectID,
		Name: "mobilenet", MLflowRegisteredModelName: tenantID + "-mobilenet",
	}
	store.CreateOrGetModelRecord(ctx, rec)

	ver := &models.ModelVersion{
		ModelRecordID: rec.ID, TenantID: tenantID,
		VersionNumber: 1, MLflowRunID: "mlflow-run-xyz",
		SourceRunID: runID, ArtifactURI: "runs:/mlflow-run-xyz/model/",
		Status: "candidate",
	}
	store.CreateModelVersion(ctx, ver)

	got, err := store.GetModelVersionByNumber(ctx, rec.ID, 1)
	if err != nil {
		t.Fatalf("get version: %v", err)
	}
	if got.ID != ver.ID {
		t.Fatalf("expected ID %q, got %q", ver.ID, got.ID)
	}
}

func TestUpdateModelVersionStatus(t *testing.T) {
	store, tenantID, projectID, runID := setupStoreTest(t)
	ctx := context.Background()

	rec := &models.ModelRecord{
		TenantID: tenantID, ProjectID: projectID,
		Name: "squeezenet", MLflowRegisteredModelName: tenantID + "-squeezenet",
	}
	store.CreateOrGetModelRecord(ctx, rec)

	ver := &models.ModelVersion{
		ModelRecordID: rec.ID, TenantID: tenantID,
		VersionNumber: 1, MLflowRunID: "mlflow-run-xyz",
		SourceRunID: runID, ArtifactURI: "runs:/mlflow-run-xyz/model/",
		Status: "candidate",
	}
	store.CreateModelVersion(ctx, ver)

	if err := store.UpdateModelVersionStatus(ctx, ver.ID, "archived"); err != nil {
		t.Fatalf("update status: %v", err)
	}

	got, _ := store.GetModelVersionByNumber(ctx, rec.ID, 1)
	if got.Status != "archived" {
		t.Fatalf("expected status 'archived', got %q", got.Status)
	}
}

func TestPromoteModelVersionStatus_ClearsPriorHolder(t *testing.T) {
	store, tenantID, projectID, runID := setupStoreTest(t)
	ctx := context.Background()

	rec := &models.ModelRecord{
		TenantID: tenantID, ProjectID: projectID,
		Name: "alexnet", MLflowRegisteredModelName: tenantID + "-alexnet",
	}
	store.CreateOrGetModelRecord(ctx, rec)

	// Create v1 and v2.
	v1 := &models.ModelVersion{
		ModelRecordID: rec.ID, TenantID: tenantID,
		VersionNumber: 1, MLflowRunID: "mlflow-run-xyz",
		SourceRunID: runID, ArtifactURI: "runs:/mlflow-run-xyz/model/", Status: "candidate",
	}
	v2 := &models.ModelVersion{
		ModelRecordID: rec.ID, TenantID: tenantID,
		VersionNumber: 2, MLflowRunID: "mlflow-run-xyz",
		SourceRunID: runID, ArtifactURI: "runs:/mlflow-run-xyz/model/", Status: "candidate",
	}
	store.CreateModelVersion(ctx, v1)
	store.CreateModelVersion(ctx, v2)

	// Promote v1 to production.
	if err := store.PromoteModelVersionStatus(ctx, rec.ID, v1.ID, "production"); err != nil {
		t.Fatalf("promote v1 to production: %v", err)
	}
	got1, _ := store.GetModelVersionByNumber(ctx, rec.ID, 1)
	if got1.Status != "production" {
		t.Fatalf("expected v1 status 'production', got %q", got1.Status)
	}

	// Promote v2 to production — v1 should be reset to candidate.
	if err := store.PromoteModelVersionStatus(ctx, rec.ID, v2.ID, "production"); err != nil {
		t.Fatalf("promote v2 to production: %v", err)
	}
	got1, _ = store.GetModelVersionByNumber(ctx, rec.ID, 1)
	if got1.Status != "candidate" {
		t.Fatalf("expected v1 status reset to 'candidate', got %q", got1.Status)
	}
	got2, _ := store.GetModelVersionByNumber(ctx, rec.ID, 2)
	if got2.Status != "production" {
		t.Fatalf("expected v2 status 'production', got %q", got2.Status)
	}
}
