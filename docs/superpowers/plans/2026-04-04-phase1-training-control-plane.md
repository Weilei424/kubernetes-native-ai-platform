# Phase 1 — Training Control Plane Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver the full training job lifecycle — submit, queue, schedule, dispatch to RayJob, reconcile status — with per-tenant resource quota, a state machine, Kafka event publishing, and a controller-runtime operator.

**Architecture:** The control-plane HTTP server accepts structured job submissions, runs synchronous admission, and persists jobs as PENDING. An in-process dispatcher goroutine promotes PENDING→QUEUED jobs, submits RayJob CRDs to Kubernetes, and retries failed CRD submissions. A separate controller-runtime operator binary watches RayJob objects and reports status back to the control plane via an internal HTTP endpoint.

**Tech Stack:** Go 1.26, chi v5, pgx/v5, golang-migrate, k8s.io/client-go (dynamic), k8s.io/apimachinery (resource quantities), segmentio/kafka-go, testcontainers-go (integration tests), controller-runtime (operator).

---

## File Map

```
control-plane/
  go.mod                                      modified: add k8s, kafka-go, testcontainers deps
  internal/
    testutil/
      db.go                                   new: testcontainers postgres helper for all integration tests
    auth/
      postgres_store.go                       modified: token_prefix keyed lookup
    jobs/
      model.go                                new: TrainingJob, TrainingRun structs; JobSubmitRequest
      statemachine.go                         new: ValidateTransition(from, to string) error
      store.go                                new: PostgresJobStore — CRUD for training_jobs + training_runs
      rayjob.go                               new: BuildRayJob(job *TrainingJob) *unstructured.Unstructured
      dispatcher.go                           new: Dispatcher — goroutine, FIFO pop, quota check, CRD submit
    scheduler/
      admission.go                            new: Admit(req AdmissionRequest) error
      quota.go                                new: CheckQuota(quota TenantQuota, active []JobResources, req JobResources) error
      placement.go                            new: Hints(job *TrainingJob) map[string]string
    events/
      publisher.go                            new: Publisher interface + NoOpPublisher
      kafka.go                                new: KafkaPublisher wrapping segmentio/kafka-go Writer
    k8s/
      client.go                               new: NewDynamicClient() (dynamic.Interface, error) — auto-detect
    api/
      router.go                               modified: job routes + internal sub-router on port 8081
      jobs.go                                 new: handleSubmitJob, handleListJobs, handleGetJob, handleGetRun
      internal.go                             new: handleUpdateJobStatus
  cmd/server/main.go                          modified: start dispatcher goroutine + internal HTTP server
  migrations/
    005_token_prefix.up.sql                   new
    005_token_prefix.down.sql                 new
    006_tenant_quota.up.sql                   new
    006_tenant_quota.down.sql                 new
    007_create_training_jobs.up.sql           new
    007_create_training_jobs.down.sql         new
    008_create_training_runs.up.sql           new
    008_create_training_runs.down.sql         new

operator/
  go.mod                                      new: operator module
  cmd/operator/main.go                        new: controller-runtime manager setup
  internal/
    reconciler/
      rayjob_reconciler.go                    new: RayJobReconciler — watch, map status, call internal API
```

---

## Task 1: Add Control-Plane Dependencies

**Files:**
- Modify: `control-plane/go.mod`

- [ ] **Step 1: Add k8s, kafka, and testcontainers dependencies**

Run from `control-plane/`:
```bash
go get k8s.io/apimachinery@v0.32.3
go get k8s.io/client-go@v0.32.3
go get github.com/segmentio/kafka-go@v0.4.47
go get github.com/testcontainers/testcontainers-go@v0.37.0
go get github.com/testcontainers/testcontainers-go/modules/postgres@v0.37.0
go mod tidy
```

- [ ] **Step 2: Verify build still passes**

```bash
go build ./...
```
Expected: no errors.

---

## Task 2: DB Migrations 005–008

**Files:**
- Create: `control-plane/migrations/005_token_prefix.up.sql`
- Create: `control-plane/migrations/005_token_prefix.down.sql`
- Create: `control-plane/migrations/006_tenant_quota.up.sql`
- Create: `control-plane/migrations/006_tenant_quota.down.sql`
- Create: `control-plane/migrations/007_create_training_jobs.up.sql`
- Create: `control-plane/migrations/007_create_training_jobs.down.sql`
- Create: `control-plane/migrations/008_create_training_runs.up.sql`
- Create: `control-plane/migrations/008_create_training_runs.down.sql`

- [ ] **Step 1: Write migration 005 — token prefix**

`control-plane/migrations/005_token_prefix.up.sql`:
```sql
ALTER TABLE api_tokens
  ADD COLUMN token_prefix TEXT NOT NULL DEFAULT '';

CREATE INDEX idx_api_tokens_prefix ON api_tokens (token_prefix);
```

`control-plane/migrations/005_token_prefix.down.sql`:
```sql
DROP INDEX IF EXISTS idx_api_tokens_prefix;
ALTER TABLE api_tokens DROP COLUMN IF EXISTS token_prefix;
```

- [ ] **Step 2: Write migration 006 — tenant resource quota**

`control-plane/migrations/006_tenant_quota.up.sql`:
```sql
ALTER TABLE tenants
  ADD COLUMN cpu_quota    BIGINT NOT NULL DEFAULT 0,
  ADD COLUMN memory_quota BIGINT NOT NULL DEFAULT 0;
```

`cpu_quota` is in millicores (e.g. 2000 = 2 cores). `memory_quota` is in bytes. `0` means unlimited.

`control-plane/migrations/006_tenant_quota.down.sql`:
```sql
ALTER TABLE tenants
  DROP COLUMN IF EXISTS cpu_quota,
  DROP COLUMN IF EXISTS memory_quota;
```

- [ ] **Step 3: Write migration 007 — training_jobs**

`control-plane/migrations/007_create_training_jobs.up.sql`:
```sql
CREATE TABLE training_jobs (
  id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id     UUID        NOT NULL REFERENCES tenants(id),
  project_id    UUID        NOT NULL REFERENCES projects(id),
  name          TEXT        NOT NULL,
  status        TEXT        NOT NULL DEFAULT 'PENDING',
  image         TEXT        NOT NULL,
  command       TEXT[]      NOT NULL,
  args          TEXT[]      NOT NULL DEFAULT '{}',
  env           JSONB       NOT NULL DEFAULT '{}',
  num_workers   INT         NOT NULL,
  worker_cpu    TEXT        NOT NULL,
  worker_memory TEXT        NOT NULL,
  head_cpu      TEXT        NOT NULL,
  head_memory   TEXT        NOT NULL,
  rayjob_name   TEXT,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_training_jobs_tenant_status ON training_jobs (tenant_id, status);
```

`control-plane/migrations/007_create_training_jobs.down.sql`:
```sql
DROP TABLE IF EXISTS training_jobs;
```

- [ ] **Step 4: Write migration 008 — training_runs**

`control-plane/migrations/008_create_training_runs.up.sql`:
```sql
CREATE TABLE training_runs (
  id             UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  job_id         UUID        NOT NULL REFERENCES training_jobs(id),
  tenant_id      UUID        NOT NULL REFERENCES tenants(id),
  status         TEXT        NOT NULL DEFAULT 'PENDING',
  mlflow_run_id  TEXT,
  started_at     TIMESTAMPTZ,
  finished_at    TIMESTAMPTZ,
  failure_reason TEXT,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_training_runs_job_id ON training_runs (job_id);
```

`control-plane/migrations/008_create_training_runs.down.sql`:
```sql
DROP TABLE IF EXISTS training_runs;
```

---

## Task 3: Integration Test Helper

**Files:**
- Create: `control-plane/internal/testutil/db.go`

All integration tests use this helper to get a live PostgreSQL pool with migrations applied.

- [ ] **Step 1: Write the testcontainers helper**

`control-plane/internal/testutil/db.go`:
```go
// control-plane/internal/testutil/db.go
package testutil

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/storage"
)

// SetupDB starts a postgres testcontainer, runs all migrations, and returns a live pool.
// The container and pool are cleaned up via t.Cleanup.
func SetupDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	c, err := postgres.Run(ctx,
		"postgres:16-alpine",
		postgres.WithDatabase("platform_test"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForListeningPort("5432/tcp"),
		),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(ctx) })

	host, err := c.Host(ctx)
	if err != nil {
		t.Fatalf("get container host: %v", err)
	}
	port, err := c.MappedPort(ctx, "5432")
	if err != nil {
		t.Fatalf("get container port: %v", err)
	}

	dsn := fmt.Sprintf(
		"postgresql://test:test@%s:%s/platform_test?sslmode=disable",
		host, port.Port(),
	)

	if err := storage.RunMigrations(dsn); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	pool, err := storage.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect pool: %v", err)
	}
	t.Cleanup(pool.Close)

	return pool
}
```

- [ ] **Step 2: Verify it compiles**

```bash
cd control-plane && go build ./internal/testutil/...
```
Expected: no errors.

---

## Task 4: Token Prefix Optimization

**Files:**
- Modify: `control-plane/internal/auth/postgres_store.go`
- Test: `control-plane/internal/auth/postgres_store_test.go`

- [ ] **Step 1: Write the failing integration test**

`control-plane/internal/auth/postgres_store_test.go`:
```go
// control-plane/internal/auth/postgres_store_test.go
package auth_test

import (
	"context"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/auth"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/testutil"
)

func TestFindToken_PrefixLookup(t *testing.T) {
	pool := testutil.SetupDB(t)
	ctx := context.Background()

	// Insert a tenant
	var tenantID string
	err := pool.QueryRow(ctx,
		`INSERT INTO tenants (name) VALUES ('test-tenant') RETURNING id::text`,
	).Scan(&tenantID)
	if err != nil {
		t.Fatalf("insert tenant: %v", err)
	}

	// Issue a plaintext token and store hash + prefix
	plaintext := "abcdefgh-1234-5678-rest-of-token"
	prefix := plaintext[:8] // "abcdefgh"
	hash, err := bcrypt.GenerateFromPassword([]byte(plaintext), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}

	_, err = pool.Exec(ctx,
		`INSERT INTO api_tokens (tenant_id, token_hash, token_prefix) VALUES ($1, $2, $3)`,
		tenantID, string(hash), prefix,
	)
	if err != nil {
		t.Fatalf("insert token: %v", err)
	}

	store := auth.NewPostgresTokenStore(pool)

	// Valid token resolves to tenant
	got, err := store.FindToken(ctx, plaintext)
	if err != nil {
		t.Fatalf("FindToken: %v", err)
	}
	if got != tenantID {
		t.Fatalf("expected tenantID %q, got %q", tenantID, got)
	}

	// Wrong token returns error
	_, err = store.FindToken(ctx, "wrongtoken-that-has-no-prefix")
	if err == nil {
		t.Fatal("expected error for wrong token, got nil")
	}
}

func TestFindToken_ExpiredToken(t *testing.T) {
	pool := testutil.SetupDB(t)
	ctx := context.Background()

	var tenantID string
	err := pool.QueryRow(ctx,
		`INSERT INTO tenants (name) VALUES ('expired-tenant') RETURNING id::text`,
	).Scan(&tenantID)
	if err != nil {
		t.Fatalf("insert tenant: %v", err)
	}

	plaintext := "exptoken-xxxx-yyyy-zzzz"
	prefix := plaintext[:8]
	hash, _ := bcrypt.GenerateFromPassword([]byte(plaintext), bcrypt.MinCost)
	past := time.Now().Add(-time.Hour)

	_, err = pool.Exec(ctx,
		`INSERT INTO api_tokens (tenant_id, token_hash, token_prefix, expires_at) VALUES ($1, $2, $3, $4)`,
		tenantID, string(hash), prefix, past,
	)
	if err != nil {
		t.Fatalf("insert expired token: %v", err)
	}

	store := auth.NewPostgresTokenStore(pool)
	_, err = store.FindToken(ctx, plaintext)
	if err == nil {
		t.Fatal("expected error for expired token, got nil")
	}
}
```

- [ ] **Step 2: Run test — expect FAIL**

```bash
cd control-plane && go test ./internal/auth/... -run TestFindToken -v
```
Expected: FAIL — test compiles but `FindToken` still does full-table scan; the token with a non-empty prefix won't be found if the query doesn't filter by prefix (it will succeed by accident for full-table scan). Actually: the test passes today because full scan works. The goal here is to update the implementation to use prefix, confirm test still passes, then it's a verified optimization.

- [ ] **Step 3: Update FindToken to use prefix keyed lookup**

Replace the body of `FindToken` in `control-plane/internal/auth/postgres_store.go`:
```go
// control-plane/internal/auth/postgres_store.go
package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

// PostgresTokenStore implements TokenStore against the api_tokens table.
type PostgresTokenStore struct {
	db *pgxpool.Pool
}

// NewPostgresTokenStore creates a PostgresTokenStore backed by the given pool.
func NewPostgresTokenStore(db *pgxpool.Pool) *PostgresTokenStore {
	return &PostgresTokenStore{db: db}
}

// FindToken looks up a token by its prefix (first 8 chars of plaintext), then
// bcrypt-compares only the matching candidate rows. Returns the tenant_id on match.
func (s *PostgresTokenStore) FindToken(ctx context.Context, plaintext string) (string, error) {
	if len(plaintext) < 8 {
		return "", fmt.Errorf("token too short")
	}
	prefix := plaintext[:8]

	rows, err := s.db.Query(ctx,
		`SELECT tenant_id::text, token_hash, expires_at
		 FROM api_tokens
		 WHERE token_prefix = $1`,
		prefix,
	)
	if err != nil {
		return "", fmt.Errorf("query api_tokens: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var tenantID, hash string
		var expiresAt *time.Time
		if err := rows.Scan(&tenantID, &hash, &expiresAt); err != nil {
			continue
		}
		if expiresAt != nil && time.Now().After(*expiresAt) {
			continue
		}
		if bcrypt.CompareHashAndPassword([]byte(hash), []byte(plaintext)) == nil {
			return tenantID, nil
		}
	}
	return "", fmt.Errorf("token not found")
}
```

- [ ] **Step 4: Run tests — expect PASS**

```bash
cd control-plane && go test ./internal/auth/... -v
```
Expected: all auth tests PASS (including the existing middleware tests).

---

## Task 5: Job Model

**Files:**
- Create: `control-plane/internal/jobs/model.go`

Defines all shared types used by store, API handlers, scheduler, and dispatcher.

- [ ] **Step 1: Write model.go**

`control-plane/internal/jobs/model.go`:
```go
// control-plane/internal/jobs/model.go
package jobs

import "time"

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
}
```

- [ ] **Step 2: Verify compile**

```bash
cd control-plane && go build ./internal/jobs/...
```
Expected: no errors.

---

## Task 6: State Machine

**Files:**
- Create: `control-plane/internal/jobs/statemachine.go`
- Test: `control-plane/internal/jobs/statemachine_test.go`

- [ ] **Step 1: Write failing unit tests**

`control-plane/internal/jobs/statemachine_test.go`:
```go
// control-plane/internal/jobs/statemachine_test.go
package jobs_test

import (
	"testing"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/jobs"
)

func TestValidateTransition_ValidPaths(t *testing.T) {
	cases := []struct{ from, to string }{
		{"PENDING", "QUEUED"},
		{"PENDING", "CANCELLED"},
		{"QUEUED", "RUNNING"},
		{"QUEUED", "CANCELLED"},
		{"RUNNING", "SUCCEEDED"},
		{"RUNNING", "FAILED"},
		{"RUNNING", "CANCELLED"},
	}
	for _, c := range cases {
		if err := jobs.ValidateTransition(c.from, c.to); err != nil {
			t.Errorf("%s → %s: unexpected error: %v", c.from, c.to, err)
		}
	}
}

func TestValidateTransition_InvalidPaths(t *testing.T) {
	cases := []struct{ from, to string }{
		{"PENDING", "RUNNING"},
		{"PENDING", "SUCCEEDED"},
		{"PENDING", "FAILED"},
		{"QUEUED", "SUCCEEDED"},
		{"QUEUED", "FAILED"},
		{"RUNNING", "PENDING"},
		{"RUNNING", "QUEUED"},
		{"SUCCEEDED", "RUNNING"},
		{"SUCCEEDED", "FAILED"},
		{"FAILED", "RUNNING"},
		{"FAILED", "SUCCEEDED"},
	}
	for _, c := range cases {
		if err := jobs.ValidateTransition(c.from, c.to); err == nil {
			t.Errorf("%s → %s: expected error, got nil", c.from, c.to)
		}
	}
}

func TestValidateTransition_UnknownStatus(t *testing.T) {
	if err := jobs.ValidateTransition("UNKNOWN", "RUNNING"); err == nil {
		t.Error("expected error for unknown from-status, got nil")
	}
}
```

- [ ] **Step 2: Run — expect FAIL**

```bash
cd control-plane && go test ./internal/jobs/... -run TestValidateTransition -v
```
Expected: FAIL — `jobs.ValidateTransition` undefined.

- [ ] **Step 3: Implement state machine**

`control-plane/internal/jobs/statemachine.go`:
```go
// control-plane/internal/jobs/statemachine.go
package jobs

import "fmt"

var validTransitions = map[string]map[string]bool{
	"PENDING": {"QUEUED": true, "CANCELLED": true},
	"QUEUED":  {"RUNNING": true, "CANCELLED": true},
	"RUNNING": {"SUCCEEDED": true, "FAILED": true, "CANCELLED": true},
}

// ValidateTransition returns nil if transitioning from → to is allowed,
// or an error describing the invalid transition.
func ValidateTransition(from, to string) error {
	allowed, ok := validTransitions[from]
	if !ok {
		return fmt.Errorf("unknown status %q", from)
	}
	if !allowed[to] {
		return fmt.Errorf("invalid transition %s → %s", from, to)
	}
	return nil
}
```

- [ ] **Step 4: Run — expect PASS**

```bash
cd control-plane && go test ./internal/jobs/... -run TestValidateTransition -v
```
Expected: all 3 tests PASS.

---

## Task 7: Scheduler — Admission

**Files:**
- Create: `control-plane/internal/scheduler/admission.go`
- Test: `control-plane/internal/scheduler/admission_test.go`

- [ ] **Step 1: Write failing tests**

`control-plane/internal/scheduler/admission_test.go`:
```go
// control-plane/internal/scheduler/admission_test.go
package scheduler_test

import (
	"testing"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/scheduler"
)

func validRequest() scheduler.AdmissionRequest {
	return scheduler.AdmissionRequest{
		Image:        "ghcr.io/org/ray-torch:2.9",
		NumWorkers:   2,
		WorkerCPU:    "2",
		WorkerMemory: "4Gi",
		HeadCPU:      "1",
		HeadMemory:   "2Gi",
	}
}

func TestAdmit_Valid(t *testing.T) {
	if err := scheduler.Admit(validRequest()); err != nil {
		t.Fatalf("expected valid request to pass: %v", err)
	}
}

func TestAdmit_EmptyImage(t *testing.T) {
	r := validRequest()
	r.Image = ""
	if err := scheduler.Admit(r); err == nil {
		t.Fatal("expected error for empty image")
	}
}

func TestAdmit_ZeroWorkers(t *testing.T) {
	r := validRequest()
	r.NumWorkers = 0
	if err := scheduler.Admit(r); err == nil {
		t.Fatal("expected error for zero workers")
	}
}

func TestAdmit_NegativeWorkers(t *testing.T) {
	r := validRequest()
	r.NumWorkers = -1
	if err := scheduler.Admit(r); err == nil {
		t.Fatal("expected error for negative workers")
	}
}

func TestAdmit_InvalidCPU(t *testing.T) {
	r := validRequest()
	r.WorkerCPU = "not-a-cpu"
	if err := scheduler.Admit(r); err == nil {
		t.Fatal("expected error for invalid CPU quantity")
	}
}

func TestAdmit_InvalidMemory(t *testing.T) {
	r := validRequest()
	r.WorkerMemory = "not-memory"
	if err := scheduler.Admit(r); err == nil {
		t.Fatal("expected error for invalid memory quantity")
	}
}

func TestAdmit_MillicoreCPU(t *testing.T) {
	r := validRequest()
	r.WorkerCPU = "500m"
	if err := scheduler.Admit(r); err != nil {
		t.Fatalf("500m should be valid CPU: %v", err)
	}
}
```

- [ ] **Step 2: Run — expect FAIL**

```bash
cd control-plane && go test ./internal/scheduler/... -run TestAdmit -v
```
Expected: FAIL — `scheduler.Admit` undefined.

- [ ] **Step 3: Implement admission**

`control-plane/internal/scheduler/admission.go`:
```go
// control-plane/internal/scheduler/admission.go
package scheduler

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/resource"
)

// AdmissionRequest contains the fields validated at job submission time.
type AdmissionRequest struct {
	Image        string
	NumWorkers   int
	WorkerCPU    string
	WorkerMemory string
	HeadCPU      string
	HeadMemory   string
}

// Admit validates that a job spec is executable. Returns nil on success.
// Called synchronously in the HTTP handler before persisting the job.
func Admit(req AdmissionRequest) error {
	if req.Image == "" {
		return fmt.Errorf("image is required")
	}
	if req.NumWorkers < 1 {
		return fmt.Errorf("num_workers must be at least 1, got %d", req.NumWorkers)
	}
	if err := parseQuantity("worker_cpu", req.WorkerCPU); err != nil {
		return err
	}
	if err := parseQuantity("worker_memory", req.WorkerMemory); err != nil {
		return err
	}
	if err := parseQuantity("head_cpu", req.HeadCPU); err != nil {
		return err
	}
	if err := parseQuantity("head_memory", req.HeadMemory); err != nil {
		return err
	}
	return nil
}

func parseQuantity(field, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", field)
	}
	if _, err := resource.ParseQuantity(value); err != nil {
		return fmt.Errorf("%s %q is not a valid Kubernetes resource quantity: %w", field, value, err)
	}
	return nil
}
```

- [ ] **Step 4: Run — expect PASS**

```bash
cd control-plane && go test ./internal/scheduler/... -run TestAdmit -v
```
Expected: all 7 tests PASS.

---

## Task 8: Scheduler — Quota

**Files:**
- Create: `control-plane/internal/scheduler/quota.go`
- Test: `control-plane/internal/scheduler/quota_test.go`

- [ ] **Step 1: Write failing tests**

`control-plane/internal/scheduler/quota_test.go`:
```go
// control-plane/internal/scheduler/quota_test.go
package scheduler_test

import (
	"testing"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/scheduler"
)

func TestCheckQuota_Unlimited(t *testing.T) {
	quota := scheduler.TenantQuota{CPUMillicores: 0, MemoryBytes: 0}
	active := []scheduler.JobResources{}
	req := scheduler.JobResources{NumWorkers: 10, WorkerCPU: "8", WorkerMemory: "32Gi", HeadCPU: "2", HeadMemory: "4Gi"}
	if err := scheduler.CheckQuota(quota, active, req); err != nil {
		t.Fatalf("unlimited quota should always pass: %v", err)
	}
}

func TestCheckQuota_WithinLimit(t *testing.T) {
	quota := scheduler.TenantQuota{CPUMillicores: 8000, MemoryBytes: 16 * 1024 * 1024 * 1024} // 8 cores, 16Gi
	active := []scheduler.JobResources{
		{NumWorkers: 1, WorkerCPU: "2", WorkerMemory: "4Gi", HeadCPU: "1", HeadMemory: "2Gi"}, // 3 cores, 6Gi in use
	}
	req := scheduler.JobResources{NumWorkers: 1, WorkerCPU: "2", WorkerMemory: "4Gi", HeadCPU: "1", HeadMemory: "2Gi"} // 3 more = 6 total
	if err := scheduler.CheckQuota(quota, active, req); err != nil {
		t.Fatalf("6 cores within 8-core quota: %v", err)
	}
}

func TestCheckQuota_CPUExceeded(t *testing.T) {
	quota := scheduler.TenantQuota{CPUMillicores: 4000, MemoryBytes: 0} // 4 cores CPU quota, unlimited memory
	active := []scheduler.JobResources{
		{NumWorkers: 2, WorkerCPU: "1", WorkerMemory: "1Gi", HeadCPU: "1", HeadMemory: "1Gi"}, // 3 cores in use
	}
	req := scheduler.JobResources{NumWorkers: 1, WorkerCPU: "2", WorkerMemory: "1Gi", HeadCPU: "1", HeadMemory: "1Gi"} // 3 more = 6 > 4
	if err := scheduler.CheckQuota(quota, active, req); err == nil {
		t.Fatal("expected quota exceeded error")
	}
}

func TestCheckQuota_MemoryExceeded(t *testing.T) {
	quota := scheduler.TenantQuota{CPUMillicores: 0, MemoryBytes: 8 * 1024 * 1024 * 1024} // unlimited CPU, 8Gi memory
	active := []scheduler.JobResources{
		{NumWorkers: 1, WorkerCPU: "1", WorkerMemory: "4Gi", HeadCPU: "1", HeadMemory: "2Gi"}, // 6Gi in use
	}
	req := scheduler.JobResources{NumWorkers: 1, WorkerCPU: "1", WorkerMemory: "4Gi", HeadCPU: "1", HeadMemory: "1Gi"} // 5 more = 11 > 8
	if err := scheduler.CheckQuota(quota, active, req); err == nil {
		t.Fatal("expected memory quota exceeded error")
	}
}

func TestCheckQuota_ExactLimit(t *testing.T) {
	quota := scheduler.TenantQuota{CPUMillicores: 4000, MemoryBytes: 0}
	active := []scheduler.JobResources{
		{NumWorkers: 1, WorkerCPU: "2", WorkerMemory: "1Gi", HeadCPU: "1", HeadMemory: "1Gi"}, // 3 cores
	}
	req := scheduler.JobResources{NumWorkers: 1, WorkerCPU: "1", WorkerMemory: "1Gi", HeadCPU: "0", HeadMemory: "0"} // NOTE: "0" is valid
	// total = 3+1 = 4 = quota exactly → should pass
	if err := scheduler.CheckQuota(quota, active, req); err != nil {
		t.Fatalf("exact limit should pass: %v", err)
	}
}
```

- [ ] **Step 2: Run — expect FAIL**

```bash
cd control-plane && go test ./internal/scheduler/... -run TestCheckQuota -v
```
Expected: FAIL — `scheduler.CheckQuota` undefined.

- [ ] **Step 3: Implement quota check**

`control-plane/internal/scheduler/quota.go`:
```go
// control-plane/internal/scheduler/quota.go
package scheduler

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/resource"
)

// TenantQuota holds the resource limits for a tenant.
// 0 means unlimited for that dimension.
type TenantQuota struct {
	CPUMillicores int64
	MemoryBytes   int64
}

// JobResources describes the resource shape of one job.
type JobResources struct {
	NumWorkers   int
	WorkerCPU    string
	WorkerMemory string
	HeadCPU      string
	HeadMemory   string
}

// CheckQuota returns nil if the requested resources fit within the tenant quota
// given the currently active (QUEUED + RUNNING) jobs. Returns an error if any
// quota dimension is exceeded.
func CheckQuota(quota TenantQuota, active []JobResources, req JobResources) error {
	usedCPU, usedMem, err := sumResources(active)
	if err != nil {
		return fmt.Errorf("sum active resources: %w", err)
	}

	reqCPU, reqMem, err := jobMillicoresAndBytes(req)
	if err != nil {
		return fmt.Errorf("parse requested resources: %w", err)
	}

	if quota.CPUMillicores > 0 && usedCPU+reqCPU > quota.CPUMillicores {
		return fmt.Errorf("CPU quota exceeded: %dm used + %dm requested > %dm limit",
			usedCPU, reqCPU, quota.CPUMillicores)
	}
	if quota.MemoryBytes > 0 && usedMem+reqMem > quota.MemoryBytes {
		return fmt.Errorf("memory quota exceeded: %d used + %d requested > %d limit",
			usedMem, reqMem, quota.MemoryBytes)
	}
	return nil
}

func sumResources(jobs []JobResources) (cpuMillicores, memBytes int64, err error) {
	for _, j := range jobs {
		cpu, mem, err := jobMillicoresAndBytes(j)
		if err != nil {
			return 0, 0, err
		}
		cpuMillicores += cpu
		memBytes += mem
	}
	return
}

func jobMillicoresAndBytes(j JobResources) (cpuMillicores, memBytes int64, err error) {
	wCPU, err := parseMillicores(j.WorkerCPU)
	if err != nil {
		return 0, 0, fmt.Errorf("worker_cpu: %w", err)
	}
	hCPU, err := parseMillicores(j.HeadCPU)
	if err != nil {
		return 0, 0, fmt.Errorf("head_cpu: %w", err)
	}
	wMem, err := parseBytes(j.WorkerMemory)
	if err != nil {
		return 0, 0, fmt.Errorf("worker_memory: %w", err)
	}
	hMem, err := parseBytes(j.HeadMemory)
	if err != nil {
		return 0, 0, fmt.Errorf("head_memory: %w", err)
	}

	cpuMillicores = int64(j.NumWorkers)*wCPU + hCPU
	memBytes = int64(j.NumWorkers)*wMem + hMem
	return
}

func parseMillicores(s string) (int64, error) {
	if s == "0" || s == "" {
		return 0, nil
	}
	q, err := resource.ParseQuantity(s)
	if err != nil {
		return 0, err
	}
	return q.MilliValue(), nil
}

func parseBytes(s string) (int64, error) {
	if s == "0" || s == "" {
		return 0, nil
	}
	q, err := resource.ParseQuantity(s)
	if err != nil {
		return 0, err
	}
	return q.Value(), nil
}
```

- [ ] **Step 4: Run — expect PASS**

```bash
cd control-plane && go test ./internal/scheduler/... -run TestCheckQuota -v
```
Expected: all 5 tests PASS.

---

## Task 9: Scheduler — Placement Hints

**Files:**
- Create: `control-plane/internal/scheduler/placement.go`
- Test: `control-plane/internal/scheduler/placement_test.go`

- [ ] **Step 1: Write failing tests**

`control-plane/internal/scheduler/placement_test.go`:
```go
// control-plane/internal/scheduler/placement_test.go
package scheduler_test

import (
	"testing"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/scheduler"
)

func TestHints_DefaultCPU(t *testing.T) {
	hints := scheduler.Hints()
	got, ok := hints["kubernetes.io/arch"]
	if !ok {
		t.Fatal("expected kubernetes.io/arch hint")
	}
	if got != "amd64" {
		t.Fatalf("expected amd64, got %q", got)
	}
}

func TestHints_ReturnsNewMapEachCall(t *testing.T) {
	h1 := scheduler.Hints()
	h2 := scheduler.Hints()
	h1["extra"] = "val"
	if _, ok := h2["extra"]; ok {
		t.Fatal("hints maps should be independent")
	}
}
```

- [ ] **Step 2: Run — expect FAIL**

```bash
cd control-plane && go test ./internal/scheduler/... -run TestHints -v
```
Expected: FAIL.

- [ ] **Step 3: Implement placement hints**

`control-plane/internal/scheduler/placement.go`:
```go
// control-plane/internal/scheduler/placement.go
package scheduler

// Hints returns a map[string]string of Kubernetes node selector labels
// to attach to both the head and worker groups of the RayJob.
// Phase 1: CPU-only default. Extend here for GPU workloads in Phase 4+.
func Hints() map[string]string {
	return map[string]string{
		"kubernetes.io/arch": "amd64",
	}
}
```

- [ ] **Step 4: Run — expect PASS**

```bash
cd control-plane && go test ./internal/scheduler/... -v
```
Expected: all scheduler tests PASS.

---

## Task 10: Jobs DB Store

**Files:**
- Create: `control-plane/internal/jobs/store.go`
- Test: `control-plane/internal/jobs/store_test.go`

- [ ] **Step 1: Write failing integration tests**

`control-plane/internal/jobs/store_test.go`:
```go
// control-plane/internal/jobs/store_test.go
package jobs_test

import (
	"context"
	"testing"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/jobs"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/testutil"
)

func setupStoreTest(t *testing.T) (jobs.Store, context.Context, string, string) {
	t.Helper()
	pool := testutil.SetupDB(t)
	ctx := context.Background()

	var tenantID, projectID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO tenants (name) VALUES ('store-test-tenant') RETURNING id::text`,
	).Scan(&tenantID); err != nil {
		t.Fatalf("insert tenant: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO projects (tenant_id, name) VALUES ($1, 'store-test-project') RETURNING id::text`,
		tenantID,
	).Scan(&projectID); err != nil {
		t.Fatalf("insert project: %v", err)
	}

	store := jobs.NewPostgresJobStore(pool)
	return store, ctx, tenantID, projectID
}

func TestCreateJobWithRun(t *testing.T) {
	store, ctx, tenantID, projectID := setupStoreTest(t)

	job := &jobs.TrainingJob{
		TenantID: tenantID, ProjectID: projectID, Name: "test-job",
		Status: "PENDING", Image: "test:latest", Command: []string{"python", "train.py"},
		Args: []string{}, Env: map[string]string{},
		NumWorkers: 2, WorkerCPU: "2", WorkerMemory: "4Gi",
		HeadCPU: "1", HeadMemory: "2Gi",
	}
	run := &jobs.TrainingRun{TenantID: tenantID, Status: "PENDING"}

	if err := store.CreateJobWithRun(ctx, job, run); err != nil {
		t.Fatalf("CreateJobWithRun: %v", err)
	}
	if job.ID == "" {
		t.Fatal("expected job.ID to be set after create")
	}
	if run.ID == "" {
		t.Fatal("expected run.ID to be set after create")
	}
	if run.JobID != job.ID {
		t.Fatalf("run.JobID %q != job.ID %q", run.JobID, job.ID)
	}
}

func TestGetJob(t *testing.T) {
	store, ctx, tenantID, projectID := setupStoreTest(t)

	job := &jobs.TrainingJob{
		TenantID: tenantID, ProjectID: projectID, Name: "get-test-job",
		Status: "PENDING", Image: "img:1", Command: []string{"run"},
		Args: []string{}, Env: map[string]string{},
		NumWorkers: 1, WorkerCPU: "1", WorkerMemory: "1Gi",
		HeadCPU: "1", HeadMemory: "1Gi",
	}
	run := &jobs.TrainingRun{TenantID: tenantID, Status: "PENDING"}
	if err := store.CreateJobWithRun(ctx, job, run); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := store.GetJob(ctx, job.ID, tenantID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.Name != job.Name {
		t.Fatalf("name mismatch: %q vs %q", got.Name, job.Name)
	}
}

func TestTransitionJobStatus(t *testing.T) {
	store, ctx, tenantID, projectID := setupStoreTest(t)

	job := &jobs.TrainingJob{
		TenantID: tenantID, ProjectID: projectID, Name: "transition-job",
		Status: "PENDING", Image: "img:1", Command: []string{"run"},
		Args: []string{}, Env: map[string]string{},
		NumWorkers: 1, WorkerCPU: "1", WorkerMemory: "1Gi",
		HeadCPU: "1", HeadMemory: "1Gi",
	}
	run := &jobs.TrainingRun{TenantID: tenantID, Status: "PENDING"}
	if err := store.CreateJobWithRun(ctx, job, run); err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := store.TransitionJobStatus(ctx, job.ID, "PENDING", "QUEUED", nil); err != nil {
		t.Fatalf("TransitionJobStatus: %v", err)
	}

	got, _ := store.GetJob(ctx, job.ID, tenantID)
	if got.Status != "QUEUED" {
		t.Fatalf("expected QUEUED, got %q", got.Status)
	}
}

func TestTransitionJobStatus_InvalidTransition(t *testing.T) {
	store, ctx, tenantID, projectID := setupStoreTest(t)

	job := &jobs.TrainingJob{
		TenantID: tenantID, ProjectID: projectID, Name: "bad-transition-job",
		Status: "PENDING", Image: "img:1", Command: []string{"run"},
		Args: []string{}, Env: map[string]string{},
		NumWorkers: 1, WorkerCPU: "1", WorkerMemory: "1Gi",
		HeadCPU: "1", HeadMemory: "1Gi",
	}
	run := &jobs.TrainingRun{TenantID: tenantID, Status: "PENDING"}
	store.CreateJobWithRun(ctx, job, run)

	err := store.TransitionJobStatus(ctx, job.ID, "PENDING", "SUCCEEDED", nil)
	if err == nil {
		t.Fatal("expected error for PENDING → SUCCEEDED, got nil")
	}
}
```

- [ ] **Step 2: Run — expect FAIL**

```bash
cd control-plane && go test ./internal/jobs/... -run "TestCreate|TestGetJob|TestTransition" -v
```
Expected: FAIL — `jobs.Store`, `jobs.NewPostgresJobStore` undefined.

- [ ] **Step 3: Implement the store**

`control-plane/internal/jobs/store.go`:
```go
// control-plane/internal/jobs/store.go
package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is the interface used by handlers and the dispatcher.
type Store interface {
	CreateJobWithRun(ctx context.Context, job *TrainingJob, run *TrainingRun) error
	GetJob(ctx context.Context, id, tenantID string) (*TrainingJob, error)
	ListJobs(ctx context.Context, tenantID string) ([]*TrainingJob, error)
	GetRun(ctx context.Context, jobID, runID, tenantID string) (*TrainingRun, error)
	GetRunByJobID(ctx context.Context, jobID string) (*TrainingRun, error)
	ListActiveJobs(ctx context.Context, tenantID string) ([]*TrainingJob, error)
	GetOldestPendingJob(ctx context.Context, tenantID string) (*TrainingJob, error)
	GetTenantIDsWithPendingJobs(ctx context.Context) ([]string, error)
	GetQueuedJobsWithoutRayJob(ctx context.Context) ([]*TrainingJob, error)
	SetRayJobName(ctx context.Context, id, rayJobName string) error
	TransitionJobStatus(ctx context.Context, id, from, to string, failureReason *string) error
	GetTenantQuota(ctx context.Context, tenantID string) (cpuMillicores, memoryBytes int64, err error)
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
		       rayjob_name, created_at, updated_at
		FROM training_jobs
		WHERE id = $1 AND tenant_id = $2`,
		id, tenantID,
	)
	return scanJob(row)
}

func (s *PostgresJobStore) ListJobs(ctx context.Context, tenantID string) ([]*TrainingJob, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id::text, tenant_id::text, project_id::text, name, status,
		       image, command, args, env, num_workers,
		       worker_cpu, worker_memory, head_cpu, head_memory,
		       rayjob_name, created_at, updated_at
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
		       rayjob_name, created_at, updated_at
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

func (s *PostgresJobStore) GetOldestPendingJob(ctx context.Context, tenantID string) (*TrainingJob, error) {
	row := s.db.QueryRow(ctx, `
		SELECT id::text, tenant_id::text, project_id::text, name, status,
		       image, command, args, env, num_workers,
		       worker_cpu, worker_memory, head_cpu, head_memory,
		       rayjob_name, created_at, updated_at
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
		       rayjob_name, created_at, updated_at
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

	// Update run status and timestamps
	var runUpdate string
	switch to {
	case "RUNNING":
		runUpdate = `UPDATE training_runs SET status = $1, started_at = now(), updated_at = now() WHERE job_id = $2`
	case "SUCCEEDED":
		runUpdate = `UPDATE training_runs SET status = $1, finished_at = now(), updated_at = now() WHERE job_id = $2`
	case "FAILED":
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

// scanJob is a helper used by both QueryRow and Query row scanning.
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
		&rayJobName, &j.CreatedAt, &j.UpdatedAt,
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
```

- [ ] **Step 4: Check that projects table has a name column — verify migration**

```bash
cd control-plane && grep -r "CREATE TABLE projects" migrations/
```
Expected: finds `002_create_projects.up.sql`.

Read the file to confirm column names before running tests:
```bash
cat control-plane/migrations/002_create_projects.up.sql
```
If the projects table has a different column name than `name`, adjust the `INSERT INTO projects` in the test accordingly.

- [ ] **Step 5: Run — expect PASS**

```bash
cd control-plane && go test ./internal/jobs/... -run "TestCreate|TestGetJob|TestTransition" -v -timeout 120s
```
Expected: all 4 tests PASS.

---

## Task 11: Kubernetes Dynamic Client

**Files:**
- Create: `control-plane/internal/k8s/client.go`

No unit test — this is wiring code verified by `go build`.

- [ ] **Step 1: Write the auto-detect client**

`control-plane/internal/k8s/client.go`:
```go
// control-plane/internal/k8s/client.go
package k8s

import (
	"fmt"
	"os"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// NewDynamicClient returns a dynamic.Interface using in-cluster config when
// KUBERNETES_SERVICE_HOST is set, falling back to ~/.kube/config for local dev.
func NewDynamicClient() (dynamic.Interface, error) {
	cfg, err := restConfig()
	if err != nil {
		return nil, fmt.Errorf("build rest config: %w", err)
	}
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("dynamic.NewForConfig: %w", err)
	}
	return client, nil
}

func restConfig() (*rest.Config, error) {
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		return rest.InClusterConfig()
	}
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	configOverrides := &clientcmd.ConfigOverrides{}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loadingRules, configOverrides,
	).ClientConfig()
}
```

- [ ] **Step 2: Verify compile**

```bash
cd control-plane && go build ./internal/k8s/...
```
Expected: no errors.

---

## Task 12: RayJob CRD Builder

**Files:**
- Create: `control-plane/internal/jobs/rayjob.go`
- Test: `control-plane/internal/jobs/rayjob_test.go`

- [ ] **Step 1: Write failing tests**

`control-plane/internal/jobs/rayjob_test.go`:
```go
// control-plane/internal/jobs/rayjob_test.go
package jobs_test

import (
	"strings"
	"testing"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/jobs"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/scheduler"
)

func sampleJob() *jobs.TrainingJob {
	return &jobs.TrainingJob{
		ID:           "abcdef12-0000-0000-0000-000000000000",
		TenantID:     "tenant-uuid-1111",
		Name:         "resnet-run-1",
		Image:        "ghcr.io/org/ray-torch:2.9",
		Command:      []string{"python", "train.py"},
		Args:         []string{"--epochs", "10"},
		Env:          map[string]string{"LR": "0.001"},
		NumWorkers:   2,
		WorkerCPU:    "2",
		WorkerMemory: "4Gi",
		HeadCPU:      "1",
		HeadMemory:   "2Gi",
	}
}

func TestBuildRayJob_Name(t *testing.T) {
	obj := jobs.BuildRayJob(sampleJob(), scheduler.Hints())
	name, _, _ := unstructuredString(obj.Object, "metadata", "name")
	if !strings.HasPrefix(name, "rayjob-") {
		t.Fatalf("expected name to start with rayjob-, got %q", name)
	}
	// Name is derived from first 8 chars of job ID
	if name != "rayjob-abcdef12" {
		t.Fatalf("expected rayjob-abcdef12, got %q", name)
	}
}

func TestBuildRayJob_Namespace(t *testing.T) {
	obj := jobs.BuildRayJob(sampleJob(), scheduler.Hints())
	ns, _, _ := unstructuredString(obj.Object, "metadata", "namespace")
	if ns != "platform-jobs" {
		t.Fatalf("expected platform-jobs namespace, got %q", ns)
	}
}

func TestBuildRayJob_Labels(t *testing.T) {
	obj := jobs.BuildRayJob(sampleJob(), scheduler.Hints())
	labels, _, _ := unstructuredStringMap(obj.Object, "metadata", "labels")
	if labels["platform.io/job-id"] != sampleJob().ID {
		t.Fatalf("missing or wrong platform.io/job-id label")
	}
	if labels["platform.io/tenant-id"] != sampleJob().TenantID {
		t.Fatalf("missing or wrong platform.io/tenant-id label")
	}
}

func TestBuildRayJob_APIVersion(t *testing.T) {
	obj := jobs.BuildRayJob(sampleJob(), scheduler.Hints())
	if obj.GetAPIVersion() != "ray.io/v1" {
		t.Fatalf("expected ray.io/v1, got %q", obj.GetAPIVersion())
	}
	if obj.GetKind() != "RayJob" {
		t.Fatalf("expected RayJob, got %q", obj.GetKind())
	}
}

// helpers for reading nested unstructured fields in tests
func unstructuredString(obj map[string]interface{}, fields ...string) (string, bool, error) {
	cur := obj
	for i, f := range fields {
		if i == len(fields)-1 {
			v, ok := cur[f].(string)
			return v, ok, nil
		}
		next, ok := cur[f].(map[string]interface{})
		if !ok {
			return "", false, nil
		}
		cur = next
	}
	return "", false, nil
}

func unstructuredStringMap(obj map[string]interface{}, fields ...string) (map[string]string, bool, error) {
	cur := obj
	for i, f := range fields {
		if i == len(fields)-1 {
			raw, ok := cur[f].(map[string]interface{})
			if !ok {
				return nil, false, nil
			}
			result := make(map[string]string)
			for k, v := range raw {
				result[k], _ = v.(string)
			}
			return result, true, nil
		}
		next, ok := cur[f].(map[string]interface{})
		if !ok {
			return nil, false, nil
		}
		cur = next
	}
	return nil, false, nil
}
```

- [ ] **Step 2: Run — expect FAIL**

```bash
cd control-plane && go test ./internal/jobs/... -run TestBuildRayJob -v
```
Expected: FAIL — `jobs.BuildRayJob` undefined.

- [ ] **Step 3: Implement RayJob builder**

`control-plane/internal/jobs/rayjob.go`:
```go
// control-plane/internal/jobs/rayjob.go
package jobs

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// RayJobGVR is the GroupVersionResource for RayJob CRDs.
var RayJobGVR = schema.GroupVersionResource{
	Group:    "ray.io",
	Version:  "v1",
	Resource: "rayjobs",
}

// RayJobNamespace is the Kubernetes namespace where platform RayJobs are created.
const RayJobNamespace = "platform-jobs"

// BuildRayJob constructs an unstructured RayJob object from a TrainingJob and
// placement hints. The returned object is ready for submission via the dynamic client.
func BuildRayJob(job *TrainingJob, hints map[string]string) *unstructured.Unstructured {
	name := rayJobName(job.ID)
	entrypoint := buildEntrypoint(job.Command, job.Args)
	runtimeEnv := buildRuntimeEnvYAML(job.Env)

	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "ray.io/v1",
			"kind":       "RayJob",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": RayJobNamespace,
				"labels": map[string]interface{}{
					"platform.io/job-id":    job.ID,
					"platform.io/tenant-id": job.TenantID,
				},
			},
			"spec": map[string]interface{}{
				"entrypoint":      entrypoint,
				"runtimeEnvYAML": runtimeEnv,
				"rayClusterSpec": map[string]interface{}{
					"headGroupSpec": map[string]interface{}{
						"replicas": int64(1),
						"template": podTemplate(job.Image, job.HeadCPU, job.HeadMemory, hints),
					},
					"workerGroupSpecs": []interface{}{
						map[string]interface{}{
							"groupName": "workers",
							"replicas":  int64(job.NumWorkers),
							"template":  podTemplate(job.Image, job.WorkerCPU, job.WorkerMemory, hints),
						},
					},
				},
			},
		},
	}
	return obj
}

func rayJobName(jobID string) string {
	short := jobID
	if len(short) > 8 {
		short = short[:8]
	}
	return fmt.Sprintf("rayjob-%s", short)
}

func buildEntrypoint(command, args []string) string {
	parts := append(command, args...)
	return strings.Join(parts, " ")
}

func buildRuntimeEnvYAML(env map[string]string) string {
	if len(env) == 0 {
		return "env_vars: {}"
	}
	var sb strings.Builder
	sb.WriteString("env_vars:\n")
	for k, v := range env {
		fmt.Fprintf(&sb, "  %s: %q\n", k, v)
	}
	return sb.String()
}

func podTemplate(image, cpu, memory string, hints map[string]string) map[string]interface{} {
	nodeSelector := make(map[string]interface{})
	for k, v := range hints {
		nodeSelector[k] = v
	}
	return map[string]interface{}{
		"spec": map[string]interface{}{
			"nodeSelector": nodeSelector,
			"containers": []interface{}{
				map[string]interface{}{
					"name":  "ray-worker",
					"image": image,
					"resources": map[string]interface{}{
						"requests": map[string]interface{}{
							"cpu":    cpu,
							"memory": memory,
						},
						"limits": map[string]interface{}{
							"cpu":    cpu,
							"memory": memory,
						},
					},
				},
			},
		},
	}
}
```

- [ ] **Step 4: Run — expect PASS**

```bash
cd control-plane && go test ./internal/jobs/... -run TestBuildRayJob -v
```
Expected: all 4 tests PASS.

---

## Task 13: Events Publisher

**Files:**
- Create: `control-plane/internal/events/publisher.go`
- Create: `control-plane/internal/events/kafka.go`

- [ ] **Step 1: Write publisher interface and no-op**

`control-plane/internal/events/publisher.go`:
```go
// control-plane/internal/events/publisher.go
package events

import "context"

// Publisher publishes platform events. Implementations must be safe to call
// concurrently. Publish failures must not block the calling operation.
type Publisher interface {
	Publish(ctx context.Context, topic string, payload interface{}) error
}

// NoOpPublisher silently discards all events. Used in tests and when Kafka
// is not configured.
type NoOpPublisher struct{}

func (n *NoOpPublisher) Publish(_ context.Context, _ string, _ interface{}) error {
	return nil
}
```

- [ ] **Step 2: Write the Kafka publisher**

`control-plane/internal/events/kafka.go`:
```go
// control-plane/internal/events/kafka.go
package events

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	kafka "github.com/segmentio/kafka-go"
)

// KafkaPublisher publishes events to Kafka topics using segmentio/kafka-go.
// Publish failures are returned to the caller for logging; they must not
// block state transitions (caller is responsible for the fire-and-forget pattern).
type KafkaPublisher struct {
	writer *kafka.Writer
}

// NewKafkaPublisher creates a KafkaPublisher connected to the given broker address.
func NewKafkaPublisher(brokerAddr string) *KafkaPublisher {
	return &KafkaPublisher{
		writer: &kafka.Writer{
			Addr:         kafka.TCP(brokerAddr),
			Balancer:     &kafka.LeastBytes{},
			WriteTimeout: 5 * time.Second,
		},
	}
}

// Close shuts down the underlying writer.
func (p *KafkaPublisher) Close() error {
	return p.writer.Close()
}

// Publish serializes payload as JSON and writes it to the given topic.
func (p *KafkaPublisher) Publish(ctx context.Context, topic string, payload interface{}) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	return p.writer.WriteMessages(ctx, kafka.Message{
		Topic: topic,
		Value: b,
	})
}
```

- [ ] **Step 3: Add segmentio/kafka-go to go.mod**

```bash
cd control-plane && go get github.com/segmentio/kafka-go@v0.4.47 && go mod tidy
```

- [ ] **Step 4: Verify compile**

```bash
cd control-plane && go build ./internal/events/...
```
Expected: no errors.

---

## Task 14: Dispatcher Goroutine

**Files:**
- Create: `control-plane/internal/jobs/dispatcher.go`
- Test: `control-plane/internal/jobs/dispatcher_test.go`

- [ ] **Step 1: Write failing integration test**

`control-plane/internal/jobs/dispatcher_test.go`:
```go
// control-plane/internal/jobs/dispatcher_test.go
package jobs_test

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/events"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/jobs"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/testutil"
)

func TestDispatcher_PromotesPendingToQueued(t *testing.T) {
	pool := testutil.SetupDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Seed tenant and project
	var tenantID, projectID string
	pool.QueryRow(ctx, `INSERT INTO tenants (name) VALUES ('disp-tenant') RETURNING id::text`).Scan(&tenantID)
	pool.QueryRow(ctx, `INSERT INTO projects (tenant_id, name) VALUES ($1, 'disp-proj') RETURNING id::text`, tenantID).Scan(&projectID)

	store := jobs.NewPostgresJobStore(pool)

	job := &jobs.TrainingJob{
		TenantID: tenantID, ProjectID: projectID, Name: "disp-job",
		Status: "PENDING", Image: "img:1", Command: []string{"run"},
		Args: []string{}, Env: map[string]string{},
		NumWorkers: 1, WorkerCPU: "1", WorkerMemory: "1Gi",
		HeadCPU: "1", HeadMemory: "1Gi",
	}
	run := &jobs.TrainingRun{TenantID: tenantID, Status: "PENDING"}
	if err := store.CreateJobWithRun(ctx, job, run); err != nil {
		t.Fatalf("create job: %v", err)
	}

	// Set up fake dynamic Kubernetes client
	scheme := runtime.NewScheme()
	rayJobGVR := schema.GroupVersionResource{Group: "ray.io", Version: "v1", Resource: "rayjobs"}
	fakeClient := fake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		rayJobGVR: "RayJobList",
	})

	pub := &events.NoOpPublisher{}
	d := jobs.NewDispatcher(store, fakeClient, pub, 100*time.Millisecond)

	dispCtx, dispCancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		d.Run(dispCtx)
		close(done)
	}()

	// Wait for the job to become QUEUED
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		j, err := store.GetJob(ctx, job.ID, tenantID)
		if err == nil && j.Status == "QUEUED" && j.RayJobName != nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	dispCancel()
	<-done

	finalJob, err := store.GetJob(ctx, job.ID, tenantID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if finalJob.Status != "QUEUED" {
		t.Fatalf("expected QUEUED, got %q", finalJob.Status)
	}
	if finalJob.RayJobName == nil {
		t.Fatal("expected rayjob_name to be set")
	}

	// Verify the RayJob was created in the fake k8s client
	actions := fakeClient.Actions()
	created := false
	for _, a := range actions {
		if a.GetVerb() == "create" && a.GetResource() == rayJobGVR {
			created = true
			break
		}
	}
	if !created {
		t.Fatal("expected a create action on rayjobs GVR")
	}

	// Verify the Kubernetes action list included a "create" for the rayjobs resource
	var createAction k8stesting.CreateAction
	for _, a := range actions {
		if ca, ok := a.(k8stesting.CreateAction); ok && a.GetResource() == rayJobGVR {
			createAction = ca
			break
		}
	}
	if createAction == nil {
		t.Fatal("no create action found for rayjobs")
	}
}
```

- [ ] **Step 2: Run — expect FAIL**

```bash
cd control-plane && go test ./internal/jobs/... -run TestDispatcher -v -timeout 60s
```
Expected: FAIL — `jobs.NewDispatcher` undefined.

- [ ] **Step 3: Implement the dispatcher**

`control-plane/internal/jobs/dispatcher.go`:
```go
// control-plane/internal/jobs/dispatcher.go
package jobs

import (
	"context"
	"log/slog"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/events"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/scheduler"
)

// JobEvent payload published to Kafka on every state transition.
type JobEvent struct {
	JobID         string  `json:"job_id"`
	TenantID      string  `json:"tenant_id"`
	Status        string  `json:"status"`
	Timestamp     string  `json:"timestamp"`
	FailureReason *string `json:"failure_reason,omitempty"`
}

// Dispatcher polls for PENDING jobs, enforces quota, submits RayJob CRDs,
// and retries QUEUED jobs that haven't had their CRD submitted yet.
type Dispatcher struct {
	store     Store
	k8s       dynamic.Interface
	publisher events.Publisher
	interval  time.Duration
}

// NewDispatcher creates a Dispatcher. interval is the polling tick duration.
func NewDispatcher(store Store, k8s dynamic.Interface, publisher events.Publisher, interval time.Duration) *Dispatcher {
	return &Dispatcher{
		store:     store,
		k8s:       k8s,
		publisher: publisher,
		interval:  interval,
	}
}

// Run starts the dispatcher loop. Blocks until ctx is cancelled.
func (d *Dispatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.tick(ctx)
		}
	}
}

func (d *Dispatcher) tick(ctx context.Context) {
	// 1. Retry QUEUED jobs that haven't had a RayJob CRD submitted yet.
	d.retryQueuedWithoutRayJob(ctx)

	// 2. Promote the oldest PENDING job per tenant (FIFO, one per tenant per tick).
	tenantIDs, err := d.store.GetTenantIDsWithPendingJobs(ctx)
	if err != nil {
		slog.Error("dispatcher: list pending tenants", "error", err)
		return
	}
	for _, tenantID := range tenantIDs {
		d.promoteOldestPending(ctx, tenantID)
	}
}

func (d *Dispatcher) promoteOldestPending(ctx context.Context, tenantID string) {
	job, err := d.store.GetOldestPendingJob(ctx, tenantID)
	if err != nil {
		return // no pending job for this tenant
	}

	// Quota check
	cpuQuota, memQuota, err := d.store.GetTenantQuota(ctx, tenantID)
	if err != nil {
		slog.Error("dispatcher: get tenant quota", "tenant_id", tenantID, "error", err)
		return
	}
	activeJobs, err := d.store.ListActiveJobs(ctx, tenantID)
	if err != nil {
		slog.Error("dispatcher: list active jobs", "tenant_id", tenantID, "error", err)
		return
	}

	activeResources := make([]scheduler.JobResources, 0, len(activeJobs))
	for _, aj := range activeJobs {
		activeResources = append(activeResources, scheduler.JobResources{
			NumWorkers: aj.NumWorkers, WorkerCPU: aj.WorkerCPU,
			WorkerMemory: aj.WorkerMemory, HeadCPU: aj.HeadCPU, HeadMemory: aj.HeadMemory,
		})
	}
	quota := scheduler.TenantQuota{CPUMillicores: cpuQuota, MemoryBytes: memQuota}
	requested := scheduler.JobResources{
		NumWorkers: job.NumWorkers, WorkerCPU: job.WorkerCPU,
		WorkerMemory: job.WorkerMemory, HeadCPU: job.HeadCPU, HeadMemory: job.HeadMemory,
	}
	if err := scheduler.CheckQuota(quota, activeResources, requested); err != nil {
		slog.Debug("dispatcher: quota exceeded, skipping", "job_id", job.ID, "reason", err)
		return
	}

	// Transition PENDING → QUEUED
	if err := d.store.TransitionJobStatus(ctx, job.ID, "PENDING", "QUEUED", nil); err != nil {
		slog.Error("dispatcher: transition PENDING→QUEUED", "job_id", job.ID, "error", err)
		return
	}
	d.publishEvent(ctx, "platform.job.queued", job.ID, job.TenantID, "QUEUED", nil)

	// Submit RayJob CRD
	d.submitRayJob(ctx, job)
}

func (d *Dispatcher) retryQueuedWithoutRayJob(ctx context.Context) {
	jobs, err := d.store.GetQueuedJobsWithoutRayJob(ctx)
	if err != nil {
		slog.Error("dispatcher: list queued without rayjob", "error", err)
		return
	}
	for _, job := range jobs {
		d.submitRayJob(ctx, job)
	}
}

func (d *Dispatcher) submitRayJob(ctx context.Context, job *TrainingJob) {
	hints := scheduler.Hints()
	obj := BuildRayJob(job, hints)

	_, err := d.k8s.Resource(RayJobGVR).Namespace(RayJobNamespace).Create(
		ctx, obj, metav1.CreateOptions{},
	)
	if err != nil {
		slog.Error("dispatcher: create RayJob", "job_id", job.ID, "error", err)
		return
	}

	if err := d.store.SetRayJobName(ctx, job.ID, obj.GetName()); err != nil {
		slog.Error("dispatcher: set rayjob_name", "job_id", job.ID, "error", err)
	}
	slog.Info("dispatcher: RayJob submitted", "job_id", job.ID, "rayjob_name", obj.GetName())
}

func (d *Dispatcher) publishEvent(ctx context.Context, topic, jobID, tenantID, status string, failureReason *string) {
	evt := JobEvent{
		JobID:         jobID,
		TenantID:      tenantID,
		Status:        status,
		Timestamp:     time.Now().UTC().Format(time.RFC3339),
		FailureReason: failureReason,
	}
	if err := d.publisher.Publish(ctx, topic, evt); err != nil {
		slog.Warn("dispatcher: kafka publish failed", "topic", topic, "job_id", jobID, "error", err)
	}
}
```

- [ ] **Step 4: Run — expect PASS**

```bash
cd control-plane && go test ./internal/jobs/... -run TestDispatcher -v -timeout 60s
```
Expected: PASS.

---

## Task 15: Jobs API Handlers

**Files:**
- Create: `control-plane/internal/api/jobs.go`
- Test: `control-plane/internal/api/jobs_test.go`

- [ ] **Step 1: Write failing integration tests**

`control-plane/internal/api/jobs_test.go`:
```go
// control-plane/internal/api/jobs_test.go
package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/api"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/events"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/jobs"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/testutil"
)

type apiTestEnv struct {
	handler   http.Handler
	pool      *pgxpool.Pool
	tenantID  string
	projectID string
	token     string
}

func setupAPITest(t *testing.T) *apiTestEnv {
	t.Helper()
	pool := testutil.SetupDB(t)
	ctx := context.Background()

	var tenantID, projectID string
	pool.QueryRow(ctx, `INSERT INTO tenants (name) VALUES ('api-test-tenant') RETURNING id::text`).Scan(&tenantID)
	pool.QueryRow(ctx, `INSERT INTO projects (tenant_id, name) VALUES ($1, 'api-proj') RETURNING id::text`, tenantID).Scan(&projectID)

	plaintext := "testtoken-xxxx-yyyy"
	prefix := plaintext[:8]
	hash, _ := bcrypt.GenerateFromPassword([]byte(plaintext), bcrypt.MinCost)
	pool.Exec(ctx, `INSERT INTO api_tokens (tenant_id, token_hash, token_prefix) VALUES ($1, $2, $3)`,
		tenantID, string(hash), prefix)

	store := jobs.NewPostgresJobStore(pool)
	pub := &events.NoOpPublisher{}
	handler := api.NewRouter(pool, store, pub, nil)

	return &apiTestEnv{handler: handler, pool: pool, tenantID: tenantID, projectID: projectID, token: plaintext}
}

func TestSubmitJob_HappyPath(t *testing.T) {
	env := setupAPITest(t)

	body := map[string]interface{}{
		"name":       "test-job",
		"project_id": env.projectID,
		"runtime": map[string]interface{}{
			"image":   "img:1",
			"command": []string{"python", "train.py"},
			"args":    []string{},
			"env":     map[string]string{},
		},
		"resources": map[string]interface{}{
			"num_workers":   1,
			"worker_cpu":    "1",
			"worker_memory": "1Gi",
			"head_cpu":      "1",
			"head_memory":   "1Gi",
		},
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/jobs", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+env.token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["job_id"] == "" {
		t.Fatal("expected job_id in response")
	}
	if resp["run_id"] == "" {
		t.Fatal("expected run_id in response")
	}
}

func TestSubmitJob_InvalidCPU(t *testing.T) {
	env := setupAPITest(t)

	body := map[string]interface{}{
		"name": "bad-job", "project_id": env.projectID,
		"runtime": map[string]interface{}{"image": "img:1", "command": []string{"run"}, "args": []string{}, "env": map[string]string{}},
		"resources": map[string]interface{}{
			"num_workers": 1, "worker_cpu": "not-valid", "worker_memory": "1Gi",
			"head_cpu": "1", "head_memory": "1Gi",
		},
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/jobs", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+env.token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", rec.Code)
	}
}

func TestSubmitJob_Unauthenticated(t *testing.T) {
	env := setupAPITest(t)

	req := httptest.NewRequest(http.MethodPost, "/v1/jobs", bytes.NewReader([]byte("{}")))
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestSubmitJob_QuotaExceeded(t *testing.T) {
	env := setupAPITest(t)
	ctx := context.Background()

	// Set a tight CPU quota on the tenant: 1 core = 1000 millicores
	env.pool.Exec(ctx, `UPDATE tenants SET cpu_quota = 1000 WHERE id = $1`, env.tenantID)

	submitJob := func() int {
		body := map[string]interface{}{
			"name": "quota-job", "project_id": env.projectID,
			"runtime": map[string]interface{}{"image": "img:1", "command": []string{"run"}, "args": []string{}, "env": map[string]string{}},
			"resources": map[string]interface{}{
				"num_workers": 1, "worker_cpu": "1", "worker_memory": "1Gi",
				"head_cpu": "0", "head_memory": "0",
			},
		}
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/v1/jobs", bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+env.token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		env.handler.ServeHTTP(rec, req)
		return rec.Code
	}

	first := submitJob()
	if first != http.StatusAccepted {
		t.Fatalf("first job: expected 202, got %d", first)
	}
	second := submitJob()
	if second != http.StatusTooManyRequests {
		t.Fatalf("second job over quota: expected 429, got %d", second)
	}
}

func TestListJobs(t *testing.T) {
	env := setupAPITest(t)

	// Submit one job first via the API
	body := map[string]interface{}{
		"name": "list-job", "project_id": env.projectID,
		"runtime": map[string]interface{}{"image": "img:1", "command": []string{"run"}, "args": []string{}, "env": map[string]string{}},
		"resources": map[string]interface{}{
			"num_workers": 1, "worker_cpu": "1", "worker_memory": "1Gi",
			"head_cpu": "1", "head_memory": "1Gi",
		},
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/jobs", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+env.token)
	req.Header.Set("Content-Type", "application/json")
	env.handler.ServeHTTP(httptest.NewRecorder(), req)

	// List
	listReq := httptest.NewRequest(http.MethodGet, "/v1/jobs", nil)
	listReq.Header.Set("Authorization", "Bearer "+env.token)
	listRec := httptest.NewRecorder()
	env.handler.ServeHTTP(listRec, listReq)

	if listRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", listRec.Code, listRec.Body.String())
	}
	var resp map[string]interface{}
	json.NewDecoder(listRec.Body).Decode(&resp)
	jobsList, ok := resp["jobs"].([]interface{})
	if !ok || len(jobsList) == 0 {
		t.Fatal("expected non-empty jobs list")
	}
}
```

- [ ] **Step 2: Run — expect FAIL**

```bash
cd control-plane && go test ./internal/api/... -run "TestSubmitJob|TestListJobs" -v -timeout 120s
```
Expected: FAIL — `api.NewRouter` signature change undefined.

- [ ] **Step 3: Implement jobs API handler**

`control-plane/internal/api/jobs.go`:
```go
// control-plane/internal/api/jobs.go
package api

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/auth"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/events"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/jobs"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/scheduler"
)

type jobsHandler struct {
	store     jobs.Store
	publisher events.Publisher
}

func (h *jobsHandler) handleSubmitJob(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDFromContext(r.Context())

	var req jobs.JobSubmitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	// Admission check
	admReq := scheduler.AdmissionRequest{
		Image:        req.Runtime.Image,
		NumWorkers:   req.Resources.NumWorkers,
		WorkerCPU:    req.Resources.WorkerCPU,
		WorkerMemory: req.Resources.WorkerMemory,
		HeadCPU:      req.Resources.HeadCPU,
		HeadMemory:   req.Resources.HeadMemory,
	}
	if err := scheduler.Admit(admReq); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}

	// Quota check at submission time (pre-check; dispatcher re-checks before promotion)
	cpuQuota, memQuota, err := h.store.GetTenantQuota(r.Context(), tenantID)
	if err != nil {
		slog.Error("get tenant quota", "tenant_id", tenantID, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	activeJobs, err := h.store.ListActiveJobs(r.Context(), tenantID)
	if err != nil {
		slog.Error("list active jobs", "tenant_id", tenantID, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	activeResources := make([]scheduler.JobResources, 0, len(activeJobs))
	for _, aj := range activeJobs {
		activeResources = append(activeResources, scheduler.JobResources{
			NumWorkers: aj.NumWorkers, WorkerCPU: aj.WorkerCPU,
			WorkerMemory: aj.WorkerMemory, HeadCPU: aj.HeadCPU, HeadMemory: aj.HeadMemory,
		})
	}
	quotaErr := scheduler.CheckQuota(
		scheduler.TenantQuota{CPUMillicores: cpuQuota, MemoryBytes: memQuota},
		activeResources,
		scheduler.JobResources{
			NumWorkers: req.Resources.NumWorkers, WorkerCPU: req.Resources.WorkerCPU,
			WorkerMemory: req.Resources.WorkerMemory, HeadCPU: req.Resources.HeadCPU,
			HeadMemory: req.Resources.HeadMemory,
		},
	)
	if quotaErr != nil {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": quotaErr.Error()})
		return
	}

	env := req.Runtime.Env
	if env == nil {
		env = map[string]string{}
	}
	args := req.Runtime.Args
	if args == nil {
		args = []string{}
	}

	job := &jobs.TrainingJob{
		TenantID: tenantID, ProjectID: req.ProjectID, Name: req.Name,
		Status: "PENDING", Image: req.Runtime.Image,
		Command: req.Runtime.Command, Args: args, Env: env,
		NumWorkers: req.Resources.NumWorkers, WorkerCPU: req.Resources.WorkerCPU,
		WorkerMemory: req.Resources.WorkerMemory, HeadCPU: req.Resources.HeadCPU,
		HeadMemory: req.Resources.HeadMemory,
	}
	run := &jobs.TrainingRun{TenantID: tenantID, Status: "PENDING"}

	if err := h.store.CreateJobWithRun(r.Context(), job, run); err != nil {
		slog.Error("create job with run", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	// Publish PENDING event (best-effort)
	evt := jobs.JobEvent{JobID: job.ID, TenantID: tenantID, Status: "PENDING"}
	if err := h.publisher.Publish(r.Context(), "platform.job.pending", evt); err != nil {
		slog.Warn("publish job.pending", "job_id", job.ID, "error", err)
	}

	writeJSON(w, http.StatusAccepted, map[string]string{"job_id": job.ID, "run_id": run.ID})
}

func (h *jobsHandler) handleListJobs(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDFromContext(r.Context())
	list, err := h.store.ListJobs(r.Context(), tenantID)
	if err != nil {
		slog.Error("list jobs", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if list == nil {
		list = []*jobs.TrainingJob{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"jobs": list})
}

func (h *jobsHandler) handleGetJob(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDFromContext(r.Context())
	jobID := chi.URLParam(r, "id")

	job, err := h.store.GetJob(r.Context(), jobID, tenantID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found"})
		return
	}
	run, _ := h.store.GetRunByJobID(r.Context(), jobID)
	writeJSON(w, http.StatusOK, map[string]interface{}{"job": job, "run": run})
}

func (h *jobsHandler) handleGetRun(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDFromContext(r.Context())
	jobID := chi.URLParam(r, "id")
	runID := chi.URLParam(r, "run_id")

	run, err := h.store.GetRun(r.Context(), jobID, runID, tenantID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "run not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"run": run})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}
```

- [ ] **Step 4: Run — expect PASS (after router is updated in Task 17)**

Skip running until Task 17 updates the router to accept the new NewRouter signature. Continue to Task 16.

---

## Task 16: Internal Status API Handler

**Files:**
- Create: `control-plane/internal/api/internal.go`
- Test: `control-plane/internal/api/internal_test.go`

- [ ] **Step 1: Write failing integration tests**

`control-plane/internal/api/internal_test.go`:
```go
// control-plane/internal/api/internal_test.go
package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/api"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/events"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/jobs"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/testutil"
)

func setupInternalTest(t *testing.T) (http.Handler, jobs.Store, string, string) {
	t.Helper()
	pool := testutil.SetupDB(t)
	ctx := context.Background()

	var tenantID, projectID string
	pool.QueryRow(ctx, `INSERT INTO tenants (name) VALUES ('int-tenant') RETURNING id::text`).Scan(&tenantID)
	pool.QueryRow(ctx, `INSERT INTO projects (tenant_id, name) VALUES ($1, 'int-proj') RETURNING id::text`, tenantID).Scan(&projectID)

	store := jobs.NewPostgresJobStore(pool)
	handler := api.NewInternalRouter(store, &events.NoOpPublisher{})
	return handler, store, tenantID, projectID
}

func TestInternalStatus_QueuedToRunning(t *testing.T) {
	handler, store, tenantID, projectID := setupInternalTest(t)
	ctx := context.Background()

	job := &jobs.TrainingJob{
		TenantID: tenantID, ProjectID: projectID, Name: "int-job",
		Status: "QUEUED", Image: "img:1", Command: []string{"run"},
		Args: []string{}, Env: map[string]string{},
		NumWorkers: 1, WorkerCPU: "1", WorkerMemory: "1Gi",
		HeadCPU: "1", HeadMemory: "1Gi",
	}
	run := &jobs.TrainingRun{TenantID: tenantID, Status: "QUEUED"}
	// Create as PENDING first, then transition to QUEUED
	job.Status = "PENDING"
	run.Status = "PENDING"
	store.CreateJobWithRun(ctx, job, run)
	store.TransitionJobStatus(ctx, job.ID, "PENDING", "QUEUED", nil)

	body := map[string]interface{}{"status": "RUNNING"}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPatch, "/internal/v1/jobs/"+job.ID+"/status", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	got, _ := store.GetJob(ctx, job.ID, tenantID)
	if got.Status != "RUNNING" {
		t.Fatalf("expected RUNNING, got %q", got.Status)
	}
}

func TestInternalStatus_InvalidTransition(t *testing.T) {
	handler, store, tenantID, projectID := setupInternalTest(t)
	ctx := context.Background()

	job := &jobs.TrainingJob{
		TenantID: tenantID, ProjectID: projectID, Name: "conflict-job",
		Status: "PENDING", Image: "img:1", Command: []string{"run"},
		Args: []string{}, Env: map[string]string{},
		NumWorkers: 1, WorkerCPU: "1", WorkerMemory: "1Gi",
		HeadCPU: "1", HeadMemory: "1Gi",
	}
	run := &jobs.TrainingRun{TenantID: tenantID, Status: "PENDING"}
	store.CreateJobWithRun(ctx, job, run)

	// PENDING → SUCCEEDED is invalid
	body := map[string]interface{}{"status": "SUCCEEDED"}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPatch, "/internal/v1/jobs/"+job.ID+"/status", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", rec.Code)
	}
}
```

- [ ] **Step 2: Run — expect FAIL**

```bash
cd control-plane && go test ./internal/api/... -run TestInternalStatus -v -timeout 120s
```
Expected: FAIL — `api.NewInternalRouter` undefined.

- [ ] **Step 3: Implement internal status handler**

`control-plane/internal/api/internal.go`:
```go
// control-plane/internal/api/internal.go
package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/events"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/jobs"
)

// NewInternalRouter builds the internal-only HTTP handler for operator callbacks.
// This router has no auth middleware — it must be bound to an internal-only port.
func NewInternalRouter(store jobs.Store, publisher events.Publisher) http.Handler {
	r := chi.NewRouter()
	h := &internalHandler{store: store, publisher: publisher}
	r.Patch("/internal/v1/jobs/{id}/status", h.handleUpdateJobStatus)
	return r
}

type internalHandler struct {
	store     jobs.Store
	publisher events.Publisher
}

func (h *internalHandler) handleUpdateJobStatus(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "id")

	var req jobs.StatusUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	// Fetch current job to know the from-status
	// We need tenant_id too — since this is internal, query without tenant filter
	job, err := h.store.GetJobByID(r.Context(), jobID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found"})
		return
	}

	if err := h.store.TransitionJobStatus(r.Context(), jobID, job.Status, req.Status, req.FailureReason); err != nil {
		slog.Error("internal: transition status", "job_id", jobID, "from", job.Status, "to", req.Status, "error", err)
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}

	topic := statusToTopic(req.Status)
	evt := jobs.JobEvent{
		JobID:         jobID,
		TenantID:      job.TenantID,
		Status:        req.Status,
		Timestamp:     time.Now().UTC().Format(time.RFC3339),
		FailureReason: req.FailureReason,
	}
	if err := h.publisher.Publish(r.Context(), topic, evt); err != nil {
		slog.Warn("internal: publish event", "topic", topic, "job_id", jobID, "error", err)
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": req.Status})
}

func statusToTopic(status string) string {
	switch status {
	case "RUNNING":
		return "platform.job.running"
	case "SUCCEEDED":
		return "platform.job.succeeded"
	case "FAILED":
		return "platform.job.failed"
	default:
		return "platform.job." + status
	}
}
```

- [ ] **Step 4: Add GetJobByID to Store interface and PostgresJobStore**

The internal handler needs to fetch a job by ID without tenant filtering. Add to `control-plane/internal/jobs/store.go`:

In the `Store` interface, add:
```go
GetJobByID(ctx context.Context, id string) (*TrainingJob, error)
```

In `PostgresJobStore`, add:
```go
func (s *PostgresJobStore) GetJobByID(ctx context.Context, id string) (*TrainingJob, error) {
	row := s.db.QueryRow(ctx, `
		SELECT id::text, tenant_id::text, project_id::text, name, status,
		       image, command, args, env, num_workers,
		       worker_cpu, worker_memory, head_cpu, head_memory,
		       rayjob_name, created_at, updated_at
		FROM training_jobs WHERE id = $1`, id,
	)
	return scanJob(row)
}
```

- [ ] **Step 5: Run — expect PASS (after router update in Task 17)**

Continue to Task 17 to update the router, then run all API tests.

---

## Task 17: Router and main.go Wiring

**Files:**
- Modify: `control-plane/internal/api/router.go`
- Modify: `control-plane/cmd/server/main.go`

- [ ] **Step 1: Update router.go to accept store and publisher**

Replace `control-plane/internal/api/router.go` entirely:
```go
// control-plane/internal/api/router.go
package api

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/auth"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/events"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/jobs"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/observability"
)

// NewRouter builds the public HTTP handler with all middleware and routes.
// store and publisher may be nil in tests that only exercise health endpoints.
func NewRouter(db *pgxpool.Pool, store jobs.Store, publisher events.Publisher, _ interface{}) http.Handler {
	r := chi.NewRouter()

	r.Get("/healthz", LivenessHandler)
	r.Get("/readyz", ReadinessHandler(db))

	r.Group(func(r chi.Router) {
		r.Use(chimiddleware.RequestID)
		r.Use(observability.RequestLogger(slog.Default()))
		r.Use(auth.TokenAuth(auth.NewPostgresTokenStore(db)))

		if store != nil && publisher != nil {
			h := &jobsHandler{store: store, publisher: publisher}
			r.Post("/v1/jobs", h.handleSubmitJob)
			r.Get("/v1/jobs", h.handleListJobs)
			r.Get("/v1/jobs/{id}", h.handleGetJob)
			r.Get("/v1/jobs/{id}/runs/{run_id}", h.handleGetRun)
		}
	})

	return r
}
```

- [ ] **Step 2: Update main.go to wire everything together**

Replace `control-plane/cmd/server/main.go` entirely:
```go
// control-plane/cmd/server/main.go
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/api"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/events"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/jobs"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/k8s"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/observability"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/storage"
)

func main() {
	logger := observability.NewLogger(os.Getenv("LOG_LEVEL"))
	slog.SetDefault(logger)

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		slog.Error("DATABASE_URL environment variable is required")
		os.Exit(1)
	}

	pool, err := storage.Connect(context.Background(), dbURL)
	if err != nil {
		slog.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := storage.RunMigrations(dbURL); err != nil {
		slog.Error("failed to run migrations", "error", err)
		os.Exit(1)
	}
	slog.Info("migrations applied")

	// Kubernetes dynamic client (optional — warn and skip if unavailable)
	k8sClient, err := k8s.NewDynamicClient()
	if err != nil {
		slog.Warn("kubernetes client unavailable, dispatcher will not submit RayJobs", "error", err)
	}

	// Kafka publisher (optional — fall back to no-op if not configured)
	var publisher events.Publisher
	if brokerAddr := os.Getenv("KAFKA_BROKER"); brokerAddr != "" {
		kp := events.NewKafkaPublisher(brokerAddr)
		defer kp.Close()
		publisher = kp
		slog.Info("kafka publisher configured", "broker", brokerAddr)
	} else {
		publisher = &events.NoOpPublisher{}
		slog.Warn("KAFKA_BROKER not set, events will not be published")
	}

	store := jobs.NewPostgresJobStore(pool)

	// Start dispatcher goroutine
	if k8sClient != nil {
		dispatchInterval := 5 * time.Second
		dispatcher := jobs.NewDispatcher(store, k8sClient, publisher, dispatchInterval)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go dispatcher.Run(ctx)
		slog.Info("dispatcher started", "interval", dispatchInterval)
	}

	// Internal HTTP server (operator callbacks) on a separate port
	internalPort := os.Getenv("SERVER_INTERNAL_PORT")
	if internalPort == "" {
		internalPort = "8081"
	}
	internalHandler := api.NewInternalRouter(store, publisher)
	go func() {
		slog.Info("internal server starting", "port", internalPort)
		if err := http.ListenAndServe(fmt.Sprintf(":%s", internalPort), internalHandler); err != nil {
			slog.Error("internal server stopped", "error", err)
		}
	}()

	// Public HTTP server
	port := os.Getenv("SERVER_PORT")
	if port == "" {
		port = "8080"
	}
	r := api.NewRouter(pool, store, publisher, nil)
	slog.Info("server starting", "port", port)
	if err := http.ListenAndServe(fmt.Sprintf(":%s", port), r); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
```

- [ ] **Step 3: Build the control plane**

```bash
cd control-plane && go build ./...
```
Expected: no errors. Fix any import or signature issues.

- [ ] **Step 4: Run all tests**

```bash
cd control-plane && go test ./... -timeout 120s -v 2>&1 | tail -50
```
Expected: all tests PASS.

---

## Task 18: Operator Module

**Files:**
- Create: `operator/go.mod`
- Create: `operator/cmd/operator/main.go`
- Create: `operator/internal/reconciler/rayjob_reconciler.go`
- Test: `operator/internal/reconciler/rayjob_reconciler_test.go`

- [ ] **Step 1: Initialize the operator Go module**

```bash
mkdir -p operator/cmd/operator operator/internal/reconciler
cd operator
go mod init github.com/Weilei424/kubernetes-native-ai-platform/operator
go get sigs.k8s.io/controller-runtime@v0.20.4
go get k8s.io/apimachinery@v0.32.3
go get k8s.io/client-go@v0.32.3
go mod tidy
```

- [ ] **Step 2: Write failing reconciler tests**

`operator/internal/reconciler/rayjob_reconciler_test.go`:
```go
// operator/internal/reconciler/rayjob_reconciler_test.go
package reconciler_test

import (
	"testing"

	"github.com/Weilei424/kubernetes-native-ai-platform/operator/internal/reconciler"
)

func TestMapStatus_Running(t *testing.T) {
	got, ok := reconciler.MapRayJobStatus("Running")
	if !ok {
		t.Fatal("Running should map to a platform status")
	}
	if got != "RUNNING" {
		t.Fatalf("expected RUNNING, got %q", got)
	}
}

func TestMapStatus_Complete(t *testing.T) {
	got, ok := reconciler.MapRayJobStatus("Complete")
	if !ok {
		t.Fatal("Complete should map to a platform status")
	}
	if got != "SUCCEEDED" {
		t.Fatalf("expected SUCCEEDED, got %q", got)
	}
}

func TestMapStatus_Failed(t *testing.T) {
	got, ok := reconciler.MapRayJobStatus("Failed")
	if !ok {
		t.Fatal("Failed should map to a platform status")
	}
	if got != "FAILED" {
		t.Fatalf("expected FAILED, got %q", got)
	}
}

func TestMapStatus_Suspended(t *testing.T) {
	_, ok := reconciler.MapRayJobStatus("Suspended")
	if ok {
		t.Fatal("Suspended should not map (ignored in Phase 1)")
	}
}

func TestMapStatus_Unknown(t *testing.T) {
	_, ok := reconciler.MapRayJobStatus("SomeFutureStatus")
	if ok {
		t.Fatal("unknown status should not map")
	}
}
```

- [ ] **Step 3: Run — expect FAIL**

```bash
cd operator && go test ./internal/reconciler/... -run TestMapStatus -v
```
Expected: FAIL — `reconciler.MapRayJobStatus` undefined.

- [ ] **Step 4: Implement the reconciler**

`operator/internal/reconciler/rayjob_reconciler.go`:
```go
// operator/internal/reconciler/rayjob_reconciler.go
package reconciler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// RayJobReconciler watches RayJob objects and reports status back to the
// control plane via the internal status API.
type RayJobReconciler struct {
	client.Client
	ControlPlaneURL string // base URL of the control plane internal port, e.g. "http://control-plane:8081"
	HTTPClient      *http.Client
}

// Reconcile is called by controller-runtime whenever a RayJob changes.
func (r *RayJobReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(rayJobGVK())
	if err := r.Get(ctx, req.NamespacedName, obj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Only process platform-managed RayJobs
	labels := obj.GetLabels()
	jobID, ok := labels["platform.io/job-id"]
	if !ok {
		return ctrl.Result{}, nil
	}

	rayStatus, _, _ := unstructured.NestedString(obj.Object, "status", "jobDeploymentStatus")
	platformStatus, mapped := MapRayJobStatus(rayStatus)
	if !mapped {
		return ctrl.Result{}, nil
	}

	var failureReason *string
	if platformStatus == "FAILED" {
		msg, found, _ := unstructured.NestedString(obj.Object, "status", "message")
		if found && msg != "" {
			failureReason = &msg
		}
	}

	if err := r.reportStatus(ctx, jobID, platformStatus, failureReason); err != nil {
		slog.Error("reconciler: report status", "job_id", jobID, "status", platformStatus, "error", err)
		return ctrl.Result{}, err
	}

	slog.Info("reconciler: status reported", "job_id", jobID, "status", platformStatus)
	return ctrl.Result{}, nil
}

// MapRayJobStatus converts a KubeRay JobDeploymentStatus string to a platform
// status string. Returns (status, true) on a known mapping, ("", false) if the
// status should be ignored.
func MapRayJobStatus(rayStatus string) (string, bool) {
	switch rayStatus {
	case "Running":
		return "RUNNING", true
	case "Complete":
		return "SUCCEEDED", true
	case "Failed":
		return "FAILED", true
	default:
		return "", false
	}
}

func (r *RayJobReconciler) reportStatus(ctx context.Context, jobID, status string, failureReason *string) error {
	payload := map[string]interface{}{
		"status": status,
	}
	if failureReason != nil {
		payload["failure_reason"] = *failureReason
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	url := fmt.Sprintf("%s/internal/v1/jobs/%s/status", r.ControlPlaneURL, jobID)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := r.HTTPClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("PATCH %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusConflict {
		// Already in terminal state — idempotent, not an error
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d from internal API", resp.StatusCode)
	}
	return nil
}

func rayJobGVK() unstructured.Unstructured {
	obj := unstructured.Unstructured{}
	obj.SetAPIVersion("ray.io/v1")
	obj.SetKind("RayJob")
	return obj
}

// SetupWithManager registers the reconciler with the controller-runtime manager.
func (r *RayJobReconciler) SetupWithManager(mgr ctrl.Manager) error {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(rayJobGVK().GroupVersionKind())
	return ctrl.NewControllerManagedBy(mgr).
		For(obj).
		Complete(r)
}
```

- [ ] **Step 5: Run reconciler tests — expect PASS**

```bash
cd operator && go test ./internal/reconciler/... -run TestMapStatus -v
```
Expected: all 5 tests PASS.

- [ ] **Step 6: Write operator entrypoint**

`operator/cmd/operator/main.go`:
```go
// operator/cmd/operator/main.go
package main

import (
	"log/slog"
	"net/http"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/Weilei424/kubernetes-native-ai-platform/operator/internal/reconciler"
)

func main() {
	scheme := runtime.NewScheme()

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
	})
	if err != nil {
		slog.Error("unable to start manager", "error", err)
		os.Exit(1)
	}

	controlPlaneURL := os.Getenv("CONTROL_PLANE_INTERNAL_URL")
	if controlPlaneURL == "" {
		controlPlaneURL = "http://control-plane:8081"
	}

	r := &reconciler.RayJobReconciler{
		Client:          mgr.GetClient(),
		ControlPlaneURL: controlPlaneURL,
		HTTPClient:      &http.Client{Timeout: 10 * time.Second},
	}
	if err := r.SetupWithManager(mgr); err != nil {
		slog.Error("unable to set up reconciler", "error", err)
		os.Exit(1)
	}

	slog.Info("operator starting", "control_plane_url", controlPlaneURL)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		slog.Error("operator stopped", "error", err)
		os.Exit(1)
	}
}
```

- [ ] **Step 7: Build operator**

```bash
cd operator && go build ./...
```
Expected: no errors.

---

## Task 19: Final Verification

- [ ] **Step 1: Run all control-plane tests**

```bash
cd control-plane && go test ./... -timeout 180s 2>&1 | tail -30
```
Expected: all PASS, no FAILs.

- [ ] **Step 2: Run all operator tests**

```bash
cd operator && go test ./... -timeout 60s -v
```
Expected: all PASS.

- [ ] **Step 3: Full build check**

```bash
cd control-plane && go build ./... && go vet ./...
cd ../operator && go build ./... && go vet ./...
```
Expected: no errors or warnings.

- [ ] **Step 4: Verify migrations apply in order**

With a local PostgreSQL running (from `make local-up` or port-forward):
```bash
DATABASE_URL="postgresql://platform:platform@localhost:5432/platform?sslmode=disable" \
  go run ./cmd/server/main.go
```
Expected: logs show "migrations applied", server starts on :8080.

- [ ] **Step 5: Smoke test the API**

```bash
# Health check
curl -s http://localhost:8080/healthz

# Submit a job (replace TOKEN with a valid token from the DB)
curl -s -X POST http://localhost:8080/v1/jobs \
  -H "Authorization: Bearer <TOKEN>" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "smoke-test",
    "project_id": "<PROJECT_UUID>",
    "runtime": {"image": "rayproject/ray:2.9.0", "command": ["python", "-c", "print(1)"], "args": [], "env": {}},
    "resources": {"num_workers": 1, "worker_cpu": "1", "worker_memory": "1Gi", "head_cpu": "1", "head_memory": "1Gi"}
  }'
```
Expected: `{"job_id": "...", "run_id": "..."}` with HTTP 202.

---

## Commit Messages

After completing all tasks, one commit message per changed or created file:

```
control-plane/migrations/005_token_prefix: add token_prefix column and index to api_tokens
control-plane/migrations/006_tenant_quota: add cpu_quota and memory_quota to tenants
control-plane/migrations/007_create_training_jobs: create training_jobs table
control-plane/migrations/008_create_training_runs: create training_runs table
control-plane/internal/testutil/db.go: add testcontainers postgres helper
control-plane/internal/auth/postgres_store.go: use token_prefix keyed lookup in FindToken
control-plane/internal/jobs/model.go: define TrainingJob, TrainingRun, and request types
control-plane/internal/jobs/statemachine.go: implement state machine transition validation
control-plane/internal/jobs/store.go: implement PostgresJobStore with full CRUD
control-plane/internal/jobs/rayjob.go: build unstructured RayJob CRD from TrainingJob
control-plane/internal/jobs/dispatcher.go: implement async FIFO dispatcher goroutine
control-plane/internal/scheduler/admission.go: validate job spec at submission time
control-plane/internal/scheduler/quota.go: enforce per-tenant CPU/memory resource quota
control-plane/internal/scheduler/placement.go: generate node selector placement hints
control-plane/internal/events/publisher.go: define Publisher interface and NoOpPublisher
control-plane/internal/events/kafka.go: implement KafkaPublisher with segmentio/kafka-go
control-plane/internal/k8s/client.go: auto-detect Kubernetes dynamic client
control-plane/internal/api/jobs.go: implement POST /v1/jobs, GET /v1/jobs, GET /v1/jobs/:id
control-plane/internal/api/internal.go: implement PATCH /internal/v1/jobs/:id/status
control-plane/internal/api/router.go: update router to register job routes and accept store/publisher
control-plane/cmd/server/main.go: wire dispatcher, internal server, kafka, k8s client
operator/go.mod: initialize operator module with controller-runtime
operator/cmd/operator/main.go: controller-runtime manager entrypoint
operator/internal/reconciler/rayjob_reconciler.go: watch RayJobs and report status to control plane
```
