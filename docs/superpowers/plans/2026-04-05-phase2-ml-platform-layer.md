# Phase 2 — ML Platform Layer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the ML Platform layer — MLflow run linkage on training runs, model registration from succeeded runs, and alias-based model promotion — completing the `train → track → register → promote` lifecycle path.

**Architecture:** The control plane calls MLflow's REST API (v2 alias API) directly from Go for all registry operations. PostgreSQL holds platform metadata (tenant/project scoping, source run traceability). MLflow is authoritative for version numbers and aliases; PostgreSQL is authoritative for platform context. MLflow registered model names are prefixed with `<tenant_id>-` to provide tenant isolation within a shared MLflow instance.

**Tech Stack:** Go, PostgreSQL (pgx v5), MLflow REST API (v2), chi router, testcontainers (integration tests), httptest.NewServer (MLflow unit tests)

---

## File Map

| Action | File |
|---|---|
| Create | `control-plane/migrations/009_create_model_records.up.sql` |
| Create | `control-plane/migrations/009_create_model_records.down.sql` |
| Create | `control-plane/migrations/010_create_model_versions.up.sql` |
| Create | `control-plane/migrations/010_create_model_versions.down.sql` |
| Modify | `control-plane/internal/jobs/model.go` — add `MLflowRunID` to `StatusUpdateRequest` |
| Modify | `control-plane/internal/jobs/store.go` — add `SetMLflowRunID` + `GetRunForRegistration` to interface + impl |
| Modify | `control-plane/internal/api/internal.go` — call `SetMLflowRunID` when field present |
| Modify | `control-plane/internal/api/internal_test.go` — add `TestInternalStatus_SetsMLflowRunID` |
| Create | `control-plane/internal/mlflow/client.go` — MLflow REST client + `Client` interface |
| Create | `control-plane/internal/mlflow/client_test.go` — unit tests with `httptest.NewServer` |
| Create | `control-plane/internal/models/model.go` — domain types, error sentinels |
| Create | `control-plane/internal/models/store.go` — `Store` interface + `PostgresModelStore` |
| Create | `control-plane/internal/models/store_test.go` — integration tests (real PG) |
| Create | `control-plane/internal/models/service.go` — `Service` struct + orchestration logic |
| Create | `control-plane/internal/models/service_test.go` — unit tests with mocks |
| Create | `control-plane/internal/api/models.go` — HTTP handlers for 5 model endpoints |
| Create | `control-plane/internal/api/models_test.go` — integration tests (real PG + mock MLflow) |
| Modify | `control-plane/internal/api/router.go` — register model routes |
| Modify | `control-plane/cmd/server/main.go` — wire MLflow client + models service |

---

## Task 1: Migrations 009 and 010

**Files:**
- Create: `control-plane/migrations/009_create_model_records.up.sql`
- Create: `control-plane/migrations/009_create_model_records.down.sql`
- Create: `control-plane/migrations/010_create_model_versions.up.sql`
- Create: `control-plane/migrations/010_create_model_versions.down.sql`

- [ ] **Step 1: Write migration 009 up**

`control-plane/migrations/009_create_model_records.up.sql`:
```sql
CREATE TABLE model_records (
  id                           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id                    UUID        NOT NULL REFERENCES tenants(id),
  project_id                   UUID        NOT NULL REFERENCES projects(id),
  name                         TEXT        NOT NULL,
  mlflow_registered_model_name TEXT        NOT NULL,
  created_at                   TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at                   TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, name)
);

CREATE INDEX idx_model_records_tenant_name ON model_records (tenant_id, name);
```

- [ ] **Step 2: Write migration 009 down**

`control-plane/migrations/009_create_model_records.down.sql`:
```sql
DROP TABLE IF EXISTS model_records;
```

- [ ] **Step 3: Write migration 010 up**

`control-plane/migrations/010_create_model_versions.up.sql`:
```sql
CREATE TABLE model_versions (
  id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  model_record_id UUID        NOT NULL REFERENCES model_records(id),
  tenant_id       UUID        NOT NULL REFERENCES tenants(id),
  version_number  INT         NOT NULL,
  mlflow_run_id   TEXT        NOT NULL,
  source_run_id   UUID        NOT NULL REFERENCES training_runs(id),
  artifact_uri    TEXT        NOT NULL,
  status          TEXT        NOT NULL DEFAULT 'candidate',
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (model_record_id, version_number)
);

CREATE INDEX idx_model_versions_model_record_id ON model_versions (model_record_id);
CREATE INDEX idx_model_versions_tenant_id ON model_versions (tenant_id);
```

- [ ] **Step 4: Write migration 010 down**

`control-plane/migrations/010_create_model_versions.down.sql`:
```sql
DROP TABLE IF EXISTS model_versions;
```

- [ ] **Step 5: Verify migrations run cleanly**

Run from `control-plane/`:
```bash
go test ./internal/storage/... -v -run TestMigrations 2>&1 || go test ./... -run TestSetupDB -v
```

If no dedicated migration test exists, the migrations are verified implicitly by `testutil.SetupDB` running all migrations in subsequent tasks. At minimum confirm the files are present:
```bash
ls control-plane/migrations/009* control-plane/migrations/010*
```
Expected: 4 files listed.

- [ ] **Step 6: Commit message**

```
migrations/009_create_model_records: add model_records table with tenant/project scoping
migrations/010_create_model_versions: add model_versions table with source run traceability
```

---

## Task 2: Extend Internal Status API with mlflow_run_id

**Files:**
- Modify: `control-plane/internal/jobs/model.go`
- Modify: `control-plane/internal/jobs/store.go`
- Modify: `control-plane/internal/api/internal.go`
- Modify: `control-plane/internal/api/internal_test.go`

- [ ] **Step 1: Write the failing test**

Add to `control-plane/internal/api/internal_test.go`, after the existing tests:

```go
func TestInternalStatus_SetsMLflowRunID(t *testing.T) {
	handler, store, tenantID, projectID := setupInternalTest(t)
	ctx := context.Background()

	job := &jobs.TrainingJob{
		TenantID: tenantID, ProjectID: projectID, Name: "mlflow-job",
		Status: "PENDING", Image: "img:1", Command: []string{"run"},
		Args: []string{}, Env: map[string]string{},
		NumWorkers: 1, WorkerCPU: "1", WorkerMemory: "1Gi",
		HeadCPU: "1", HeadMemory: "1Gi",
	}
	run := &jobs.TrainingRun{TenantID: tenantID, Status: "PENDING"}
	store.CreateJobWithRun(ctx, job, run)
	store.TransitionJobStatus(ctx, job.ID, "PENDING", "QUEUED", nil)
	store.TransitionJobStatus(ctx, job.ID, "QUEUED", "RUNNING", nil)

	mlflowID := "mlflow-abc123"
	body := map[string]interface{}{
		"status":       "SUCCEEDED",
		"mlflow_run_id": mlflowID,
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPatch, "/internal/v1/jobs/"+job.ID+"/status", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	got, err := store.GetRunByJobID(ctx, job.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if got.MLflowRunID == nil || *got.MLflowRunID != mlflowID {
		t.Fatalf("expected mlflow_run_id %q, got %v", mlflowID, got.MLflowRunID)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd control-plane && go test ./internal/api/... -run TestInternalStatus_SetsMLflowRunID -v
```
Expected: FAIL — `StatusUpdateRequest` has no `MLflowRunID` field, and `SetMLflowRunID` does not exist.

- [ ] **Step 3: Add MLflowRunID to StatusUpdateRequest**

In `control-plane/internal/jobs/model.go`, update `StatusUpdateRequest`:
```go
// StatusUpdateRequest is the body for PATCH /internal/v1/jobs/:id/status.
type StatusUpdateRequest struct {
	Status        string  `json:"status"`
	FailureReason *string `json:"failure_reason,omitempty"`
	MLflowRunID   *string `json:"mlflow_run_id,omitempty"`
}
```

- [ ] **Step 4: Add SetMLflowRunID and GetRunForRegistration to Store interface**

In `control-plane/internal/jobs/store.go`, add two methods to the `Store` interface (after `SetRayJobName`):
```go
// SetMLflowRunID records the MLflow run ID on the training_run for the given job.
SetMLflowRunID(ctx context.Context, jobID, mlflowRunID string) error
// GetRunForRegistration returns the project ID, status, and MLflow run ID for a
// training run, used by the model registration workflow.
GetRunForRegistration(ctx context.Context, runID, tenantID string) (projectID, status string, mlflowRunID *string, err error)
```

- [ ] **Step 5: Implement SetMLflowRunID on PostgresJobStore**

Add after `SetRayJobName` in `control-plane/internal/jobs/store.go`:
```go
func (s *PostgresJobStore) SetMLflowRunID(ctx context.Context, jobID, mlflowRunID string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE training_runs SET mlflow_run_id = $1, updated_at = now() WHERE job_id = $2`,
		mlflowRunID, jobID,
	)
	return err
}
```

- [ ] **Step 6: Implement GetRunForRegistration on PostgresJobStore**

Add after `SetMLflowRunID` in `control-plane/internal/jobs/store.go`:
```go
func (s *PostgresJobStore) GetRunForRegistration(ctx context.Context, runID, tenantID string) (projectID, status string, mlflowRunID *string, err error) {
	err = s.db.QueryRow(ctx, `
		SELECT tj.project_id::text, tr.status, tr.mlflow_run_id
		FROM training_runs tr
		JOIN training_jobs tj ON tj.id = tr.job_id
		WHERE tr.id = $1 AND tr.tenant_id = $2`,
		runID, tenantID,
	).Scan(&projectID, &status, &mlflowRunID)
	return
}
```

- [ ] **Step 7: Update internal handler to call SetMLflowRunID**

In `control-plane/internal/api/internal.go`, in `handleUpdateJobStatus`, add after the `TransitionJobStatus` call and before the Kafka publish:
```go
if req.MLflowRunID != nil {
	if err := h.store.SetMLflowRunID(r.Context(), jobID, *req.MLflowRunID); err != nil {
		slog.Warn("internal: set mlflow run id", "job_id", jobID, "error", err)
	}
}
```

- [ ] **Step 8: Run the test to verify it passes**

```bash
cd control-plane && go test ./internal/api/... -run TestInternalStatus_SetsMLflowRunID -v
```
Expected: PASS

- [ ] **Step 9: Run all existing internal tests to confirm no regression**

```bash
cd control-plane && go test ./internal/... -v 2>&1 | tail -20
```
Expected: all tests PASS.

- [ ] **Step 10: Commit messages**

```
internal/jobs/model.go: add MLflowRunID field to StatusUpdateRequest
internal/jobs/store.go: add SetMLflowRunID and GetRunForRegistration to Store interface and PostgresJobStore
internal/api/internal.go: persist mlflow_run_id when present in status update
internal/api/internal_test.go: add TestInternalStatus_SetsMLflowRunID
```

---

## Task 3: MLflow Client Package

**Files:**
- Create: `control-plane/internal/mlflow/client.go`
- Create: `control-plane/internal/mlflow/client_test.go`

- [ ] **Step 1: Write the failing tests**

Create `control-plane/internal/mlflow/client_test.go`:

```go
// control-plane/internal/mlflow/client_test.go
package mlflow_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/mlflow"
)

func TestCreateRegisteredModel_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/2.0/mlflow/registered-models/create" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"registered_model": map[string]string{"name": "tenant1-resnet50"},
		})
	}))
	defer srv.Close()

	c := mlflow.New(srv.URL)
	if err := c.CreateRegisteredModel("tenant1-resnet50"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCreateRegisteredModel_AlreadyExists(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{
			"error_code": "RESOURCE_ALREADY_EXISTS",
			"message":    "Model already exists",
		})
	}))
	defer srv.Close()

	c := mlflow.New(srv.URL)
	// RESOURCE_ALREADY_EXISTS must be silently ignored.
	if err := c.CreateRegisteredModel("tenant1-resnet50"); err != nil {
		t.Fatalf("expected nil for RESOURCE_ALREADY_EXISTS, got: %v", err)
	}
}

func TestCreateModelVersion_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/2.0/mlflow/model-versions/create" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"model_version": map[string]string{
				"name":    "tenant1-resnet50",
				"version": "3",
				"source":  "runs:/abc123/model/",
			},
		})
	}))
	defer srv.Close()

	c := mlflow.New(srv.URL)
	vNum, artifactURI, err := c.CreateModelVersion("tenant1-resnet50", "runs:/abc123/model/", "abc123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vNum != 3 {
		t.Errorf("expected version 3, got %d", vNum)
	}
	if artifactURI != "runs:/abc123/model/" {
		t.Errorf("unexpected artifact URI: %s", artifactURI)
	}
}

func TestSetModelAlias_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/api/2.0/mlflow/registered-models/alias" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"registered_model_alias": map[string]string{"alias": "production", "version": "3"},
		})
	}))
	defer srv.Close()

	c := mlflow.New(srv.URL)
	if err := c.SetModelAlias("tenant1-resnet50", "production", 3); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDeleteModelAlias_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/2.0/mlflow/registered-models/alias" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{})
	}))
	defer srv.Close()

	c := mlflow.New(srv.URL)
	if err := c.DeleteModelAlias("tenant1-resnet50", "production"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGetModelVersionByAlias_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/2.0/mlflow/registered-models/alias" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("name") != "tenant1-resnet50" || r.URL.Query().Get("alias") != "production" {
			t.Errorf("unexpected query params: %s", r.URL.RawQuery)
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"model_version": map[string]string{
				"name":    "tenant1-resnet50",
				"version": "3",
			},
		})
	}))
	defer srv.Close()

	c := mlflow.New(srv.URL)
	vNum, err := c.GetModelVersionByAlias("tenant1-resnet50", "production")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vNum != 3 {
		t.Errorf("expected version 3, got %d", vNum)
	}
}

func TestGetModelVersionByAlias_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{
			"error_code": "RESOURCE_DOES_NOT_EXIST",
			"message":    "alias not found",
		})
	}))
	defer srv.Close()

	c := mlflow.New(srv.URL)
	_, err := c.GetModelVersionByAlias("tenant1-resnet50", "production")
	if err == nil {
		t.Fatal("expected error for not found alias, got nil")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd control-plane && go test ./internal/mlflow/... -v
```
Expected: FAIL — package does not exist yet.

- [ ] **Step 3: Implement the MLflow client**

Create `control-plane/internal/mlflow/client.go`:

```go
// control-plane/internal/mlflow/client.go
package mlflow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Client is the interface for interacting with the MLflow REST API.
type Client interface {
	// CreateRegisteredModel registers a model name. RESOURCE_ALREADY_EXISTS is silently ignored.
	CreateRegisteredModel(name string) error
	// CreateModelVersion creates a new version from the given artifact source.
	// Returns the version number and the artifact URI echoed by MLflow.
	CreateModelVersion(modelName, sourceURI, runID string) (versionNumber int, artifactURI string, err error)
	// SetModelAlias sets an alias on a specific version (MLflow v2 alias API).
	SetModelAlias(modelName, alias string, version int) error
	// DeleteModelAlias removes an alias. Missing aliases are silently ignored.
	DeleteModelAlias(modelName, alias string) error
	// GetModelVersionByAlias resolves an alias to a version number.
	GetModelVersionByAlias(modelName, alias string) (versionNumber int, err error)
}

// HTTPClient implements Client by calling the MLflow REST API.
type HTTPClient struct {
	baseURL    string
	httpClient *http.Client
}

// New creates a new HTTPClient targeting the given MLflow tracking URI.
func New(trackingURI string) *HTTPClient {
	return &HTTPClient{
		baseURL:    strings.TrimRight(trackingURI, "/"),
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

type mlflowError struct {
	ErrorCode string `json:"error_code"`
	Message   string `json:"message"`
}

func (c *HTTPClient) doJSON(method, path string, reqBody, respBody interface{}) error {
	var bodyReader io.Reader
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, c.baseURL+path, bodyReader)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}

	if resp.StatusCode >= 400 {
		var mlErr mlflowError
		_ = json.Unmarshal(raw, &mlErr)
		return &mlflowError{ErrorCode: mlErr.ErrorCode, Message: mlErr.Message}
	}

	if respBody != nil {
		if err := json.Unmarshal(raw, respBody); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

func (e *mlflowError) Error() string {
	return fmt.Sprintf("mlflow %s: %s", e.ErrorCode, e.Message)
}

func isAlreadyExists(err error) bool {
	if e, ok := err.(*mlflowError); ok {
		return e.ErrorCode == "RESOURCE_ALREADY_EXISTS"
	}
	return false
}

func (c *HTTPClient) CreateRegisteredModel(name string) error {
	body := map[string]string{"name": name}
	err := c.doJSON(http.MethodPost, "/api/2.0/mlflow/registered-models/create", body, nil)
	if isAlreadyExists(err) {
		return nil
	}
	return err
}

func (c *HTTPClient) CreateModelVersion(modelName, sourceURI, runID string) (int, string, error) {
	reqBody := map[string]string{
		"name":   modelName,
		"source": sourceURI,
		"run_id": runID,
	}
	var resp struct {
		ModelVersion struct {
			Version string `json:"version"`
			Source  string `json:"source"`
		} `json:"model_version"`
	}
	if err := c.doJSON(http.MethodPost, "/api/2.0/mlflow/model-versions/create", reqBody, &resp); err != nil {
		return 0, "", err
	}
	vNum, err := strconv.Atoi(resp.ModelVersion.Version)
	if err != nil {
		return 0, "", fmt.Errorf("parse version number %q: %w", resp.ModelVersion.Version, err)
	}
	return vNum, resp.ModelVersion.Source, nil
}

func (c *HTTPClient) SetModelAlias(modelName, alias string, version int) error {
	reqBody := map[string]string{
		"name":    modelName,
		"alias":   alias,
		"version": strconv.Itoa(version),
	}
	return c.doJSON(http.MethodPatch, "/api/2.0/mlflow/registered-models/alias", reqBody, nil)
}

func (c *HTTPClient) DeleteModelAlias(modelName, alias string) error {
	reqBody := map[string]string{
		"name":  modelName,
		"alias": alias,
	}
	err := c.doJSON(http.MethodDelete, "/api/2.0/mlflow/registered-models/alias", reqBody, nil)
	if err != nil {
		if e, ok := err.(*mlflowError); ok && (e.ErrorCode == "RESOURCE_DOES_NOT_EXIST" || e.ErrorCode == "NOT_FOUND") {
			return nil
		}
	}
	return err
}

func (c *HTTPClient) GetModelVersionByAlias(modelName, alias string) (int, error) {
	path := fmt.Sprintf("/api/2.0/mlflow/registered-models/alias?name=%s&alias=%s", modelName, alias)
	var resp struct {
		ModelVersion struct {
			Version string `json:"version"`
		} `json:"model_version"`
	}
	if err := c.doJSON(http.MethodGet, path, nil, &resp); err != nil {
		return 0, err
	}
	vNum, err := strconv.Atoi(resp.ModelVersion.Version)
	if err != nil {
		return 0, fmt.Errorf("parse version number %q: %w", resp.ModelVersion.Version, err)
	}
	return vNum, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
cd control-plane && go test ./internal/mlflow/... -v
```
Expected: all 6 tests PASS.

- [ ] **Step 5: Commit messages**

```
internal/mlflow/client.go: MLflow REST client implementing the Client interface
internal/mlflow/client_test.go: unit tests for all client methods using httptest mock server
```

---

## Task 4: Models Domain Types

**Files:**
- Create: `control-plane/internal/models/model.go`

- [ ] **Step 1: Create model.go**

Create `control-plane/internal/models/model.go`:

```go
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
	ErrAliasNotFound   = errors.New("alias not found")
)

// ModelRecord is the platform's representation of a named model.
type ModelRecord struct {
	ID                        string
	TenantID                  string
	ProjectID                 string
	Name                      string
	MLflowRegisteredModelName string
	CreatedAt                 time.Time
	UpdatedAt                 time.Time
}

// ModelVersion is one registered version of a ModelRecord.
type ModelVersion struct {
	ID            string
	ModelRecordID string
	TenantID      string
	VersionNumber int
	MLflowRunID   string
	SourceRunID   string
	ArtifactURI   string
	Status        string
	CreatedAt     time.Time
	UpdatedAt     time.Time
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
```

- [ ] **Step 2: Verify it compiles**

```bash
cd control-plane && go build ./internal/models/...
```
Expected: no errors.

---

## Task 5: Models Store

**Files:**
- Create: `control-plane/internal/models/store.go`
- Create: `control-plane/internal/models/store_test.go`

- [ ] **Step 1: Write the failing store tests**

Create `control-plane/internal/models/store_test.go`:

```go
// control-plane/internal/models/store_test.go
package models_test

import (
	"context"
	"testing"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/jobs"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/models"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/testutil"
)

func setupStoreTest(t *testing.T) (models.Store, string, string, string) {
	t.Helper()
	pool := testutil.SetupDB(t)
	ctx := context.Background()

	var tenantID, projectID string
	pool.QueryRow(ctx, `INSERT INTO tenants (name, cpu_quota, memory_quota) VALUES ('model-tenant', 16000, 32000000000) RETURNING id::text`).Scan(&tenantID)
	pool.QueryRow(ctx, `INSERT INTO projects (tenant_id, name) VALUES ($1, 'model-proj') RETURNING id::text`, tenantID).Scan(&projectID)

	// Create a succeeded training run for source linkage.
	jobStore := jobs.NewPostgresJobStore(pool)
	job := &jobs.TrainingJob{
		TenantID: tenantID, ProjectID: projectID, Name: "src-job",
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

	store := models.NewPostgresModelStore(pool)
	return store, tenantID, projectID, run.ID
}

func TestCreateOrGetModelRecord_Create(t *testing.T) {
	store, tenantID, projectID, _ := setupStoreTest(t)
	ctx := context.Background()

	rec := &models.ModelRecord{
		TenantID:                  tenantID,
		ProjectID:                 projectID,
		Name:                      "resnet50",
		MLflowRegisteredModelName: tenantID + "-resnet50",
	}
	if err := store.CreateOrGetModelRecord(ctx, rec); err != nil {
		t.Fatalf("create model record: %v", err)
	}
	if rec.ID == "" {
		t.Fatal("expected ID to be set after create")
	}
}

func TestCreateOrGetModelRecord_Idempotent(t *testing.T) {
	store, tenantID, projectID, _ := setupStoreTest(t)
	ctx := context.Background()

	rec1 := &models.ModelRecord{
		TenantID: tenantID, ProjectID: projectID,
		Name: "bert", MLflowRegisteredModelName: tenantID + "-bert",
	}
	if err := store.CreateOrGetModelRecord(ctx, rec1); err != nil {
		t.Fatalf("first create: %v", err)
	}

	rec2 := &models.ModelRecord{
		TenantID: tenantID, ProjectID: projectID,
		Name: "bert", MLflowRegisteredModelName: tenantID + "-bert",
	}
	if err := store.CreateOrGetModelRecord(ctx, rec2); err != nil {
		t.Fatalf("second create (should be idempotent): %v", err)
	}
	if rec1.ID != rec2.ID {
		t.Fatalf("expected same ID on duplicate create, got %q vs %q", rec1.ID, rec2.ID)
	}
}

func TestCreateModelVersion(t *testing.T) {
	store, tenantID, projectID, runID := setupStoreTest(t)
	ctx := context.Background()

	rec := &models.ModelRecord{
		TenantID: tenantID, ProjectID: projectID,
		Name: "vit", MLflowRegisteredModelName: tenantID + "-vit",
	}
	store.CreateOrGetModelRecord(ctx, rec)

	ver := &models.ModelVersion{
		ModelRecordID: rec.ID,
		TenantID:      tenantID,
		VersionNumber: 1,
		MLflowRunID:   "mlflow-run-xyz",
		SourceRunID:   runID,
		ArtifactURI:   "runs:/mlflow-run-xyz/model/",
		Status:        "candidate",
	}
	if err := store.CreateModelVersion(ctx, ver); err != nil {
		t.Fatalf("create version: %v", err)
	}
	if ver.ID == "" {
		t.Fatal("expected ID to be set after create")
	}
}

func TestGetModelRecordByName_Found(t *testing.T) {
	store, tenantID, projectID, _ := setupStoreTest(t)
	ctx := context.Background()

	rec := &models.ModelRecord{
		TenantID: tenantID, ProjectID: projectID,
		Name: "gpt2", MLflowRegisteredModelName: tenantID + "-gpt2",
	}
	store.CreateOrGetModelRecord(ctx, rec)

	got, err := store.GetModelRecordByName(ctx, "gpt2", tenantID)
	if err != nil {
		t.Fatalf("get model record: %v", err)
	}
	if got.ID != rec.ID {
		t.Fatalf("expected ID %q, got %q", rec.ID, got.ID)
	}
}

func TestGetModelRecordByName_NotFound(t *testing.T) {
	store, tenantID, _, _ := setupStoreTest(t)
	ctx := context.Background()

	_, err := store.GetModelRecordByName(ctx, "does-not-exist", tenantID)
	if err == nil {
		t.Fatal("expected error for missing model, got nil")
	}
}

func TestListModelVersions(t *testing.T) {
	store, tenantID, projectID, runID := setupStoreTest(t)
	ctx := context.Background()

	rec := &models.ModelRecord{
		TenantID: tenantID, ProjectID: projectID,
		Name: "efficientnet", MLflowRegisteredModelName: tenantID + "-efficientnet",
	}
	store.CreateOrGetModelRecord(ctx, rec)

	for i := 1; i <= 3; i++ {
		v := &models.ModelVersion{
			ModelRecordID: rec.ID, TenantID: tenantID,
			VersionNumber: i, MLflowRunID: "mlflow-run-xyz",
			SourceRunID: runID, ArtifactURI: "runs:/mlflow-run-xyz/model/",
			Status: "candidate",
		}
		store.CreateModelVersion(ctx, v)
	}

	versions, err := store.ListModelVersions(ctx, rec.ID)
	if err != nil {
		t.Fatalf("list versions: %v", err)
	}
	if len(versions) != 3 {
		t.Fatalf("expected 3 versions, got %d", len(versions))
	}
}

func TestGetModelVersionByNumber(t *testing.T) {
	store, tenantID, projectID, runID := setupStoreTest(t)
	ctx := context.Background()

	rec := &models.ModelRecord{
		TenantID: tenantID, ProjectID: projectID,
		Name: "mobilenet", MLflowRegisteredModelName: tenantID + "-mobilenet",
	}
	store.CreateOrGetModelRecord(ctx, rec)

	ver := &models.ModelVersion{
		ModelRecordID: rec.ID, TenantID: tenantID,
		VersionNumber: 1, MLflowRunID: "mlflow-run-xyz",
		SourceRunID: runID, ArtifactURI: "runs:/mlflow-run-xyz/model/",
		Status: "candidate",
	}
	store.CreateModelVersion(ctx, ver)

	got, err := store.GetModelVersionByNumber(ctx, rec.ID, 1)
	if err != nil {
		t.Fatalf("get version: %v", err)
	}
	if got.ID != ver.ID {
		t.Fatalf("expected ID %q, got %q", ver.ID, got.ID)
	}
}

func TestUpdateModelVersionStatus(t *testing.T) {
	store, tenantID, projectID, runID := setupStoreTest(t)
	ctx := context.Background()

	rec := &models.ModelRecord{
		TenantID: tenantID, ProjectID: projectID,
		Name: "squeezenet", MLflowRegisteredModelName: tenantID + "-squeezenet",
	}
	store.CreateOrGetModelRecord(ctx, rec)

	ver := &models.ModelVersion{
		ModelRecordID: rec.ID, TenantID: tenantID,
		VersionNumber: 1, MLflowRunID: "mlflow-run-xyz",
		SourceRunID: runID, ArtifactURI: "runs:/mlflow-run-xyz/model/",
		Status: "candidate",
	}
	store.CreateModelVersion(ctx, ver)

	if err := store.UpdateModelVersionStatus(ctx, ver.ID, "production"); err != nil {
		t.Fatalf("update status: %v", err)
	}

	got, _ := store.GetModelVersionByNumber(ctx, rec.ID, 1)
	if got.Status != "production" {
		t.Fatalf("expected status 'production', got %q", got.Status)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd control-plane && go test ./internal/models/... -v 2>&1 | head -20
```
Expected: FAIL — `models.Store`, `models.NewPostgresModelStore` do not exist.

- [ ] **Step 3: Implement the models store**

Create `control-plane/internal/models/store.go`:

```go
// control-plane/internal/models/store.go
package models

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is the interface for model metadata persistence.
type Store interface {
	// CreateOrGetModelRecord upserts a model_record row. On conflict (tenant_id, name),
	// returns the existing record. rec.ID is always set on return.
	CreateOrGetModelRecord(ctx context.Context, rec *ModelRecord) error
	// CreateModelVersion inserts a new model_versions row. ver.ID is set on return.
	CreateModelVersion(ctx context.Context, ver *ModelVersion) error
	// GetModelRecordByName returns the model record for the given name and tenant.
	GetModelRecordByName(ctx context.Context, name, tenantID string) (*ModelRecord, error)
	// ListModelVersions returns all versions for the given model record, ordered by version_number ASC.
	ListModelVersions(ctx context.Context, modelRecordID string) ([]*ModelVersion, error)
	// GetModelVersionByNumber returns a specific version of a model record.
	GetModelVersionByNumber(ctx context.Context, modelRecordID string, versionNumber int) (*ModelVersion, error)
	// UpdateModelVersionStatus sets the status field on a model version.
	UpdateModelVersionStatus(ctx context.Context, id, status string) error
}

// PostgresModelStore implements Store against PostgreSQL.
type PostgresModelStore struct {
	db *pgxpool.Pool
}

// NewPostgresModelStore returns a Store backed by the given pool.
func NewPostgresModelStore(db *pgxpool.Pool) Store {
	return &PostgresModelStore{db: db}
}

func (s *PostgresModelStore) CreateOrGetModelRecord(ctx context.Context, rec *ModelRecord) error {
	err := s.db.QueryRow(ctx, `
		INSERT INTO model_records (tenant_id, project_id, name, mlflow_registered_model_name)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (tenant_id, name) DO UPDATE
		  SET updated_at = model_records.updated_at
		RETURNING id::text, tenant_id::text, project_id::text, name,
		          mlflow_registered_model_name, created_at, updated_at`,
		rec.TenantID, rec.ProjectID, rec.Name, rec.MLflowRegisteredModelName,
	).Scan(
		&rec.ID, &rec.TenantID, &rec.ProjectID, &rec.Name,
		&rec.MLflowRegisteredModelName, &rec.CreatedAt, &rec.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("upsert model_record: %w", err)
	}
	return nil
}

func (s *PostgresModelStore) CreateModelVersion(ctx context.Context, ver *ModelVersion) error {
	err := s.db.QueryRow(ctx, `
		INSERT INTO model_versions
		  (model_record_id, tenant_id, version_number, mlflow_run_id, source_run_id, artifact_uri, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id::text, model_record_id::text, tenant_id::text, version_number,
		          mlflow_run_id, source_run_id::text, artifact_uri, status, created_at, updated_at`,
		ver.ModelRecordID, ver.TenantID, ver.VersionNumber,
		ver.MLflowRunID, ver.SourceRunID, ver.ArtifactURI, ver.Status,
	).Scan(
		&ver.ID, &ver.ModelRecordID, &ver.TenantID, &ver.VersionNumber,
		&ver.MLflowRunID, &ver.SourceRunID, &ver.ArtifactURI, &ver.Status,
		&ver.CreatedAt, &ver.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert model_version: %w", err)
	}
	return nil
}

func (s *PostgresModelStore) GetModelRecordByName(ctx context.Context, name, tenantID string) (*ModelRecord, error) {
	var rec ModelRecord
	err := s.db.QueryRow(ctx, `
		SELECT id::text, tenant_id::text, project_id::text, name,
		       mlflow_registered_model_name, created_at, updated_at
		FROM model_records
		WHERE name = $1 AND tenant_id = $2`,
		name, tenantID,
	).Scan(
		&rec.ID, &rec.TenantID, &rec.ProjectID, &rec.Name,
		&rec.MLflowRegisteredModelName, &rec.CreatedAt, &rec.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrModelNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get model_record: %w", err)
	}
	return &rec, nil
}

func (s *PostgresModelStore) ListModelVersions(ctx context.Context, modelRecordID string) ([]*ModelVersion, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id::text, model_record_id::text, tenant_id::text, version_number,
		       mlflow_run_id, source_run_id::text, artifact_uri, status, created_at, updated_at
		FROM model_versions
		WHERE model_record_id = $1
		ORDER BY version_number ASC`,
		modelRecordID,
	)
	if err != nil {
		return nil, fmt.Errorf("list model_versions: %w", err)
	}
	defer rows.Close()

	var result []*ModelVersion
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	return result, rows.Err()
}

func (s *PostgresModelStore) GetModelVersionByNumber(ctx context.Context, modelRecordID string, versionNumber int) (*ModelVersion, error) {
	row := s.db.QueryRow(ctx, `
		SELECT id::text, model_record_id::text, tenant_id::text, version_number,
		       mlflow_run_id, source_run_id::text, artifact_uri, status, created_at, updated_at
		FROM model_versions
		WHERE model_record_id = $1 AND version_number = $2`,
		modelRecordID, versionNumber,
	)
	ver, err := scanVersion(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrVersionNotFound
	}
	return ver, err
}

func (s *PostgresModelStore) UpdateModelVersionStatus(ctx context.Context, id, status string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE model_versions SET status = $1, updated_at = now() WHERE id = $2`,
		status, id,
	)
	return err
}

type scannable interface {
	Scan(dest ...any) error
}

func scanVersion(row scannable) (*ModelVersion, error) {
	var v ModelVersion
	err := row.Scan(
		&v.ID, &v.ModelRecordID, &v.TenantID, &v.VersionNumber,
		&v.MLflowRunID, &v.SourceRunID, &v.ArtifactURI, &v.Status,
		&v.CreatedAt, &v.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &v, nil
}
```

- [ ] **Step 4: Run the store tests to verify they pass**

```bash
cd control-plane && go test ./internal/models/... -run TestCreate -v
cd control-plane && go test ./internal/models/... -run TestGet -v
cd control-plane && go test ./internal/models/... -run TestList -v
cd control-plane && go test ./internal/models/... -run TestUpdate -v
```
Expected: all PASS.

- [ ] **Step 5: Commit messages**

```
internal/models/model.go: domain types, error sentinels for model lifecycle
internal/models/store.go: PostgresModelStore implementing the Store interface
internal/models/store_test.go: integration tests for all store methods against real PostgreSQL
```

---

## Task 6: Models Service

**Files:**
- Create: `control-plane/internal/models/service.go`
- Create: `control-plane/internal/models/service_test.go`

- [ ] **Step 1: Write the failing service tests**

Create `control-plane/internal/models/service_test.go`:

```go
// control-plane/internal/models/service_test.go
package models_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/models"
)

// --- Mock MLflow client ---

type mockMLflowClient struct {
	createRegisteredModelErr  error
	createVersionNum          int
	createVersionArtifactURI  string
	createVersionErr          error
	setAliasErr               error
	deleteAliasErr            error
	getVersionByAliasNum      int
	getVersionByAliasErr      error
}

func (m *mockMLflowClient) CreateRegisteredModel(name string) error {
	return m.createRegisteredModelErr
}
func (m *mockMLflowClient) CreateModelVersion(modelName, sourceURI, runID string) (int, string, error) {
	return m.createVersionNum, m.createVersionArtifactURI, m.createVersionErr
}
func (m *mockMLflowClient) SetModelAlias(modelName, alias string, version int) error {
	return m.setAliasErr
}
func (m *mockMLflowClient) DeleteModelAlias(modelName, alias string) error {
	return m.deleteAliasErr
}
func (m *mockMLflowClient) GetModelVersionByAlias(modelName, alias string) (int, error) {
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
	record   *models.ModelRecord
	versions []*models.ModelVersion
	createRecordErr  error
	createVersionErr error
	getRecordErr     error
	getVersionErr    error
	updateStatusErr  error
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

// --- Tests ---

func mlflowRunID(s string) *string { return &s }

func TestService_Register_HappyPath(t *testing.T) {
	mlflowRunIDStr := "mlflow-run-abc"
	reader := &mockRunReader{projectID: "proj-1", status: "SUCCEEDED", mlflowRunID: &mlflowRunIDStr}
	mc := &mockMLflowClient{createVersionNum: 1, createVersionArtifactURI: "runs:/mlflow-run-abc/model/"}
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
	reader := &mockRunReader{err: errors.New("no rows")}
	svc := models.NewService(&mockModelStore{}, reader, &mockMLflowClient{})

	_, err := svc.Register(context.Background(), "tenant-1", models.RegisterRequest{
		RunID: "run-uuid", ModelName: "resnet50",
	})
	if !errors.Is(err, models.ErrRunNotFound) {
		t.Fatalf("expected ErrRunNotFound, got %v", err)
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
	store := &mockModelStore{}
	mc := &mockMLflowClient{getVersionByAliasErr: errors.New("alias not found")}
	svc := models.NewService(store, &mockRunReader{}, mc)

	_, err := svc.ResolveAlias(context.Background(), "resnet50", "no-alias", "tenant-1")
	if !errors.Is(err, models.ErrAliasNotFound) {
		t.Fatalf("expected ErrAliasNotFound, got %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd control-plane && go test ./internal/models/... -run TestService -v 2>&1 | head -20
```
Expected: FAIL — `models.NewService` does not exist.

- [ ] **Step 3: Implement the models service**

Create `control-plane/internal/models/service.go`:

```go
// control-plane/internal/models/service.go
package models

import (
	"context"
	"fmt"

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
	if err != nil {
		return nil, ErrRunNotFound
	}
	if status != "SUCCEEDED" {
		return nil, ErrRunNotSucceeded
	}
	if mlflowRunID == nil {
		return nil, ErrRunNoMLflowID
	}

	source := "runs:/" + *mlflowRunID + "/" + artifactPath
	mlflowModelName := tenantID + "-" + req.ModelName

	if err := s.mlflowClient.CreateRegisteredModel(mlflowModelName); err != nil {
		return nil, fmt.Errorf("create registered model: %w", err)
	}

	versionNum, artifactURI, err := s.mlflowClient.CreateModelVersion(mlflowModelName, source, *mlflowRunID)
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
		return nil, fmt.Errorf("persist model version: %w", err)
	}

	return ver, nil
}

// GetModel returns the model record and all its versions.
func (s *Service) GetModel(ctx context.Context, name, tenantID string) (*ModelRecord, []*ModelVersion, error) {
	rec, err := s.store.GetModelRecordByName(ctx, name, tenantID)
	if err != nil {
		return nil, nil, ErrModelNotFound
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
		return nil, ErrModelNotFound
	}
	ver, err := s.store.GetModelVersionByNumber(ctx, rec.ID, versionNumber)
	if err != nil {
		return nil, ErrVersionNotFound
	}
	return ver, nil
}

// Promote assigns an alias to a model version. "archived" removes all common aliases
// and sets the version to a terminal state.
func (s *Service) Promote(ctx context.Context, name string, versionNumber int, alias, tenantID string) error {
	rec, err := s.store.GetModelRecordByName(ctx, name, tenantID)
	if err != nil {
		return ErrModelNotFound
	}
	ver, err := s.store.GetModelVersionByNumber(ctx, rec.ID, versionNumber)
	if err != nil {
		return ErrVersionNotFound
	}
	if ver.Status == "archived" {
		return ErrVersionArchived
	}

	if alias == "archived" {
		// Best-effort removal of common aliases from MLflow.
		_ = s.mlflowClient.DeleteModelAlias(rec.MLflowRegisteredModelName, "staging")
		_ = s.mlflowClient.DeleteModelAlias(rec.MLflowRegisteredModelName, "production")
	} else {
		if err := s.mlflowClient.SetModelAlias(rec.MLflowRegisteredModelName, alias, versionNumber); err != nil {
			return fmt.Errorf("set mlflow alias: %w", err)
		}
	}

	return s.store.UpdateModelVersionStatus(ctx, ver.ID, alias)
}

// ResolveAlias resolves an MLflow alias to the full version record.
// Returns ErrAliasNotFound if MLflow has no such alias.
// Returns ErrVersionNotFound if MLflow has the alias but PostgreSQL has no matching version
// (should not occur in normal operation — the control plane is the registration gate).
func (s *Service) ResolveAlias(ctx context.Context, name, alias, tenantID string) (*ModelVersion, error) {
	rec, err := s.store.GetModelRecordByName(ctx, name, tenantID)
	if err != nil {
		return nil, ErrModelNotFound
	}
	versionNum, err := s.mlflowClient.GetModelVersionByAlias(rec.MLflowRegisteredModelName, alias)
	if err != nil {
		return nil, ErrAliasNotFound
	}
	ver, err := s.store.GetModelVersionByNumber(ctx, rec.ID, versionNum)
	if err != nil {
		return nil, ErrVersionNotFound
	}
	return ver, nil
}
```

- [ ] **Step 4: Run the service tests to verify they pass**

```bash
cd control-plane && go test ./internal/models/... -run TestService -v
```
Expected: all 8 service tests PASS.

- [ ] **Step 5: Run all model tests**

```bash
cd control-plane && go test ./internal/models/... -v 2>&1 | tail -20
```
Expected: all tests PASS.

- [ ] **Step 6: Commit messages**

```
internal/models/service.go: Service orchestrating run validation, MLflow calls, and PostgreSQL persistence
internal/models/service_test.go: unit tests for all service methods with mock dependencies
```

---

## Task 7: Models API Handlers

**Files:**
- Create: `control-plane/internal/api/models.go`

- [ ] **Step 1: Write the failing handler tests**

Create `control-plane/internal/api/models_test.go`:

```go
// control-plane/internal/api/models_test.go
package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/api"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/events"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/jobs"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/models"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/testutil"
	"golang.org/x/crypto/bcrypt"
)

// mockModelsService satisfies the modelsService interface used by the handler.
type mockModelsService struct {
	registerResult  *models.ModelVersion
	registerErr     error
	getModelRecord  *models.ModelRecord
	getModelVersions []*models.ModelVersion
	getModelErr     error
	getVersionResult *models.ModelVersion
	getVersionErr   error
	promoteErr      error
	resolveResult   *models.ModelVersion
	resolveErr      error
}

func (m *mockModelsService) Register(ctx context.Context, tenantID string, req models.RegisterRequest) (*models.ModelVersion, error) {
	return m.registerResult, m.registerErr
}
func (m *mockModelsService) GetModel(ctx context.Context, name, tenantID string) (*models.ModelRecord, []*models.ModelVersion, error) {
	return m.getModelRecord, m.getModelVersions, m.getModelErr
}
func (m *mockModelsService) GetModelVersion(ctx context.Context, name string, version int, tenantID string) (*models.ModelVersion, error) {
	return m.getVersionResult, m.getVersionErr
}
func (m *mockModelsService) Promote(ctx context.Context, name string, version int, alias, tenantID string) error {
	return m.promoteErr
}
func (m *mockModelsService) ResolveAlias(ctx context.Context, name, alias, tenantID string) (*models.ModelVersion, error) {
	return m.resolveResult, m.resolveErr
}

func setupModelsAPITest(t *testing.T, svc api.ModelsService) (http.Handler, string) {
	t.Helper()
	pool := testutil.SetupDB(t)
	ctx := context.Background()

	var tenantID string
	pool.QueryRow(ctx, `INSERT INTO tenants (name) VALUES ('models-api-tenant') RETURNING id::text`).Scan(&tenantID)

	plaintext := "modelstoken-xxxx"
	prefix := plaintext[:8]
	hash, _ := bcrypt.GenerateFromPassword([]byte(plaintext), bcrypt.MinCost)
	pool.Exec(ctx, `INSERT INTO api_tokens (tenant_id, token_hash, token_prefix) VALUES ($1, $2, $3)`,
		tenantID, string(hash), prefix)

	store := jobs.NewPostgresJobStore(pool)
	pub := &events.NoOpPublisher{}
	handler := api.NewRouter(pool, store, pub, svc)
	return handler, plaintext
}

func TestModelsAPI_Register_HappyPath(t *testing.T) {
	svc := &mockModelsService{
		registerResult: &models.ModelVersion{
			ID: "ver-1", VersionNumber: 1,
			ArtifactURI: "runs:/mlflow-abc/model/", Status: "candidate",
		},
	}
	handler, token := setupModelsAPITest(t, svc)

	body := map[string]interface{}{
		"run_id":     "run-uuid-123",
		"model_name": "resnet50",
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/models", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestModelsAPI_Register_RunNotSucceeded(t *testing.T) {
	svc := &mockModelsService{registerErr: models.ErrRunNotSucceeded}
	handler, token := setupModelsAPITest(t, svc)

	body := map[string]interface{}{"run_id": "run-1", "model_name": "resnet50"}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/models", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestModelsAPI_GetModel_HappyPath(t *testing.T) {
	svc := &mockModelsService{
		getModelRecord:   &models.ModelRecord{ID: "rec-1", Name: "resnet50"},
		getModelVersions: []*models.ModelVersion{{ID: "ver-1", VersionNumber: 1, Status: "candidate"}},
	}
	handler, token := setupModelsAPITest(t, svc)

	req := httptest.NewRequest(http.MethodGet, "/v1/models/resnet50", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestModelsAPI_GetModel_NotFound(t *testing.T) {
	svc := &mockModelsService{getModelErr: models.ErrModelNotFound}
	handler, token := setupModelsAPITest(t, svc)

	req := httptest.NewRequest(http.MethodGet, "/v1/models/no-such-model", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestModelsAPI_Promote_HappyPath(t *testing.T) {
	svc := &mockModelsService{}
	handler, token := setupModelsAPITest(t, svc)

	body := map[string]string{"alias": "production"}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/models/resnet50/versions/1/promote", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestModelsAPI_Promote_ArchivedRejected(t *testing.T) {
	svc := &mockModelsService{promoteErr: models.ErrVersionArchived}
	handler, token := setupModelsAPITest(t, svc)

	body := map[string]string{"alias": "production"}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/models/resnet50/versions/1/promote", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestModelsAPI_ResolveAlias_HappyPath(t *testing.T) {
	svc := &mockModelsService{
		resolveResult: &models.ModelVersion{ID: "ver-1", VersionNumber: 3, Status: "production"},
	}
	handler, token := setupModelsAPITest(t, svc)

	req := httptest.NewRequest(http.MethodGet, "/v1/models/resnet50/alias/production", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestModelsAPI_ResolveAlias_NotFound(t *testing.T) {
	svc := &mockModelsService{resolveErr: models.ErrAliasNotFound}
	handler, token := setupModelsAPITest(t, svc)

	req := httptest.NewRequest(http.MethodGet, "/v1/models/resnet50/alias/staging", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestModelsAPI_GetModelVersion_SourceRunTraceability(t *testing.T) {
	svc := &mockModelsService{
		getVersionResult: &models.ModelVersion{
			ID: "ver-1", VersionNumber: 2, Status: "staging",
			SourceRunID: "source-run-uuid", MLflowRunID: "mlflow-run-id",
		},
	}
	handler, token := setupModelsAPITest(t, svc)

	req := httptest.NewRequest(http.MethodGet, "/v1/models/resnet50/versions/2", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&resp)
	ver := resp["version"].(map[string]interface{})
	if ver["source_run_id"] != "source-run-uuid" {
		t.Errorf("expected source_run_id in response, got: %v", ver["source_run_id"])
	}
}

func TestModelsAPI_Unauthenticated(t *testing.T) {
	svc := &mockModelsService{}
	handler, _ := setupModelsAPITest(t, svc)

	req := httptest.NewRequest(http.MethodGet, "/v1/models/resnet50", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestModelsAPI_CrossTenantRejection(t *testing.T) {
	// Service returns model not found — simulates cross-tenant isolation.
	svc := &mockModelsService{getModelErr: models.ErrModelNotFound}
	handler, token := setupModelsAPITest(t, svc)

	req := httptest.NewRequest(http.MethodGet, "/v1/models/other-tenant-model", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for cross-tenant access, got %d", rec.Code)
	}
}

// Ensure errors package is used (suppress unused import).
var _ = errors.New
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd control-plane && go test ./internal/api/... -run TestModelsAPI -v 2>&1 | head -20
```
Expected: FAIL — `api.ModelsService` interface and model routes do not exist.

- [ ] **Step 3: Implement the models handlers**

Create `control-plane/internal/api/models.go`:

```go
// control-plane/internal/api/models.go
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/auth"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/models"
)

// ModelsService is the interface the handler depends on (exported for test use).
type ModelsService interface {
	Register(ctx context.Context, tenantID string, req models.RegisterRequest) (*models.ModelVersion, error)
	GetModel(ctx context.Context, name, tenantID string) (*models.ModelRecord, []*models.ModelVersion, error)
	GetModelVersion(ctx context.Context, name string, version int, tenantID string) (*models.ModelVersion, error)
	Promote(ctx context.Context, name string, version int, alias, tenantID string) error
	ResolveAlias(ctx context.Context, name, alias, tenantID string) (*models.ModelVersion, error)
}

type modelsHandler struct {
	svc ModelsService
}

func (h *modelsHandler) handleRegister(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDFromContext(r.Context())

	var req models.RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if req.RunID == "" || req.ModelName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "run_id and model_name are required"})
		return
	}

	ver, err := h.svc.Register(r.Context(), tenantID, req)
	if err != nil {
		switch {
		case errors.Is(err, models.ErrRunNotFound):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		case errors.Is(err, models.ErrRunNotSucceeded), errors.Is(err, models.ErrRunNoMLflowID):
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		default:
			slog.Error("register model", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		}
		return
	}
	writeJSON(w, http.StatusCreated, map[string]interface{}{"version": ver})
}

func (h *modelsHandler) handleGetModel(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDFromContext(r.Context())
	name := chi.URLParam(r, "name")

	rec, versions, err := h.svc.GetModel(r.Context(), name, tenantID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "model not found"})
		return
	}
	if versions == nil {
		versions = []*models.ModelVersion{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"model": rec, "versions": versions})
}

func (h *modelsHandler) handleGetModelVersion(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDFromContext(r.Context())
	name := chi.URLParam(r, "name")
	versionStr := chi.URLParam(r, "version")

	versionNum, err := strconv.Atoi(versionStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "version must be an integer"})
		return
	}

	ver, err := h.svc.GetModelVersion(r.Context(), name, versionNum, tenantID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "version not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"version": ver})
}

func (h *modelsHandler) handlePromote(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDFromContext(r.Context())
	name := chi.URLParam(r, "name")
	versionStr := chi.URLParam(r, "version")

	versionNum, err := strconv.Atoi(versionStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "version must be an integer"})
		return
	}

	var req models.PromoteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if req.Alias == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "alias is required"})
		return
	}

	err = h.svc.Promote(r.Context(), name, versionNum, req.Alias, tenantID)
	if err != nil {
		switch {
		case errors.Is(err, models.ErrModelNotFound), errors.Is(err, models.ErrVersionNotFound):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		case errors.Is(err, models.ErrVersionArchived):
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		default:
			slog.Error("promote model version", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *modelsHandler) handleResolveAlias(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDFromContext(r.Context())
	name := chi.URLParam(r, "name")
	alias := chi.URLParam(r, "alias")

	ver, err := h.svc.ResolveAlias(r.Context(), name, alias, tenantID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "alias not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"version": ver})
}
```

- [ ] **Step 4: Update router.go to accept and wire the models service**

In `control-plane/internal/api/router.go`, change the function signature and add model routes:

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
func NewRouter(db *pgxpool.Pool, store jobs.Store, publisher events.Publisher, modelsSvc ModelsService) http.Handler {
	r := chi.NewRouter()

	// Public routes — no auth required
	r.Get("/healthz", LivenessHandler)
	r.Get("/readyz", ReadinessHandler(db))

	logger := slog.Default()

	jh := &jobsHandler{store: store, publisher: publisher}
	var mh *modelsHandler
	if modelsSvc != nil {
		mh = &modelsHandler{svc: modelsSvc}
	}

	// Protected routes — auth + logging middleware
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
	})

	return r
}
```

- [ ] **Step 5: Run the handler tests to verify they pass**

```bash
cd control-plane && go test ./internal/api/... -run TestModelsAPI -v
```
Expected: all handler tests PASS.

- [ ] **Step 6: Add full-stack integration tests to models_test.go**

Append the following to `control-plane/internal/api/models_test.go`. These tests use the **real service + real DB + mock MLflow client**, verifying the full handler → service → store → PostgreSQL path:

```go
// --- Full-stack integration test helpers ---

type mockMLflowForIntegration struct {
	versionNum int
}

func (m *mockMLflowForIntegration) CreateRegisteredModel(name string) error { return nil }
func (m *mockMLflowForIntegration) CreateModelVersion(modelName, sourceURI, runID string) (int, string, error) {
	m.versionNum++
	return m.versionNum, sourceURI, nil
}
func (m *mockMLflowForIntegration) SetModelAlias(modelName, alias string, version int) error { return nil }
func (m *mockMLflowForIntegration) DeleteModelAlias(modelName, alias string) error           { return nil }
func (m *mockMLflowForIntegration) GetModelVersionByAlias(modelName, alias string) (int, error) {
	return m.versionNum, nil
}

func setupModelsIntegrationTest(t *testing.T) (http.Handler, string, string, string) {
	t.Helper()
	pool := testutil.SetupDB(t)
	ctx := context.Background()

	var tenantID, projectID string
	pool.QueryRow(ctx, `INSERT INTO tenants (name) VALUES ('int-models-tenant') RETURNING id::text`).Scan(&tenantID)
	pool.QueryRow(ctx, `INSERT INTO projects (tenant_id, name) VALUES ($1, 'int-models-proj') RETURNING id::text`, tenantID).Scan(&projectID)

	plaintext := "int-models-tok"
	prefix := plaintext[:8]
	hash, _ := bcrypt.GenerateFromPassword([]byte(plaintext), bcrypt.MinCost)
	pool.Exec(ctx, `INSERT INTO api_tokens (tenant_id, token_hash, token_prefix) VALUES ($1, $2, $3)`, tenantID, string(hash), prefix)

	// Create a succeeded training run with mlflow_run_id set.
	jobStore := jobs.NewPostgresJobStore(pool)
	job := &jobs.TrainingJob{
		TenantID: tenantID, ProjectID: projectID, Name: "int-job",
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
	jobStore.SetMLflowRunID(ctx, job.ID, "mlflow-int-run")

	mc := &mockMLflowForIntegration{}
	modelStore := models.NewPostgresModelStore(pool)
	svc := models.NewService(modelStore, jobStore, mc)

	pub := &events.NoOpPublisher{}
	handler := api.NewRouter(pool, jobStore, pub, svc)
	return handler, plaintext, run.ID, tenantID
}

func TestModelsIntegration_RegisterThenGet(t *testing.T) {
	handler, token, runID, _ := setupModelsIntegrationTest(t)

	// Register model
	body := map[string]interface{}{"run_id": runID, "model_name": "resnet50-int"}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/models", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("register: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// GET model
	req2 := httptest.NewRequest(http.MethodGet, "/v1/models/resnet50-int", nil)
	req2.Header.Set("Authorization", "Bearer "+token)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("get model: expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}
	var resp map[string]interface{}
	json.NewDecoder(rec2.Body).Decode(&resp)
	versions := resp["versions"].([]interface{})
	if len(versions) != 1 {
		t.Fatalf("expected 1 version, got %d", len(versions))
	}
}

func TestModelsIntegration_RegisterThenPromoteThenResolve(t *testing.T) {
	handler, token, runID, _ := setupModelsIntegrationTest(t)

	// Register
	body := map[string]interface{}{"run_id": runID, "model_name": "resnet50-promote"}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/models", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("register: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// Promote to production
	promoteBody := map[string]string{"alias": "production"}
	pb, _ := json.Marshal(promoteBody)
	req2 := httptest.NewRequest(http.MethodPost, "/v1/models/resnet50-promote/versions/1/promote", bytes.NewReader(pb))
	req2.Header.Set("Authorization", "Bearer "+token)
	req2.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("promote: expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}

	// Resolve alias
	req3 := httptest.NewRequest(http.MethodGet, "/v1/models/resnet50-promote/alias/production", nil)
	req3.Header.Set("Authorization", "Bearer "+token)
	rec3 := httptest.NewRecorder()
	handler.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Fatalf("resolve alias: expected 200, got %d: %s", rec3.Code, rec3.Body.String())
	}
}

func TestModelsIntegration_SourceRunTraceability(t *testing.T) {
	handler, token, runID, _ := setupModelsIntegrationTest(t)

	// Register
	body := map[string]interface{}{"run_id": runID, "model_name": "resnet50-trace"}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/models", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("register: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// GET version — verify source_run_id traces back to the training run
	req2 := httptest.NewRequest(http.MethodGet, "/v1/models/resnet50-trace/versions/1", nil)
	req2.Header.Set("Authorization", "Bearer "+token)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("get version: expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}
	var resp map[string]interface{}
	json.NewDecoder(rec2.Body).Decode(&resp)
	ver := resp["version"].(map[string]interface{})
	if ver["source_run_id"] != runID {
		t.Errorf("expected source_run_id=%q, got %v", runID, ver["source_run_id"])
	}
}
```

Also add `"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/models"` to the imports in `models_test.go` if not already present (it is already imported via mockModelsService).

- [ ] **Step 7: Run all API tests to confirm no regression**

```bash
cd control-plane && go test ./internal/api/... -v 2>&1 | tail -30
```
Expected: all tests PASS, including the 3 new `TestModelsIntegration_*` tests.

- [ ] **Step 8: Commit messages**

```
internal/api/models.go: HTTP handlers for model registration, promotion, and alias resolution
internal/api/models_test.go: handler tests and full-stack integration tests covering all endpoints
internal/api/router.go: register model routes and accept ModelsService dependency
```

---

## Task 8: Wire MLflow Client and Models Service in main.go

**Files:**
- Modify: `control-plane/cmd/server/main.go`

- [ ] **Step 1: Update main.go**

Replace the `NewRouter` call and add MLflow + models wiring in `control-plane/cmd/server/main.go`.

Add the following imports (add to the existing import block):
```go
"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/mlflow"
"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/models"
```

Add the following block before `r := api.NewRouter(...)`:
```go
// MLflow client and models service
mlflowTrackingURI := os.Getenv("MLFLOW_TRACKING_URI")
if mlflowTrackingURI == "" {
    mlflowTrackingURI = "http://localhost:5000"
    slog.Warn("MLFLOW_TRACKING_URI not set, using default", "uri", mlflowTrackingURI)
}
mlflowClient := mlflow.New(mlflowTrackingURI)
modelStore := models.NewPostgresModelStore(pool)
modelsSvc := models.NewService(modelStore, store, mlflowClient)
```

Replace the last `NewRouter` call:
```go
r := api.NewRouter(pool, store, publisher, modelsSvc)
```

- [ ] **Step 2: Verify the binary compiles**

```bash
cd control-plane && go build ./cmd/server/...
```
Expected: no errors.

- [ ] **Step 3: Run the full test suite**

```bash
cd control-plane && go test ./... 2>&1 | tail -30
```
Expected: all tests PASS.

- [ ] **Step 4: Commit message**

```
cmd/server/main.go: wire MLflow client and models service into the HTTP router
```

---

## Self-Review Checklist

After all tasks are complete, verify:

- [ ] `go test ./...` passes with no failures
- [ ] `go build ./...` and `go build ./cmd/server/...` produce no errors  
- [ ] `go vet ./...` produces no warnings
- [ ] Migration files 009 and 010 exist and follow the existing naming convention
- [ ] `model_versions.source_run_id` traces back to `training_runs.id` (FK in migration)
- [ ] `SetMLflowRunID` is called in the internal handler only when `req.MLflowRunID != nil`
- [ ] `archived` status is terminal in `Promote` (verified by `TestService_Promote_ArchivedRejected`)
- [ ] `candidate → production` skip is valid (verified by `TestService_Promote_SkipToProduction`)
- [ ] MLflow model name is prefixed with `tenantID + "-"` in `Service.Register`
- [ ] `ModelsService` interface is exported (capital M) so test file can reference it
- [ ] No model routes are registered when `modelsSvc == nil` (router nil-guard)
- [ ] Mark all Phase 2 items as `[x]` in `docs/planning/BACKLOG.md`
