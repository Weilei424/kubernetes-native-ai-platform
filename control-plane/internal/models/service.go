// control-plane/internal/models/service.go
package models

import (
	"context"
	"errors"
	"fmt"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/jobs"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/mlflow"
)

// RunReader provides minimal run lookup for model registration.
// Implemented by jobs.PostgresJobStore.
type RunReader interface {
	GetRunForRegistration(ctx context.Context, runID, tenantID string) (projectID, status string, mlflowRunID *string, err error)
}

// Service orchestrates model registration, promotion, and alias resolution.
type Service struct {
	store        Store
	runReader    RunReader
	mlflowClient mlflow.Client
}

// NewService constructs a Service with the given dependencies.
func NewService(store Store, runReader RunReader, mlflowClient mlflow.Client) *Service {
	return &Service{store: store, runReader: runReader, mlflowClient: mlflowClient}
}

// validPromoteAliases is the set of aliases accepted by Promote.
var validPromoteAliases = map[string]bool{
	"staging": true, "production": true, "archived": true,
}

// Register validates the source run and creates a model version in MLflow and PostgreSQL.
func (s *Service) Register(ctx context.Context, tenantID string, req RegisterRequest) (*ModelVersion, error) {
	if req.RunID == "" || req.ModelName == "" {
		return nil, fmt.Errorf("run_id and model_name are required")
	}
	artifactPath := req.ArtifactPath
	if artifactPath == "" {
		artifactPath = "model/"
	}

	projectID, status, mlflowRunID, err := s.runReader.GetRunForRegistration(ctx, req.RunID, tenantID)
	if errors.Is(err, jobs.ErrRunNotFound) {
		return nil, ErrRunNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("look up training run: %w", err)
	}
	if status != "SUCCEEDED" {
		return nil, ErrRunNotSucceeded
	}
	if mlflowRunID == nil {
		return nil, ErrRunNoMLflowID
	}

	source := "runs:/" + *mlflowRunID + "/" + artifactPath
	mlflowModelName := tenantID + "-" + req.ModelName

	if err := s.mlflowClient.CreateRegisteredModel(ctx, mlflowModelName); err != nil {
		return nil, fmt.Errorf("create registered model: %w", err)
	}

	versionNum, artifactURI, err := s.mlflowClient.CreateModelVersion(ctx, mlflowModelName, source, *mlflowRunID)
	if err != nil {
		return nil, fmt.Errorf("create model version: %w", err)
	}

	rec := &ModelRecord{
		TenantID:                  tenantID,
		ProjectID:                 projectID,
		Name:                      req.ModelName,
		MLflowRegisteredModelName: mlflowModelName,
	}
	if err := s.store.CreateOrGetModelRecord(ctx, rec); err != nil {
		// Best-effort cleanup: delete the MLflow version we just created.
		_ = s.mlflowClient.DeleteModelVersion(ctx, mlflowModelName, versionNum)
		return nil, fmt.Errorf("persist model record: %w", err)
	}

	ver := &ModelVersion{
		ModelRecordID: rec.ID,
		TenantID:      tenantID,
		VersionNumber: versionNum,
		MLflowRunID:   *mlflowRunID,
		SourceRunID:   req.RunID,
		ArtifactURI:   artifactURI,
		Status:        "candidate",
	}
	if err := s.store.CreateModelVersion(ctx, ver); err != nil {
		// Best-effort cleanup: delete the MLflow version we just created.
		_ = s.mlflowClient.DeleteModelVersion(ctx, mlflowModelName, versionNum)
		return nil, fmt.Errorf("persist model version: %w", err)
	}

	return ver, nil
}

// GetModel returns the model record and all its versions.
func (s *Service) GetModel(ctx context.Context, name, tenantID string) (*ModelRecord, []*ModelVersion, error) {
	rec, err := s.store.GetModelRecordByName(ctx, name, tenantID)
	if err != nil {
		return nil, nil, err // store already returns ErrModelNotFound for not-found case
	}
	versions, err := s.store.ListModelVersions(ctx, rec.ID)
	if err != nil {
		return nil, nil, err
	}
	return rec, versions, nil
}

// GetModelVersion returns a single version with full metadata for source traceability.
func (s *Service) GetModelVersion(ctx context.Context, name string, versionNumber int, tenantID string) (*ModelVersion, error) {
	rec, err := s.store.GetModelRecordByName(ctx, name, tenantID)
	if err != nil {
		return nil, err // store already returns ErrModelNotFound for not-found case
	}
	ver, err := s.store.GetModelVersionByNumber(ctx, rec.ID, versionNumber)
	if err != nil {
		return nil, err // store already returns ErrVersionNotFound for not-found case
	}
	return ver, nil
}

// Promote assigns an alias to a model version. "archived" removes all common aliases
// and sets the version to a terminal state. staging/production atomically clear any
// prior holder of that alias in PostgreSQL to maintain single-holder consistency.
func (s *Service) Promote(ctx context.Context, name string, versionNumber int, alias, tenantID string) error {
	if !validPromoteAliases[alias] {
		return fmt.Errorf("invalid alias %q: must be staging, production, or archived", alias)
	}

	rec, err := s.store.GetModelRecordByName(ctx, name, tenantID)
	if err != nil {
		return err // store maps not-found to ErrModelNotFound
	}
	ver, err := s.store.GetModelVersionByNumber(ctx, rec.ID, versionNumber)
	if err != nil {
		return err // store maps not-found to ErrVersionNotFound
	}
	if ver.Status == "archived" {
		return ErrVersionArchived
	}

	if alias == "archived" {
		// Best-effort removal of common aliases from MLflow.
		_ = s.mlflowClient.DeleteModelAlias(ctx, rec.MLflowRegisteredModelName, "staging")
		_ = s.mlflowClient.DeleteModelAlias(ctx, rec.MLflowRegisteredModelName, "production")
		return s.store.UpdateModelVersionStatus(ctx, ver.ID, "archived")
	}

	if err := s.mlflowClient.SetModelAlias(ctx, rec.MLflowRegisteredModelName, alias, versionNumber); err != nil {
		return fmt.Errorf("set mlflow alias: %w", err)
	}
	// Atomically clear prior holders of this alias and set the new holder.
	return s.store.PromoteModelVersionStatus(ctx, rec.ID, ver.ID, alias)
}

// ResolveAlias resolves an MLflow alias to the full version record.
// Returns ErrAliasNotFound if MLflow has no such alias.
func (s *Service) ResolveAlias(ctx context.Context, name, alias, tenantID string) (*ModelVersion, error) {
	rec, err := s.store.GetModelRecordByName(ctx, name, tenantID)
	if err != nil {
		return nil, err // store maps not-found to ErrModelNotFound
	}
	versionNum, err := s.mlflowClient.GetModelVersionByAlias(ctx, rec.MLflowRegisteredModelName, alias)
	if err != nil {
		if mlflow.IsNotFound(err) {
			return nil, ErrAliasNotFound
		}
		return nil, fmt.Errorf("get mlflow alias: %w", err)
	}
	ver, err := s.store.GetModelVersionByNumber(ctx, rec.ID, versionNum)
	if err != nil {
		return nil, err // store maps not-found to ErrVersionNotFound
	}
	return ver, nil
}
