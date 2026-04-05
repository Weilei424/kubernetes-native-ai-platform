# Phase 1 — Training Control Plane: Design Spec

**Date:** 2026-04-04
**Phase:** 1
**Status:** Approved

---

## Overview

Phase 1 delivers the training job lifecycle: users submit jobs via a structured API, the platform queues and schedules them, creates RayJob CRDs in Kubernetes, and reconciles run status back into platform metadata. Every state transition publishes a Kafka event. Phase 1 also closes the Phase 0 carryover: the token prefix optimization for keyed `api_tokens` lookup.

**Sacred lifecycle entry point:** `train` — this phase owns everything up to a job reaching `SUCCEEDED` or `FAILED`.

---

## Component Overview

| Component | Binary | Responsibility |
|---|---|---|
| Jobs API | `control-plane` | Accept submissions, run admission, persist metadata, expose status |
| Scheduler | `control-plane` (internal package) | Admission validation, quota enforcement, FIFO ordering, placement hints |
| Dispatcher | `control-plane` (goroutine) | Promote `PENDING → QUEUED`, submit RayJob CRD |
| Operator | `operator` (new binary) | Watch RayJob objects, report terminal status back via internal API |

### Data Flow

```
Client
  │  POST /v1/jobs
  ▼
Jobs API ──► Scheduler (admit + quota check)
  │          passes? persist PENDING, return 202
  │
  ▼ (async, dispatcher goroutine)
Dispatcher
  │  FIFO pop → quota check → submit RayJob CRD
  ▼
Kubernetes (RayJob running)
  │
  ▼ (controller-runtime watch)
Operator
  │  PATCH /internal/v1/jobs/:id/status
  ▼
Jobs API ──► state machine transition ──► PostgreSQL + Kafka event
```

**Kafka** is write-only in Phase 1. No component reads from Kafka. Every state transition publishes an event; downstream consumers are introduced in later phases.

---

## Phase 0 Carryover: Token Prefix Optimization

Migration 005 adds `token_prefix TEXT` to `api_tokens` and an index on it. `FindToken` queries `WHERE token_prefix = $1` first, then bcrypt-compares only the matching rows — eliminating the full-table scan.

```sql
ALTER TABLE api_tokens ADD COLUMN token_prefix TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_api_tokens_prefix ON api_tokens (token_prefix);
```

Token prefix is the first 8 characters of the plaintext token (stored at issuance, never the full token). `FindToken` is updated in `internal/auth/postgres_store.go`.

---

## Database Schema

### Migration 006 — Tenant Resource Quota

```sql
ALTER TABLE tenants
  ADD COLUMN cpu_quota    NUMERIC NOT NULL DEFAULT 0,
  ADD COLUMN memory_quota BIGINT  NOT NULL DEFAULT 0;
```

`0` means unlimited. Admission checks sum `(num_workers × worker_cpu) + head_cpu` for all `QUEUED` and `RUNNING` jobs and compares against `cpu_quota`. Same for memory. Values are stored in canonical units: CPU as cores (numeric), memory as bytes (bigint).

### Migration 007 — `training_jobs`

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
```

`rayjob_name` is NULL until the dispatcher successfully submits the CRD. The dispatcher checks `rayjob_name IS NULL` before submitting to ensure idempotency on retry.

### Migration 008 — `training_runs`

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
```

One `training_run` is created atomically with its `training_job` on submission. `mlflow_run_id` is populated in Phase 2. Retry support (multiple runs per job) is deferred to Phase 4.

---

## API Surface

### Public Endpoints (token auth required)

| Method | Path | Description |
|---|---|---|
| `POST` | `/v1/jobs` | Submit a training job |
| `GET` | `/v1/jobs` | List jobs for the authenticated tenant |
| `GET` | `/v1/jobs/:id` | Get job with embedded run status |
| `GET` | `/v1/jobs/:id/runs/:run_id` | Get run detail |

**POST /v1/jobs request body:**

```json
{
  "name": "resnet-run-1",
  "project_id": "<uuid>",
  "runtime": {
    "image": "ghcr.io/org/ray-torch:2.9",
    "command": ["python", "train.py"],
    "args": ["--epochs", "10"],
    "env": {"LEARNING_RATE": "0.001"}
  },
  "resources": {
    "num_workers": 2,
    "worker_cpu": "2",
    "worker_memory": "4Gi",
    "head_cpu": "1",
    "head_memory": "2Gi"
  }
}
```

**Response codes:**
- `202 Accepted` — job accepted, body contains `job_id` and `run_id`
- `422 Unprocessable Entity` — admission failure (invalid spec, unknown project)
- `429 Too Many Requests` — quota exceeded

**GET /v1/jobs/:id** embeds a `run` object with `status`, `started_at`, `finished_at`, `failure_reason`.

### Internal Endpoint (no public auth)

| Method | Path | Description |
|---|---|---|
| `PATCH` | `/internal/v1/jobs/:id/status` | Operator reports terminal RayJob status |

```json
{ "status": "RUNNING" }
{ "status": "SUCCEEDED" }
{ "status": "FAILED", "failure_reason": "OOMKilled on worker-0" }
```

Registered on a separate port (`SERVER_INTERNAL_PORT`, default `8081`). Not reachable outside the cluster in deployed environments.

- `200 OK` — transition accepted
- `409 Conflict` — invalid state transition

---

## State Machine

Both `training_jobs.status` and `training_runs.status` track the same states.

```
┌─────────┐
│ PENDING │  created on submission
└────┬────┘
     │ dispatcher: quota + FIFO
┌────▼────┐
│ QUEUED  │  RayJob CRD submitted
└────┬────┘
     │ operator: RayJob Running
┌────▼────┐
│ RUNNING │
└────┬────┘
     ├──────────────┬──────────────┐
┌────▼────┐   ┌─────▼──┐   ┌──────▼────┐
│SUCCEEDED│   │ FAILED │   │ CANCELLED │
└─────────┘   └────────┘   └───────────┘
```

**Transition ownership:**

| Transition | Owner |
|---|---|
| created → `PENDING` | Jobs API |
| `PENDING` → `QUEUED` | Dispatcher (claimed for CRD submission; `rayjob_name` set on success) |
| `QUEUED` → `RUNNING` | Operator via internal API |
| `RUNNING` → `SUCCEEDED` / `FAILED` | Operator via internal API |
| any → `CANCELLED` | Jobs API (Phase 4) |

Invalid `(from, to)` pairs are rejected by `internal/jobs/statemachine.go` before any DB write. The internal API returns `409` on invalid transitions.

**Kafka events published on every transition:**

| Event topic | Payload fields |
|---|---|
| `platform.job.pending` | `job_id`, `run_id`, `tenant_id`, `status`, `timestamp` |
| `platform.job.queued` | same |
| `platform.job.running` | same |
| `platform.job.succeeded` | same |
| `platform.job.failed` | same + `failure_reason` |

Kafka publish failures are logged and skipped — they do not block state transitions in Phase 1.

---

## Scheduler

Lives in `control-plane/internal/scheduler/`. Three sequential steps:

### Step 1 — Admission (synchronous, in HTTP handler)

Validates at submission time:
- `image` is non-empty and valid format
- `num_workers` ≥ 1
- CPU values parse as valid Kubernetes resource quantities (`"2"`, `"500m"`)
- Memory values parse as valid Kubernetes resource quantities (`"4Gi"`, `"512Mi"`)
- `project_id` exists and belongs to the authenticated tenant

Failures return `422` immediately. Jobs that fail admission are never persisted.

### Step 2 — Quota Enforcement (in dispatcher, before PENDING → QUEUED)

Quota arithmetic happens in Go, not SQL, because `worker_cpu` and `worker_memory` are stored as Kubernetes resource quantity strings (`"2"`, `"500m"`, `"4Gi"`) that SQL cannot natively sum.

**Process:**
1. Query all `QUEUED` and `RUNNING` jobs for the tenant from PostgreSQL
2. Parse each job's `worker_cpu`, `head_cpu`, `worker_memory`, `head_memory` using `k8s.io/apimachinery/pkg/api/resource.MustParse`
3. Sum to get `used_cpu_millicores` and `used_memory_bytes`
4. Parse the incoming job's resource requests the same way
5. Compare: if `used + requested > quota` (and quota > 0), the job stays `PENDING` this cycle

`cpu_quota` in `tenants` is stored as millicores (e.g., `2000` = 2 cores). `memory_quota` is stored as bytes. `0` means unlimited.

### Step 3 — FIFO Ordering + Placement Hints (in dispatcher)

The dispatcher selects the oldest `PENDING` job per tenant (`ORDER BY created_at ASC`) that passes quota. Placement hints are returned as a `map[string]string` by `internal/scheduler/placement.go` and applied as node selectors on both head and worker groups in the RayJob spec.

**Phase 1 placement hint:** `kubernetes.io/arch: amd64` (CPU-only default).

The placement function signature is designed for future GPU extension:
```go
func PlacementHints(job *TrainingJob) map[string]string
```

---

## RayJob CRD Mapping

**File:** `control-plane/internal/jobs/rayjob.go`

**RayJob name:** `rayjob-<first-8-chars-of-job-uuid>` — deterministic, stored on `training_jobs.rayjob_name`.

**Kubernetes namespace:** `platform-jobs` (fixed for Phase 1).

**Labels on every RayJob:**
```yaml
platform.io/job-id: <job-uuid>
platform.io/tenant-id: <tenant-uuid>
```

The operator filters on `platform.io/job-id` to ignore non-platform RayJobs.

**Kubernetes client:** `control-plane/internal/k8s/client.go` uses `client-go` auto-detect — in-cluster service account when `KUBERNETES_SERVICE_HOST` is set, falls back to `~/.kube/config` for local development.

**KubeRay Go module:** `github.com/ray-project/kuberay/ray-operator` for the `RayJob` struct.

---

## Dispatcher Goroutine

**File:** `control-plane/internal/jobs/dispatcher.go`

Started by `main.go` with a `context.Context`. Exits cleanly on context cancellation.

**Tick interval:** 5 seconds (configurable via `DISPATCHER_INTERVAL` env var).

**Per-tick behavior:**
1. Fetch one oldest `PENDING` job per tenant (FIFO)
2. Run quota check — if fails, skip tenant this tick
3. Transition `PENDING → QUEUED`, publish `platform.job.queued`
4. Submit RayJob CRD; on success set `rayjob_name` on the job row
5. If RayJob submission fails: log error, job stays `QUEUED` with `rayjob_name = NULL`; next tick retries CRD submission for any `QUEUED` jobs where `rayjob_name IS NULL`

`QUEUED` means the dispatcher has claimed the job. `rayjob_name` being set confirms the CRD was successfully submitted to Kubernetes. The dispatcher handles both new PENDING jobs and retry of QUEUED jobs awaiting CRD submission in each tick.

One job per tenant per tick prevents any single tenant from starving others.

---

## Operator

**Binary:** `operator/cmd/operator/main.go`

**Framework:** `controller-runtime`

**Reconciler:** `operator/internal/reconciler/rayjob_reconciler.go`

- Watches `RayJob` objects in the `platform-jobs` namespace
- Filters to objects with `platform.io/job-id` label
- Reads `RayJob.Status.JobDeploymentStatus`

**Status mapping:**

| KubeRay `JobDeploymentStatus` | Platform status |
|---|---|
| `Running` | `RUNNING` |
| `Complete` | `SUCCEEDED` |
| `Failed` | `FAILED` |
| `Suspended` / `Retrying` | ignored in Phase 1 |

- Calls `PATCH /internal/v1/jobs/:id/status` with mapped status
- Extracts `failure_reason` from `RayJob.Status.Message` on failure

**Operator configuration via env vars:**
- `CONTROL_PLANE_INTERNAL_URL` — base URL of the control plane internal port
- `WATCH_NAMESPACE` — default `platform-jobs`

---

## Testing Strategy

### Unit Tests (no external dependencies)

| Package | Coverage |
|---|---|
| `internal/scheduler` | Admission rejects invalid specs; quota blocks over-budget jobs; FIFO returns oldest job; placement hints produce correct node selectors |
| `internal/jobs/statemachine` | Valid transitions succeed; invalid `(from, to)` pairs error; terminal → terminal rejected |
| `internal/jobs/rayjob` | Spec maps correctly to RayJob CRD; deterministic name generation; env vars serialized correctly |
| `operator/internal/reconciler` | KubeRay status maps to platform status; non-platform RayJobs ignored; failure reason extracted |

### Integration Tests (real PostgreSQL via testcontainers-go)

| Test | Verified |
|---|---|
| `POST /v1/jobs` happy path | Job and run rows created; status `PENDING`; `202` returned |
| Quota exceeded | Second over-budget job returns `429`; first job unaffected |
| Admission failure | Invalid resource quantity returns `422`; no rows created |
| `PATCH /internal/v1/jobs/:id/status` | Transition persists; `409` on invalid transition |
| Dispatcher cycle | `PENDING → QUEUED`; `rayjob_name` set; fake K8s client used |
| Token prefix lookup | `FindToken` resolves via prefix index |

Kubernetes client replaced with `k8s.io/client-go/kubernetes/fake` for dispatcher integration tests.

No E2E tests in Phase 1. Full lifecycle test is Phase 5.

---

## File Layout

```
control-plane/
  cmd/server/main.go                     (updated: add dispatcher start, internal port)
  internal/
    api/
      router.go                          (updated: register job routes + internal router)
      jobs.go                            (new: HTTP handlers for /v1/jobs)
      internal.go                        (new: HTTP handler for /internal/v1/jobs/:id/status)
    auth/
      postgres_store.go                  (updated: token_prefix keyed lookup)
    jobs/
      statemachine.go                    (new: transition validation)
      dispatcher.go                      (new: async dispatcher goroutine)
      rayjob.go                          (new: RayJob CRD builder)
      store.go                           (new: training_jobs + training_runs DB access)
    scheduler/
      admission.go                       (new: spec validation)
      quota.go                           (new: resource budget enforcement)
      placement.go                       (new: node selector hint generation)
    events/
      kafka.go                           (new: Kafka producer, publish state events)
    k8s/
      client.go                          (new: auto-detect Kubernetes client)
  migrations/
    005_token_prefix.up.sql              (new)
    005_token_prefix.down.sql            (new)
    006_tenant_quota.up.sql              (new)
    006_tenant_quota.down.sql            (new)
    007_create_training_jobs.up.sql      (new)
    007_create_training_jobs.down.sql    (new)
    008_create_training_runs.up.sql      (new)
    008_create_training_runs.down.sql    (new)

operator/
  cmd/operator/main.go                   (new: operator entrypoint)
  internal/
    reconciler/
      rayjob_reconciler.go               (new: controller-runtime reconciler)
```
