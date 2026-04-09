// control-plane/internal/jobs/store.go
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrProjectNotFound is returned when a project does not exist or does not belong to the tenant.
var ErrProjectNotFound = errors.New("project not found")

// Store is the interface used by handlers and the dispatcher.
type Store interface {
	CreateJobWithRun(ctx context.Context, job *TrainingJob, run *TrainingRun) error
	GetJob(ctx context.Context, id, tenantID string) (*TrainingJob, error)
	GetJobByID(ctx context.Context, id string) (*TrainingJob, error)
	ListJobs(ctx context.Context, tenantID string) ([]*TrainingJob, error)
	GetRun(ctx context.Context, jobID, runID, tenantID string) (*TrainingRun, error)
	GetRunByJobID(ctx context.Context, jobID string) (*TrainingRun, error)
	// ListActiveJobs returns QUEUED and RUNNING jobs (used by the dispatcher for quota checks).
	ListActiveJobs(ctx context.Context, tenantID string) ([]*TrainingJob, error)
	// ListNonTerminalJobs returns PENDING, QUEUED, and RUNNING jobs (used by the API handler
	// for a pre-submission quota check that includes already-accepted but not-yet-queued jobs).
	ListNonTerminalJobs(ctx context.Context, tenantID string) ([]*TrainingJob, error)
	GetOldestPendingJob(ctx context.Context, tenantID string) (*TrainingJob, error)
	GetTenantIDsWithPendingJobs(ctx context.Context) ([]string, error)
	GetQueuedJobsWithoutRayJob(ctx context.Context) ([]*TrainingJob, error)
	SetRayJobName(ctx context.Context, id, rayJobName string) error
	// SetMLflowRunID records the MLflow run ID on the training_run for the given job.
	SetMLflowRunID(ctx context.Context, jobID, mlflowRunID string) error
	// GetRunForRegistration returns the project ID, status, and MLflow run ID for a
	// training run, used by the model registration workflow.
	GetRunForRegistration(ctx context.Context, runID, tenantID string) (projectID, status string, mlflowRunID *string, err error)
	TransitionJobStatus(ctx context.Context, id, from, to string, failureReason *string) error
	GetTenantQuota(ctx context.Context, tenantID string) (cpuMillicores, memoryBytes int64, err error)
	// GetRunningResourceUsage returns the approximate sum of cpu and memory used by
	// RUNNING training_runs for the tenant. Values are parsed from TEXT columns via
	// regex; precise accounting is handled by the scheduler.
	GetRunningResourceUsage(ctx context.Context, tenantID string) (cpuMillicores, memoryBytes int64, runningJobs int, err error)
	// ProjectBelongsToTenant returns nil if project exists and is owned by tenantID,
	// or an error (ErrProjectNotFound) if it does not.
	ProjectBelongsToTenant(ctx context.Context, projectID, tenantID string) error
	// CountQueuedJobs returns the total number of PENDING + QUEUED jobs across all tenants.
	CountQueuedJobs(ctx context.Context) (int, error)
	// IncrementRetryCount increments retry_count for the given job and returns the new count.
	IncrementRetryCount(ctx context.Context, jobID string) (newCount int, err error)
	// CreateRetryRun inserts a new training_run for the given job (used on automatic retry).
	CreateRetryRun(ctx context.Context, jobID, tenantID string) (*TrainingRun, error)
}

// PostgresJobStore implements Store against PostgreSQL.
type PostgresJobStore struct {
	db *pgxpool.Pool
}

// NewPostgresJobStore creates a PostgresJobStore backed by the given pool.
func NewPostgresJobStore(db *pgxpool.Pool) Store {
	return &PostgresJobStore{db: db}
}

func (s *PostgresJobStore) CreateJobWithRun(ctx context.Context, job *TrainingJob, run *TrainingRun) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	envJSON, err := json.Marshal(job.Env)
	if err != nil {
		return fmt.Errorf("marshal env: %w", err)
	}

	err = tx.QueryRow(ctx, `
		INSERT INTO training_jobs
			(tenant_id, project_id, name, status, image, command, args, env,
			 num_workers, worker_cpu, worker_memory, head_cpu, head_memory)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		RETURNING id::text, created_at, updated_at`,
		job.TenantID, job.ProjectID, job.Name, job.Status,
		job.Image, job.Command, job.Args, envJSON,
		job.NumWorkers, job.WorkerCPU, job.WorkerMemory,
		job.HeadCPU, job.HeadMemory,
	).Scan(&job.ID, &job.CreatedAt, &job.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert training_job: %w", err)
	}

	err = tx.QueryRow(ctx, `
		INSERT INTO training_runs (job_id, tenant_id, status)
		VALUES ($1, $2, $3)
		RETURNING id::text, created_at, updated_at`,
		job.ID, run.TenantID, run.Status,
	).Scan(&run.ID, &run.CreatedAt, &run.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert training_run: %w", err)
	}
	run.JobID = job.ID

	return tx.Commit(ctx)
}

func (s *PostgresJobStore) GetJob(ctx context.Context, id, tenantID string) (*TrainingJob, error) {
	row := s.db.QueryRow(ctx, `
		SELECT id::text, tenant_id::text, project_id::text, name, status,
		       image, command, args, env, num_workers,
		       worker_cpu, worker_memory, head_cpu, head_memory,
		       rayjob_name, retry_count, max_retries, created_at, updated_at
		FROM training_jobs
		WHERE id = $1 AND tenant_id = $2`,
		id, tenantID,
	)
	return scanJob(row)
}

func (s *PostgresJobStore) GetJobByID(ctx context.Context, id string) (*TrainingJob, error) {
	row := s.db.QueryRow(ctx, `
		SELECT id::text, tenant_id::text, project_id::text, name, status,
		       image, command, args, env, num_workers,
		       worker_cpu, worker_memory, head_cpu, head_memory,
		       rayjob_name, retry_count, max_retries, created_at, updated_at
		FROM training_jobs WHERE id = $1`, id,
	)
	return scanJob(row)
}

func (s *PostgresJobStore) ListJobs(ctx context.Context, tenantID string) ([]*TrainingJob, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id::text, tenant_id::text, project_id::text, name, status,
		       image, command, args, env, num_workers,
		       worker_cpu, worker_memory, head_cpu, head_memory,
		       rayjob_name, retry_count, max_retries, created_at, updated_at
		FROM training_jobs
		WHERE tenant_id = $1
		ORDER BY created_at DESC`,
		tenantID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []*TrainingJob
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, j)
	}
	return result, rows.Err()
}

func (s *PostgresJobStore) GetRun(ctx context.Context, jobID, runID, tenantID string) (*TrainingRun, error) {
	row := s.db.QueryRow(ctx, `
		SELECT id::text, job_id::text, tenant_id::text, status,
		       mlflow_run_id, started_at, finished_at, failure_reason,
		       created_at, updated_at
		FROM training_runs
		WHERE id = $1 AND job_id = $2 AND tenant_id = $3`,
		runID, jobID, tenantID,
	)
	return scanRun(row)
}

func (s *PostgresJobStore) GetRunByJobID(ctx context.Context, jobID string) (*TrainingRun, error) {
	row := s.db.QueryRow(ctx, `
		SELECT id::text, job_id::text, tenant_id::text, status,
		       mlflow_run_id, started_at, finished_at, failure_reason,
		       created_at, updated_at
		FROM training_runs WHERE job_id = $1 ORDER BY created_at DESC LIMIT 1`,
		jobID,
	)
	return scanRun(row)
}

func (s *PostgresJobStore) ListActiveJobs(ctx context.Context, tenantID string) ([]*TrainingJob, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id::text, tenant_id::text, project_id::text, name, status,
		       image, command, args, env, num_workers,
		       worker_cpu, worker_memory, head_cpu, head_memory,
		       rayjob_name, retry_count, max_retries, created_at, updated_at
		FROM training_jobs
		WHERE tenant_id = $1 AND status IN ('QUEUED','RUNNING')`,
		tenantID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []*TrainingJob
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, j)
	}
	return result, rows.Err()
}

func (s *PostgresJobStore) ListNonTerminalJobs(ctx context.Context, tenantID string) ([]*TrainingJob, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id::text, tenant_id::text, project_id::text, name, status,
		       image, command, args, env, num_workers,
		       worker_cpu, worker_memory, head_cpu, head_memory,
		       rayjob_name, retry_count, max_retries, created_at, updated_at
		FROM training_jobs
		WHERE tenant_id = $1 AND status IN ('PENDING','QUEUED','RUNNING')`,
		tenantID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []*TrainingJob
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, j)
	}
	return result, rows.Err()
}

func (s *PostgresJobStore) GetOldestPendingJob(ctx context.Context, tenantID string) (*TrainingJob, error) {
	row := s.db.QueryRow(ctx, `
		SELECT id::text, tenant_id::text, project_id::text, name, status,
		       image, command, args, env, num_workers,
		       worker_cpu, worker_memory, head_cpu, head_memory,
		       rayjob_name, retry_count, max_retries, created_at, updated_at
		FROM training_jobs
		WHERE tenant_id = $1 AND status = 'PENDING'
		ORDER BY created_at ASC
		LIMIT 1`,
		tenantID,
	)
	return scanJob(row)
}

func (s *PostgresJobStore) GetTenantIDsWithPendingJobs(ctx context.Context) ([]string, error) {
	rows, err := s.db.Query(ctx,
		`SELECT DISTINCT tenant_id::text FROM training_jobs WHERE status = 'PENDING'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *PostgresJobStore) GetQueuedJobsWithoutRayJob(ctx context.Context) ([]*TrainingJob, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id::text, tenant_id::text, project_id::text, name, status,
		       image, command, args, env, num_workers,
		       worker_cpu, worker_memory, head_cpu, head_memory,
		       rayjob_name, retry_count, max_retries, created_at, updated_at
		FROM training_jobs
		WHERE status = 'QUEUED' AND rayjob_name IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []*TrainingJob
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, j)
	}
	return result, rows.Err()
}

func (s *PostgresJobStore) SetRayJobName(ctx context.Context, id, rayJobName string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE training_jobs SET rayjob_name = $1, updated_at = now() WHERE id = $2`,
		rayJobName, id,
	)
	return err
}

func (s *PostgresJobStore) SetMLflowRunID(ctx context.Context, jobID, mlflowRunID string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE training_runs SET mlflow_run_id = $1, updated_at = now() WHERE job_id = $2`,
		mlflowRunID, jobID,
	)
	return err
}

func (s *PostgresJobStore) GetRunForRegistration(ctx context.Context, runID, tenantID string) (projectID, status string, mlflowRunID *string, err error) {
	err = s.db.QueryRow(ctx, `
		SELECT tj.project_id::text, tr.status, tr.mlflow_run_id
		FROM training_runs tr
		JOIN training_jobs tj ON tj.id = tr.job_id
		WHERE tr.id = $1 AND tr.tenant_id = $2`,
		runID, tenantID,
	).Scan(&projectID, &status, &mlflowRunID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrRunNotFound
	}
	return
}

func (s *PostgresJobStore) TransitionJobStatus(ctx context.Context, id, from, to string, failureReason *string) error {
	if err := ValidateTransition(from, to); err != nil {
		return fmt.Errorf("invalid transition: %w", err)
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	tag, err := tx.Exec(ctx,
		`UPDATE training_jobs SET status = $1, updated_at = now() WHERE id = $2 AND status = $3`,
		to, id, from,
	)
	if err != nil {
		return fmt.Errorf("update training_job status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("job %s not found in status %s", id, from)
	}

	var runUpdate string
	switch to {
	case "RUNNING":
		runUpdate = `UPDATE training_runs SET status = $1, started_at = now(), updated_at = now() WHERE job_id = $2`
	case "SUCCEEDED", "FAILED":
		runUpdate = `UPDATE training_runs SET status = $1, finished_at = now(), updated_at = now() WHERE job_id = $2`
	default:
		runUpdate = `UPDATE training_runs SET status = $1, updated_at = now() WHERE job_id = $2`
	}

	if _, err := tx.Exec(ctx, runUpdate, to, id); err != nil {
		return fmt.Errorf("update training_run status: %w", err)
	}

	if failureReason != nil {
		if _, err := tx.Exec(ctx,
			`UPDATE training_runs SET failure_reason = $1 WHERE job_id = $2`,
			*failureReason, id,
		); err != nil {
			return fmt.Errorf("update failure_reason: %w", err)
		}
	}

	return tx.Commit(ctx)
}

func (s *PostgresJobStore) GetTenantQuota(ctx context.Context, tenantID string) (cpuMillicores, memoryBytes int64, err error) {
	err = s.db.QueryRow(ctx,
		`SELECT cpu_quota, memory_quota FROM tenants WHERE id = $1`,
		tenantID,
	).Scan(&cpuMillicores, &memoryBytes)
	return
}

// parseMemoryBytes converts a Kubernetes memory quantity string to bytes.
// Handles the common suffixes: Gi, Mi, Ki (binary) and G, M, K (decimal).
// Returns the raw integer when no recognised suffix is present.
const parseMemorySQL = `
CASE
  WHEN %[1]s LIKE '%%Gi' THEN CAST(regexp_replace(%[1]s,'[^0-9]','','g') AS BIGINT) * 1073741824
  WHEN %[1]s LIKE '%%Mi' THEN CAST(regexp_replace(%[1]s,'[^0-9]','','g') AS BIGINT) * 1048576
  WHEN %[1]s LIKE '%%Ki' THEN CAST(regexp_replace(%[1]s,'[^0-9]','','g') AS BIGINT) * 1024
  WHEN %[1]s LIKE '%%G'  THEN CAST(regexp_replace(%[1]s,'[^0-9]','','g') AS BIGINT) * 1000000000
  WHEN %[1]s LIKE '%%M'  THEN CAST(regexp_replace(%[1]s,'[^0-9]','','g') AS BIGINT) * 1000000
  WHEN %[1]s LIKE '%%K'  THEN CAST(regexp_replace(%[1]s,'[^0-9]','','g') AS BIGINT) * 1000
  ELSE CAST(regexp_replace(%[1]s,'[^0-9]','','g') AS BIGINT)
END`

func (s *PostgresJobStore) GetRunningResourceUsage(ctx context.Context, tenantID string) (cpuMillicores, memoryBytes int64, runningJobs int, err error) {
	workerMemExpr := fmt.Sprintf(parseMemorySQL, "worker_memory")
	headMemExpr := fmt.Sprintf(parseMemorySQL, "head_memory")
	query := fmt.Sprintf(`
		SELECT
			COALESCE(SUM(
				(SELECT CAST(regexp_replace(worker_cpu,'[^0-9]','','g') AS BIGINT) * num_workers +
				            CAST(regexp_replace(head_cpu,'[^0-9]','','g') AS BIGINT)
				 FROM training_jobs tj2 WHERE tj2.id = tr.job_id)
			), 0),
			COALESCE(SUM(
				(SELECT (%s) * num_workers + (%s)
				 FROM training_jobs tj2 WHERE tj2.id = tr.job_id)
			), 0),
			COUNT(*)
		FROM training_runs tr
		WHERE tr.tenant_id = $1::uuid AND tr.status = 'RUNNING'`,
		workerMemExpr, headMemExpr)
	err = s.db.QueryRow(ctx, query, tenantID).Scan(&cpuMillicores, &memoryBytes, &runningJobs)
	return
}

func (s *PostgresJobStore) CountQueuedJobs(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM training_jobs WHERE status IN ('PENDING', 'QUEUED')`).Scan(&count)
	return count, err
}

func (s *PostgresJobStore) ProjectBelongsToTenant(ctx context.Context, projectID, tenantID string) error {
	var exists bool
	err := s.db.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM projects WHERE id = $1 AND tenant_id = $2)`,
		projectID, tenantID,
	).Scan(&exists)
	if err != nil {
		return fmt.Errorf("check project ownership: %w", err)
	}
	if !exists {
		return ErrProjectNotFound
	}
	return nil
}

func (s *PostgresJobStore) IncrementRetryCount(ctx context.Context, jobID string) (int, error) {
	var newCount int
	err := s.db.QueryRow(ctx,
		`UPDATE training_jobs SET retry_count = retry_count + 1, updated_at = now()
		 WHERE id = $1 RETURNING retry_count`,
		jobID,
	).Scan(&newCount)
	return newCount, err
}

func (s *PostgresJobStore) CreateRetryRun(ctx context.Context, jobID, tenantID string) (*TrainingRun, error) {
	run := &TrainingRun{}
	err := s.db.QueryRow(ctx, `
		INSERT INTO training_runs (job_id, tenant_id, status)
		VALUES ($1, $2, 'QUEUED')
		RETURNING id::text, job_id::text, tenant_id::text, status,
		          mlflow_run_id, started_at, finished_at, failure_reason,
		          created_at, updated_at`,
		jobID, tenantID,
	).Scan(
		&run.ID, &run.JobID, &run.TenantID, &run.Status,
		&run.MLflowRunID, &run.StartedAt, &run.FinishedAt, &run.FailureReason,
		&run.CreatedAt, &run.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("create retry run: %w", err)
	}
	return run, nil
}

type scannable interface {
	Scan(dest ...any) error
}

func scanJob(row scannable) (*TrainingJob, error) {
	var j TrainingJob
	var envJSON []byte
	var rayJobName *string
	err := row.Scan(
		&j.ID, &j.TenantID, &j.ProjectID, &j.Name, &j.Status,
		&j.Image, &j.Command, &j.Args, &envJSON, &j.NumWorkers,
		&j.WorkerCPU, &j.WorkerMemory, &j.HeadCPU, &j.HeadMemory,
		&rayJobName, &j.RetryCount, &j.MaxRetries, &j.CreatedAt, &j.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	j.RayJobName = rayJobName
	if len(envJSON) > 0 {
		if err := json.Unmarshal(envJSON, &j.Env); err != nil {
			return nil, fmt.Errorf("unmarshal env: %w", err)
		}
	}
	if j.Env == nil {
		j.Env = map[string]string{}
	}
	return &j, nil
}

func scanRun(row scannable) (*TrainingRun, error) {
	var r TrainingRun
	var (
		mlflowRunID   *string
		startedAt     *time.Time
		finishedAt    *time.Time
		failureReason *string
	)
	err := row.Scan(
		&r.ID, &r.JobID, &r.TenantID, &r.Status,
		&mlflowRunID, &startedAt, &finishedAt, &failureReason,
		&r.CreatedAt, &r.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	r.MLflowRunID = mlflowRunID
	r.StartedAt = startedAt
	r.FinishedAt = finishedAt
	r.FailureReason = failureReason
	return &r, nil
}
