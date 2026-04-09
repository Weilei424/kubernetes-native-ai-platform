// control-plane/internal/deployments/model.go
package deployments

import (
	"errors"
	"time"
)

// Sentinel errors returned by Service methods.
var (
	ErrDeploymentNotFound        = errors.New("deployment not found")
	ErrModelVersionNotProduction = errors.New("model version is not at production status")
	ErrDuplicateDeploymentName   = errors.New("a deployment with this name already exists for this tenant")
	ErrModelNotFound             = errors.New("model not found")
	ErrVersionNotFound           = errors.New("model version not found")
)

// Deployment is the platform's representation of a model serving deployment.
type Deployment struct {
	ID              string    `json:"id"`
	TenantID        string    `json:"tenant_id"`
	ProjectID       string    `json:"project_id"`
	ModelRecordID   string    `json:"model_record_id"`
	ModelVersionID  string    `json:"model_version_id"`
	Name            string    `json:"name"`
	Namespace       string    `json:"namespace"`
	Status          string    `json:"status"`
	DesiredReplicas int       `json:"desired_replicas"`
	ServingEndpoint string    `json:"serving_endpoint,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`

	// Populated only by ListPendingDeployments (JOIN fields for operator use).
	ArtifactURI string `json:"artifact_uri,omitempty"`
	ModelName   string `json:"model_name,omitempty"`
}

// DeploymentRevision records which model version was active at each revision.
type DeploymentRevision struct {
	ID             string    `json:"id"`
	DeploymentID   string    `json:"deployment_id"`
	RevisionNumber int       `json:"revision_number"`
	ModelVersionID string    `json:"model_version_id"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"created_at"`
}

// CreateDeploymentRequest is the body for POST /v1/deployments.
type CreateDeploymentRequest struct {
	ModelName    string `json:"model_name"`
	ModelVersion int    `json:"model_version"`
	Name         string `json:"name"`
	Namespace    string `json:"namespace"`
	Replicas     int    `json:"replicas"`
}

// UpdateStatusRequest is the body for PATCH /internal/v1/deployments/:id/status.
type UpdateStatusRequest struct {
	Status          string `json:"status"`
	ServingEndpoint string `json:"serving_endpoint,omitempty"`
}
