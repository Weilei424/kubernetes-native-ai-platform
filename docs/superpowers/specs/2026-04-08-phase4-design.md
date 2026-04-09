# Phase 4 Design — Observability and Reliability

**Date:** 2026-04-08
**Phase:** 4 of 5
**Sequence:** Observability-first (Option A)

---

## Overview

Phase 4 adds production-grade observability, retry logic, failure handling, and event/log visibility to the platform. It builds directly on the control plane, operator, and serving infrastructure from phases 0–3.

Implementation order:
1. Structured logging + Prometheus metrics
2. Grafana dashboards + local stack wiring
3. Job retry logic
4. Deployment revisions + rollback
5. Events API + Quota API
6. Operator hardening (local + prod configs)
7. Failure test suite

---

## 1. Structured Logging

**Approach:** Extend `log/slog` — no new logging library.

- `observability.FromContext(ctx) *slog.Logger` — returns a logger with `request_id` baked in from the chi request ID middleware.
- All handlers attach entity-specific fields (`job_id`, `run_id`, `deployment_id`, `model_id`) to log lines when those IDs are known.
- The existing `RequestLogger` middleware already captures `request_id`; it is extended to inject the logger into context so downstream code can call `FromContext`.
- Operator logs include `deployment_id` and `job_id` on all reconciler log lines.

---

## 2. Prometheus Metrics

**Dependency:** `prometheus/client_golang` added to control plane and operator modules.

**File:** `control-plane/internal/observability/metrics.go`

Metrics registered at init:

| Metric | Type | Labels |
|---|---|---|
| `http_request_duration_seconds` | Histogram | method, route, status_code |
| `job_queue_depth` | Gauge | — |
| `job_admission_failures_total` | Counter | reason |
| `training_run_duration_seconds` | Histogram | — |
| `training_run_retry_total` | Counter | — |
| `deployment_count` | Gauge | status |
| `operator_reconcile_duration_seconds` | Histogram | reconciler |
| `operator_reconcile_errors_total` | Counter | reconciler |

**Wiring:**
- A Prometheus middleware wraps the public API router and records `http_request_duration_seconds`.
- The dispatcher increments/decrements `job_queue_depth` each tick.
- The scheduler admission check increments `job_admission_failures_total` on rejection.
- `GET /metrics` is added to the public API server (not the internal router).
- The operator exposes its own `/metrics` endpoint on a configurable port (default 8081).

---

## 3. Grafana Dashboards + Local Stack Wiring

**Dashboard files:** `observability/dashboards/` — four pre-defined JSON files, provisioned automatically.

| File | Content |
|---|---|
| `control-plane.json` | API request rate, latency p50/p95/p99 by route, error rate, active deployments |
| `training.json` | Queue depth, admission failures, run duration histogram, success/failure/retry rate |
| `triton-serving.json` | Triton request rate, p50/p95/p99 latency, error rate, model load success/failure |
| `resource.json` | CPU/memory usage per tenant, quota utilization |

**Provisioning:**
- `observability/grafana/provisioning/datasources/prometheus.yaml` — Prometheus datasource pointing at `http://prometheus:9090`.
- `observability/grafana/provisioning/dashboards/default.yaml` — loads dashboards from `/var/lib/grafana/dashboards`.
- `observability/prometheus/prometheus.yml` — scrape configs for control plane (`host.docker.internal:8080/metrics`), operator, and Triton.

**Docker Compose:** `infra/docker/docker-compose.yml` gains `prometheus` and `grafana` services. Both start as part of `make local-up`.

**Prod:** A separate Helm values file (`infra/helm/observability-values.yaml`) configures Prometheus and Grafana for cluster deployment — no Docker Compose dependency.

---

## 4. Job Retry Logic

**Migration 013:** Add `retry_count INT NOT NULL DEFAULT 0` and `max_retries INT NOT NULL DEFAULT 3` to `training_jobs`.

**State machine change:** `FAILED → QUEUED` is a valid transition on the retry path. `CANCELLED` remains terminal — no retry.

**Retry trigger:** The internal status handler (`PATCH /internal/v1/jobs/:id/status`). After writing `FAILED` to the DB:
1. Check `retry_count < max_retries`.
2. If true: increment `retry_count`, create a new `training_run` record, transition job to `QUEUED`.
3. If false: job stays `FAILED` permanently.

**Audit trail:** Previous failed `training_run` records are never deleted. The new run gets a fresh `run_id`. The job's `retry_count` is returned on `GET /v1/jobs/:id`.

**Metrics:** `training_run_retry_total` counter is incremented on each retry.

---

## 5. Deployment Revisions + Rollback

**Existing state:** `deployment_revisions` table exists. Revision 1 is created atomically with the deployment. Each revision stores `model_version_id`, `revision_number`, and `artifact_uri`.

**New store methods:**
- `CreateRevision(ctx, deploymentID, modelVersionID, artifactURI) (*DeploymentRevision, error)`
- `GetRevision(ctx, deploymentID, revisionNumber int) (*DeploymentRevision, error)`
- `GetCurrentRevisionNumber(ctx, deploymentID) (int, error)` — returns `MAX(revision_number)`.

**Rollback API:** `POST /v1/deployments/:id/rollback`

Request body (revision is optional):
```json
{ "revision": 2 }
```

Behavior:
- If `revision` is omitted, defaults to `current_revision_number - 1`.
- Returns 400 if already at revision 1 (nothing to roll back to).
- Creates revision N+1 mirroring the target's `model_version_id` and `artifact_uri` — audit history is never rewritten.
- Sets deployment status to `pending`.
- Operator picks up `pending` deployment on next poll and redeploys (same reconcile loop as initial deployment — no special rollback path in the operator).

---

## 6. Events API + Quota API

### Events Dual-Write

The internal status handler writes to `platform_events` after each successful state transition (jobs and deployments):

| Field | Value |
|---|---|
| `tenant_id` | from job/deployment record |
| `entity_type` | `"job"` or `"deployment"` |
| `entity_id` | job/deployment UUID |
| `event_type` | new status string (e.g. `"FAILED"`, `"RUNNING"`) |
| `payload` | `{"from": "...", "to": "...", "failure_reason": "...", "timestamp": "..."}` |

**Store:** `internal/events/store.go` — `ListEvents(ctx, tenantID, filter EventFilter) ([]*PlatformEvent, int, error)`.

### Events API

`GET /v1/events` — tenant-scoped.

Query params: `entity_type`, `entity_id`, `limit` (default 50, max 200), `offset`.

Response:
```json
{ "events": [...], "total": 142 }
```

### Quota API

`GET /v1/quota` — tenant-scoped.

Response:
```json
{
  "cpu_quota": "16",
  "memory_quota": "64Gi",
  "cpu_used": "8",
  "memory_used": "32Gi",
  "running_jobs": 2
}
```

Used values are computed by summing `cpu_request` and `memory_request` across `training_runs` with status `RUNNING` for the tenant. Reuses quota logic already in the scheduler package.

---

## 7. Operator Hardening

**Single binary, two config profiles.** The operator reads `--env local|prod` (default: `local`). A `operator/internal/config/config.go` struct holds all tunable values.

Example config files:
- `operator/config/local.yaml`
- `operator/config/prod.yaml`

### Local Profile

| Feature | Setting |
|---|---|
| Leader election | Disabled |
| Webhook server | Disabled |
| Reconciliation | Poll-based (current design) |
| Namespace | Single, set via `--namespace` flag |
| Retry intervals | Fast (5s base, 30s max) |

### Prod Profile

| Feature | Setting |
|---|---|
| Leader election | Enabled via controller-runtime `LeaderElectionID` using configmap lock |
| Webhook server | Enabled — validates RayJob specs (required fields: image, resources) before admission |
| RayJob reconciliation | Full informer cache via controller-runtime default cache |
| DeploymentReconciler | Keeps poll loop (calls internal API, not watching a CRD) |
| Retry backoff | Exponential: base 5s, max 5m, with jitter |
| Namespace | `--namespace ""` = cluster-wide; non-empty = namespace-scoped |

---

## 8. Failure Test Suite

Five failure scenarios as integration tests. All hit a real PostgreSQL instance via `testutil`.

| Scenario | Location | What is verified |
|---|---|---|
| Bad image | `operator/internal/reconciler/` | `ImagePullBackOff` → job `FAILED`, `failure_reason` contains image error, `retry_count` reaches 3 then stops |
| Quota exceeded | `control-plane/internal/api/` | `POST /v1/jobs` returns 422, no `training_run` created |
| Missing artifact | `operator/internal/reconciler/` | Init container error → deployment `failed` with `failure_reason` |
| Invalid deployment | `control-plane/internal/api/` | `POST /v1/deployments` with non-production model version returns 422, no record created |
| Triton readiness failure | `operator/internal/reconciler/` | Pod running but readiness probe fails → deployment stays `provisioning`, does not advance to `running` |

Tests are table-driven where scenarios share setup shape.

---

## New Files

| File | Purpose |
|---|---|
| `control-plane/internal/observability/metrics.go` | Prometheus metric registrations |
| `control-plane/internal/events/store.go` | `ListEvents` query against `platform_events` |
| `control-plane/internal/api/events.go` | `GET /v1/events` handler |
| `control-plane/internal/api/quota.go` | `GET /v1/quota` handler |
| `control-plane/migrations/013_job_retry.up.sql` | `retry_count` + `max_retries` columns |
| `control-plane/migrations/013_job_retry.down.sql` | Rollback |
| `operator/internal/config/config.go` | Operator config struct |
| `operator/config/local.yaml` | Local profile defaults |
| `operator/config/prod.yaml` | Prod profile defaults |
| `observability/dashboards/control-plane.json` | Grafana dashboard |
| `observability/dashboards/training.json` | Grafana dashboard |
| `observability/dashboards/triton-serving.json` | Grafana dashboard |
| `observability/dashboards/resource.json` | Grafana dashboard |
| `observability/grafana/provisioning/datasources/prometheus.yaml` | Grafana datasource config |
| `observability/grafana/provisioning/dashboards/default.yaml` | Grafana dashboard provisioning |
| `observability/prometheus/prometheus.yml` | Prometheus scrape config |

## Modified Files

| File | Change |
|---|---|
| `control-plane/internal/observability/middleware.go` | Inject logger into context |
| `control-plane/internal/observability/logger.go` | Add `FromContext` helper |
| `control-plane/internal/api/router.go` | Add `/metrics`, events, quota, rollback routes |
| `control-plane/internal/api/internal.go` | Add `platform_events` dual-write on state transitions |
| `control-plane/internal/api/deployments.go` | Add rollback handler |
| `control-plane/internal/deployments/store.go` | Add `CreateRevision`, `GetRevision`, `GetCurrentRevisionNumber` |
| `control-plane/internal/jobs/statemachine.go` | Add `FAILED → QUEUED` transition |
| `control-plane/internal/jobs/store.go` | Add retry_count increment, new run creation on retry |
| `infra/docker/docker-compose.yml` | Add Prometheus + Grafana services |
| `operator/internal/reconciler/deployment_reconciler.go` | Add prod config hooks |
| `operator/cmd/operator/main.go` | Wire config struct + leader election + webhook |
