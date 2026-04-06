// control-plane/internal/models/model.go
package models

import (
	"errors"
	"time"
)

// Sentinel errors returned by Service methods.
var (
	ErrRunNotFound     = errors.New("training run not found")
	ErrRunNotSucceeded = errors.New("training run has not succeeded")
	ErrRunNoMLflowID   = errors.New("training run has no mlflow_run_id set")
	ErrModelNotFound   = errors.New("model not found")
	ErrVersionNotFound = errors.New("model version not found")
	ErrVersionArchived = errors.New("model version is archived and cannot be promoted")
	ErrAliasNotFound        = errors.New("alias not found")
	ErrVersionAlreadyExists = errors.New("model version already exists")
	ErrInvalidAlias         = errors.New("invalid alias: must be staging, production, or archived")
)

// ModelRecord is the platform's representation of a named model.
type ModelRecord struct {
	ID                        string    `json:"id"`
	TenantID                  string    `json:"tenant_id"`
	ProjectID                 string    `json:"project_id"`
	Name                      string    `json:"name"`
	MLflowRegisteredModelName string    `json:"mlflow_registered_model_name"`
	CreatedAt                 time.Time `json:"created_at"`
	UpdatedAt                 time.Time `json:"updated_at"`
}

// ModelVersion is one registered version of a ModelRecord.
type ModelVersion struct {
	ID            string    `json:"id"`
	ModelRecordID string    `json:"model_record_id"`
	TenantID      string    `json:"tenant_id"`
	VersionNumber int       `json:"version_number"`
	MLflowRunID   string    `json:"mlflow_run_id"`
	SourceRunID   string    `json:"source_run_id"`
	ArtifactURI   string    `json:"artifact_uri"`
	Status        string    `json:"status"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// RegisterRequest is the body for POST /v1/models.
type RegisterRequest struct {
	RunID        string `json:"run_id"`
	ModelName    string `json:"model_name"`
	ArtifactPath string `json:"artifact_path"` // defaults to "model/" if empty
}

// PromoteRequest is the body for POST /v1/models/:name/versions/:version/promote.
type PromoteRequest struct {
	Alias string `json:"alias"`
}
