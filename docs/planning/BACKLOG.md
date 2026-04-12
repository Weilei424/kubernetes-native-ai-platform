# Backlog

## Status Legend
- [ ] Not started
- [x] Complete
- [~] In progress

---

### Phase 0 Execution Checklist — Foundation 

- [x] Repo bootstrap (directory layout per PROJECT_INSTRUCTION.md §15)
- [x] Local infrastructure stack (kind/k3d, PostgreSQL, Kafka, MinIO, MLflow)
- [x] Helm chart or Docker Compose for local dependency management (`make local-up` / `make local-down`)
- [x] Control plane skeleton (Go module, cmd entrypoint, router, middleware)
- [x] Health and readiness endpoints (`GET /healthz`, `GET /readyz`)
- [x] Auth skeleton (token-based, middleware, token validation)
- [x] Base DB schema migrations (tenants, projects, api_tokens, platform_events tables)
- [x] **[Task 17 — manual]** Live-stack verification: port-forward postgres, run server, curl /healthz /readyz, verify 401, verify DB tables, verify bcrypt token auth
- [x] **[Phase 1 prerequisite]** Token lookup optimization: add `token_prefix TEXT` index column to api_tokens so FindToken does a keyed lookup instead of full-table bcrypt scan (current v1 design is acceptable for near-zero rows)

---

### Phase 1 Execution Checklist — Training Control Plane

- [x] Migration 005: token_prefix column + index on api_tokens; update FindToken to keyed lookup
- [x] Migration 006: cpu_quota + memory_quota columns on tenants
- [x] Migration 007: training_jobs table
- [x] Migration 008: training_runs table
- [x] Scheduler: admission validation (`internal/scheduler/admission.go`)
- [x] Scheduler: quota enforcement in Go with resource quantity parsing (`internal/scheduler/quota.go`)
- [x] Scheduler: per-tenant fair scheduling and placement hint generation (`internal/scheduler/placement.go`)
- [x] Job state machine with transition validation (`internal/jobs/statemachine.go`)
- [x] Training jobs + runs DB store (`internal/jobs/store.go`)
- [x] RayJob CRD builder (`internal/jobs/rayjob.go`)
- [x] Kubernetes client auto-detect (`internal/k8s/client.go`)
- [x] Kafka producer for state transition events (`internal/events/kafka.go`)
- [x] Dispatcher goroutine: per-tenant fair promotion (oldest PENDING per tenant per tick), CRD submission, retry on rayjob_name IS NULL (`internal/jobs/dispatcher.go`)
- [x] Jobs API handlers: POST /v1/jobs, GET /v1/jobs, GET /v1/jobs/:id, GET /v1/jobs/:id/runs/:run_id (`internal/api/jobs.go`)
- [x] Internal status API handler: PATCH /internal/v1/jobs/:id/status (`internal/api/internal.go`)
- [x] Router: register job routes + internal router on separate port (`internal/api/router.go`)
- [x] main.go: start dispatcher goroutine + internal HTTP server
- [x] Operator binary entrypoint (`operator/cmd/operator/main.go`)
- [x] RayJob reconciler: watch, status map, call internal API (`operator/internal/reconciler/rayjob_reconciler.go`)
- [x] Unit tests: scheduler admission, quota, per-tenant fair scheduling, placement hints
- [x] Unit tests: state machine transitions
- [x] Unit tests: RayJob CRD builder
- [x] Unit tests: operator reconciler status mapping
- [x] Integration tests: POST /v1/jobs happy path and failure cases
- [x] Integration tests: PATCH /internal/v1/jobs/:id/status transitions
- [x] Integration tests: dispatcher cycle with fake K8s client
- [x] Integration tests: token prefix lookup

---

### Phase 2 Execution Checklist — ML Platform Layer

- [x] Migration 009: model_records table (tenant/project scoped, unique on tenant_id+name)
- [x] Migration 010: model_versions table (FK to model_records + training_runs, status field)
- [x] Extend StatusUpdateRequest with optional `mlflow_run_id` field
- [x] Add `SetMLflowRunID` + `GetRunForRegistration` to jobs.Store interface + PostgresJobStore
- [x] Internal status handler: persist mlflow_run_id when present in PATCH body
- [x] MLflow client package (`internal/mlflow/client.go`): CreateRegisteredModel, CreateModelVersion, SetModelAlias, DeleteModelAlias, GetModelVersionByAlias
- [x] Models domain types (`internal/models/model.go`): ModelRecord, ModelVersion, RegisterRequest, PromoteRequest, error sentinels
- [x] Models store (`internal/models/store.go`): CreateOrGetModelRecord, CreateModelVersion, GetModelRecordByName, ListModelVersions, GetModelVersionByNumber, UpdateModelVersionStatus
- [x] Models service (`internal/models/service.go`): Register, GetModel, GetModelVersion, Promote (flexible + archived terminal), ResolveAlias
- [x] Models API handlers (`internal/api/models.go`): POST /v1/models, GET /v1/models/:name, GET /v1/models/:name/versions/:version, POST /v1/models/:name/versions/:version/promote, GET /v1/models/:name/alias/:alias
- [x] Router: register model routes; export ModelsService interface
- [x] main.go: wire MLflow client + models service
- [x] Unit tests: MLflow client (httptest mock server), service (promotion rules, alias resolution, run validation)
- [x] Integration tests: store (real PG), handler tests (mock service), full-stack tests (real service + real PG + mock MLflow)

---

### Phase 3 Execution Checklist — Serving Plane

- [x] Migration 011: deployments table (tenant/project/model scoped, unique on tenant_id+name, status + serving_endpoint columns)
- [x] Migration 012: deployment_revisions table (FK to deployments + model_versions, revision_number)
- [x] Domain types (`internal/deployments/model.go`): Deployment, DeploymentRevision, CreateDeploymentRequest, UpdateStatusRequest, sentinel errors
- [x] State machine (`internal/deployments/statemachine.go`): ValidTransition function + unit tests
- [x] Deployment store (`internal/deployments/store.go`): Store interface + PostgresDeploymentStore (CreateDeployment atomic with revision 1, GetDeployment, UpdateDeploymentStatus, DeleteDeployment, ListPendingDeployments with JOIN for artifact_uri + model_name)
- [x] Deployment store integration tests (`internal/deployments/store_test.go`): real PostgreSQL via testutil
- [x] Deployment service (`internal/deployments/service.go`): ModelVersionReader interface, Create with production gate validation, Get (tenant scoped), Delete, UpdateStatus
- [x] Deployment service unit tests (`internal/deployments/service_test.go`): happy path, version-not-production, model-not-found, duplicate name, missing fields
- [x] Public API handlers (`internal/api/deployments.go`): DeploymentsService interface, POST /v1/deployments, GET /v1/deployments/:id, DELETE /v1/deployments/:id
- [x] API handler tests (`internal/api/deployments_test.go`): mock service, happy paths + error cases
- [x] Extend internal router (`internal/api/internal.go`): GET /internal/v1/deployments (list pending for operator), PATCH /internal/v1/deployments/:id/status (operator callback)
- [x] Router wiring (`internal/api/router.go`): add DeploymentsService param + deployment routes; update existing test call sites
- [x] main.go wiring: wire deploymentStore + deploymentsSvc; update NewInternalRouter call
- [x] Operator deployment reconciler (`operator/internal/reconciler/deployment_reconciler.go`): DeploymentReconciler implementing manager.Runnable, poll-based 10s tick, pod + ClusterIP service creation, MapPodPhase status mapping, reportStatus callback
- [x] Operator reconciler unit tests (`operator/internal/reconciler/deployment_reconciler_test.go`): MapPodPhase, TritonPodName, TritonServiceName, ServingEndpoint, DefaultPollInterval + HTTP round-trip tests
- [x] Operator main.go: add clientgoscheme.AddToScheme + register DeploymentReconciler via mgr.Add
- [x] Init container (`infra/docker/model-loader/`): loader.py (MinIO download + Triton layout), Dockerfile (python:3.11-slim), requirements.txt (boto3)
- [x] Verify: `cd control-plane && go test ./... && go vet ./...` passes
- [x] Verify: `cd operator && go test ./... && go vet ./...` passes

---

### Phase 4 Execution Checklist — Observability and Reliability

- [x] **[Plan]** Implementation plan written (`docs/superpowers/plans/2026-04-08-phase4-observability-reliability.md`)
- [x] Structured logging with correlation IDs and entity IDs (`internal/observability/logger.go`, `middleware.go`)
- [x] Prometheus metrics instrumentation (API latency, queue depth, job failures) (`internal/observability/metrics.go`, router wiring)
- [x] Grafana dashboards: control plane, training, Triton serving, resource (`infra/local/manifests/prometheus/`, `infra/local/manifests/grafana/`)
- [x] Migration 013: job retry columns (`retry_count`, `max_retries`) on `training_jobs`
- [x] Job retry logic and failure handling (state machine `FAILED→QUEUED`, store methods, internal handler trigger)
- [x] Deployment revisions and rollback (`POST /v1/deployments/{id}/rollback`, atomic revision, service + store + handler)
- [x] Event visibility API (`GET /v1/events`) — dual-write to `platform_events` + query endpoint
- [x] Quota visibility API (`GET /v1/quota`)
- [x] Operator hardening: local/prod config profiles, leader election, operator metrics endpoint (`operator_reconcile_duration_seconds`, `operator_reconcile_errors_total`), metrics HTTP server on `MetricsPort`, per-deployment exponential backoff using `RetryBaseDelay`/`RetryMaxDelay` (`operator/internal/config/config.go`, `operator/internal/observability/metrics.go`, `operator/cmd/operator/main.go`)
- [x] Failure tests: quota exceeded, invalid deployment (non-production model), retry exhaustion, Triton readiness failure, init-container error phase mapping, bad image (RayJob ImagePullBackOff propagates failure_reason), missing artifact (PodFailed → deployment failed with actionable failure_reason), rollback clears stale failure_reason so recovered deployments are not corrupted

**Phase 4 complete.**

---

### Phase 5 Execution Checklist — Polish

- [ ] `platformctl` CLI: `train submit`, `train status`
- [ ] `platformctl` CLI: `model register`, `model promote`
- [ ] `platformctl` CLI: `deploy create`, `deploy status`
- [ ] Python SDK: spec generation helpers, submission convenience, run metadata helpers
- [ ] Example training specs (`examples/training-specs/resnet.yaml`)
- [ ] Example deployment specs (`examples/deployment-specs/resnet-prod.yaml`)
- [ ] Architecture diagrams
- [ ] Runbooks
- [ ] Scripted end-to-end demo
- [ ] Documentation cleanup
- [ ] End-to-end test: submit → train → register → promote → deploy → infer
