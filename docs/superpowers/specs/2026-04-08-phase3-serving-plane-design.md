# Phase 3 — Serving Plane Design

**Date:** 2026-04-08
**Status:** Approved

---

## Overview

Phase 3 adds the serving plane to the kubernetes-native-ai-platform. It completes the `promote → deploy → infer` tail of the sacred lifecycle by:

1. Exposing a Deployment API in the control plane
2. Persisting deployment metadata in PostgreSQL
3. Extending the existing operator binary with a `DeploymentReconciler` that creates Triton pods and ClusterIP Services in Kubernetes
4. Using an init container to fetch ONNX artifacts from MinIO and prepare the Triton model repository layout
5. Reporting serving status and endpoint back to the control plane via an internal callback

---

## Data Model

### Migration 011 — `deployments` table

```sql
CREATE TABLE deployments (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         UUID NOT NULL REFERENCES tenants(id),
    project_id        UUID NOT NULL REFERENCES projects(id),
    model_record_id   UUID NOT NULL REFERENCES model_records(id),
    model_version_id  UUID NOT NULL REFERENCES model_versions(id),
    name              TEXT NOT NULL,
    namespace         TEXT NOT NULL DEFAULT 'default',
    status            TEXT NOT NULL DEFAULT 'pending',
    desired_replicas  INT NOT NULL DEFAULT 1,
    serving_endpoint  TEXT,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, name)
);
```

**Status values:** `pending | provisioning | running | failed | deleted`

**State machine transitions:**
- `pending → provisioning` (operator picks up the deployment)
- `provisioning → running` (Triton pod is Running, Service created)
- `provisioning → failed` (pod failed)
- `running → failed` (pod crashed)
- `any → deleted` (DELETE API called)

### Migration 012 — `deployment_revisions` table

```sql
CREATE TABLE deployment_revisions (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    deployment_id    UUID NOT NULL REFERENCES deployments(id),
    revision_number  INT NOT NULL,
    model_version_id UUID NOT NULL REFERENCES model_versions(id),
    status           TEXT NOT NULL DEFAULT 'active',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (deployment_id, revision_number)
);
```

**Status values:** `active | superseded`

Revisions are the foundation for Phase 4 rollback: each `POST /v1/deployments` or model version swap creates a new revision record.

### Domain types

`internal/deployments/model.go` — mirrors `internal/models/model.go`:

- `Deployment` struct (maps to `deployments` table)
- `DeploymentRevision` struct (maps to `deployment_revisions` table)
- `CreateDeploymentRequest` (POST body)
- `UpdateStatusRequest` (internal PATCH body)
- Sentinel errors: `ErrDeploymentNotFound`, `ErrModelVersionNotProduction`, `ErrDuplicateDeploymentName`

---

## Deployment API

### Public routes (added to `internal/api/router.go`)

```
POST   /v1/deployments       -- create deployment from a production-aliased model version
GET    /v1/deployments/:id   -- get deployment status and serving endpoint
DELETE /v1/deployments/:id   -- mark deployment deleted; operator tears down pod and service
```

### POST /v1/deployments — request body

```json
{
  "model_name":    "resnet50",
  "model_version": 3,
  "name":          "resnet50-prod",
  "namespace":     "default",
  "replicas":      1
}
```

**Validation (service layer):**
- `model_name` and `model_version` must reference an existing `ModelVersion` belonging to the tenant
- The version's status must be `production` — enforces the `promote → deploy` gate
- No active deployment with the same `(tenant_id, name)` may exist

**Response:** full `Deployment` record with `status: "pending"`

### Internal callback route (added to `internal/api/internal.go`)

```
PATCH /internal/v1/deployments/:id/status
```

Body:
```json
{
  "status":           "running",
  "serving_endpoint": "resnet50-prod.default.svc.cluster.local:8000"
}
```

The operator calls this once per reconcile cycle when status changes. The control plane updates `deployments.status` and `deployments.serving_endpoint`.

### Service layer

`internal/deployments/service.go`:
- `Create(ctx, tenantID, req)` — validates, persists deployment + revision 1, returns Deployment
- `Get(ctx, id, tenantID)` — returns Deployment with current status
- `Delete(ctx, id, tenantID)` — transitions status to `deleted`
- `UpdateStatus(ctx, id, status, endpoint)` — called from internal handler

### Store layer

`internal/deployments/store.go` — `Store` interface + `PostgresDeploymentStore`:
- `CreateDeployment(ctx, d)` — inserts deployment + revision atomically in a transaction
- `GetDeployment(ctx, id)` — returns Deployment
- `ListPendingDeployments(ctx)` — returns deployments with status `pending` or `provisioning` (used by operator poll)
- `UpdateDeploymentStatus(ctx, id, status, endpoint)` — updates status + serving_endpoint + updated_at
- `DeleteDeployment(ctx, id)` — sets status to `deleted`

---

## Operator Extension

The existing `operator/` binary gains a second reconciler alongside `RayJobReconciler`.

### DeploymentReconciler (`operator/internal/reconciler/deployment_reconciler.go`)

Poll-based, identical in structure to the existing dispatcher pattern:

```
every 10s:
  1. GET /internal/v1/deployments?status=pending,provisioning
  2. for each deployment:
     a. ensure Triton Pod exists (create if missing)
     b. ensure ClusterIP Service exists (create if missing)
     c. check Pod phase → map to platform status
     d. if status changed or endpoint not yet set:
          PATCH /internal/v1/deployments/:id/status
```

### Triton Pod spec

```yaml
metadata:
  name: triton-<deployment-id>
  labels:
    platform.ai/deployment-id: <deployment-id>
spec:
  initContainers:
    - name: model-loader
      image: <local-registry>/model-loader:latest
      env:
        - name: ARTIFACT_URI       # from deployment record
        - name: MINIO_ENDPOINT
        - name: MINIO_ACCESS_KEY   # from Kubernetes Secret
        - name: MINIO_SECRET_KEY   # from Kubernetes Secret
        - name: MODEL_NAME         # model record name
        - name: MODEL_VERSION      # "1" (Triton version dir)
      volumeMounts:
        - name: model-repo
          mountPath: /model-repo
  containers:
    - name: triton
      image: nvcr.io/nvidia/tritonserver:24.01-py3
      args: ["tritonserver", "--model-repository=/model-repo", "--strict-model-config=false"]
      ports:
        - containerPort: 8000  # HTTP
        - containerPort: 8001  # gRPC
      volumeMounts:
        - name: model-repo
          mountPath: /model-repo
  volumes:
    - name: model-repo
      emptyDir: {}
```

### ClusterIP Service spec

```yaml
metadata:
  name: triton-<deployment-id>
spec:
  selector:
    platform.ai/deployment-id: <deployment-id>
  ports:
    - name: http
      port: 8000
      targetPort: 8000
    - name: grpc
      port: 8001
      targetPort: 8001
  type: ClusterIP
```

**Serving endpoint stored:** `triton-<deployment-id>.default.svc.cluster.local:8000`

### Status mapping

| Pod Phase   | Platform Status |
|-------------|----------------|
| Pending     | provisioning   |
| Running     | running        |
| Failed      | failed         |
| Not found   | pending        |

---

## Init Container (model-loader)

**Location:** `infra/docker/model-loader/`

**Language:** Python (boto3 for MinIO, onnx library for model inspection)

**Behavior:**

1. Parse `ARTIFACT_URI` → extract MinIO bucket and object prefix
2. Download all files under prefix to `/model-repo/<MODEL_NAME>/1/`
3. Write `/model-repo/<MODEL_NAME>/config.pbtxt`:
   ```
   name: "<MODEL_NAME>"
   platform: "onnxruntime_onnx"
   ```
   `--strict-model-config=false` allows Triton to auto-infer input/output tensor shapes from the ONNX file — no shape specification needed in Phase 3.
4. Exit 0 on success, non-zero on failure (pod stays in Init state → maps to `provisioning`)

**Credentials:** MinIO access key and secret key injected via Kubernetes Secret as environment variables. Never hardcoded or committed to git.

**Image build:** included in `make local-up` via a local Docker registry push step.

---

## Wiring (main.go)

```go
// Deployments service
deploymentStore := deployments.NewPostgresDeploymentStore(pool)
deploymentsSvc  := deployments.NewService(deploymentStore, modelStore)

// Router — pass deploymentsSvc alongside modelsSvc
r := api.NewRouter(pool, store, publisher, modelsSvc, deploymentsSvc)
```

Internal router gains the deployment status callback route.

Operator `main.go` starts both reconcilers:

```go
go rayJobReconciler.Run(ctx)
go deploymentReconciler.Run(ctx)
```

---

## Testing

### Unit tests

| File | Covers |
|------|--------|
| `internal/deployments/statemachine_test.go` | Valid and invalid status transitions |
| `internal/deployments/service_test.go` | Validation: version must be `production`, duplicate name rejection, model not found |
| `operator/internal/reconciler/deployment_reconciler_test.go` | Pod spec rendering, status mapping (fake k8s client, mirrors rayjob_reconciler_test.go) |

### Integration tests

| File | Covers |
|------|--------|
| `internal/api/deployments_test.go` | Handler tests with mock service (mirrors models_test.go) |
| `internal/deployments/store_test.go` | Real PostgreSQL via `testutil.NewTestDB()`: CRUD, status update, duplicate name constraint |
| Full-stack handler test | `POST /v1/deployments` → real PG → deployment record persisted; internal PATCH → status + endpoint updated |

### Verification commands

```bash
cd control-plane && go test ./internal/deployments/... ./internal/api/...
cd operator     && go test ./internal/reconciler/...
cd control-plane && go vet ./...
```

### Out of scope for Phase 3

- Live Triton inference (deferred to Phase 5 E2E test)
- Init container image build verification (manual during `make local-up`)
- Real MinIO artifact download (mocked in unit tests; real MinIO in Phase 5 E2E)

---

## Success Criteria

- `POST /v1/deployments` rejects a model version that is not at `production` status
- A valid deployment record transitions `pending → provisioning → running` via operator callbacks
- `GET /v1/deployments/:id` returns current status and `serving_endpoint`
- `DELETE /v1/deployments/:id` sets status to `deleted`
- All unit and integration tests pass
