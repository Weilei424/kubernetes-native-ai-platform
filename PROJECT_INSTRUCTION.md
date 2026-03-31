# kubernetes-native-ai-platform

A Kubernetes-native portfolio project that combines **AI Infrastructure** and **ML Platform** engineering.

This system allows users to:

- submit distributed training jobs,
- track runs and artifacts,
- register and promote models,
- deploy models for online inference,
- observe platform, training, and serving health.

The project is intentionally designed to signal both roles:

- **AI Infrastructure Engineer:** Kubernetes orchestration, workload placement, distributed training, model serving, observability, reliability
- **ML Platform Engineer:** experiment tracking, model registry, promotion workflows, deployment workflows, CLI/API abstraction

---

## 1. Project Positioning

### What this project is
This is a **platform project**, not a single-model demo.

Users should be able to go through this end-to-end flow:

**train -> track -> register -> promote -> deploy -> infer**

### Role alignment
This project should stay roughly:

- **70% AI Infrastructure**
- **30% ML Platform**

That means the project should emphasize:
- execution architecture,
- distributed systems,
- Kubernetes-native orchestration,
- serving and reliability,

while still proving:
- model lifecycle management,
- developer workflows,
- platform APIs and CLI.

### What this project is not
Do not turn this into:
- a notebook platform,
- a feature store,
- a pure frontend-heavy product,
- a toy Flask model server,
- a generic PaaS for everything.

---

## 2. Core Goals

### Primary goals
The platform must support these workflows as first-class features:

1. submit distributed training jobs
2. track runs and artifacts
3. register and promote models
4. deploy models to online inference
5. observe health, status, and performance

### Resume signal goals
A reviewer should immediately see:
- strong Kubernetes knowledge
- distributed systems ability
- backend/platform engineering skills
- understanding of ML workload lifecycle
- awareness of production concerns such as observability, retries, metadata, artifacts, and rollout control

### Non-goals for v1
The first version should not include:
- feature store
- AutoML
- notebook execution service
- full custom kube-scheduler plugin
- canary/shadow deployments
- support for many ML frameworks
- complex UI-first product

---

## 3. Final Stack

| Layer | Choice |
|---|---|
| Control plane API | Go |
| Metadata store | PostgreSQL |
| Event bus | Kafka |
| Artifact store | MinIO locally, S3 in cloud |
| Training runtime | Ray + KubeRay |
| Experiment tracking | MLflow |
| Model registry | MLflow Model Registry |
| Serving plane | Triton Inference Server |
| Metrics and dashboards | Prometheus + Grafana |
| Secrets | Vault |
| Packaging and deployment | Helm + Terraform |
| Optional GitOps | ArgoCD |

### Stack rationale
- **Go** gives strong backend/infra signal.
- **Kubernetes** is the execution substrate.
- **Ray + KubeRay** provide distributed training on Kubernetes.
- **MLflow** gives run tracking and model lifecycle management.
- **Triton** gives real serving-platform credibility.
- **PostgreSQL** stores durable platform metadata.
- **Kafka** supports async workflows and event-driven state transitions.
- **MinIO/S3** store artifacts and model bundles.
- **Vault** handles credentials and service secrets.

---

## 4. High-Level Architecture

### Major components
- **Control Plane**
  - validates requests
  - persists metadata
  - orchestrates workflows
  - exposes APIs
- **Scheduler / Orchestrator**
  - admission control
  - quota checks
  - queueing
  - placement decisions
- **Training Runtime**
  - executes distributed training jobs using Ray on Kubernetes
- **Lifecycle Layer**
  - tracks runs, artifacts, model versions, and aliases through MLflow
- **Serving Layer**
  - deploys Triton-based inference services
- **Observability Layer**
  - metrics, dashboards, events, logs
- **Security Layer**
  - secrets, service credentials, namespace isolation

### Ownership boundaries
This platform owns:
- submission
- orchestration
- metadata
- promotion
- deployment
- status visibility

It does not own:
- ML framework internals
- Kubernetes internals
- Triton internals
- MLflow internals

---

## 5. Core User Experience

### Primary user
An ML engineer or backend/platform engineer who wants to:
- run training,
- inspect runs,
- register models,
- deploy models,
- check serving status.

### Required end-to-end workflow
1. user submits training job
2. platform creates and tracks run
3. training logs metrics and artifacts
4. user registers a model from a successful run
5. user promotes a model alias such as `staging` or `production`
6. user deploys the promoted model
7. user sends inference requests
8. user checks metrics and deployment health

---

## 6. Core Design Principles

1. **Platform-first, not script-first**
   - users should interact through API, CLI, and specs

2. **Kubernetes-native, but practical**
   - use Kubernetes heavily, but do not overbuild v1

3. **Metadata is first-class**
   - every important workflow step must be traceable

4. **Strong separation of concerns**
   - Kubernetes runs workloads
   - Ray handles distributed execution
   - MLflow handles tracking and registry
   - Triton handles serving
   - the platform orchestrates the lifecycle

5. **Boring durability over clever complexity**
   - prefer reliable, understandable designs

6. **Protect the end-to-end path**
   - never weaken:
     - **train -> track -> register -> promote -> deploy -> infer**

---

## 7. Core Functional Design

### 7.1 Control Plane
Implemented in **Go**.

Responsibilities:
- REST API
- authentication
- validation
- metadata persistence
- workflow state transitions
- event publishing
- reconciliation and status reporting

### 7.2 Scheduler / Orchestrator
This is **not** a full kube-scheduler replacement in v1.

Responsibilities:
- queue jobs
- enforce quota
- choose execution profile
- attach node selectors / tolerations / resource requirements
- create RayJob resources
- decide target namespace or node pool strategy

Kubernetes still performs final pod-to-node scheduling.

### 7.3 Training Runtime
Training runs through **Ray + KubeRay**.

Flow:
- user submits training spec
- platform validates spec and resources
- platform creates RayJob
- Ray launches workers
- training code logs metrics to MLflow
- model artifacts are saved to object storage

### 7.4 Model Lifecycle
MLflow is the lifecycle system.

Required model states:
- `candidate`
- `staging`
- `production`
- `archived`

Required capabilities:
- register model from successful run
- store version metadata
- promote version by alias
- resolve deployable version from alias
- trace model version back to source run

### 7.5 Serving Plane
Serving uses **Triton Inference Server**.

Flow:
- platform resolves model version
- platform prepares Triton model repository layout
- deployment controller creates serving deployment
- service becomes available for inference
- platform exposes deployment status and endpoint

For the first polished path:
- train in **PyTorch**
- export to **ONNX**
- serve through **Triton**

---

## 8. Metadata Model

The platform must maintain durable metadata in PostgreSQL.

### Core entities
- tenants
- projects
- training_jobs
- training_runs
- model_records
- model_versions
- deployments
- deployment_revisions
- platform_events

### Source of truth rules
- **PostgreSQL**: platform metadata
- **MLflow**: runs and model registry metadata
- **Kubernetes**: workload runtime state
- **Triton**: serving process state
- **Object storage**: artifact bytes

---

## 9. API Requirements

Use **REST** in v1.

### Required endpoint groups
- health and readiness
- training job submission and status
- run inspection
- model registration and promotion
- deployment creation and status
- events and quota visibility

### Example training request
```json
{
  "name": "resnet50-train-v1",
  "project": "vision-demo",
  "image": "ghcr.io/example/vision-trainer:latest",
  "entrypoint": "python train.py",
  "framework": "pytorch",
  "num_workers": 2,
  "resources": {
    "cpu": "4",
    "memory": "16Gi",
    "gpu": 1
  }
}
```

### Example deployment request
```json
{
  "name": "vision-prod",
  "project": "vision-demo",
  "model_name": "resnet50",
  "model_alias": "production",
  "replicas": 2,
  "batching": {
    "enabled": true
  }
}
```

---

## 10. CLI and SDK

### CLI goals
The CLI is the main developer interface.

Example commands:
```bash
platformctl train submit -f examples/training-specs/resnet.yaml
platformctl train status <job-id>
platformctl model register --run-id <run-id> --name resnet50
platformctl model promote --name resnet50 --version 3 --alias production
platformctl deploy create -f examples/deployment-specs/resnet-prod.yaml
platformctl deploy status <deployment-id>
```

### SDK goals
Provide a lightweight **Python SDK** for ML users.

The SDK should help with:
- spec generation
- job submission convenience
- run metadata helpers

The SDK must stay thin. Core business logic belongs in the control plane.

---

## 11. Scheduling Rules

### v1 scheduling scope
The scheduler should handle:
- admission control
- quota enforcement
- FIFO within tenant by default
- fairness across tenants
- resource profile generation
- placement hints for Kubernetes workloads

### v1 output
The scheduler should produce:
- target namespace
- resource requests/limits
- node selectors / affinity
- tolerations
- RayJob execution shape

### Important guardrail
Do **not** build a full kube-scheduler plugin before the core platform works.

---

## 12. Observability Requirements

### Platform metrics
- API latency
- queue depth
- job admission failures
- reconciliation loop duration
- deployment success/failure counts

### Training metrics
- run duration
- worker count
- retry/failure count
- success rate

### Serving metrics
- request rate
- p50/p95/p99 latency
- error rate
- replica count
- model load success/failure

### Logging
All services should emit:
- structured logs
- correlation IDs
- entity IDs such as job ID, run ID, deployment ID

### Dashboards
At minimum:
- control plane dashboard
- training dashboard
- Triton serving dashboard
- resource consumption dashboard

---

## 13. Security and Multi-Tenancy

### Minimum security requirements
- no secrets in git
- credentials loaded from Vault or Kubernetes secrets
- namespace-based isolation
- minimal service-account permissions
- clear separation between control-plane services and execution workloads

### Multi-tenancy model
v1 should use namespace-based isolation and platform-level quotas.

### Authentication
v1 can use simple token-based auth.

Do not overbuild auth in the first version.

---

## 14. Environments

### Local development
Local development should be **CPU-first**.

Recommended local stack:
- kind, k3d, or Docker Desktop Kubernetes
- PostgreSQL
- Kafka
- MinIO
- MLflow
- control plane
- Triton in local demo mode

Local mode is for:
- API work
- metadata workflows
- integration testing
- end-to-end lifecycle validation

### Cloud environment
Cloud is where the infra signal becomes strongest.

Recommended cloud target:
- EKS
- S3
- GPU node group
- Prometheus + Grafana
- Vault if included in infra scope

### Development order
1. local CPU-only correctness
2. end-to-end lifecycle working
3. cloud deployment
4. GPU-aware enhancements

---

## 15. Repository Layout

```text
kubernetes-native-ai-platform/
├── README.md
├── docs/
│   ├── architecture/
│   ├── api/
│   ├── runbooks/
│   └── diagrams/
├── control-plane/
│   ├── cmd/
│   ├── internal/
│   │   ├── api/
│   │   ├── auth/
│   │   ├── jobs/
│   │   ├── models/
│   │   ├── deployments/
│   │   ├── scheduler/
│   │   ├── events/
│   │   ├── storage/
│   │   └── observability/
│   └── migrations/
├── operator/
│   └── reconciliation/
├── sdk/
│   └── python/
├── cli/
├── training-runtime/
│   ├── base-images/
│   ├── examples/
│   └── wrappers/
├── serving/
│   ├── triton/
│   ├── model-bundles/
│   └── deployment-templates/
├── infra/
│   ├── terraform/
│   ├── helm/
│   ├── kubernetes/
│   └── argocd/
├── observability/
│   ├── prometheus/
│   ├── grafana/
│   └── dashboards/
└── examples/
    ├── training-specs/
    ├── deployment-specs/
    └── demo-clients/
```

---

## 16. Implementation Phases

### Phase 0 - Foundation
- repo bootstrap
- local infrastructure stack
- control plane skeleton
- health endpoints
- auth skeleton
- base metadata schema

### Phase 1 - Training Control Plane
- training job API
- metadata persistence
- queueing and status model
- RayJob submission
- run status reconciliation

### Phase 2 - ML Platform Layer
- MLflow run linkage
- model registration
- promotion workflow
- alias-based model resolution

### Phase 3 - Serving Plane
- deployment API
- Triton model packaging
- deployment controller
- endpoint and status tracking

### Phase 4 - Observability and Reliability
- dashboards
- retries
- failure handling
- deployment revisions
- logs and event visibility

### Phase 5 - Polish
- CLI
- examples
- diagrams
- runbooks
- scripted demo
- documentation cleanup

---

## 17. Demo Requirements

The final project must support this demo:

1. submit a distributed training job
2. show Ray workers running
3. show MLflow run with metrics and artifact
4. register a model version
5. promote model alias to `production`
6. deploy model to Triton
7. send inference requests
8. show metrics and deployment health
9. show at least one failure scenario and recovery path

If this demo works cleanly, the project strongly signals both target roles.

---

## 18. Testing Requirements

### Unit tests
Must cover:
- validation
- state transitions
- scheduler decisions
- promotion rules
- deployment spec rendering

### Integration tests
Must cover:
- API to PostgreSQL persistence
- API to RayJob creation
- run registration flow
- model promotion flow
- deployment creation flow

### End-to-end tests
Must cover:
- submit -> train -> register -> promote -> deploy -> infer

### Failure tests
Must cover:
- bad image
- quota exceeded
- missing artifact
- invalid deployment request
- Triton readiness failure

---

## 19. Guardrails for Future Design

1. Do not collapse the system into one Python service.
2. Do not replace Triton with a simple Flask or FastAPI model server.
3. Do not skip MLflow model registry.
4. Do not overbuild the frontend.
5. Do not support too many ML frameworks in v1.
6. Do not build a full scheduler plugin before the core platform works.
7. Keep the end-to-end lifecycle sacred:
   - **train -> track -> register -> promote -> deploy -> infer**

---

## 20. Definition of Done

Version 1 is complete when all of the following are true:

- users can submit training jobs through API or CLI
- distributed training runs through Ray on Kubernetes
- runs and artifacts are visible in MLflow
- a successful run can be registered as a model
- a model can be promoted with an alias
- a promoted model can be deployed to Triton
- inference requests succeed through a stable endpoint
- dashboards and core metrics exist
- basic failure states are handled
- docs and demo flow are present

---

## 21. Final Summary

**kubernetes-native-ai-platform** is a Kubernetes-native system that combines:

- **AI Infrastructure**
  - orchestration
  - distributed execution
  - resource-aware scheduling
  - serving
  - observability
  - reliability

- **ML Platform**
  - experiment tracking
  - model registration
  - promotion
  - deployment workflows
  - developer-facing API and CLI

This file is the project source of truth.

All future design and implementation decisions should refine this architecture, not contradict it, unless the project direction is explicitly changed.
