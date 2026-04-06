// control-plane/internal/models/service_test.go
package models_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/jobs"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/mlflow"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/models"
)

// --- Mock MLflow client ---

type mockMLflowClient struct {
	createRegisteredModelCreated bool
	createRegisteredModelErr     error
	createVersionNum             int
	createVersionArtifactURI     string
	createVersionErr             error
	deleteVersionErr             error
	deleteVersionCalled          bool
	deleteRegisteredModelErr     error
	deleteRegisteredModelCalled  bool
	setAliasErr                  error
	deleteAliasErr               error
	getVersionByAliasNum         int
	getVersionByAliasErr         error
	deletedAliases               []string
}

func (m *mockMLflowClient) CreateRegisteredModel(ctx context.Context, name string) (bool, error) {
	return m.createRegisteredModelCreated, m.createRegisteredModelErr
}
func (m *mockMLflowClient) DeleteRegisteredModel(ctx context.Context, name string) error {
	m.deleteRegisteredModelCalled = true
	return m.deleteRegisteredModelErr
}
func (m *mockMLflowClient) CreateModelVersion(ctx context.Context, modelName, sourceURI, runID string) (int, string, error) {
	return m.createVersionNum, m.createVersionArtifactURI, m.createVersionErr
}
func (m *mockMLflowClient) DeleteModelVersion(ctx context.Context, modelName string, version int) error {
	m.deleteVersionCalled = true
	return m.deleteVersionErr
}
func (m *mockMLflowClient) SetModelAlias(ctx context.Context, modelName, alias string, version int) error {
	return m.setAliasErr
}
func (m *mockMLflowClient) DeleteModelAlias(ctx context.Context, modelName, alias string) error {
	m.deletedAliases = append(m.deletedAliases, alias)
	return m.deleteAliasErr
}
func (m *mockMLflowClient) GetModelVersionByAlias(ctx context.Context, modelName, alias string) (int, error) {
	return m.getVersionByAliasNum, m.getVersionByAliasErr
}

// --- Mock RunReader ---

type mockRunReader struct {
	projectID   string
	status      string
	mlflowRunID *string
	err         error
}

func (m *mockRunReader) GetRunForRegistration(ctx context.Context, runID, tenantID string) (string, string, *string, error) {
	return m.projectID, m.status, m.mlflowRunID, m.err
}

// --- Mock Store ---

type mockModelStore struct {
	record               *models.ModelRecord
	versions             []*models.ModelVersion
	createRecordErr      error
	createVersionErr     error
	getRecordErr         error
	getVersionErr        error
	updateStatusErr      error
	promoteStatusErr     error
	promoteStatusCalled  bool
}

func (m *mockModelStore) CreateOrGetModelRecord(ctx context.Context, rec *models.ModelRecord) error {
	if m.createRecordErr != nil {
		return m.createRecordErr
	}
	rec.ID = "mock-record-id"
	return nil
}
func (m *mockModelStore) CreateModelVersion(ctx context.Context, ver *models.ModelVersion) error {
	if m.createVersionErr != nil {
		return m.createVersionErr
	}
	ver.ID = "mock-version-id"
	return nil
}
func (m *mockModelStore) GetModelRecordByName(ctx context.Context, name, tenantID string) (*models.ModelRecord, error) {
	if m.getRecordErr != nil {
		return nil, m.getRecordErr
	}
	if m.record != nil {
		return m.record, nil
	}
	return &models.ModelRecord{
		ID: "mock-record-id", TenantID: tenantID,
		Name: name, MLflowRegisteredModelName: tenantID + "-" + name,
	}, nil
}
func (m *mockModelStore) ListModelVersions(ctx context.Context, modelRecordID string) ([]*models.ModelVersion, error) {
	return m.versions, nil
}
func (m *mockModelStore) GetModelVersionByNumber(ctx context.Context, modelRecordID string, versionNumber int) (*models.ModelVersion, error) {
	if m.getVersionErr != nil {
		return nil, m.getVersionErr
	}
	if len(m.versions) > 0 {
		return m.versions[0], nil
	}
	return &models.ModelVersion{
		ID: "mock-version-id", ModelRecordID: modelRecordID,
		VersionNumber: versionNumber, Status: "candidate",
	}, nil
}
func (m *mockModelStore) UpdateModelVersionStatus(ctx context.Context, id, status string) error {
	return m.updateStatusErr
}
func (m *mockModelStore) PromoteModelVersionStatus(ctx context.Context, modelRecordID, versionID, alias string) error {
	m.promoteStatusCalled = true
	return m.promoteStatusErr
}

// --- Tests ---

func mlflowRunID(s string) *string { return &s }

func TestService_Register_HappyPath(t *testing.T) {
	mlflowRunIDStr := "mlflow-run-abc"
	reader := &mockRunReader{projectID: "proj-1", status: "SUCCEEDED", mlflowRunID: &mlflowRunIDStr}
	mc := &mockMLflowClient{
		createRegisteredModelCreated: true,
		createVersionNum:             1,
		createVersionArtifactURI:     "runs:/mlflow-run-abc/model/",
	}
	store := &mockModelStore{}

	svc := models.NewService(store, reader, mc)
	ver, err := svc.Register(context.Background(), "tenant-1", models.RegisterRequest{
		RunID: "run-uuid", ModelName: "resnet50",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ver.VersionNumber != 1 {
		t.Errorf("expected version 1, got %d", ver.VersionNumber)
	}
	if ver.ArtifactURI != "runs:/mlflow-run-abc/model/" {
		t.Errorf("unexpected artifact URI: %s", ver.ArtifactURI)
	}
	if ver.Status != "candidate" {
		t.Errorf("expected status 'candidate', got %s", ver.Status)
	}
}

func TestService_Register_RunNotFound(t *testing.T) {
	// The jobs store returns ErrRunNotFound for missing runs; service maps it to models.ErrRunNotFound.
	reader := &mockRunReader{err: jobs.ErrRunNotFound}
	svc := models.NewService(&mockModelStore{}, reader, &mockMLflowClient{})

	_, err := svc.Register(context.Background(), "tenant-1", models.RegisterRequest{
		RunID: "run-uuid", ModelName: "resnet50",
	})
	if !errors.Is(err, models.ErrRunNotFound) {
		t.Fatalf("expected ErrRunNotFound, got %v", err)
	}
}

func TestService_Register_InfraError(t *testing.T) {
	// A generic DB error is propagated as-is, not masked as ErrRunNotFound.
	reader := &mockRunReader{err: errors.New("connection refused")}
	svc := models.NewService(&mockModelStore{}, reader, &mockMLflowClient{})

	_, err := svc.Register(context.Background(), "tenant-1", models.RegisterRequest{
		RunID: "run-uuid", ModelName: "resnet50",
	})
	if errors.Is(err, models.ErrRunNotFound) {
		t.Fatal("infra error should not be reported as ErrRunNotFound")
	}
	if err == nil {
		t.Fatal("expected an error")
	}
}

func TestService_Register_PGVersionFailureCleansUpVersion(t *testing.T) {
	// PG CreateModelVersion fails after the model record was persisted.
	// Only the MLflow version should be cleaned up (registered model is tracked via model record).
	mlflowRunIDStr := "mlflow-run-abc"
	reader := &mockRunReader{projectID: "proj-1", status: "SUCCEEDED", mlflowRunID: &mlflowRunIDStr}
	mc := &mockMLflowClient{
		createRegisteredModelCreated: true,
		createVersionNum:             1,
		createVersionArtifactURI:     "runs:/mlflow-run-abc/model/",
	}
	store := &mockModelStore{createVersionErr: errors.New("db error")}

	svc := models.NewService(store, reader, mc)
	_, err := svc.Register(context.Background(), "tenant-1", models.RegisterRequest{
		RunID: "run-uuid", ModelName: "resnet50",
	})
	if err == nil {
		t.Fatal("expected error from PG failure")
	}
	if !mc.deleteVersionCalled {
		t.Error("expected DeleteModelVersion to be called for cleanup on PG failure")
	}
	if mc.deleteRegisteredModelCalled {
		t.Error("should NOT delete registered model when model record was persisted to PG")
	}
}

func TestService_Register_PGRecordFailureCleansUpModelAndVersion(t *testing.T) {
	// PG CreateOrGetModelRecord fails — no model record in PG, so the newly created MLflow
	// registered model and version are both orphaned and must be cleaned up.
	mlflowRunIDStr := "mlflow-run-abc"
	reader := &mockRunReader{projectID: "proj-1", status: "SUCCEEDED", mlflowRunID: &mlflowRunIDStr}
	mc := &mockMLflowClient{
		createRegisteredModelCreated: true,
		createVersionNum:             1,
		createVersionArtifactURI:     "runs:/mlflow-run-abc/model/",
	}
	store := &mockModelStore{createRecordErr: errors.New("db error")}

	svc := models.NewService(store, reader, mc)
	_, err := svc.Register(context.Background(), "tenant-1", models.RegisterRequest{
		RunID: "run-uuid", ModelName: "resnet50",
	})
	if err == nil {
		t.Fatal("expected error from PG failure")
	}
	if !mc.deleteVersionCalled {
		t.Error("expected DeleteModelVersion to be called for cleanup")
	}
	if !mc.deleteRegisteredModelCalled {
		t.Error("expected DeleteRegisteredModel to be called when registered model was newly created")
	}
}

func TestService_Register_PGRecordFailureNoCleanupIfModelExisted(t *testing.T) {
	// PG CreateOrGetModelRecord fails, but the registered model already existed in MLflow
	// (createRegisteredModelCreated=false). Should NOT delete the registered model.
	mlflowRunIDStr := "mlflow-run-abc"
	reader := &mockRunReader{projectID: "proj-1", status: "SUCCEEDED", mlflowRunID: &mlflowRunIDStr}
	mc := &mockMLflowClient{
		createRegisteredModelCreated: false, // already existed
		createVersionNum:             2,
		createVersionArtifactURI:     "runs:/mlflow-run-abc/model/",
	}
	store := &mockModelStore{createRecordErr: errors.New("db error")}

	svc := models.NewService(store, reader, mc)
	_, err := svc.Register(context.Background(), "tenant-1", models.RegisterRequest{
		RunID: "run-uuid", ModelName: "resnet50",
	})
	if err == nil {
		t.Fatal("expected error from PG failure")
	}
	if !mc.deleteVersionCalled {
		t.Error("expected DeleteModelVersion to be called for cleanup")
	}
	if mc.deleteRegisteredModelCalled {
		t.Error("should NOT delete registered model that pre-existed in MLflow")
	}
}

func TestService_Register_RunNotSucceeded(t *testing.T) {
	mlflowRunIDStr := "mlflow-run-abc"
	reader := &mockRunReader{projectID: "proj-1", status: "RUNNING", mlflowRunID: &mlflowRunIDStr}
	svc := models.NewService(&mockModelStore{}, reader, &mockMLflowClient{})

	_, err := svc.Register(context.Background(), "tenant-1", models.RegisterRequest{
		RunID: "run-uuid", ModelName: "resnet50",
	})
	if !errors.Is(err, models.ErrRunNotSucceeded) {
		t.Fatalf("expected ErrRunNotSucceeded, got %v", err)
	}
}

func TestService_Register_RunNoMLflowID(t *testing.T) {
	reader := &mockRunReader{projectID: "proj-1", status: "SUCCEEDED", mlflowRunID: nil}
	svc := models.NewService(&mockModelStore{}, reader, &mockMLflowClient{})

	_, err := svc.Register(context.Background(), "tenant-1", models.RegisterRequest{
		RunID: "run-uuid", ModelName: "resnet50",
	})
	if !errors.Is(err, models.ErrRunNoMLflowID) {
		t.Fatalf("expected ErrRunNoMLflowID, got %v", err)
	}
}

func TestService_Promote_HappyPath(t *testing.T) {
	ver := &models.ModelVersion{ID: "v-1", VersionNumber: 1, Status: "candidate"}
	store := &mockModelStore{versions: []*models.ModelVersion{ver}}
	svc := models.NewService(store, &mockRunReader{}, &mockMLflowClient{})

	if err := svc.Promote(context.Background(), "resnet50", 1, "production", "tenant-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !store.promoteStatusCalled {
		t.Error("expected PromoteModelVersionStatus to be called for production alias")
	}
}

func TestService_Promote_SkipToProduction(t *testing.T) {
	// candidate → production directly (flexible transitions).
	ver := &models.ModelVersion{ID: "v-1", VersionNumber: 1, Status: "candidate"}
	store := &mockModelStore{versions: []*models.ModelVersion{ver}}
	svc := models.NewService(store, &mockRunReader{}, &mockMLflowClient{})

	if err := svc.Promote(context.Background(), "resnet50", 1, "production", "tenant-1"); err != nil {
		t.Fatalf("expected no error for candidate → production skip, got: %v", err)
	}
}

func TestService_Promote_InvalidAlias(t *testing.T) {
	svc := models.NewService(&mockModelStore{}, &mockRunReader{}, &mockMLflowClient{})

	err := svc.Promote(context.Background(), "resnet50", 1, "experimental", "tenant-1")
	if err == nil {
		t.Fatal("expected error for invalid alias")
	}
}

func TestService_Promote_ArchivedRejected(t *testing.T) {
	ver := &models.ModelVersion{ID: "v-1", VersionNumber: 1, Status: "archived"}
	store := &mockModelStore{versions: []*models.ModelVersion{ver}}
	svc := models.NewService(store, &mockRunReader{}, &mockMLflowClient{})

	err := svc.Promote(context.Background(), "resnet50", 1, "production", "tenant-1")
	if !errors.Is(err, models.ErrVersionArchived) {
		t.Fatalf("expected ErrVersionArchived, got %v", err)
	}
}

func TestService_Promote_ModelNotFound(t *testing.T) {
	store := &mockModelStore{getRecordErr: models.ErrModelNotFound}
	svc := models.NewService(store, &mockRunReader{}, &mockMLflowClient{})

	err := svc.Promote(context.Background(), "no-such-model", 1, "production", "tenant-1")
	if !errors.Is(err, models.ErrModelNotFound) {
		t.Fatalf("expected ErrModelNotFound, got %v", err)
	}
}

func TestService_Promote_Archived(t *testing.T) {
	mc := &mockMLflowClient{}
	ver := &models.ModelVersion{ID: "v-1", VersionNumber: 1, Status: "candidate"}
	store := &mockModelStore{versions: []*models.ModelVersion{ver}}
	svc := models.NewService(store, &mockRunReader{}, mc)

	if err := svc.Promote(context.Background(), "resnet50", 1, "archived", "tenant-1"); err != nil {
		t.Fatalf("unexpected error promoting to archived: %v", err)
	}
	found := map[string]bool{}
	for _, a := range mc.deletedAliases {
		found[a] = true
	}
	if !found["staging"] || !found["production"] {
		t.Errorf("expected DeleteModelAlias for staging and production, got: %v", mc.deletedAliases)
	}
}

func TestService_ResolveAlias_HappyPath(t *testing.T) {
	ver := &models.ModelVersion{ID: "v-1", VersionNumber: 3, Status: "production"}
	store := &mockModelStore{versions: []*models.ModelVersion{ver}}
	mc := &mockMLflowClient{getVersionByAliasNum: 3}
	svc := models.NewService(store, &mockRunReader{}, mc)

	got, err := svc.ResolveAlias(context.Background(), "resnet50", "production", "tenant-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.VersionNumber != 3 {
		t.Errorf("expected version 3, got %d", got.VersionNumber)
	}
}

func TestService_ResolveAlias_NotFound(t *testing.T) {
	// Use mlflow.ErrNotFound so that mlflow.IsNotFound returns true.
	store := &mockModelStore{}
	mc := &mockMLflowClient{getVersionByAliasErr: mlflow.ErrNotFound}
	svc := models.NewService(store, &mockRunReader{}, mc)

	_, err := svc.ResolveAlias(context.Background(), "resnet50", "no-alias", "tenant-1")
	if !errors.Is(err, models.ErrAliasNotFound) {
		t.Fatalf("expected ErrAliasNotFound, got %v", err)
	}
}

func TestService_ResolveAlias_MLflowInfraError(t *testing.T) {
	// A generic MLflow error (not a not-found) should be propagated as an infra error, not ErrAliasNotFound.
	store := &mockModelStore{}
	mc := &mockMLflowClient{getVersionByAliasErr: errors.New("connection refused")}
	svc := models.NewService(store, &mockRunReader{}, mc)

	_, err := svc.ResolveAlias(context.Background(), "resnet50", "production", "tenant-1")
	if errors.Is(err, models.ErrAliasNotFound) {
		t.Fatal("infra error should not be reported as ErrAliasNotFound")
	}
	if err == nil {
		t.Fatal("expected an error")
	}
}
