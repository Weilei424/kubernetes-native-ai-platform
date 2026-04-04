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
- [ ] **[Phase 1 prerequisite]** Token lookup optimization: add `token_prefix TEXT` index column to api_tokens so FindToken does a keyed lookup instead of full-table bcrypt scan (current v1 design is acceptable for near-zero rows)

---

### Phase 1 Execution Checklist — Training Control Plane

- [ ] Training job API (`POST /jobs`, `GET /jobs/:id`, `GET /jobs`)
- [ ] Training job metadata persistence (training_jobs table, CRUD)
- [ ] Training run metadata persistence (training_runs table)
- [ ] Job queueing and status model (PENDING → QUEUED → RUNNING → SUCCEEDED/FAILED)
- [ ] Scheduler: admission control and quota enforcement
- [ ] Scheduler: FIFO ordering and placement hint generation
- [ ] RayJob CRD submission via Kubernetes API
- [ ] Run status reconciliation (operator loop or polling)
- [ ] Unit tests: validation, state transitions, scheduler decisions
- [ ] Integration tests: API → PostgreSQL, API → RayJob creation

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
