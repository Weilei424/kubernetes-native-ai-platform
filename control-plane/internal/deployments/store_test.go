// control-plane/internal/deployments/store_test.go
package deployments_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/deployments"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/jobs"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/models"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/testutil"
)

func setupDeploymentStoreTest(t *testing.T) (deployments.Store, string, string, string, string) {
	t.Helper()
	pool := testutil.SetupDB(t)
	ctx := context.Background()

	var tenantID, projectID string
	pool.QueryRow(ctx, `INSERT INTO tenants (name, cpu_quota, memory_quota) VALUES ('dep-tenant', 16000, 32000000000) RETURNING id::text`).Scan(&tenantID)
	pool.QueryRow(ctx, `INSERT INTO projects (tenant_id, name) VALUES ($1, 'dep-proj') RETURNING id::text`, tenantID).Scan(&projectID)

	// Create a succeeded training run.
	jobStore := jobs.NewPostgresJobStore(pool)
	job := &jobs.TrainingJob{
		TenantID: tenantID, ProjectID: projectID, Name: "dep-src-job",
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

	// Create model record + version at "production" status.
	modelStore := models.NewPostgresModelStore(pool)
	rec := &models.ModelRecord{
		TenantID:                  tenantID,
		ProjectID:                 projectID,
		Name:                      "resnet50",
		MLflowRegisteredModelName: tenantID + "-resnet50",
	}
	if err := modelStore.CreateOrGetModelRecord(ctx, rec); err != nil {
		t.Fatalf("setup: create model record: %v", err)
	}

	ver := &models.ModelVersion{
		ModelRecordID: rec.ID,
		TenantID:      tenantID,
		VersionNumber: 1,
		MLflowRunID:   "mlflow-run-xyz",
		SourceRunID:   run.ID,
		ArtifactURI:   "mlflow-artifacts:/resnet50/1/model/",
		Status:        "production",
	}
	if err := modelStore.CreateModelVersion(ctx, ver); err != nil {
		t.Fatalf("setup: create model version: %v", err)
	}

	store := deployments.NewPostgresDeploymentStore(pool)
	return store, tenantID, projectID, rec.ID, ver.ID
}

func TestDeploymentStore_CreateAndGet(t *testing.T) {
	store, tenantID, projectID, modelRecordID, modelVersionID := setupDeploymentStoreTest(t)
	ctx := context.Background()

	d := &deployments.Deployment{
		TenantID:        tenantID,
		ProjectID:       projectID,
		ModelRecordID:   modelRecordID,
		ModelVersionID:  modelVersionID,
		Name:            "resnet50-prod",
		Namespace:       "default",
		Status:          "pending",
		DesiredReplicas: 1,
	}
	if err := store.CreateDeployment(ctx, d); err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	if d.ID == "" {
		t.Fatal("expected ID to be set after create")
	}

	got, err := store.GetDeployment(ctx, d.ID)
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	if got.Name != "resnet50-prod" {
		t.Errorf("expected name resnet50-prod, got %q", got.Name)
	}
	if got.Status != "pending" {
		t.Errorf("expected status pending, got %q", got.Status)
	}
}

func TestDeploymentStore_DuplicateName(t *testing.T) {
	store, tenantID, projectID, recID, verID := setupDeploymentStoreTest(t)
	ctx := context.Background()

	d := &deployments.Deployment{
		TenantID: tenantID, ProjectID: projectID,
		ModelRecordID: recID, ModelVersionID: verID,
		Name: "dup-dep", Namespace: "default", Status: "pending", DesiredReplicas: 1,
	}
	if err := store.CreateDeployment(ctx, d); err != nil {
		t.Fatalf("first create: %v", err)
	}
	d2 := &deployments.Deployment{
		TenantID: tenantID, ProjectID: projectID,
		ModelRecordID: recID, ModelVersionID: verID,
		Name: "dup-dep", Namespace: "default", Status: "pending", DesiredReplicas: 1,
	}
	err := store.CreateDeployment(ctx, d2)
	if !errors.Is(err, deployments.ErrDuplicateDeploymentName) {
		t.Fatalf("expected ErrDuplicateDeploymentName, got %v", err)
	}
}

func TestDeploymentStore_UpdateStatus(t *testing.T) {
	store, tenantID, projectID, recID, verID := setupDeploymentStoreTest(t)
	ctx := context.Background()

	d := &deployments.Deployment{
		TenantID: tenantID, ProjectID: projectID,
		ModelRecordID: recID, ModelVersionID: verID,
		Name: "status-dep", Namespace: "default", Status: "pending", DesiredReplicas: 1,
	}
	store.CreateDeployment(ctx, d)

	endpoint := "triton-" + d.ID + ".default.svc.cluster.local:8000"
	if err := store.UpdateDeploymentStatus(ctx, d.ID, "running", endpoint); err != nil {
		t.Fatalf("update status: %v", err)
	}

	got, _ := store.GetDeployment(ctx, d.ID)
	if got.Status != "running" {
		t.Errorf("expected status running, got %q", got.Status)
	}
	if got.ServingEndpoint != endpoint {
		t.Errorf("expected endpoint %q, got %q", endpoint, got.ServingEndpoint)
	}
}

func TestDeploymentStore_ListPendingDeployments(t *testing.T) {
	store, tenantID, projectID, recID, verID := setupDeploymentStoreTest(t)
	ctx := context.Background()

	d := &deployments.Deployment{
		TenantID: tenantID, ProjectID: projectID,
		ModelRecordID: recID, ModelVersionID: verID,
		Name: "list-dep", Namespace: "default", Status: "pending", DesiredReplicas: 1,
	}
	store.CreateDeployment(ctx, d)

	deps, err := store.ListPendingDeployments(ctx)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(deps) == 0 {
		t.Fatal("expected at least one pending deployment")
	}
	var found *deployments.Deployment
	for _, dep := range deps {
		if dep.ID == d.ID {
			found = dep
			break
		}
	}
	if found == nil {
		t.Fatal("created deployment not found in list")
	}
	if found.ArtifactURI == "" {
		t.Error("expected ArtifactURI to be populated by ListPendingDeployments")
	}
	if found.ModelName == "" {
		t.Error("expected ModelName to be populated by ListPendingDeployments")
	}
}

func TestDeploymentStore_GetNotFound(t *testing.T) {
	store, _, _, _, _ := setupDeploymentStoreTest(t)
	_, err := store.GetDeployment(context.Background(), "00000000-0000-0000-0000-000000000000")
	if !errors.Is(err, deployments.ErrDeploymentNotFound) {
		t.Fatalf("expected ErrDeploymentNotFound, got %v", err)
	}
}

func TestDeploymentStore_DeleteDeployment(t *testing.T) {
	store, tenantID, projectID, recID, verID := setupDeploymentStoreTest(t)
	ctx := context.Background()

	d := &deployments.Deployment{
		TenantID: tenantID, ProjectID: projectID,
		ModelRecordID: recID, ModelVersionID: verID,
		Name: "del-dep", Namespace: "default", Status: "pending", DesiredReplicas: 1,
	}
	if err := store.CreateDeployment(ctx, d); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.DeleteDeployment(ctx, d.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	// Verify status is now "deleting" (operator will transition to "deleted" after K8s cleanup).
	got, err := store.GetDeployment(ctx, d.ID)
	if err != nil {
		t.Fatalf("get after delete: %v", err)
	}
	if got.Status != "deleting" {
		t.Errorf("expected status deleting, got %q", got.Status)
	}
	// Second delete on already-deleting returns ErrDeploymentNotFound (idempotent guard).
	if err := store.DeleteDeployment(ctx, d.ID); !errors.Is(err, deployments.ErrDeploymentNotFound) {
		t.Fatalf("expected ErrDeploymentNotFound on second delete, got %v", err)
	}
}

func TestDeploymentStore_UpdateStatus_NotFound(t *testing.T) {
	store, _, _, _, _ := setupDeploymentStoreTest(t)
	err := store.UpdateDeploymentStatus(context.Background(), "00000000-0000-0000-0000-000000000000", "running", "")
	if !errors.Is(err, deployments.ErrDeploymentNotFound) {
		t.Fatalf("expected ErrDeploymentNotFound, got %v", err)
	}
}

func TestStore_RollbackDeployment(t *testing.T) {
	store, tenantID, projectID, recID, verID := setupDeploymentStoreTest(t)
	ctx := context.Background()

	// Create the deployment (revision 1 is inserted automatically).
	d := &deployments.Deployment{
		TenantID:        tenantID,
		ProjectID:       projectID,
		ModelRecordID:   recID,
		ModelVersionID:  verID,
		Name:            "rollback-dep",
		Namespace:       "default",
		Status:          "running",
		DesiredReplicas: 1,
	}
	if err := store.CreateDeployment(ctx, d); err != nil {
		t.Fatalf("create deployment: %v", err)
	}

	// Confirm revision 1 exists and current revision is 1.
	rev1, err := store.GetCurrentRevisionNumber(ctx, d.ID)
	if err != nil {
		t.Fatalf("get current revision number: %v", err)
	}
	if rev1 != 1 {
		t.Fatalf("expected revision 1 after create, got %d", rev1)
	}

	// Rollback using the same model version (simulates reverting to rev 1 from rev 1+).
	rolled, err := store.RollbackDeployment(ctx, d.ID, verID)
	if err != nil {
		t.Fatalf("rollback deployment: %v", err)
	}

	// The returned deployment must be pending and have the target model version.
	if rolled.Status != "pending" {
		t.Errorf("expected status pending after rollback, got %q", rolled.Status)
	}
	if rolled.ModelVersionID != verID {
		t.Errorf("expected model version %q, got %q", verID, rolled.ModelVersionID)
	}
	if rolled.ServingEndpoint != "" {
		t.Errorf("expected serving_endpoint to be cleared, got %q", rolled.ServingEndpoint)
	}

	// A new revision (2) must now exist.
	rev2, err := store.GetCurrentRevisionNumber(ctx, d.ID)
	if err != nil {
		t.Fatalf("get current revision number after rollback: %v", err)
	}
	if rev2 != 2 {
		t.Fatalf("expected revision 2 after rollback, got %d", rev2)
	}

	// GetRevision must return revision 2.
	revRecord, err := store.GetRevision(ctx, d.ID, 2)
	if err != nil {
		t.Fatalf("get revision 2: %v", err)
	}
	if revRecord.RevisionNumber != 2 {
		t.Errorf("expected revision_number 2, got %d", revRecord.RevisionNumber)
	}
	if revRecord.ModelVersionID != verID {
		t.Errorf("expected model_version_id %q, got %q", verID, revRecord.ModelVersionID)
	}
}
