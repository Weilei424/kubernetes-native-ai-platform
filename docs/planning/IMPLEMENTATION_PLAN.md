# Implementation Plan

## Overview

**kubernetes-native-ai-platform** is a Kubernetes-native AI/ML platform portfolio project. It demonstrates both AI Infrastructure and ML Platform engineering through a complete end-to-end workflow:

**train → track → register → promote → deploy → infer**

Development follows a CPU-first local approach before cloud and GPU enhancements. The system is built around a Go control plane, Kubernetes-native orchestration, Ray/KubeRay for distributed training, MLflow for experiment tracking and model registry, and Triton Inference Server for model serving.

---

## Phases

### Phase 0 — Foundation

**Goal:** Establish the repo structure, local infrastructure stack, and a running control plane skeleton that can accept requests, authenticate them, and persist metadata.

**Deliverables:**
- Repository layout matching `PROJECT_INSTRUCTION.md §15`
- Local stack running via `make local-up` (kind/k3d, PostgreSQL, Kafka, MinIO, MLflow)
- Go control plane with HTTP router, middleware, health endpoints
- Token-based auth middleware (skeleton)
- Base PostgreSQL schema migrations (tenants, projects, platform_events)
- Helm chart or Docker Compose for local dependency management

**Success criteria:**
- `GET /healthz` and `GET /readyz` return 200
- Control plane boots and connects to PostgreSQL
- Auth middleware rejects unauthenticated requests
- Base schema migrations apply cleanly

---

### Phase 1 — Training Control Plane

**Goal:** Allow users to submit training jobs, have the platform queue and schedule them, create RayJob resources in Kubernetes, and reconcile run status back into platform metadata.

**Deliverables:**
- Training job submission API (`POST /v1/jobs`)
- Training job and run metadata tables in PostgreSQL
- Job state machine: PENDING → QUEUED → RUNNING → SUCCEEDED / FAILED / CANCELLED
- Scheduler: admission control, quota enforcement, per-tenant fair scheduling (promotes oldest PENDING job per tenant per tick), placement hint generation
- RayJob CRD creation via Kubernetes API
- Reconciliation loop: syncs RayJob status back to training_run records
- Unit tests for validation, state transitions, scheduler logic
- Integration tests for API → PostgreSQL and API → RayJob creation

**Success criteria:**
- A job submitted via API transitions through the full state machine
- A RayJob is created in Kubernetes with correct resource shape
- Run status is reflected accurately in platform metadata

---

### Phase 2 — ML Platform Layer

**Goal:** Link training runs to MLflow, enable model registration from successful runs, and implement a promotion workflow with alias-based model resolution.

**Deliverables:**
- MLflow run ID stored on training_run records
- Model registration API (`POST /v1/models`, `GET /v1/models/:name`)
- model_records and model_versions tables in PostgreSQL
- Promotion API (`POST /v1/models/:name/versions/:version/promote`)
- Alias-based resolution (`GET /v1/models/:name/alias/:alias`)
- Source run traceability from any model version
- Required model states: `candidate`, `staging`, `production`, `archived`
- Unit tests for promotion rules and alias resolution
- Integration tests for registration and promotion flows

**Success criteria:**
- A model can be registered from a succeeded training run
- A model version can be promoted to `production` alias
- Alias resolution returns the correct model version
- Model version traces back to originating run

---

### Phase 3 — Serving Plane

**Goal:** Deploy promoted models to Triton Inference Server through a platform-managed deployment API and controller.

**Deliverables:**
- Deployment API (`POST /v1/deployments`, `GET /v1/deployments/:id`, `DELETE /v1/deployments/:id`)
- deployments and deployment_revisions tables in PostgreSQL
- Triton model repository layout preparation (ONNX model bundle packaging)
- Deployment controller: reconciles desired serving state in Kubernetes
- Endpoint tracking and serving status exposure
- Integration tests for deployment creation flow
- Unit tests for deployment spec rendering

**Success criteria:**
- A deployment created from a `production`-aliased model spins up a Triton pod
- The deployment status is accurately reflected in platform metadata
- Inference requests succeed through the serving endpoint

---

### Phase 4 — Observability and Reliability

**Goal:** Add production-grade observability, retry logic, failure handling, and event/log visibility.

**Deliverables:**
- Operator hardening: local/prod config profiles, leader election via controller-runtime, operator metrics endpoint (`operator_reconcile_duration_seconds`, `operator_reconcile_errors_total`), retry delay config wiring — webhook admission validation and full informer cache are deferred to a future hardening pass
- Prometheus metrics: API latency, queue depth, job admission failures, reconciliation duration, deployment counts
- Training metrics: run duration, retry count, completion counter by result (succeeded/failed) enabling success-rate computation — training worker count deferred (requires K8s pod introspection per RayJob)
- Serving metrics: request rate, p50/p95/p99 latency, error rate, and model load success/failure are exposed natively by Triton's own /metrics endpoint — no platform instrumentation needed; replica count tracked implicitly via deployment_count gauge
- Structured logging with correlation IDs and entity IDs on all services
- Grafana dashboards: control plane, training, Triton serving, resource consumption
- Job retry logic and failure state handling
- Deployment revisions and rollback support
- Events API (`GET /v1/events`)
- Quota visibility API (`GET /v1/quota`)
- Failure test suite: bad image (RayJob ImagePullBackOff → FAILED + failure_reason propagated), quota exceeded (422 at admission), missing artifact (PodFailed → deployment failed with init-container failure_reason), invalid deployment (non-production model version → 422), Triton readiness failure (pod not running → stays provisioning), retry exhaustion (job stays FAILED after max_retries), rollback clears stale failure_reason (failure metadata does not persist across recovery)
- Migration 014: `failure_reason TEXT` column on deployments; `RollbackDeployment` clears it atomically

**Success criteria:**
- All key platform operations emit metrics visible in Grafana
- A failed job retries and surfaces actionable error state
- A deployment rollback restores the previous serving revision
- Failure scenarios produce clear error states without corrupting platform metadata

---

### Phase 5 — Polish

**Goal:** Deliver a complete developer experience with CLI, Python SDK, examples, diagrams, and a scripted demo that cleanly covers the full end-to-end workflow.

**Deliverables:**
- `platformctl` CLI: `train submit`, `train status`, `model register`, `model promote`, `deploy create`, `deploy status`
- Python SDK: spec generation, job submission convenience, run metadata helpers
- Example specs: `examples/training-specs/resnet.yaml`, `examples/deployment-specs/resnet-prod.yaml`
- Demo client: `examples/demo-clients/`
- Architecture diagrams in `docs/diagrams/`
- Runbooks in `docs/runbooks/`
- Scripted demo covering all 9 demo requirements from `PROJECT_INSTRUCTION.md §17`
- End-to-end test: submit → train → register → promote → deploy → infer
- Documentation cleanup

**Success criteria:**
- The scripted demo runs cleanly from start to finish on a local stack
- All CLI commands work against a running control plane
- End-to-end test passes
- The demo covers at least one failure scenario and recovery path
