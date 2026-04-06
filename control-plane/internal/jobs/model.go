// control-plane/internal/jobs/model.go
package jobs

import (
	"errors"
	"time"
)

// ErrRunNotFound is returned by GetRunForRegistration when the run does not exist.
var ErrRunNotFound = errors.New("training run not found")

// TrainingJob is the platform's representation of a submitted training job.
type TrainingJob struct {
	ID           string
	TenantID     string
	ProjectID    string
	Name         string
	Status       string
	Image        string
	Command      []string
	Args         []string
	Env          map[string]string
	NumWorkers   int
	WorkerCPU    string
	WorkerMemory string
	HeadCPU      string
	HeadMemory   string
	RayJobName   *string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// TrainingRun is one execution attempt for a TrainingJob.
type TrainingRun struct {
	ID            string
	JobID         string
	TenantID      string
	Status        string
	MLflowRunID   *string
	StartedAt     *time.Time
	FinishedAt    *time.Time
	FailureReason *string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// JobSubmitRequest is the HTTP request body for POST /v1/jobs.
type JobSubmitRequest struct {
	Name      string       `json:"name"`
	ProjectID string       `json:"project_id"`
	Runtime   RuntimeSpec  `json:"runtime"`
	Resources ResourceSpec `json:"resources"`
}

// RuntimeSpec describes the container image and entrypoint.
type RuntimeSpec struct {
	Image   string            `json:"image"`
	Command []string          `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

// ResourceSpec describes compute resources for the RayJob cluster.
type ResourceSpec struct {
	NumWorkers   int    `json:"num_workers"`
	WorkerCPU    string `json:"worker_cpu"`
	WorkerMemory string `json:"worker_memory"`
	HeadCPU      string `json:"head_cpu"`
	HeadMemory   string `json:"head_memory"`
}

// StatusUpdateRequest is the body for PATCH /internal/v1/jobs/:id/status.
type StatusUpdateRequest struct {
	Status        string  `json:"status"`
	FailureReason *string `json:"failure_reason,omitempty"`
	MLflowRunID   *string `json:"mlflow_run_id,omitempty"`
}

// JobEvent payload published to Kafka on every state transition.
type JobEvent struct {
	JobID         string  `json:"job_id"`
	TenantID      string  `json:"tenant_id"`
	Status        string  `json:"status"`
	Timestamp     string  `json:"timestamp"`
	FailureReason *string `json:"failure_reason,omitempty"`
}
