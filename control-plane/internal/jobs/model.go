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
	ID           string            `json:"id"`
	TenantID     string            `json:"tenant_id"`
	ProjectID    string            `json:"project_id"`
	Name         string            `json:"name"`
	Status       string            `json:"status"`
	Image        string            `json:"image"`
	Command      []string          `json:"command"`
	Args         []string          `json:"args"`
	Env          map[string]string `json:"env"`
	NumWorkers   int               `json:"num_workers"`
	WorkerCPU    string            `json:"worker_cpu"`
	WorkerMemory string            `json:"worker_memory"`
	HeadCPU      string            `json:"head_cpu"`
	HeadMemory   string            `json:"head_memory"`
	RayJobName   *string           `json:"ray_job_name,omitempty"`
	RetryCount   int               `json:"retry_count"`
	MaxRetries   int               `json:"max_retries"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
}

// TrainingRun is one execution attempt for a TrainingJob.
type TrainingRun struct {
	ID            string     `json:"id"`
	JobID         string     `json:"job_id"`
	TenantID      string     `json:"tenant_id"`
	Status        string     `json:"status"`
	MLflowRunID   *string    `json:"mlflow_run_id,omitempty"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
	FailureReason *string    `json:"failure_reason,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
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
