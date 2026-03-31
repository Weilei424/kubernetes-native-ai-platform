# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## AI Roles

- **Claude** (claude.ai/code) — primary implementor. All code authoring, planning, and execution happens here.
- **Codex** — code reviewer. After implementation milestones, Codex reviews the work for correctness, quality, and adherence to the plan.

## Project Overview

This is a **Kubernetes-native AI/ML platform** built as a portfolio project signaling both AI Infrastructure and ML Platform engineering skills. The sacred end-to-end workflow is:

**train → track → register → promote → deploy → infer**

Never weaken or bypass this lifecycle path.

## Technology Stack

| Layer | Technology |
|---|---|
| Control plane API | Go |
| Metadata store | PostgreSQL |
| Event bus | Kafka |
| Artifact store | MinIO (local) / S3 (cloud) |
| Training runtime | Ray + KubeRay |
| Experiment tracking + Model registry | MLflow |
| Serving | Triton Inference Server |
| Observability | Prometheus + Grafana |
| Secrets | Vault |
| Packaging | Helm + Terraform |
| Optional GitOps | ArgoCD |

## Planning Docs

All planning artifacts live under `docs/planning/`:

| File | Purpose |
|---|---|
| `docs/planning/BACKLOG.md` | Phase-by-phase execution checklists |
| `docs/planning/IMPLEMENTATION_PLAN.md` | Phase roadmap with goals, deliverables, and success criteria |
| `docs/planning/ARCHITECTURE_NOTES.md` | Architecture decisions, component ownership, design constraints |

## Repository Layout

```
docs/
  planning/         # BACKLOG.md, IMPLEMENTATION_PLAN.md, ARCHITECTURE_NOTES.md
  architecture/     # architecture diagrams and decision records
  api/              # API reference docs
  runbooks/         # operational runbooks
control-plane/      # Go REST API — validation, metadata, orchestration, events
  cmd/              # entrypoints
  internal/
    api/            # HTTP handlers and routing
    auth/           # token-based auth
    jobs/           # training job logic
    models/         # model lifecycle
    deployments/    # deployment logic
    scheduler/      # admission, quota, placement
    events/         # Kafka publishing
    storage/        # PostgreSQL access
    observability/  # metrics/logging helpers
  migrations/       # DB schema migrations
operator/           # Kubernetes reconciliation loops
sdk/python/         # Thin Python SDK for ML users (spec gen, submission helpers)
cli/                # platformctl CLI (Go)
training-runtime/   # Ray job base images, wrappers, examples
  base-images/
  wrappers/
  examples/
serving/            # Triton config templates, model bundle tooling
  triton/
  model-bundles/
  deployment-templates/
infra/              # Terraform, Helm charts, Kubernetes manifests, ArgoCD apps
  terraform/
  helm/
  kubernetes/
  argocd/
observability/      # Prometheus rules, Grafana dashboards
  prometheus/
  grafana/
  dashboards/
examples/           # Training specs, deployment specs, demo clients
  training-specs/
  deployment-specs/
  demo-clients/
```

## Architecture: Separation of Concerns

Each system owns a distinct concern — do not blur these boundaries:

- **Kubernetes**: runs workloads (pods, RayJob CRDs)
- **Ray/KubeRay**: distributed training execution
- **MLflow**: run tracking and model registry (states: `candidate` → `staging` → `production` → `archived`)
- **Triton**: inference serving process
- **Control plane (Go)**: orchestrates the entire lifecycle, owns all metadata, exposes APIs
- **PostgreSQL**: durable platform metadata (tenants, projects, training_jobs, training_runs, model_records, model_versions, deployments, deployment_revisions, platform_events)

## Build, Test, and Development Commands

> Commands will be added here as the codebase is built out. The sections below reflect intended patterns.

### Control Plane (Go)

```bash
# From control-plane/
go build ./...
go test ./...
go test ./internal/scheduler/...    # run a single package
go vet ./...
golangci-lint run
```

### Local Infrastructure Stack

```bash
# Spin up local deps (kind/k3d + PostgreSQL + Kafka + MinIO + MLflow)
# Scripts will live in infra/local/ or a Makefile at repo root
make local-up
make local-down
```

### CLI

```bash
platformctl train submit -f examples/training-specs/resnet.yaml
platformctl train status <job-id>
platformctl model register --run-id <run-id> --name resnet50
platformctl model promote --name resnet50 --version 3 --alias production
platformctl deploy create -f examples/deployment-specs/resnet-prod.yaml
platformctl deploy status <deployment-id>
```

## Implementation Phases

Work in this order; do not skip ahead:

1. **Phase 0** — repo bootstrap, local infra stack, control plane skeleton, health endpoints, auth skeleton, base DB schema
2. **Phase 1** — training job API, metadata persistence, queueing/status model, RayJob submission, run reconciliation
3. **Phase 2** — MLflow run linkage, model registration, promotion workflow, alias-based resolution
4. **Phase 3** — deployment API, Triton model packaging, deployment controller, endpoint/status tracking
5. **Phase 4** — dashboards, retries, failure handling, deployment revisions, logs and event visibility
6. **Phase 5** — CLI polish, examples, runbooks, scripted demo, docs cleanup

## Design Guardrails

- The control plane is in **Go** — do not collapse it into a Python service.
- Serving uses **Triton** — do not replace with Flask/FastAPI.
- **MLflow model registry is required** — do not skip it.
- The scheduler in v1 is **not** a full kube-scheduler plugin — handle admission, quota, FIFO, placement hints only.
- Local development is **CPU-first** (kind/k3d, no GPU required).
- Auth in v1 is **simple token-based** — do not overbuild it.
- The SDK must stay **thin** — core logic belongs in the control plane.
- **No secrets in git** — credentials must come from Vault or Kubernetes secrets.

## Testing Requirements

- **Unit**: validation logic, state machine transitions, scheduler decisions, promotion rules, deployment spec rendering
- **Integration**: API→PostgreSQL persistence, API→RayJob creation, run/model/deployment flows (must hit real DB, not mocks)
- **E2E**: full submit→train→register→promote→deploy→infer path
- **Failure**: bad image, quota exceeded, missing artifact, invalid deployment, Triton readiness failure

## Observability Standards

All services must emit **structured logs** with correlation IDs and entity IDs (job ID, run ID, deployment ID). Key metric categories: API latency, queue depth, job admission failures, run duration, serving p50/p95/p99 latency, error rate.

Required Grafana dashboards: control plane, training, Triton serving, resource consumption.
