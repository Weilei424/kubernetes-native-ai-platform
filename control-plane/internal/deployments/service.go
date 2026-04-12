// control-plane/internal/deployments/service.go
package deployments

import (
	"context"
	"errors"
	"fmt"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/models"
)

// ModelVersionReader provides model version lookup needed for deployment validation.
// Implemented by models.Store (via models.NewPostgresModelStore).
type ModelVersionReader interface {
	GetModelRecordByName(ctx context.Context, name, tenantID string) (*models.ModelRecord, error)
	GetModelVersionByNumber(ctx context.Context, modelRecordID string, versionNumber int) (*models.ModelVersion, error)
}

// Service orchestrates deployment creation, retrieval, and status updates.
type Service struct {
	store         Store
	versionReader ModelVersionReader
}

// NewService constructs a Service with the given dependencies.
func NewService(store Store, versionReader ModelVersionReader) *Service {
	return &Service{store: store, versionReader: versionReader}
}

// Create validates the request, checks the model version is at production status,
// and persists the deployment record.
func (s *Service) Create(ctx context.Context, tenantID string, req CreateDeploymentRequest) (*Deployment, error) {
	if req.ModelName == "" || req.Name == "" || req.ModelVersion == 0 {
		return nil, fmt.Errorf("model_name, name, and model_version are required")
	}
	if req.Replicas == 0 {
		req.Replicas = 1
	}
	if req.Namespace == "" {
		req.Namespace = "default"
	}

	rec, err := s.versionReader.GetModelRecordByName(ctx, req.ModelName, tenantID)
	if errors.Is(err, models.ErrModelNotFound) {
		return nil, ErrModelNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("look up model record: %w", err)
	}

	ver, err := s.versionReader.GetModelVersionByNumber(ctx, rec.ID, req.ModelVersion)
	if errors.Is(err, models.ErrVersionNotFound) {
		return nil, ErrVersionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("look up model version: %w", err)
	}

	if ver.Status != "production" {
		return nil, ErrModelVersionNotProduction
	}

	d := &Deployment{
		TenantID:        tenantID,
		ProjectID:       rec.ProjectID,
		ModelRecordID:   rec.ID,
		ModelVersionID:  ver.ID,
		Name:            req.Name,
		Namespace:       req.Namespace,
		Status:          "pending",
		DesiredReplicas: req.Replicas,
	}
	if err := s.store.CreateDeployment(ctx, d); err != nil {
		return nil, err // ErrDuplicateDeploymentName passes through
	}
	return d, nil
}

// Get returns a deployment by ID, scoped to the given tenant.
func (s *Service) Get(ctx context.Context, id, tenantID string) (*Deployment, error) {
	d, err := s.store.GetDeployment(ctx, id)
	if err != nil {
		return nil, err
	}
	if d.TenantID != tenantID {
		return nil, ErrDeploymentNotFound
	}
	return d, nil
}

// Delete marks the deployment as deleted, scoped to the given tenant.
func (s *Service) Delete(ctx context.Context, id, tenantID string) error {
	d, err := s.store.GetDeployment(ctx, id)
	if err != nil {
		return err
	}
	if d.TenantID != tenantID {
		return ErrDeploymentNotFound
	}
	return s.store.DeleteDeployment(ctx, id)
}

// UpdateStatus is called from the internal API handler to persist operator-reported status.
func (s *Service) UpdateStatus(ctx context.Context, id, status, endpoint, failureReason string) error {
	return s.store.UpdateDeploymentStatus(ctx, id, status, endpoint, failureReason)
}

// Rollback rolls a deployment back to a specific revision (or the previous one if revision == 0).
// Creates a new revision that mirrors the target and sets the deployment to pending.
func (s *Service) Rollback(ctx context.Context, id, tenantID string, revision int) (*Deployment, error) {
	d, err := s.store.GetDeployment(ctx, id)
	if err != nil {
		return nil, err
	}
	if d.TenantID != tenantID {
		return nil, ErrDeploymentNotFound
	}

	current, err := s.store.GetCurrentRevisionNumber(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get current revision: %w", err)
	}

	target := revision
	if target == 0 {
		target = current - 1
	}
	if target < 1 {
		return nil, ErrNoRevisionToRollback
	}

	targetRev, err := s.store.GetRevision(ctx, id, target)
	if errors.Is(err, ErrDeploymentNotFound) {
		return nil, ErrRevisionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get target revision %d: %w", target, err)
	}

	return s.store.RollbackDeployment(ctx, id, targetRev.ModelVersionID)
}
