# Phase 2 — ML Platform Layer Design

**Date:** 2026-04-05  
**Phase:** 2  
**Status:** Approved

---

## Overview

Phase 2 adds the ML Platform layer: linking training runs to MLflow experiment runs, enabling model registration from succeeded runs, and implementing a promotion workflow with alias-based model resolution.

The sacred lifecycle path extended in this phase: **train → track → register → promote**

---

## Key Decisions

### MLflow is authoritative for the model registry
MLflow's Model Registry (v2 alias API) is the source of truth for version numbers, artifact URIs, and alias state. PostgreSQL holds platform-specific metadata: tenant/project scoping, source run linkage, and cross-entity join performance. There is no dual-write registry — MLflow owns the registry, PostgreSQL owns the platform context.

### MLflow aliases (v2 API), not deprecated stage transitions
MLflow deprecated stage-based transitions (`None → Staging → Production → Archived`) in MLflow 2.x in favor of explicit aliases. This platform uses aliases exclusively. The mapping:

| Platform state | MLflow representation |
|---|---|
| `candidate` | Version registered, no alias set |
| `staging` | Alias `staging` set on the version |
| `production` | Alias `production` set on the version |
| `archived` | All aliases removed (terminal — cannot be re-promoted) |

### Flexible promotion transitions
Any forward alias assignment is valid. `candidate → production` is allowed. `archived` is the only terminal state — no promotion out of archived. This matches how MLflow aliases work and avoids unnecessary platform-side ordering constraints.

### MLflow run ID is set via the internal status API
The training container calls `PATCH /internal/v1/jobs/:id/status` (existing Phase 1 endpoint) with an optional `mlflow_run_id` field. The control plane writes it to `training_runs.mlflow_run_id` on receipt. This is the industrial standard: the training process knows its own run ID and reports it back through the existing callback mechanism.

### Registration is run-centric
`POST /v1/models` requires only a platform `run_id`. The control plane resolves the MLflow run ID, artifact URI, and registers the version automatically. An optional `artifact_path` parameter (default: `"model/"`) covers the future multi-artifact case without breaking the simple path.

### MLflow client is a Go HTTP wrapper — no sidecar
The control plane calls MLflow's REST API directly from Go. No Python sidecar. The client is injected via interface for testability.

---

## Data Layer

### Migration 009 — `model_records`

One row per named model, scoped to a tenant and project.

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
```

### Migration 010 — `model_versions`

One row per registered version.

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
```

### Existing table changes (no new migration required)

`training_runs.mlflow_run_id` already exists as a nullable `TEXT` column (migration 008). The internal status API is extended to accept and persist this field when present.

---

## MLflow Client

Package: `internal/mlflow`

Interface and implementation in `client.go`. All methods call MLflow's REST API at `MLFLOW_TRACKING_URI`.

```go
type Client interface {
    CreateRegisteredModel(name string) error
    CreateModelVersion(modelName, sourceURI, runID string) (versionNumber int, artifactURI string, err error)
    SetModelAlias(modelName, alias string, version int) error
    DeleteModelAlias(modelName, alias string) error
    GetModelVersionByAlias(modelName, alias string) (versionNumber int, err error)
}
```

Configuration: `MLFLOW_TRACKING_URI` environment variable. No auth required for local dev.

Unit tests use `httptest.NewServer` to simulate MLflow REST responses — no live MLflow required.

---

## API Endpoints

All endpoints require token auth (existing middleware). Scoped to the caller's tenant.

| Method | Path | Description |
|---|---|---|
| `POST` | `/v1/models` | Register a model version from a succeeded run |
| `GET` | `/v1/models/:name` | Get model record + all versions |
| `GET` | `/v1/models/:name/versions/:version` | Get a single version with source run traceability |
| `POST` | `/v1/models/:name/versions/:version/promote` | Promote a version to a new alias |
| `GET` | `/v1/models/:name/alias/:alias` | Resolve alias → version |

### POST /v1/models

Request:
```json
{
  "run_id": "<platform run UUID>",
  "model_name": "resnet50",
  "artifact_path": "model/"
}
```
`artifact_path` defaults to `"model/"` if omitted.

Validation:
1. Run exists and belongs to caller's tenant
2. Run status is `SUCCEEDED`
3. Run has a non-null `mlflow_run_id`

On success: creates or reuses the MLflow registered model, creates a new MLflow model version, persists `model_records` and `model_versions` rows.

### POST /v1/models/:name/versions/:version/promote

Request:
```json
{ "alias": "production" }
```

Validation:
- Version exists and belongs to caller's tenant
- Version status is not `archived`

On success: calls `SetModelAlias` on MLflow, updates `model_versions.status` in PostgreSQL.

### GET /v1/models/:name/alias/:alias

Calls `GetModelVersionByAlias` on MLflow to get the version number, then returns the full version record from PostgreSQL including `source_run_id` for traceability. If MLflow returns a version that has no corresponding row in `model_versions`, the handler returns 404 — the control plane is the registration gate and this state should never occur in normal operation.

---

## Code Layout

### New files

```
control-plane/
  internal/
    mlflow/
      client.go              # MLflow REST client + Client interface
      client_test.go         # unit tests with httptest mock server
    models/
      model.go               # ModelRecord, ModelVersion, request/response types
      store.go               # PostgreSQL reads/writes
      store_test.go          # integration tests against real DB
      service.go             # orchestration: validate run → MLflow → persist
      service_test.go        # unit tests with mocked store + MLflow client
  api/
    models.go                # HTTP handlers for all 5 endpoints
    models_test.go           # integration tests: full request → DB flow
migrations/
  009_create_model_records.up.sql
  009_create_model_records.down.sql
  010_create_model_versions.up.sql
  010_create_model_versions.down.sql
```

### Existing files modified

| File | Change |
|---|---|
| `internal/api/router.go` | Register model routes |
| `internal/api/internal.go` | Extend `StatusUpdateRequest` with optional `mlflow_run_id` |
| `internal/jobs/store.go` | Add `SetMLflowRunID(runID, mlflowRunID string) error` |

### Why `service.go`

Phase 1 put logic directly in handlers. Registration spans three systems (PostgreSQL run lookup → MLflow API → PostgreSQL model write), so a service layer is warranted here to keep handlers thin and the orchestration logic independently testable.

---

## Testing Strategy

### Unit tests

**`internal/mlflow/client_test.go`**
- Each client method tested against `httptest.NewServer` simulating MLflow REST responses
- Covers: success paths, MLflow error responses, network errors

**`internal/models/service_test.go`**
- Register: run not found, run not succeeded, run has no `mlflow_run_id`, happy path
- Promote: version not found, version is archived (rejected), valid transitions including `candidate → production` skip
- Alias resolution: alias not found, happy path

### Integration tests

**`internal/models/store_test.go`**
- Hits real PostgreSQL (same pattern as Phase 1)
- CRUD for `model_records` and `model_versions`

**`internal/api/models_test.go`**
- MLflow client stubbed via interface — no live MLflow required
- `POST /v1/models` → DB persistence → `GET /v1/models/:name` round-trip
- Promote → alias update → `GET /v1/models/:name/alias/:alias` round-trip
- Cross-tenant rejection (model from another tenant not accessible)

### Out of scope for Phase 2

- Kafka events for model state transitions (Phase 4)
- Operator or reconciler changes
- CLI commands for model operations (Phase 5)

---

## Success Criteria

- A model can be registered from a `SUCCEEDED` training run with a set `mlflow_run_id`
- A model version can be promoted directly to `production` alias (flexible transitions)
- Alias resolution returns the correct version
- A model version traces back to its originating `training_run` and `training_job`
- `archived` versions are rejected from further promotion
- All integration tests pass against real PostgreSQL with stubbed MLflow client
