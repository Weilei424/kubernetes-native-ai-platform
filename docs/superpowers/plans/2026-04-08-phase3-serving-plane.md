# Phase 3 — Serving Plane Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the serving plane — deployment API, PostgreSQL metadata, Triton pod management via an operator reconciler, and an init-container model loader — completing the `promote → deploy → infer` tail of the sacred lifecycle.

**Architecture:** The control plane exposes a public deployment API (POST/GET/DELETE) and an internal callback API that the operator polls. The operator's `DeploymentReconciler` runs as a goroutine registered with the controller-runtime manager; it polls pending deployments, creates Triton pods with an init container that fetches ONNX artifacts from MinIO, creates a ClusterIP Service, and reports status back via PATCH to the internal API.

**Tech Stack:** Go 1.22, chi router, pgx/v5, controller-runtime, k8s.io/api (v1.Pod / v1.Service), Python 3.11 + boto3 (init container)

**Spec:** `docs/superpowers/specs/2026-04-08-phase3-serving-plane-design.md`

---

## File Map

**New files — control plane:**
- `control-plane/migrations/011_create_deployments.up.sql`
- `control-plane/migrations/011_create_deployments.down.sql`
- `control-plane/migrations/012_create_deployment_revisions.up.sql`
- `control-plane/migrations/012_create_deployment_revisions.down.sql`
- `control-plane/internal/deployments/model.go` — domain types + sentinel errors
- `control-plane/internal/deployments/statemachine.go` — valid transition table
- `control-plane/internal/deployments/statemachine_test.go`
- `control-plane/internal/deployments/store.go` — Store interface + PostgresDeploymentStore
- `control-plane/internal/deployments/store_test.go`
- `control-plane/internal/deployments/service.go` — Service: Create, Get, Delete, UpdateStatus
- `control-plane/internal/deployments/service_test.go`
- `control-plane/internal/api/deployments.go` — DeploymentsService interface + HTTP handlers
- `control-plane/internal/api/deployments_test.go`

**Modified files — control plane:**
- `control-plane/internal/api/internal.go` — add deployment list + status routes; add deploymentStore field
- `control-plane/internal/api/router.go` — add deployment routes; add DeploymentsService param
- `control-plane/cmd/server/main.go` — wire deployment store + service
- `control-plane/internal/api/jobs_test.go` — update `api.NewRouter` call (add nil for deploymentsSvc)
- `control-plane/internal/api/models_test.go` — update `api.NewRouter` calls (add nil for deploymentsSvc)

**New files — operator:**
- `operator/internal/reconciler/deployment_reconciler.go`
- `operator/internal/reconciler/deployment_reconciler_test.go`

**Modified files — operator:**
- `operator/cmd/operator/main.go` — add core scheme; register DeploymentReconciler

**New files — infra:**
- `infra/docker/model-loader/loader.py`
- `infra/docker/model-loader/Dockerfile`
- `infra/docker/model-loader/requirements.txt`

**Modified docs:**
- `docs/planning/BACKLOG.md` — expand Phase 3 checklist

---

## Task 1: DB Migrations

**Files:**
- Create: `control-plane/migrations/011_create_deployments.up.sql`
- Create: `control-plane/migrations/011_create_deployments.down.sql`
- Create: `control-plane/migrations/012_create_deployment_revisions.up.sql`
- Create: `control-plane/migrations/012_create_deployment_revisions.down.sql`

- [ ] **Step 1: Write migration 011 up**

```sql
-- control-plane/migrations/011_create_deployments.up.sql
CREATE TABLE deployments (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         UUID NOT NULL REFERENCES tenants(id),
    project_id        UUID NOT NULL REFERENCES projects(id),
    model_record_id   UUID NOT NULL REFERENCES model_records(id),
    model_version_id  UUID NOT NULL REFERENCES model_versions(id),
    name              TEXT NOT NULL,
    namespace         TEXT NOT NULL DEFAULT 'default',
    status            TEXT NOT NULL DEFAULT 'pending',
    desired_replicas  INT  NOT NULL DEFAULT 1,
    serving_endpoint  TEXT,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, name)
);
```

- [ ] **Step 2: Write migration 011 down**

```sql
-- control-plane/migrations/011_create_deployments.down.sql
DROP TABLE IF EXISTS deployments;
```

- [ ] **Step 3: Write migration 012 up**

```sql
-- control-plane/migrations/012_create_deployment_revisions.up.sql
CREATE TABLE deployment_revisions (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    deployment_id    UUID NOT NULL REFERENCES deployments(id),
    revision_number  INT  NOT NULL,
    model_version_id UUID NOT NULL REFERENCES model_versions(id),
    status           TEXT NOT NULL DEFAULT 'active',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (deployment_id, revision_number)
);
```

- [ ] **Step 4: Write migration 012 down**

```sql
-- control-plane/migrations/012_create_deployment_revisions.down.sql
DROP TABLE IF EXISTS deployment_revisions;
```

- [ ] **Step 5: Verify migrations apply**

```bash
cd control-plane
go test ./internal/testutil/... -v -run TestSetupDB
```

Expected: PASS (testutil.SetupDB runs all migrations including 011+012).
If testutil has no standalone test, run any store test that uses SetupDB:
```bash
go test ./internal/models/... -v -run TestCreateOrGetModelRecord_Create
```
Expected: PASS — confirms migrations apply cleanly.

---

## Task 2: Domain Types

**Files:**
- Create: `control-plane/internal/deployments/model.go`

- [ ] **Step 1: Write model.go**

```go
// control-plane/internal/deployments/model.go
package deployments

import (
	"errors"
	"time"
)

// Sentinel errors returned by Service methods.
var (
	ErrDeploymentNotFound       = errors.New("deployment not found")
	ErrModelVersionNotProduction = errors.New("model version is not at production status")
	ErrDuplicateDeploymentName  = errors.New("a deployment with this name already exists for this tenant")
	ErrModelNotFound            = errors.New("model not found")
	ErrVersionNotFound          = errors.New("model version not found")
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
```

---

## Task 3: State Machine

**Files:**
- Create: `control-plane/internal/deployments/statemachine.go`
- Create: `control-plane/internal/deployments/statemachine_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// control-plane/internal/deployments/statemachine_test.go
package deployments_test

import (
	"testing"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/deployments"
)

func TestValidTransition_PendingToProvisioning(t *testing.T) {
	if !deployments.ValidTransition("pending", "provisioning") {
		t.Fatal("pending → provisioning must be valid")
	}
}

func TestValidTransition_ProvisioningToRunning(t *testing.T) {
	if !deployments.ValidTransition("provisioning", "running") {
		t.Fatal("provisioning → running must be valid")
	}
}

func TestValidTransition_ProvisioningToFailed(t *testing.T) {
	if !deployments.ValidTransition("provisioning", "failed") {
		t.Fatal("provisioning → failed must be valid")
	}
}

func TestValidTransition_RunningToFailed(t *testing.T) {
	if !deployments.ValidTransition("running", "failed") {
		t.Fatal("running → failed must be valid")
	}
}

func TestValidTransition_AnyToDeleted(t *testing.T) {
	for _, from := range []string{"pending", "provisioning", "running", "failed"} {
		if !deployments.ValidTransition(from, "deleted") {
			t.Fatalf("%s → deleted must be valid", from)
		}
	}
}

func TestValidTransition_Invalid(t *testing.T) {
	if deployments.ValidTransition("running", "pending") {
		t.Fatal("running → pending must be invalid")
	}
	if deployments.ValidTransition("failed", "running") {
		t.Fatal("failed → running must be invalid")
	}
	if deployments.ValidTransition("deleted", "running") {
		t.Fatal("deleted → running must be invalid")
	}
}
```

- [ ] **Step 2: Run tests — expect compile failure**

```bash
cd control-plane && go test ./internal/deployments/... 2>&1 | head -20
```

Expected: compile error — `deployments.ValidTransition` undefined.

- [ ] **Step 3: Implement statemachine.go**

```go
// control-plane/internal/deployments/statemachine.go
package deployments

// validTransitions defines the allowed from → to pairs for deployment status.
var validTransitions = map[string]map[string]bool{
	"pending": {
		"provisioning": true,
		"deleted":      true,
	},
	"provisioning": {
		"running": true,
		"failed":  true,
		"deleted": true,
	},
	"running": {
		"failed":  true,
		"deleted": true,
	},
	"failed": {
		"deleted": true,
	},
}

// ValidTransition reports whether transitioning from → to is a legal deployment
// status change.
func ValidTransition(from, to string) bool {
	targets, ok := validTransitions[from]
	if !ok {
		return false
	}
	return targets[to]
}
```

- [ ] **Step 4: Run tests — expect PASS**

```bash
cd control-plane && go test ./internal/deployments/... -v -run TestValidTransition
```

Expected: all TestValidTransition_* PASS.

---

## Task 4: Deployment Store

**Files:**
- Create: `control-plane/internal/deployments/store.go`
- Create: `control-plane/internal/deployments/store_test.go`

- [ ] **Step 1: Write the failing store tests**

```go
// control-plane/internal/deployments/store_test.go
package deployments_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/deployments"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/jobs"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/models"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/testutil"
)

// setupDeploymentStoreTest creates tenant, project, a succeeded run, a model
// record at "production" status, and returns the store + fixture IDs.
func setupDeploymentStoreTest(t *testing.T) (deployments.Store, string, string, string, string) {
	t.Helper()
	pool := testutil.SetupDB(t)
	ctx := context.Background()

	var tenantID, projectID string
	pool.QueryRow(ctx, `INSERT INTO tenants (name, cpu_quota, memory_quota) VALUES ('dep-tenant', 16000, 32000000000) RETURNING id::text`).Scan(&tenantID)
	pool.QueryRow(ctx, `INSERT INTO projects (tenant_id, name) VALUES ($1, 'dep-proj') RETURNING id::text`, tenantID).Scan(&projectID)

	// Create a succeeded training run.
	jobStore := jobs.NewPostgresJobStore(pool)
	job := &jobs.TrainingJob{
		TenantID: tenantID, ProjectID: projectID, Name: "dep-src-job",
		Status: "PENDING", Image: "img:1", Command: []string{"run"},
		Args: []string{}, Env: map[string]string{},
		NumWorkers: 1, WorkerCPU: "1", WorkerMemory: "1Gi",
		HeadCPU: "1", HeadMemory: "1Gi",
	}
	run := &jobs.TrainingRun{TenantID: tenantID, Status: "PENDING"}
	jobStore.CreateJobWithRun(ctx, job, run)
	jobStore.TransitionJobStatus(ctx, job.ID, "PENDING", "QUEUED", nil)
	jobStore.TransitionJobStatus(ctx, job.ID, "QUEUED", "RUNNING", nil)
	jobStore.TransitionJobStatus(ctx, job.ID, "RUNNING", "SUCCEEDED", nil)
	jobStore.SetMLflowRunID(ctx, job.ID, "mlflow-run-xyz")

	// Create model record + version at "production" status.
	modelStore := models.NewPostgresModelStore(pool)
	rec := &models.ModelRecord{
		TenantID:                  tenantID,
		ProjectID:                 projectID,
		Name:                      "resnet50",
		MLflowRegisteredModelName: tenantID + "-resnet50",
	}
	modelStore.CreateOrGetModelRecord(ctx, rec)

	ver := &models.ModelVersion{
		ModelRecordID: rec.ID,
		TenantID:      tenantID,
		VersionNumber: 1,
		MLflowRunID:   "mlflow-run-xyz",
		SourceRunID:   run.ID,
		ArtifactURI:   "mlflow-artifacts:/resnet50/1/model/",
		Status:        "production",
	}
	modelStore.CreateModelVersion(ctx, ver)

	store := deployments.NewPostgresDeploymentStore(pool)
	return store, tenantID, projectID, rec.ID, ver.ID
}

func TestDeploymentStore_CreateAndGet(t *testing.T) {
	store, tenantID, projectID, modelRecordID, modelVersionID := setupDeploymentStoreTest(t)
	ctx := context.Background()

	d := &deployments.Deployment{
		TenantID:        tenantID,
		ProjectID:       projectID,
		ModelRecordID:   modelRecordID,
		ModelVersionID:  modelVersionID,
		Name:            "resnet50-prod",
		Namespace:       "default",
		Status:          "pending",
		DesiredReplicas: 1,
	}
	if err := store.CreateDeployment(ctx, d); err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	if d.ID == "" {
		t.Fatal("expected ID to be set after create")
	}

	got, err := store.GetDeployment(ctx, d.ID)
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	if got.Name != "resnet50-prod" {
		t.Errorf("expected name resnet50-prod, got %q", got.Name)
	}
	if got.Status != "pending" {
		t.Errorf("expected status pending, got %q", got.Status)
	}
}

func TestDeploymentStore_DuplicateName(t *testing.T) {
	store, tenantID, projectID, recID, verID := setupDeploymentStoreTest(t)
	ctx := context.Background()

	d := &deployments.Deployment{
		TenantID: tenantID, ProjectID: projectID,
		ModelRecordID: recID, ModelVersionID: verID,
		Name: "dup-dep", Namespace: "default", Status: "pending", DesiredReplicas: 1,
	}
	if err := store.CreateDeployment(ctx, d); err != nil {
		t.Fatalf("first create: %v", err)
	}
	d2 := &deployments.Deployment{
		TenantID: tenantID, ProjectID: projectID,
		ModelRecordID: recID, ModelVersionID: verID,
		Name: "dup-dep", Namespace: "default", Status: "pending", DesiredReplicas: 1,
	}
	err := store.CreateDeployment(ctx, d2)
	if !errors.Is(err, deployments.ErrDuplicateDeploymentName) {
		t.Fatalf("expected ErrDuplicateDeploymentName, got %v", err)
	}
}

func TestDeploymentStore_UpdateStatus(t *testing.T) {
	store, tenantID, projectID, recID, verID := setupDeploymentStoreTest(t)
	ctx := context.Background()

	d := &deployments.Deployment{
		TenantID: tenantID, ProjectID: projectID,
		ModelRecordID: recID, ModelVersionID: verID,
		Name: "status-dep", Namespace: "default", Status: "pending", DesiredReplicas: 1,
	}
	store.CreateDeployment(ctx, d)

	endpoint := "triton-" + d.ID + ".default.svc.cluster.local:8000"
	if err := store.UpdateDeploymentStatus(ctx, d.ID, "running", endpoint); err != nil {
		t.Fatalf("update status: %v", err)
	}

	got, _ := store.GetDeployment(ctx, d.ID)
	if got.Status != "running" {
		t.Errorf("expected status running, got %q", got.Status)
	}
	if got.ServingEndpoint != endpoint {
		t.Errorf("expected endpoint %q, got %q", endpoint, got.ServingEndpoint)
	}
}

func TestDeploymentStore_ListPendingDeployments(t *testing.T) {
	store, tenantID, projectID, recID, verID := setupDeploymentStoreTest(t)
	ctx := context.Background()

	d := &deployments.Deployment{
		TenantID: tenantID, ProjectID: projectID,
		ModelRecordID: recID, ModelVersionID: verID,
		Name: "list-dep", Namespace: "default", Status: "pending", DesiredReplicas: 1,
	}
	store.CreateDeployment(ctx, d)

	deps, err := store.ListPendingDeployments(ctx)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(deps) == 0 {
		t.Fatal("expected at least one pending deployment")
	}
	var found *deployments.Deployment
	for _, dep := range deps {
		if dep.ID == d.ID {
			found = dep
			break
		}
	}
	if found == nil {
		t.Fatal("created deployment not found in list")
	}
	if found.ArtifactURI == "" {
		t.Error("expected ArtifactURI to be populated by ListPendingDeployments")
	}
	if found.ModelName == "" {
		t.Error("expected ModelName to be populated by ListPendingDeployments")
	}
}

func TestDeploymentStore_GetNotFound(t *testing.T) {
	store, _, _, _, _ := setupDeploymentStoreTest(t)
	_, err := store.GetDeployment(context.Background(), "00000000-0000-0000-0000-000000000000")
	if !errors.Is(err, deployments.ErrDeploymentNotFound) {
		t.Fatalf("expected ErrDeploymentNotFound, got %v", err)
	}
}
```

- [ ] **Step 2: Run tests — expect compile failure**

```bash
cd control-plane && go test ./internal/deployments/... 2>&1 | head -20
```

Expected: compile error — `deployments.Store`, `deployments.NewPostgresDeploymentStore` undefined.

- [ ] **Step 3: Implement store.go**

```go
// control-plane/internal/deployments/store.go
package deployments

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is the interface for deployment metadata persistence.
type Store interface {
	// CreateDeployment inserts a new deployment and its first revision atomically.
	// d.ID is set on return. Returns ErrDuplicateDeploymentName on unique constraint violation.
	CreateDeployment(ctx context.Context, d *Deployment) error
	// GetDeployment returns the deployment with the given id.
	// Returns ErrDeploymentNotFound if not found.
	GetDeployment(ctx context.Context, id string) (*Deployment, error)
	// UpdateDeploymentStatus updates the status and serving_endpoint of a deployment.
	UpdateDeploymentStatus(ctx context.Context, id, status, endpoint string) error
	// DeleteDeployment sets the deployment status to "deleted".
	DeleteDeployment(ctx context.Context, id string) error
	// ListPendingDeployments returns all deployments with status "pending" or "provisioning",
	// enriched with artifact_uri and model_name via JOIN for operator consumption.
	ListPendingDeployments(ctx context.Context) ([]*Deployment, error)
}

// PostgresDeploymentStore implements Store against PostgreSQL.
type PostgresDeploymentStore struct {
	db *pgxpool.Pool
}

// NewPostgresDeploymentStore returns a Store backed by the given pool.
func NewPostgresDeploymentStore(db *pgxpool.Pool) Store {
	return &PostgresDeploymentStore{db: db}
}

func (s *PostgresDeploymentStore) CreateDeployment(ctx context.Context, d *Deployment) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	err = tx.QueryRow(ctx, `
		INSERT INTO deployments
		  (tenant_id, project_id, model_record_id, model_version_id, name, namespace, status, desired_replicas)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id::text, tenant_id::text, project_id::text, model_record_id::text,
		          model_version_id::text, name, namespace, status, desired_replicas,
		          serving_endpoint, created_at, updated_at`,
		d.TenantID, d.ProjectID, d.ModelRecordID, d.ModelVersionID,
		d.Name, d.Namespace, d.Status, d.DesiredReplicas,
	).Scan(
		&d.ID, &d.TenantID, &d.ProjectID, &d.ModelRecordID,
		&d.ModelVersionID, &d.Name, &d.Namespace, &d.Status, &d.DesiredReplicas,
		&d.ServingEndpoint, &d.CreatedAt, &d.UpdatedAt,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrDuplicateDeploymentName
		}
		return fmt.Errorf("insert deployment: %w", err)
	}

	// Insert revision 1.
	_, err = tx.Exec(ctx, `
		INSERT INTO deployment_revisions (deployment_id, revision_number, model_version_id, status)
		VALUES ($1, 1, $2, 'active')`,
		d.ID, d.ModelVersionID,
	)
	if err != nil {
		return fmt.Errorf("insert deployment_revision: %w", err)
	}

	return tx.Commit(ctx)
}

func (s *PostgresDeploymentStore) GetDeployment(ctx context.Context, id string) (*Deployment, error) {
	var d Deployment
	err := s.db.QueryRow(ctx, `
		SELECT id::text, tenant_id::text, project_id::text, model_record_id::text,
		       model_version_id::text, name, namespace, status, desired_replicas,
		       COALESCE(serving_endpoint, ''), created_at, updated_at
		FROM deployments WHERE id = $1::uuid`, id,
	).Scan(
		&d.ID, &d.TenantID, &d.ProjectID, &d.ModelRecordID,
		&d.ModelVersionID, &d.Name, &d.Namespace, &d.Status, &d.DesiredReplicas,
		&d.ServingEndpoint, &d.CreatedAt, &d.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrDeploymentNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get deployment: %w", err)
	}
	return &d, nil
}

func (s *PostgresDeploymentStore) UpdateDeploymentStatus(ctx context.Context, id, status, endpoint string) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE deployments
		SET status = $1, serving_endpoint = NULLIF($2, ''), updated_at = now()
		WHERE id = $3::uuid`,
		status, endpoint, id,
	)
	if err != nil {
		return fmt.Errorf("update deployment status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrDeploymentNotFound
	}
	return nil
}

func (s *PostgresDeploymentStore) DeleteDeployment(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE deployments SET status = 'deleted', updated_at = now()
		WHERE id = $1::uuid AND status != 'deleted'`,
		id,
	)
	if err != nil {
		return fmt.Errorf("delete deployment: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrDeploymentNotFound
	}
	return nil
}

func (s *PostgresDeploymentStore) ListPendingDeployments(ctx context.Context) ([]*Deployment, error) {
	rows, err := s.db.Query(ctx, `
		SELECT d.id::text, d.tenant_id::text, d.project_id::text,
		       d.model_record_id::text, d.model_version_id::text,
		       d.name, d.namespace, d.status, d.desired_replicas,
		       COALESCE(d.serving_endpoint, ''), d.created_at, d.updated_at,
		       mv.artifact_uri, mr.name
		FROM deployments d
		JOIN model_versions mv ON mv.id = d.model_version_id
		JOIN model_records  mr ON mr.id = d.model_record_id
		WHERE d.status IN ('pending', 'provisioning')`)
	if err != nil {
		return nil, fmt.Errorf("list pending deployments: %w", err)
	}
	defer rows.Close()

	var result []*Deployment
	for rows.Next() {
		var d Deployment
		if err := rows.Scan(
			&d.ID, &d.TenantID, &d.ProjectID,
			&d.ModelRecordID, &d.ModelVersionID,
			&d.Name, &d.Namespace, &d.Status, &d.DesiredReplicas,
			&d.ServingEndpoint, &d.CreatedAt, &d.UpdatedAt,
			&d.ArtifactURI, &d.ModelName,
		); err != nil {
			return nil, fmt.Errorf("scan deployment: %w", err)
		}
		result = append(result, &d)
	}
	return result, rows.Err()
}
```

- [ ] **Step 4: Run store tests — expect PASS**

```bash
cd control-plane && go test ./internal/deployments/... -v -run TestDeploymentStore
```

Expected: all TestDeploymentStore_* PASS.

---

## Task 5: Deployment Service

**Files:**
- Create: `control-plane/internal/deployments/service.go`
- Create: `control-plane/internal/deployments/service_test.go`

- [ ] **Step 1: Write the failing service tests**

```go
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
	createErr  error
	getResult  *deployments.Deployment
	getErr     error
	updateErr  error
	deleteErr  error
	listResult []*deployments.Deployment
	listErr    error
	created    *deployments.Deployment
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
		t.Fatalf("expected ErrModelNotFound, got %v", err)
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
```

- [ ] **Step 2: Run tests — expect compile failure**

```bash
cd control-plane && go test ./internal/deployments/... 2>&1 | head -20
```

Expected: compile error — `deployments.NewService` undefined.

- [ ] **Step 3: Implement service.go**

```go
// control-plane/internal/deployments/service.go
package deployments

import (
	"context"
	"errors"
	"fmt"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/models"
)

// ModelVersionReader provides model version lookup needed for deployment validation.
// Implemented by *models.PostgresModelStore (via models.Store).
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

// Delete marks the deployment as deleted.
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
func (s *Service) UpdateStatus(ctx context.Context, id, status, endpoint string) error {
	return s.store.UpdateDeploymentStatus(ctx, id, status, endpoint)
}
```

- [ ] **Step 4: Run service tests — expect PASS**

```bash
cd control-plane && go test ./internal/deployments/... -v -run TestService
```

Expected: all TestService_* PASS.

- [ ] **Step 5: Run all deployment tests**

```bash
cd control-plane && go test ./internal/deployments/... -v
```

Expected: all tests PASS (statemachine + store + service).

---

## Task 6: Public API Handlers

**Files:**
- Create: `control-plane/internal/api/deployments.go`
- Create: `control-plane/internal/api/deployments_test.go`

- [ ] **Step 1: Write the failing handler tests**

```go
// control-plane/internal/api/deployments_test.go
package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/api"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/deployments"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/events"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/jobs"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/testutil"
	"golang.org/x/crypto/bcrypt"
)

type mockDeploymentsService struct {
	createResult *deployments.Deployment
	createErr    error
	getResult    *deployments.Deployment
	getErr       error
	deleteErr    error
}

func (m *mockDeploymentsService) Create(_ context.Context, _ string, _ deployments.CreateDeploymentRequest) (*deployments.Deployment, error) {
	return m.createResult, m.createErr
}
func (m *mockDeploymentsService) Get(_ context.Context, _, _ string) (*deployments.Deployment, error) {
	return m.getResult, m.getErr
}
func (m *mockDeploymentsService) Delete(_ context.Context, _, _ string) error {
	return m.deleteErr
}

func setupDeploymentsAPITest(t *testing.T, svc api.DeploymentsService) (http.Handler, string) {
	t.Helper()
	pool := testutil.SetupDB(t)
	ctx := context.Background()

	var tenantID string
	pool.QueryRow(ctx, `INSERT INTO tenants (name) VALUES ('dep-api-tenant') RETURNING id::text`).Scan(&tenantID)

	plaintext := "deptoken-xxxx1234"
	prefix := plaintext[:8]
	hash, _ := bcrypt.GenerateFromPassword([]byte(plaintext), bcrypt.MinCost)
	pool.Exec(ctx, `INSERT INTO api_tokens (tenant_id, token_hash, token_prefix) VALUES ($1, $2, $3)`,
		tenantID, string(hash), prefix)

	store := jobs.NewPostgresJobStore(pool)
	pub := &events.NoOpPublisher{}
	handler := api.NewRouter(pool, store, pub, nil, svc)
	return handler, plaintext
}

func TestDeploymentsAPI_Create_HappyPath(t *testing.T) {
	svc := &mockDeploymentsService{
		createResult: &deployments.Deployment{
			ID: "dep-1", Name: "resnet50-prod", Status: "pending",
			Namespace: "default", DesiredReplicas: 1,
		},
	}
	handler, token := setupDeploymentsAPITest(t, svc)

	body := map[string]interface{}{
		"model_name": "resnet50", "model_version": 1,
		"name": "resnet50-prod", "namespace": "default", "replicas": 1,
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/deployments", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeploymentsAPI_Create_ModelVersionNotProduction(t *testing.T) {
	svc := &mockDeploymentsService{createErr: deployments.ErrModelVersionNotProduction}
	handler, token := setupDeploymentsAPITest(t, svc)

	body := map[string]interface{}{
		"model_name": "resnet50", "model_version": 1,
		"name": "resnet50-dep", "namespace": "default", "replicas": 1,
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/deployments", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeploymentsAPI_Create_DuplicateName(t *testing.T) {
	svc := &mockDeploymentsService{createErr: deployments.ErrDuplicateDeploymentName}
	handler, token := setupDeploymentsAPITest(t, svc)

	body := map[string]interface{}{
		"model_name": "resnet50", "model_version": 1,
		"name": "resnet50-prod", "namespace": "default", "replicas": 1,
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/deployments", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeploymentsAPI_Create_Unauthorized(t *testing.T) {
	svc := &mockDeploymentsService{}
	handler, _ := setupDeploymentsAPITest(t, svc)

	body := map[string]interface{}{"model_name": "resnet50", "model_version": 1, "name": "dep"}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/deployments", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestDeploymentsAPI_Get_HappyPath(t *testing.T) {
	svc := &mockDeploymentsService{
		getResult: &deployments.Deployment{
			ID: "dep-1", Name: "resnet50-prod", Status: "running",
			ServingEndpoint: "triton-dep-1.default.svc.cluster.local:8000",
		},
	}
	handler, token := setupDeploymentsAPITest(t, svc)

	req := httptest.NewRequest(http.MethodGet, "/v1/deployments/dep-1", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeploymentsAPI_Get_NotFound(t *testing.T) {
	svc := &mockDeploymentsService{getErr: deployments.ErrDeploymentNotFound}
	handler, token := setupDeploymentsAPITest(t, svc)

	req := httptest.NewRequest(http.MethodGet, "/v1/deployments/nonexistent", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestDeploymentsAPI_Delete_HappyPath(t *testing.T) {
	svc := &mockDeploymentsService{}
	handler, token := setupDeploymentsAPITest(t, svc)

	req := httptest.NewRequest(http.MethodDelete, "/v1/deployments/dep-1", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}
```

- [ ] **Step 2: Run tests — expect compile failure**

```bash
cd control-plane && go test ./internal/api/... 2>&1 | head -20
```

Expected: compile error — `api.DeploymentsService` undefined, `api.NewRouter` wrong arity.

- [ ] **Step 3: Implement deployments.go**

```go
// control-plane/internal/api/deployments.go
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/auth"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/deployments"
)

// DeploymentsService is the interface the handler depends on (exported for test use).
type DeploymentsService interface {
	Create(ctx context.Context, tenantID string, req deployments.CreateDeploymentRequest) (*deployments.Deployment, error)
	Get(ctx context.Context, id, tenantID string) (*deployments.Deployment, error)
	Delete(ctx context.Context, id, tenantID string) error
}

type deploymentsHandler struct {
	svc DeploymentsService
}

func (h *deploymentsHandler) handleCreate(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDFromContext(r.Context())

	var req deployments.CreateDeploymentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if req.ModelName == "" || req.Name == "" || req.ModelVersion == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "model_name, name, and model_version are required"})
		return
	}

	dep, err := h.svc.Create(r.Context(), tenantID, req)
	if err != nil {
		switch {
		case errors.Is(err, deployments.ErrModelNotFound), errors.Is(err, deployments.ErrVersionNotFound):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		case errors.Is(err, deployments.ErrModelVersionNotProduction):
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		case errors.Is(err, deployments.ErrDuplicateDeploymentName):
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		default:
			slog.Error("create deployment", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		}
		return
	}
	writeJSON(w, http.StatusCreated, map[string]interface{}{"deployment": dep})
}

func (h *deploymentsHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDFromContext(r.Context())
	id := chi.URLParam(r, "id")

	dep, err := h.svc.Get(r.Context(), id, tenantID)
	if err != nil {
		if errors.Is(err, deployments.ErrDeploymentNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "deployment not found"})
		} else {
			slog.Error("get deployment", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"deployment": dep})
}

func (h *deploymentsHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDFromContext(r.Context())
	id := chi.URLParam(r, "id")

	if err := h.svc.Delete(r.Context(), id, tenantID); err != nil {
		if errors.Is(err, deployments.ErrDeploymentNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "deployment not found"})
		} else {
			slog.Error("delete deployment", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
```

- [ ] **Step 4: Run tests — expect compile failure on NewRouter arity**

```bash
cd control-plane && go test ./internal/api/... 2>&1 | head -20
```

Expected: compile error — `api.NewRouter` wrong number of arguments.

---

## Task 7: Internal API Extension

**Files:**
- Modify: `control-plane/internal/api/internal.go`

Add two new internal routes and a `deploymentStore` field to `internalHandler`.

- [ ] **Step 1: Update internal.go**

Replace the entire file:

```go
// control-plane/internal/api/internal.go
package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/deployments"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/events"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/jobs"
)

// NewInternalRouter builds the internal-only HTTP handler for operator callbacks.
// This router has no auth middleware — it must be bound to an internal-only port.
func NewInternalRouter(store jobs.Store, publisher events.Publisher, deploymentStore deployments.Store) http.Handler {
	r := chi.NewRouter()
	h := &internalHandler{store: store, publisher: publisher, deploymentStore: deploymentStore}
	r.Patch("/internal/v1/jobs/{id}/status", h.handleUpdateJobStatus)
	r.Get("/internal/v1/deployments", h.handleListPendingDeployments)
	r.Patch("/internal/v1/deployments/{id}/status", h.handleUpdateDeploymentStatus)
	return r
}

type internalHandler struct {
	store           jobs.Store
	publisher       events.Publisher
	deploymentStore deployments.Store
}

func (h *internalHandler) handleUpdateJobStatus(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "id")

	var req jobs.StatusUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

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

	if req.MLflowRunID != nil {
		if err := h.store.SetMLflowRunID(r.Context(), jobID, *req.MLflowRunID); err != nil {
			slog.Warn("internal: set mlflow run id", "job_id", jobID, "error", err)
		}
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

func (h *internalHandler) handleListPendingDeployments(w http.ResponseWriter, r *http.Request) {
	deps, err := h.deploymentStore.ListPendingDeployments(r.Context())
	if err != nil {
		slog.Error("internal: list pending deployments", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if deps == nil {
		deps = []*deployments.Deployment{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"deployments": deps})
}

func (h *internalHandler) handleUpdateDeploymentStatus(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var req deployments.UpdateStatusRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if req.Status == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "status is required"})
		return
	}

	if err := h.deploymentStore.UpdateDeploymentStatus(r.Context(), id, req.Status, req.ServingEndpoint); err != nil {
		slog.Error("internal: update deployment status", "id", id, "status", req.Status, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
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

- [ ] **Step 2: Update internal_test.go to pass nil deploymentStore**

In `control-plane/internal/api/internal_test.go`, update `setupInternalTest`:

```go
handler := api.NewInternalRouter(store, &events.NoOpPublisher{}, nil)
```

- [ ] **Step 3: Verify internal tests still compile and pass**

```bash
cd control-plane && go test ./internal/api/... -v -run TestInternal 2>&1 | head -30
```

Expected: TestInternal_* PASS (nil deploymentStore is fine since those tests don't hit deployment routes).

---

## Task 8: Router and main.go Wiring

**Files:**
- Modify: `control-plane/internal/api/router.go`
- Modify: `control-plane/cmd/server/main.go`
- Modify: `control-plane/internal/api/jobs_test.go` (update NewRouter call)
- Modify: `control-plane/internal/api/models_test.go` (update NewRouter calls)

- [ ] **Step 1: Update router.go**

Replace the entire file:

```go
// control-plane/internal/api/router.go
package api

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/auth"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/events"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/jobs"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/observability"
)

// NewRouter builds and returns the chi router with all middleware and routes attached.
// modelsSvc and deploymentsSvc may be nil; their routes are only registered when non-nil.
func NewRouter(db *pgxpool.Pool, store jobs.Store, publisher events.Publisher, modelsSvc ModelsService, deploymentsSvc DeploymentsService) http.Handler {
	r := chi.NewRouter()

	r.Get("/healthz", LivenessHandler)
	r.Get("/readyz", ReadinessHandler(db))

	logger := slog.Default()

	jh := &jobsHandler{store: store, publisher: publisher}

	var mh *modelsHandler
	if modelsSvc != nil {
		mh = &modelsHandler{svc: modelsSvc}
	}

	var dh *deploymentsHandler
	if deploymentsSvc != nil {
		dh = &deploymentsHandler{svc: deploymentsSvc}
	}

	r.Group(func(r chi.Router) {
		r.Use(middleware.RequestID)
		r.Use(observability.RequestLogger(logger))
		r.Use(auth.TokenAuth(auth.NewPostgresTokenStore(db)))

		r.Post("/v1/jobs", jh.handleSubmitJob)
		r.Get("/v1/jobs", jh.handleListJobs)
		r.Get("/v1/jobs/{id}", jh.handleGetJob)
		r.Get("/v1/jobs/{id}/runs/{run_id}", jh.handleGetRun)

		if mh != nil {
			r.Post("/v1/models", mh.handleRegister)
			r.Get("/v1/models/{name}", mh.handleGetModel)
			r.Get("/v1/models/{name}/versions/{version}", mh.handleGetModelVersion)
			r.Post("/v1/models/{name}/versions/{version}/promote", mh.handlePromote)
			r.Get("/v1/models/{name}/alias/{alias}", mh.handleResolveAlias)
		}

		if dh != nil {
			r.Post("/v1/deployments", dh.handleCreate)
			r.Get("/v1/deployments/{id}", dh.handleGet)
			r.Delete("/v1/deployments/{id}", dh.handleDelete)
		}
	})

	return r
}
```

- [ ] **Step 2: Fix NewRouter call in jobs_test.go**

In `control-plane/internal/api/jobs_test.go`, find:
```go
handler := api.NewRouter(pool, store, pub, nil)
```
Replace with:
```go
handler := api.NewRouter(pool, store, pub, nil, nil)
```

- [ ] **Step 3: Fix NewRouter calls in models_test.go**

In `control-plane/internal/api/models_test.go`, find all occurrences of:
```go
handler := api.NewRouter(pool, store, pub, svc)
```
and:
```go
handler := api.NewRouter(pool, jobStore, pub, svc)
```
Replace each with the same call plus `, nil`:
```go
handler := api.NewRouter(pool, store, pub, svc, nil)
```
```go
handler := api.NewRouter(pool, jobStore, pub, svc, nil)
```

- [ ] **Step 4: Update main.go**

Replace the internal router call and add deployment wiring. Find and replace these sections:

Current internal router call:
```go
internalHandler := api.NewInternalRouter(store, publisher)
```
Replace with:
```go
deploymentStore := deployments.NewPostgresDeploymentStore(pool)
internalHandler := api.NewInternalRouter(store, publisher, deploymentStore)
```

Current router call:
```go
r := api.NewRouter(pool, store, publisher, modelsSvc)
```
Replace with:
```go
deploymentsSvc := deployments.NewService(deploymentStore, modelStore)
r := api.NewRouter(pool, store, publisher, modelsSvc, deploymentsSvc)
```

Add the `deployments` import to main.go:
```go
"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/deployments"
```

The updated relevant section of main.go (after `modelStore` and before the public server):
```go
modelStore := models.NewPostgresModelStore(pool)
modelsSvc := models.NewService(modelStore, store, mlflowClient)

deploymentStore := deployments.NewPostgresDeploymentStore(pool)

// Internal HTTP server (operator callbacks) on a separate port
internalPort := os.Getenv("SERVER_INTERNAL_PORT")
if internalPort == "" {
    internalPort = "8081"
}
internalHandler := api.NewInternalRouter(store, publisher, deploymentStore)
go func() {
    slog.Info("internal server starting", "port", internalPort)
    if err := http.ListenAndServe(fmt.Sprintf(":%s", internalPort), internalHandler); err != nil {
        slog.Error("internal server stopped", "error", err)
    }
}()

deploymentsSvc := deployments.NewService(deploymentStore, modelStore)

// Public HTTP server
port := os.Getenv("SERVER_PORT")
if port == "" {
    port = "8080"
}
r := api.NewRouter(pool, store, publisher, modelsSvc, deploymentsSvc)
```

- [ ] **Step 5: Build and verify**

```bash
cd control-plane && go build ./...
```

Expected: clean build with no errors.

- [ ] **Step 6: Run all control-plane tests**

```bash
cd control-plane && go test ./... 2>&1 | tail -20
```

Expected: all packages PASS.

---

## Task 9: Operator Deployment Reconciler

**Files:**
- Create: `operator/internal/reconciler/deployment_reconciler.go`
- Create: `operator/internal/reconciler/deployment_reconciler_test.go`

- [ ] **Step 1: Write the failing reconciler tests**

```go
// operator/internal/reconciler/deployment_reconciler_test.go
package reconciler_test

import (
	"testing"
	"time"

	"github.com/Weilei424/kubernetes-native-ai-platform/operator/internal/reconciler"
	corev1 "k8s.io/api/core/v1"
)

func TestMapPodPhase_Running(t *testing.T) {
	got := reconciler.MapPodPhase(corev1.PodRunning)
	if got != "running" {
		t.Fatalf("expected running, got %q", got)
	}
}

func TestMapPodPhase_Failed(t *testing.T) {
	got := reconciler.MapPodPhase(corev1.PodFailed)
	if got != "failed" {
		t.Fatalf("expected failed, got %q", got)
	}
}

func TestMapPodPhase_Pending(t *testing.T) {
	got := reconciler.MapPodPhase(corev1.PodPending)
	if got != "provisioning" {
		t.Fatalf("expected provisioning, got %q", got)
	}
}

func TestMapPodPhase_Unknown(t *testing.T) {
	got := reconciler.MapPodPhase(corev1.PodUnknown)
	if got != "provisioning" {
		t.Fatalf("expected provisioning for unknown, got %q", got)
	}
}

func TestTritonPodName(t *testing.T) {
	name := reconciler.TritonPodName("abc-123")
	if name != "triton-abc-123" {
		t.Fatalf("expected triton-abc-123, got %q", name)
	}
}

func TestTritonServiceName(t *testing.T) {
	name := reconciler.TritonServiceName("abc-123")
	if name != "triton-abc-123" {
		t.Fatalf("expected triton-abc-123, got %q", name)
	}
}

func TestServingEndpoint(t *testing.T) {
	ep := reconciler.ServingEndpoint("abc-123", "default")
	if ep != "triton-abc-123.default.svc.cluster.local:8000" {
		t.Fatalf("unexpected endpoint: %q", ep)
	}
}

func TestDefaultInterval(t *testing.T) {
	if reconciler.DefaultPollInterval != 10*time.Second {
		t.Fatalf("expected 10s, got %v", reconciler.DefaultPollInterval)
	}
}
```

- [ ] **Step 2: Run tests — expect compile failure**

```bash
cd operator && go test ./internal/reconciler/... 2>&1 | head -20
```

Expected: compile error — `reconciler.MapPodPhase`, `reconciler.TritonPodName` undefined.

- [ ] **Step 3: Implement deployment_reconciler.go**

```go
// operator/internal/reconciler/deployment_reconciler.go
package reconciler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// DefaultPollInterval is how often the reconciler polls the control plane.
const DefaultPollInterval = 10 * time.Second

// deploymentRecord mirrors the control plane's Deployment JSON for operator use.
type deploymentRecord struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Namespace       string `json:"namespace"`
	Status          string `json:"status"`
	DesiredReplicas int    `json:"desired_replicas"`
	ArtifactURI     string `json:"artifact_uri"`
	ModelName       string `json:"model_name"`
}

// statusUpdatePayload is sent to PATCH /internal/v1/deployments/:id/status.
type statusUpdatePayload struct {
	Status          string `json:"status"`
	ServingEndpoint string `json:"serving_endpoint,omitempty"`
}

// DeploymentReconciler polls the control plane for pending deployments and
// reconciles the desired serving state in Kubernetes. It implements
// manager.Runnable so it integrates with the controller-runtime manager lifecycle.
type DeploymentReconciler struct {
	Client          client.Client
	ControlPlaneURL string
	HTTPClient      *http.Client
	MinioEndpoint   string
	PollInterval    time.Duration
}

// Start implements manager.Runnable. It is called by the manager after the
// cache is synced and runs until ctx is cancelled.
func (r *DeploymentReconciler) Start(ctx context.Context) error {
	interval := r.PollInterval
	if interval == 0 {
		interval = DefaultPollInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	slog.Info("deployment reconciler started", "interval", interval)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			r.reconcileAll(ctx)
		}
	}
}

func (r *DeploymentReconciler) reconcileAll(ctx context.Context) {
	deps, err := r.listPendingDeployments(ctx)
	if err != nil {
		slog.Error("deployment reconciler: list pending", "error", err)
		return
	}
	for _, dep := range deps {
		if err := r.reconcileOne(ctx, dep); err != nil {
			slog.Error("deployment reconciler: reconcile", "id", dep.ID, "error", err)
		}
	}
}

func (r *DeploymentReconciler) reconcileOne(ctx context.Context, dep *deploymentRecord) error {
	podName := TritonPodName(dep.ID)
	svcName := TritonServiceName(dep.ID)

	// Ensure pod exists.
	pod := &corev1.Pod{}
	podKey := client.ObjectKey{Name: podName, Namespace: dep.Namespace}
	err := r.Client.Get(ctx, podKey, pod)
	if apierrors.IsNotFound(err) {
		if createErr := r.Client.Create(ctx, r.buildPod(dep)); createErr != nil {
			return fmt.Errorf("create pod: %w", createErr)
		}
		pod = nil // just created; skip phase check this tick
	} else if err != nil {
		return fmt.Errorf("get pod: %w", err)
	}

	// Ensure service exists.
	svc := &corev1.Service{}
	svcKey := client.ObjectKey{Name: svcName, Namespace: dep.Namespace}
	if svcErr := r.Client.Get(ctx, svcKey, svc); apierrors.IsNotFound(svcErr) {
		if createErr := r.Client.Create(ctx, r.buildService(dep)); createErr != nil {
			return fmt.Errorf("create service: %w", createErr)
		}
	} else if svcErr != nil {
		return fmt.Errorf("get service: %w", svcErr)
	}

	// Map pod phase → platform status and report if changed.
	if pod == nil {
		// Pod was just created; report provisioning.
		return r.reportStatus(ctx, dep.ID, "provisioning", "")
	}

	newStatus := MapPodPhase(pod.Status.Phase)
	if newStatus == dep.Status {
		return nil // no change
	}

	endpoint := ""
	if newStatus == "running" {
		endpoint = ServingEndpoint(dep.ID, dep.Namespace)
	}
	return r.reportStatus(ctx, dep.ID, newStatus, endpoint)
}

func (r *DeploymentReconciler) buildPod(dep *deploymentRecord) *corev1.Pod {
	minioEndpoint := r.MinioEndpoint
	if minioEndpoint == "" {
		minioEndpoint = "http://minio:9000"
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      TritonPodName(dep.ID),
			Namespace: dep.Namespace,
			Labels: map[string]string{
				"platform.ai/deployment-id": dep.ID,
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			InitContainers: []corev1.Container{
				{
					Name:  "model-loader",
					Image: "localhost:5000/model-loader:latest",
					Env: []corev1.EnvVar{
						{Name: "ARTIFACT_URI", Value: dep.ArtifactURI},
						{Name: "MINIO_ENDPOINT", Value: minioEndpoint},
						{Name: "MODEL_NAME", Value: dep.ModelName},
						{Name: "MODEL_VERSION", Value: "1"},
						{
							Name: "MINIO_ACCESS_KEY",
							ValueFrom: &corev1.EnvVarSource{
								SecretKeyRef: &corev1.SecretKeySelector{
									LocalObjectReference: corev1.LocalObjectReference{Name: "minio-credentials"},
									Key:                  "access-key",
								},
							},
						},
						{
							Name: "MINIO_SECRET_KEY",
							ValueFrom: &corev1.EnvVarSource{
								SecretKeyRef: &corev1.SecretKeySelector{
									LocalObjectReference: corev1.LocalObjectReference{Name: "minio-credentials"},
									Key:                  "secret-key",
								},
							},
						},
					},
					VolumeMounts: []corev1.VolumeMount{
						{Name: "model-repo", MountPath: "/model-repo"},
					},
				},
			},
			Containers: []corev1.Container{
				{
					Name:  "triton",
					Image: "nvcr.io/nvidia/tritonserver:24.01-py3",
					Args: []string{
						"tritonserver",
						"--model-repository=/model-repo",
						"--strict-model-config=false",
					},
					Ports: []corev1.ContainerPort{
						{Name: "http", ContainerPort: 8000},
						{Name: "grpc", ContainerPort: 8001},
					},
					VolumeMounts: []corev1.VolumeMount{
						{Name: "model-repo", MountPath: "/model-repo"},
					},
				},
			},
			Volumes: []corev1.Volume{
				{
					Name:         "model-repo",
					VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
				},
			},
		},
	}
}

func (r *DeploymentReconciler) buildService(dep *deploymentRecord) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      TritonServiceName(dep.ID),
			Namespace: dep.Namespace,
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{
				"platform.ai/deployment-id": dep.ID,
			},
			Ports: []corev1.ServicePort{
				{Name: "http", Port: 8000},
				{Name: "grpc", Port: 8001},
			},
			Type: corev1.ServiceTypeClusterIP,
		},
	}
}

func (r *DeploymentReconciler) listPendingDeployments(ctx context.Context) ([]*deploymentRecord, error) {
	url := r.ControlPlaneURL + "/internal/v1/deployments"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d from internal API", resp.StatusCode)
	}

	var body struct {
		Deployments []*deploymentRecord `json:"deployments"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return body.Deployments, nil
}

func (r *DeploymentReconciler) reportStatus(ctx context.Context, id, status, endpoint string) error {
	payload := statusUpdatePayload{Status: status, ServingEndpoint: endpoint}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	url := fmt.Sprintf("%s/internal/v1/deployments/%s/status", r.ControlPlaneURL, id)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("PATCH %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d from internal API", resp.StatusCode)
	}
	return nil
}

// MapPodPhase maps a Kubernetes PodPhase to a platform deployment status string.
func MapPodPhase(phase corev1.PodPhase) string {
	switch phase {
	case corev1.PodRunning:
		return "running"
	case corev1.PodFailed:
		return "failed"
	default:
		// Pending, Succeeded (shouldn't happen for long-running server), Unknown
		return "provisioning"
	}
}

// TritonPodName returns the Kubernetes Pod name for a deployment ID.
func TritonPodName(deploymentID string) string {
	return "triton-" + deploymentID
}

// TritonServiceName returns the Kubernetes Service name for a deployment ID.
func TritonServiceName(deploymentID string) string {
	return "triton-" + deploymentID
}

// ServingEndpoint returns the in-cluster DNS address for a deployment.
func ServingEndpoint(deploymentID, namespace string) string {
	return fmt.Sprintf("triton-%s.%s.svc.cluster.local:8000", deploymentID, namespace)
}

// ensure errors package is used (for future use in reconcileOne if needed)
var _ = errors.New
```

- [ ] **Step 4: Run reconciler tests — expect PASS**

```bash
cd operator && go test ./internal/reconciler/... -v
```

Expected: all tests PASS including existing MapStatus_* tests and new deployment reconciler tests.

---

## Task 10: Operator main.go Update

**Files:**
- Modify: `operator/cmd/operator/main.go`

Add core scheme registration so the typed k8s client can create `v1.Pod` and `v1.Service`. Register `DeploymentReconciler` with the manager.

- [ ] **Step 1: Update operator main.go**

Replace the entire file:

```go
// operator/cmd/operator/main.go
package main

import (
	"log/slog"
	"net/http"
	"os"
	"time"

	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/Weilei424/kubernetes-native-ai-platform/operator/internal/reconciler"
)

func main() {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		slog.Error("unable to add client-go scheme", "error", err)
		os.Exit(1)
	}

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

	// RayJob reconciler (event-driven via controller-runtime watch).
	rjr := &reconciler.RayJobReconciler{
		Client:          mgr.GetClient(),
		ControlPlaneURL: controlPlaneURL,
		HTTPClient:      &http.Client{Timeout: 10 * time.Second},
	}
	if err := rjr.SetupWithManager(mgr); err != nil {
		slog.Error("unable to set up rayjob reconciler", "error", err)
		os.Exit(1)
	}

	// Deployment reconciler (poll-based goroutine via manager.Runnable).
	dr := &reconciler.DeploymentReconciler{
		Client:          mgr.GetClient(),
		ControlPlaneURL: controlPlaneURL,
		HTTPClient:      &http.Client{Timeout: 10 * time.Second},
		MinioEndpoint:   os.Getenv("MINIO_ENDPOINT"),
		PollInterval:    reconciler.DefaultPollInterval,
	}
	if err := mgr.Add(dr); err != nil {
		slog.Error("unable to add deployment reconciler", "error", err)
		os.Exit(1)
	}

	slog.Info("operator starting", "control_plane_url", controlPlaneURL)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		slog.Error("operator stopped", "error", err)
		os.Exit(1)
	}
}
```

- [ ] **Step 2: Check if k8s.io/client-go is already in go.mod**

```bash
cd operator && grep "k8s.io/client-go" go.mod
```

If not present, add it:

```bash
cd operator && go get k8s.io/client-go@v0.29.0
```

- [ ] **Step 3: Build operator**

```bash
cd operator && go build ./...
```

Expected: clean build.

- [ ] **Step 4: Run operator tests**

```bash
cd operator && go test ./... -v
```

Expected: all tests PASS.

---

## Task 11: Init Container (model-loader)

**Files:**
- Create: `infra/docker/model-loader/requirements.txt`
- Create: `infra/docker/model-loader/loader.py`
- Create: `infra/docker/model-loader/Dockerfile`

- [ ] **Step 1: Write requirements.txt**

```
# infra/docker/model-loader/requirements.txt
boto3==1.34.0
```

- [ ] **Step 2: Write loader.py**

```python
#!/usr/bin/env python3
# infra/docker/model-loader/loader.py
"""
model-loader: fetch a model artifact from MinIO and lay it out for Triton.

Required env vars:
  ARTIFACT_URI      e.g. "mlflow-artifacts:/resnet50/1/model/"
  MINIO_ENDPOINT    e.g. "http://minio:9000"
  MINIO_ACCESS_KEY
  MINIO_SECRET_KEY
  MODEL_NAME        used as the Triton model directory name
  MODEL_VERSION     Triton version directory, typically "1"

Output layout (written to /model-repo):
  /model-repo/<MODEL_NAME>/<MODEL_VERSION>/<files>
  /model-repo/<MODEL_NAME>/config.pbtxt
"""

import os
import sys
import pathlib
import boto3
from botocore.client import Config


def parse_artifact_uri(uri: str):
    """
    Parse an MLflow artifact URI into (bucket, prefix).

    Handles two forms:
      mlflow-artifacts:/bucket/path/to/artifact/
      s3://bucket/path/to/artifact/
    """
    if uri.startswith("mlflow-artifacts:/"):
        # Strip scheme; remainder is /bucket/prefix...
        path = uri[len("mlflow-artifacts:/"):]
    elif uri.startswith("s3://"):
        path = uri[len("s3://"):]
    else:
        raise ValueError(f"Unsupported artifact URI scheme: {uri!r}")

    parts = path.lstrip("/").split("/", 1)
    bucket = parts[0]
    prefix = parts[1] if len(parts) > 1 else ""
    return bucket, prefix


def download_prefix(s3, bucket: str, prefix: str, dest: pathlib.Path):
    """Download all objects under prefix to dest, preserving relative paths."""
    paginator = s3.get_paginator("list_objects_v2")
    found = False
    for page in paginator.paginate(Bucket=bucket, Prefix=prefix):
        for obj in page.get("Contents", []):
            key = obj["Key"]
            rel = key[len(prefix):].lstrip("/")
            if not rel:
                continue
            target = dest / rel
            target.parent.mkdir(parents=True, exist_ok=True)
            print(f"  downloading s3://{bucket}/{key} → {target}")
            s3.download_file(bucket, key, str(target))
            found = True
    if not found:
        print(f"WARNING: no objects found under s3://{bucket}/{prefix}", file=sys.stderr)


def write_config(model_dir: pathlib.Path, model_name: str):
    """Write a minimal Triton config.pbtxt using auto-inference for shapes."""
    config = f'name: "{model_name}"\nplatform: "onnxruntime_onnx"\n'
    config_path = model_dir / "config.pbtxt"
    config_path.write_text(config)
    print(f"  wrote {config_path}")


def main():
    artifact_uri  = os.environ["ARTIFACT_URI"]
    endpoint      = os.environ["MINIO_ENDPOINT"]
    access_key    = os.environ["MINIO_ACCESS_KEY"]
    secret_key    = os.environ["MINIO_SECRET_KEY"]
    model_name    = os.environ["MODEL_NAME"]
    model_version = os.environ.get("MODEL_VERSION", "1")

    print(f"model-loader: fetching {artifact_uri!r}")

    bucket, prefix = parse_artifact_uri(artifact_uri)

    s3 = boto3.client(
        "s3",
        endpoint_url=endpoint,
        aws_access_key_id=access_key,
        aws_secret_access_key=secret_key,
        config=Config(signature_version="s3v4"),
    )

    model_repo = pathlib.Path("/model-repo")
    version_dir = model_repo / model_name / model_version
    version_dir.mkdir(parents=True, exist_ok=True)

    print(f"  bucket={bucket!r} prefix={prefix!r}")
    download_prefix(s3, bucket, prefix, version_dir)

    write_config(model_repo / model_name, model_name)

    print("model-loader: done")


if __name__ == "__main__":
    try:
        main()
    except Exception as e:
        print(f"model-loader: FATAL: {e}", file=sys.stderr)
        sys.exit(1)
```

- [ ] **Step 3: Write Dockerfile**

```dockerfile
# infra/docker/model-loader/Dockerfile
FROM python:3.11-slim

WORKDIR /app

COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY loader.py .

ENTRYPOINT ["python", "loader.py"]
```

- [ ] **Step 4: Verify loader parses URIs correctly (manual sanity check)**

```bash
cd infra/docker/model-loader
python3 -c "
import sys; sys.path.insert(0, '.')
from loader import parse_artifact_uri
assert parse_artifact_uri('mlflow-artifacts:/mybucket/resnet50/1/model/') == ('mybucket', 'resnet50/1/model/')
assert parse_artifact_uri('s3://mybucket/resnet50/1/model/') == ('mybucket', 'resnet50/1/model/')
print('parse_artifact_uri: OK')
"
```

Expected: `parse_artifact_uri: OK`

---

## Task 12: BACKLOG Update

**Files:**
- Modify: `docs/planning/BACKLOG.md`

- [ ] **Step 1: Replace Phase 3 checklist**

Find and replace the Phase 3 checklist section:

```markdown
### Phase 3 Execution Checklist — Serving Plane

- [ ] Migration 011: deployments table (tenant/project/model scoped, unique on tenant_id+name)
- [ ] Migration 012: deployment_revisions table (FK to deployments + model_versions)
- [ ] Domain types (`internal/deployments/model.go`): Deployment, DeploymentRevision, CreateDeploymentRequest, UpdateStatusRequest, sentinel errors
- [ ] State machine (`internal/deployments/statemachine.go`): ValidTransition + unit tests
- [ ] Deployment store (`internal/deployments/store.go`): Store interface + PostgresDeploymentStore (CreateDeployment, GetDeployment, UpdateDeploymentStatus, DeleteDeployment, ListPendingDeployments with JOIN)
- [ ] Deployment store integration tests (`internal/deployments/store_test.go`)
- [ ] Deployment service (`internal/deployments/service.go`): Create (production gate), Get, Delete, UpdateStatus
- [ ] Deployment service unit tests (`internal/deployments/service_test.go`)
- [ ] Public API handlers (`internal/api/deployments.go`): POST /v1/deployments, GET /v1/deployments/:id, DELETE /v1/deployments/:id
- [ ] API handler tests (`internal/api/deployments_test.go`)
- [ ] Extend internal router (`internal/api/internal.go`): GET /internal/v1/deployments, PATCH /internal/v1/deployments/:id/status
- [ ] Router wiring (`internal/api/router.go`): add DeploymentsService param + deployment routes
- [ ] main.go wiring: wire deployment store + service; update NewInternalRouter call
- [ ] Operator deployment reconciler (`operator/internal/reconciler/deployment_reconciler.go`): poll-based goroutine, pod + service creation, status mapping, reportStatus
- [ ] Operator reconciler unit tests (`operator/internal/reconciler/deployment_reconciler_test.go`)
- [ ] Operator main.go: add clientgoscheme + register DeploymentReconciler via mgr.Add
- [ ] Init container (`infra/docker/model-loader/`): loader.py, Dockerfile, requirements.txt
- [ ] Verify: `cd control-plane && go test ./...` passes
- [ ] Verify: `cd operator && go test ./...` passes
- [ ] Verify: `cd control-plane && go build ./...` and `cd operator && go build ./...` clean
```

- [ ] **Step 2: Run final verification**

```bash
cd /mnt/d/kubernetes-native-ai-platform/control-plane && go test ./... && go vet ./...
cd /mnt/d/kubernetes-native-ai-platform/operator && go test ./... && go vet ./...
```

Expected: all packages PASS, no vet warnings.

---

## Commit Messages

After all tasks complete, one message per file in implementation order:

```
migrations/011_create_deployments: add deployments table with state + endpoint columns
migrations/012_create_deployment_revisions: add deployment_revisions table for revision tracking
internal/deployments/model.go: add Deployment, DeploymentRevision domain types and sentinel errors
internal/deployments/statemachine.go: add ValidTransition for deployment status state machine
internal/deployments/statemachine_test.go: unit tests for deployment state machine transitions
internal/deployments/store.go: add Store interface and PostgresDeploymentStore with JOIN-enriched list
internal/deployments/store_test.go: integration tests for deployment store against real PostgreSQL
internal/deployments/service.go: add Service with production gate validation on Create
internal/deployments/service_test.go: unit tests for deployment service validation paths
internal/api/deployments.go: add DeploymentsService interface and POST/GET/DELETE handlers
internal/api/deployments_test.go: handler tests with mock service
internal/api/internal.go: add GET /internal/v1/deployments and PATCH /internal/v1/deployments/:id/status
internal/api/router.go: add DeploymentsService param and register deployment routes
cmd/server/main.go: wire deployment store and service; update internal router
operator/internal/reconciler/deployment_reconciler.go: add poll-based DeploymentReconciler with pod/service management
operator/internal/reconciler/deployment_reconciler_test.go: unit tests for pod phase mapping and name helpers
operator/cmd/operator/main.go: register core scheme and DeploymentReconciler via mgr.Add
infra/docker/model-loader/loader.py: init container that fetches artifact from MinIO and writes Triton layout
infra/docker/model-loader/Dockerfile: Python 3.11 slim image for model-loader
infra/docker/model-loader/requirements.txt: boto3 dependency for model-loader
docs/planning/BACKLOG.md: expand Phase 3 checklist with concrete task items
```
