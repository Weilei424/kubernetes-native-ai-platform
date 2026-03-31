# Architecture Notes

## Stack

| Layer | Technology | Rationale |
|---|---|---|
| Control plane API | Go | Strong backend/infra signal; performance and reliability |
| Metadata store | PostgreSQL | Durable relational metadata for all platform entities |
| Event bus | Kafka | Async workflows and event-driven state transitions |
| Artifact store | MinIO (local) / S3 (cloud) | Object storage for model artifacts and bundles |
| Training runtime | Ray + KubeRay | Distributed training on Kubernetes |
| Experiment tracking | MLflow | Run tracking, artifact logging, metrics |
| Model registry | MLflow Model Registry | Version lifecycle, alias management |
| Serving plane | Triton Inference Server | Production-grade model serving; ONNX support |
| Metrics and dashboards | Prometheus + Grafana | Standard observability stack |
| Secrets | Vault | Credential management; no secrets in git |
| Packaging | Helm + Terraform | Kubernetes and cloud infrastructure |
| Optional GitOps | ArgoCD | Declarative deployment management |

---

## Key Decisions

### Control plane in Go, not Python
The control plane is Go. Python services that wrap orchestration logic weaken the AI Infrastructure signal. Core business logic — validation, state transitions, scheduling decisions, metadata persistence — belongs in the Go control plane.

### MLflow is required; cannot be skipped
MLflow is the model lifecycle system. It owns run tracking and the model registry. Skipping it removes the ML Platform signal and breaks the `register → promote` path.

### Triton is required; not replaceable with Flask/FastAPI
Triton Inference Server provides the production serving credibility. A Flask wrapper around a model is not acceptable for this platform's goals.

### Scheduler is not a full kube-scheduler plugin
The v1 scheduler handles admission, quota, FIFO ordering, and placement hint generation. Kubernetes performs the actual pod-to-node scheduling. Do not build a full scheduler plugin until the core platform is working.

### Auth is token-based in v1
Simple token-based auth is sufficient. Do not overbuild auth before the core lifecycle is working.

### Local development is CPU-first
The local stack runs on kind or k3d with no GPU requirement. GPU-aware enhancements come after the end-to-end lifecycle is validated locally.

### PyTorch → ONNX → Triton is the polished demo path
For the initial demo, train in PyTorch, export to ONNX, and serve through Triton. This is the simplest complete path through the serving plane.

---

## Component Responsibilities

| Component | Owns |
|---|---|
| Kubernetes | Running workloads (pods, RayJob CRDs) |
| Ray / KubeRay | Distributed training execution |
| MLflow | Run tracking, artifact logging, model registry (states: candidate → staging → production → archived) |
| Triton | Inference serving process |
| Control plane (Go) | Orchestration, lifecycle management, metadata ownership, API exposure |
| PostgreSQL | Durable platform metadata (all entities listed below) |
| Kafka | Async event publishing for state transitions |
| MinIO / S3 | Artifact bytes (model bundles, checkpoints) |
| Vault / K8s Secrets | Service credentials and secrets |

### PostgreSQL entities
- `tenants`
- `projects`
- `training_jobs`
- `training_runs`
- `model_records`
- `model_versions`
- `deployments`
- `deployment_revisions`
- `platform_events`

### Source of truth per concern
- **PostgreSQL**: platform metadata
- **MLflow**: run and registry metadata
- **Kubernetes**: workload runtime state
- **Triton**: serving process state
- **Object storage**: artifact bytes

---

## Design Constraints

1. **Sacred lifecycle** — never weaken `train → track → register → promote → deploy → infer`
2. **No monolith collapse** — do not collapse the system into a single Python service
3. **Metadata is first-class** — every significant workflow step must be traceable through platform metadata
4. **Strong separation of concerns** — each component owns exactly its concern; do not blur boundaries
5. **Boring durability over clever complexity** — prefer reliable, understandable designs
6. **Platform-first** — users interact through API, CLI, and specs; not notebooks or scripts
7. **v1 scope discipline** — no feature store, no AutoML, no notebook execution, no canary deployments, no complex UI

---

## Observability Standards

All services must emit:
- Structured logs (JSON)
- Correlation IDs on every request
- Entity IDs (job ID, run ID, deployment ID) on relevant log lines

### Required metric categories
- API latency (p50/p95/p99)
- Queue depth
- Job admission failures
- Reconciliation loop duration
- Run duration, worker count, retry/failure count
- Serving request rate, latency percentiles, error rate, model load success/failure

### Required dashboards
- Control plane dashboard
- Training dashboard
- Triton serving dashboard
- Resource consumption dashboard

---

## Environments

| Environment | Purpose |
|---|---|
| Local (kind/k3d) | CPU-first correctness, API work, integration testing |
| Cloud (EKS) | Full infra signal, GPU workloads, Prometheus + Grafana |

**Development order:** local CPU correctness → end-to-end lifecycle → cloud deployment → GPU enhancements
