// control-plane/internal/deployments/service_test.go
package deployments_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/deployments"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/models"
)

// --- mock store ---

type mockDeploymentStore struct {
	createErr          error
	getResult          *deployments.Deployment
	getErr             error
	updateErr          error
	deleteErr          error
	listResult         []*deployments.Deployment
	listErr            error
	created            *deployments.Deployment
	currentRevision    int
	currentRevisionErr error
	revisionResult     *deployments.DeploymentRevision
	revisionErr        error
	rollbackResult     *deployments.Deployment
	rollbackErr        error
}

func (m *mockDeploymentStore) CreateDeployment(_ context.Context, d *deployments.Deployment) error {
	if m.createErr != nil {
		return m.createErr
	}
	d.ID = "dep-id-1"
	m.created = d
	return nil
}
func (m *mockDeploymentStore) GetDeployment(_ context.Context, _ string) (*deployments.Deployment, error) {
	return m.getResult, m.getErr
}
func (m *mockDeploymentStore) UpdateDeploymentStatus(_ context.Context, _, _, _ string) error {
	return m.updateErr
}
func (m *mockDeploymentStore) DeleteDeployment(_ context.Context, _ string) error {
	return m.deleteErr
}
func (m *mockDeploymentStore) ListPendingDeployments(_ context.Context) ([]*deployments.Deployment, error) {
	return m.listResult, m.listErr
}
func (m *mockDeploymentStore) GetCurrentRevisionNumber(_ context.Context, _ string) (int, error) {
	return m.currentRevision, m.currentRevisionErr
}
func (m *mockDeploymentStore) GetRevision(_ context.Context, _ string, _ int) (*deployments.DeploymentRevision, error) {
	return m.revisionResult, m.revisionErr
}
func (m *mockDeploymentStore) RollbackDeployment(_ context.Context, _, _ string) (*deployments.Deployment, error) {
	return m.rollbackResult, m.rollbackErr
}

// --- mock version reader ---

type mockVersionReader struct {
	record  *models.ModelRecord
	recErr  error
	version *models.ModelVersion
	verErr  error
}

func (m *mockVersionReader) GetModelRecordByName(_ context.Context, _, _ string) (*models.ModelRecord, error) {
	return m.record, m.recErr
}
func (m *mockVersionReader) GetModelVersionByNumber(_ context.Context, _ string, _ int) (*models.ModelVersion, error) {
	return m.version, m.verErr
}

// --- helpers ---

func productionVersion() *models.ModelVersion {
	return &models.ModelVersion{
		ID: "ver-1", ModelRecordID: "rec-1", TenantID: "tenant-1",
		VersionNumber: 1, ArtifactURI: "mlflow-artifacts:/resnet50/1/model/",
		Status: "production", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
}

func modelRecord() *models.ModelRecord {
	return &models.ModelRecord{
		ID: "rec-1", TenantID: "tenant-1", ProjectID: "proj-1",
		Name: "resnet50", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
}

// --- tests ---

func TestService_Create_HappyPath(t *testing.T) {
	svc := deployments.NewService(
		&mockDeploymentStore{},
		&mockVersionReader{record: modelRecord(), version: productionVersion()},
	)
	req := deployments.CreateDeploymentRequest{
		ModelName: "resnet50", ModelVersion: 1,
		Name: "resnet50-prod", Namespace: "default", Replicas: 1,
	}
	dep, err := svc.Create(context.Background(), "tenant-1", req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dep.ID != "dep-id-1" {
		t.Errorf("expected dep-id-1, got %q", dep.ID)
	}
	if dep.Status != "pending" {
		t.Errorf("expected status pending, got %q", dep.Status)
	}
}

func TestService_Create_DefaultsApplied(t *testing.T) {
	svc := deployments.NewService(
		&mockDeploymentStore{},
		&mockVersionReader{record: modelRecord(), version: productionVersion()},
	)
	req := deployments.CreateDeploymentRequest{
		ModelName: "resnet50", ModelVersion: 1, Name: "dep1",
		// Namespace and Replicas intentionally omitted
	}
	dep, err := svc.Create(context.Background(), "tenant-1", req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dep.Namespace != "default" {
		t.Errorf("expected default namespace, got %q", dep.Namespace)
	}
	if dep.DesiredReplicas != 1 {
		t.Errorf("expected 1 replica, got %d", dep.DesiredReplicas)
	}
}

func TestService_Create_VersionNotProduction(t *testing.T) {
	ver := productionVersion()
	ver.Status = "staging"
	svc := deployments.NewService(
		&mockDeploymentStore{},
		&mockVersionReader{record: modelRecord(), version: ver},
	)
	req := deployments.CreateDeploymentRequest{
		ModelName: "resnet50", ModelVersion: 1,
		Name: "resnet50-staging", Namespace: "default", Replicas: 1,
	}
	_, err := svc.Create(context.Background(), "tenant-1", req)
	if !errors.Is(err, deployments.ErrModelVersionNotProduction) {
		t.Fatalf("expected ErrModelVersionNotProduction, got %v", err)
	}
}

func TestService_Create_ModelNotFound(t *testing.T) {
	svc := deployments.NewService(
		&mockDeploymentStore{},
		&mockVersionReader{recErr: models.ErrModelNotFound},
	)
	req := deployments.CreateDeploymentRequest{
		ModelName: "missing", ModelVersion: 1,
		Name: "missing-dep", Namespace: "default", Replicas: 1,
	}
	_, err := svc.Create(context.Background(), "tenant-1", req)
	if !errors.Is(err, deployments.ErrModelNotFound) {
		t.Fatalf("expected deployments.ErrModelNotFound, got %v", err)
	}
}

func TestService_Create_VersionNotFound(t *testing.T) {
	svc := deployments.NewService(
		&mockDeploymentStore{},
		&mockVersionReader{record: modelRecord(), verErr: models.ErrVersionNotFound},
	)
	req := deployments.CreateDeploymentRequest{
		ModelName: "resnet50", ModelVersion: 99,
		Name: "dep1", Namespace: "default", Replicas: 1,
	}
	_, err := svc.Create(context.Background(), "tenant-1", req)
	if !errors.Is(err, deployments.ErrVersionNotFound) {
		t.Fatalf("expected deployments.ErrVersionNotFound, got %v", err)
	}
}

func TestService_Create_DuplicateName(t *testing.T) {
	svc := deployments.NewService(
		&mockDeploymentStore{createErr: deployments.ErrDuplicateDeploymentName},
		&mockVersionReader{record: modelRecord(), version: productionVersion()},
	)
	req := deployments.CreateDeploymentRequest{
		ModelName: "resnet50", ModelVersion: 1,
		Name: "resnet50-prod", Namespace: "default", Replicas: 1,
	}
	_, err := svc.Create(context.Background(), "tenant-1", req)
	if !errors.Is(err, deployments.ErrDuplicateDeploymentName) {
		t.Fatalf("expected ErrDuplicateDeploymentName, got %v", err)
	}
}

func TestService_Create_MissingFields(t *testing.T) {
	svc := deployments.NewService(&mockDeploymentStore{}, &mockVersionReader{})
	_, err := svc.Create(context.Background(), "tenant-1", deployments.CreateDeploymentRequest{})
	if err == nil {
		t.Fatal("expected validation error for empty request")
	}
}

func TestService_Get_HappyPath(t *testing.T) {
	dep := &deployments.Deployment{ID: "dep-1", TenantID: "tenant-1", Name: "resnet50-prod", Status: "running"}
	svc := deployments.NewService(
		&mockDeploymentStore{getResult: dep},
		&mockVersionReader{},
	)
	got, err := svc.Get(context.Background(), "dep-1", "tenant-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != "dep-1" {
		t.Errorf("expected dep-1, got %q", got.ID)
	}
}

func TestService_Get_WrongTenant(t *testing.T) {
	dep := &deployments.Deployment{ID: "dep-1", TenantID: "tenant-A", Name: "resnet50-prod", Status: "running"}
	svc := deployments.NewService(
		&mockDeploymentStore{getResult: dep},
		&mockVersionReader{},
	)
	_, err := svc.Get(context.Background(), "dep-1", "tenant-B")
	if !errors.Is(err, deployments.ErrDeploymentNotFound) {
		t.Fatalf("expected ErrDeploymentNotFound for wrong tenant, got %v", err)
	}
}

func TestService_Get_NotFound(t *testing.T) {
	svc := deployments.NewService(
		&mockDeploymentStore{getErr: deployments.ErrDeploymentNotFound},
		&mockVersionReader{},
	)
	_, err := svc.Get(context.Background(), "bad-id", "tenant-1")
	if !errors.Is(err, deployments.ErrDeploymentNotFound) {
		t.Fatalf("expected ErrDeploymentNotFound, got %v", err)
	}
}

func TestService_Delete_HappyPath(t *testing.T) {
	dep := &deployments.Deployment{ID: "dep-1", TenantID: "tenant-1", Status: "running"}
	svc := deployments.NewService(
		&mockDeploymentStore{getResult: dep},
		&mockVersionReader{},
	)
	if err := svc.Delete(context.Background(), "dep-1", "tenant-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestService_Delete_WrongTenant(t *testing.T) {
	dep := &deployments.Deployment{ID: "dep-1", TenantID: "tenant-A", Status: "running"}
	svc := deployments.NewService(
		&mockDeploymentStore{getResult: dep},
		&mockVersionReader{},
	)
	err := svc.Delete(context.Background(), "dep-1", "tenant-B")
	if !errors.Is(err, deployments.ErrDeploymentNotFound) {
		t.Fatalf("expected ErrDeploymentNotFound for wrong tenant, got %v", err)
	}
}

func TestService_Delete_NotFound(t *testing.T) {
	svc := deployments.NewService(
		&mockDeploymentStore{getErr: deployments.ErrDeploymentNotFound},
		&mockVersionReader{},
	)
	err := svc.Delete(context.Background(), "bad-id", "tenant-1")
	if !errors.Is(err, deployments.ErrDeploymentNotFound) {
		t.Fatalf("expected ErrDeploymentNotFound, got %v", err)
	}
}

func TestService_Rollback_HappyPath(t *testing.T) {
	dep := &deployments.Deployment{ID: "dep-1", TenantID: "tenant-1", Status: "running", ModelVersionID: "ver-2"}
	rollbacked := &deployments.Deployment{ID: "dep-1", TenantID: "tenant-1", Status: "pending", ModelVersionID: "ver-1"}
	rev := &deployments.DeploymentRevision{ID: "rev-1", DeploymentID: "dep-1", RevisionNumber: 1, ModelVersionID: "ver-1", Status: "active"}
	svc := deployments.NewService(
		&mockDeploymentStore{
			getResult:       dep,
			currentRevision: 2,
			revisionResult:  rev,
			rollbackResult:  rollbacked,
		},
		&mockVersionReader{},
	)
	got, err := svc.Rollback(context.Background(), "dep-1", "tenant-1", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Status != "pending" {
		t.Errorf("expected status pending, got %q", got.Status)
	}
	if got.ModelVersionID != "ver-1" {
		t.Errorf("expected model version ver-1, got %q", got.ModelVersionID)
	}
}

func TestService_Rollback_ExplicitRevision(t *testing.T) {
	dep := &deployments.Deployment{ID: "dep-1", TenantID: "tenant-1", Status: "running", ModelVersionID: "ver-3"}
	rollbacked := &deployments.Deployment{ID: "dep-1", TenantID: "tenant-1", Status: "pending", ModelVersionID: "ver-1"}
	rev := &deployments.DeploymentRevision{ID: "rev-1", DeploymentID: "dep-1", RevisionNumber: 1, ModelVersionID: "ver-1", Status: "active"}
	svc := deployments.NewService(
		&mockDeploymentStore{
			getResult:       dep,
			currentRevision: 3,
			revisionResult:  rev,
			rollbackResult:  rollbacked,
		},
		&mockVersionReader{},
	)
	got, err := svc.Rollback(context.Background(), "dep-1", "tenant-1", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ModelVersionID != "ver-1" {
		t.Errorf("expected model version ver-1, got %q", got.ModelVersionID)
	}
}

func TestService_Rollback_AtRevision1_ReturnsError(t *testing.T) {
	dep := &deployments.Deployment{ID: "dep-1", TenantID: "tenant-1", Status: "running"}
	svc := deployments.NewService(
		&mockDeploymentStore{
			getResult:       dep,
			currentRevision: 1,
		},
		&mockVersionReader{},
	)
	_, err := svc.Rollback(context.Background(), "dep-1", "tenant-1", 0)
	if err == nil {
		t.Fatal("expected error when already at revision 1")
	}
}

func TestService_Rollback_WrongTenant(t *testing.T) {
	dep := &deployments.Deployment{ID: "dep-1", TenantID: "tenant-A", Status: "running"}
	svc := deployments.NewService(
		&mockDeploymentStore{getResult: dep},
		&mockVersionReader{},
	)
	_, err := svc.Rollback(context.Background(), "dep-1", "tenant-B", 0)
	if !errors.Is(err, deployments.ErrDeploymentNotFound) {
		t.Fatalf("expected ErrDeploymentNotFound for wrong tenant, got %v", err)
	}
}

func TestService_Rollback_DeploymentNotFound(t *testing.T) {
	svc := deployments.NewService(
		&mockDeploymentStore{getErr: deployments.ErrDeploymentNotFound},
		&mockVersionReader{},
	)
	_, err := svc.Rollback(context.Background(), "bad-id", "tenant-1", 0)
	if !errors.Is(err, deployments.ErrDeploymentNotFound) {
		t.Fatalf("expected ErrDeploymentNotFound, got %v", err)
	}
}
