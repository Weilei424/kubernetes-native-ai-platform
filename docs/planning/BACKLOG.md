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

- [ ] Migration 005: token_prefix column + index on api_tokens; update FindToken to keyed lookup
- [ ] Migration 006: cpu_quota + memory_quota columns on tenants
- [ ] Migration 007: training_jobs table
- [ ] Migration 008: training_runs table
- [ ] Scheduler: admission validation (`internal/scheduler/admission.go`)
- [ ] Scheduler: quota enforcement in Go with resource quantity parsing (`internal/scheduler/quota.go`)
- [ ] Scheduler: FIFO ordering and placement hint generation (`internal/scheduler/placement.go`)
- [ ] Job state machine with transition validation (`internal/jobs/statemachine.go`)
- [ ] Training jobs + runs DB store (`internal/jobs/store.go`)
- [ ] RayJob CRD builder (`internal/jobs/rayjob.go`)
- [ ] Kubernetes client auto-detect (`internal/k8s/client.go`)
- [ ] Kafka producer for state transition events (`internal/events/kafka.go`)
- [x] Dispatcher goroutine: FIFO promotion, CRD submission, retry on rayjob_name IS NULL (`internal/jobs/dispatcher.go`)
- [ ] Jobs API handlers: POST /v1/jobs, GET /v1/jobs, GET /v1/jobs/:id, GET /v1/jobs/:id/runs/:run_id (`internal/api/jobs.go`)
- [ ] Internal status API handler: PATCH /internal/v1/jobs/:id/status (`internal/api/internal.go`)
- [ ] Router: register job routes + internal router on separate port (`internal/api/router.go`)
- [ ] main.go: start dispatcher goroutine + internal HTTP server
- [ ] Operator binary entrypoint (`operator/cmd/operator/main.go`)
- [ ] RayJob reconciler: watch, status map, call internal API (`operator/internal/reconciler/rayjob_reconciler.go`)
- [ ] Unit tests: scheduler admission, quota, FIFO, placement hints
- [ ] Unit tests: state machine transitions
- [ ] Unit tests: RayJob CRD builder
- [ ] Unit tests: operator reconciler status mapping
- [ ] Integration tests: POST /v1/jobs happy path and failure cases
- [ ] Integration tests: PATCH /internal/v1/jobs/:id/status transitions
- [ ] Integration tests: dispatcher cycle with fake K8s client
- [ ] Integration tests: token prefix lookup

---

### Phase 2 Execution Checklist — ML Platform Layer

- [ ] MLflow run linkage (associate training_run with MLflow run ID)
- [ ] Model registration API (`POST /models`, `GET /models/:name`)
- [ ] Model version metadata persistence (model_records, model_versions tables)
- [ ] Promotion workflow (`POST /models/:name/versions/:version/promote`)
- [ ] Alias-based model resolution (`GET /models/:name/alias/:alias`)
- [ ] Model version → source run traceability
- [ ] Unit tests: promotion rules, alias resolution
- [ ] Integration tests: registration flow, promotion flow

---

### Phase 3 Execution Checklist — Serving Plane

- [ ] Deployment API (`POST /deployments`, `GET /deployments/:id`)
- [ ] Deployment metadata persistence (deployments, deployment_revisions tables)
- [ ] Triton model repository layout preparation
- [ ] Deployment controller (reconcile desired → actual serving state)
- [ ] Endpoint and status tracking
- [ ] Inference proxy or passthrough routing
- [ ] Unit tests: deployment spec rendering
- [ ] Integration tests: deployment creation flow

---

### Phase 4 Execution Checklist — Observability and Reliability

- [ ] Operator hardening: leader election, webhook validation, full informer cache, retry backoff tuning, multi-namespace support
- [ ] Prometheus metrics instrumentation (API latency, queue depth, job failures)
- [ ] Training metrics (run duration, worker count, retry/failure, success rate)
- [ ] Serving metrics (request rate, p50/p95/p99, error rate, model load)
- [ ] Structured logging with correlation IDs and entity IDs
- [ ] Grafana dashboards: control plane, training, Triton serving, resource
- [ ] Job retry logic and failure handling
- [ ] Deployment revisions and rollback
- [ ] Event visibility API (`GET /events`)
- [ ] Quota visibility API (`GET /quota`)
- [ ] Failure tests: bad image, quota exceeded, missing artifact, invalid deployment, Triton readiness failure

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
