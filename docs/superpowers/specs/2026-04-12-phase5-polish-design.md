# Phase 5 — Polish Design Spec

**Date:** 2026-04-12  
**Phase:** 5 — Polish  
**Goal:** Deliver a complete portfolio-quality developer experience: a working `platformctl` CLI, a thin Python SDK, two training runtime examples, a full live-stack demo script designed for `asciinema` recording, architecture diagrams, runbooks, a control-plane E2E integration test, and a top-level README.

**Primary goal:** Portfolio signal. The demo must run cleanly and record well.  
**Secondary goal:** Developer usability — CLI, SDK, and runbooks are genuinely usable, not just decorative.

---

## 1. Demo Script (backbone)

**File:** `examples/demo-clients/demo.sh`  
**Companion:** `examples/demo-clients/record.sh` (wraps with `asciinema rec`)

The demo script is written first and drives all other deliverables. Every component is built to make the demo work.

### Structure

Nine numbered acts matching `PROJECT_INSTRUCTION.md §17`:

1. Submit a distributed training job (`platformctl train submit`)
2. Show Ray workers running (`kubectl get pods`)
3. Show MLflow run with metrics and artifact (`platformctl train status` + MLflow URL)
4. Register a model version (`platformctl model register`)
5. Promote model alias to `production` (`platformctl model promote`)
6. Deploy model to Triton (`platformctl deploy create`)
7. Send inference requests (`curl` to Triton endpoint)
8. Show metrics and deployment health (`platformctl deploy status` + Grafana URL)
9. Failure scenario and recovery (quota exceeded → 422; deployment rollback)

Each act prints a clear banner (`=== 3. MLflow Run ===`), runs CLI commands, and has natural `sleep` pauses for asciinema readability.

### Graceful degradation

Kubernetes/Triton steps check availability first:
- If `kubectl` is not configured or the cluster is down: print `[skipping — cluster not available]` and continue
- The terminal output tells the full story even without a live cluster

### Modes

Controlled by `DEMO_MODE` env var:
- `DEMO_MODE=fast` (default): uses the synthetic MLP training script, job completes in ~60s on CPU
- `DEMO_MODE=full`: uses the ResNet50 ONNX export script, produces a real model for real Triton inference

### Live E2E test

`bash examples/demo-clients/demo.sh` with `DEMO_MODE=fast` on a full stack is the live E2E proof. The script exits non-zero on any API or assertion failure.

---

## 2. CLI

**Module:** `cli/` — standalone Go module `github.com/Weilei424/kubernetes-native-ai-platform/cli`  
**Dependencies:** `github.com/spf13/cobra`, `gopkg.in/yaml.v3` — no imports from `control-plane/`

### Commands

```
platformctl train submit -f <spec.yaml>
platformctl train status <job-id> [--watch]
platformctl model register --run-id <id> --name <name> [--artifact-path <path>]
platformctl model promote --name <name> --version <n> --alias <alias>
platformctl deploy create -f <spec.yaml>
platformctl deploy status <deployment-id> [--watch]
```

`--watch` on `train status` and `deploy status` polls every 5 seconds and exits when a terminal state is reached.

### Output

**Default (human-readable):**
```
Job ID:      abc123
Status:      SUCCEEDED
Project:     vision-demo
MLflow Run:  mlflow-abc123
Updated:     2026-04-12 10:04:31
```

**`--json` flag (available on all commands):** raw pretty-printed API response JSON.

### Config resolution (in precedence order)

1. `--host` / `--token` flags
2. `PLATFORMCTL_HOST` / `PLATFORMCTL_TOKEN` environment variables
3. Default host: `http://localhost:8080`

### File structure

```
cli/
  go.mod
  main.go
  cmd/
    root.go       — cobra root, --host/--token/--json flags, config resolution
    client.go     — doRequest helper, printHuman, printJSON
    train.go      — train submit, train status (--watch)
    model.go      — model register, model promote
    deploy.go     — deploy create, deploy status (--watch)
    client_test.go
```

---

## 3. Training Runtime

**Location:** `training-runtime/`

### Base image

`training-runtime/base-images/Dockerfile` — `python:3.11-slim` with:
- `ray[default]`
- `mlflow`
- `boto3`
- `torch` (CPU build)
- `torchvision`
- `onnx`

### Example: minimal (`DEMO_MODE=fast`)

**`training-runtime/examples/minimal/`**
- `train.py` — 2-layer MLP on synthetic random data, 5 epochs, ~30-60s on CPU
  - `ray.init()` for distributed execution (head + 1 worker)
  - Logs loss per epoch via `mlflow.log_metric`
  - Exports trained model to ONNX via `torch.onnx.export`
  - Uploads ONNX to MinIO via `boto3`, logs artifact path to MLflow
  - Prints `MLFLOW_RUN_ID=<id>` to stdout for operator pickup
- `Dockerfile` — inherits from base image
- `requirements.txt`

### Example: resnet50 (`DEMO_MODE=full`)

**`training-runtime/examples/resnet50/`**
- `export.py` — loads pretrained `torchvision.models.resnet50`, exports to ONNX immediately (no training loop)
  - Input shape: `[1, 3, 224, 224]`
  - Logs mock accuracy metric + ONNX artifact to MLflow
  - Uploads to MinIO, same pattern as minimal
  - Prints `MLFLOW_RUN_ID=<id>` to stdout
- `Dockerfile` — inherits from base image
- `requirements.txt`

---

## 4. Infra Extension

### `make local-up` (extended)

Existing: starts Docker Compose services (PostgreSQL, Kafka, MinIO, MLflow, Prometheus, Grafana).

New additions:
1. Calls `make cluster-up` after Compose services are healthy

### `make cluster-up`

1. Check if `platform-local` kind cluster already exists — skip if so
2. `kind create cluster --name platform-local --config infra/local/kind-config.yaml`
3. `helm repo add kuberay` + `helm install kuberay-operator` (waits for operator pod Ready)
4. Apply operator RBAC (`infra/local/manifests/rbac/`)
5. `kubectl wait --for=condition=Ready pod -l app=kuberay-operator`

### `make demo-images`

Builds both training example images and loads them into kind:
```
docker build -t platform/minimal-trainer:latest training-runtime/examples/minimal/
docker build -t platform/resnet50-exporter:latest training-runtime/examples/resnet50/
kind load docker-image platform/minimal-trainer:latest --name platform-local
kind load docker-image platform/resnet50-exporter:latest --name platform-local
```

Kept separate from `make local-up` — image builds are slow and only needed before the demo.

### `make local-down` (extended)

Existing: `docker compose down`  
New: `kind delete cluster --name platform-local`

### `infra/local/kind-config.yaml`

Single-node kind cluster. Triton image (`nvcr.io/nvidia/tritonserver:24.01-py3-min`) is pre-loaded via `make demo-images` to avoid pull delays during the demo.

### New files

```
infra/local/kind-config.yaml
infra/local/scripts/cluster-up.sh
infra/local/scripts/cluster-down.sh
infra/local/manifests/rbac/operator-rbac.yaml
```

---

## 5. Python SDK

**Package:** `sdk/python/platformclient/`  
**Install:** `pip install -e .`  
**Role:** Standalone artifact — complete and tested, not featured in the demo.

### Modules

**`specs.py`** — dataclasses for programmatic spec generation:
- `RuntimeSpec(image, command, args, env)`
- `ResourceSpec(num_workers, worker_cpu, worker_memory, head_cpu, head_memory)`
- `TrainingSpec(name, project_id, runtime, resources)` — `.to_dict()` → `JobSubmitRequest` JSON shape
- `DeploymentSpec(name, model_name, model_version, namespace, replicas)` — `.to_dict()` → `CreateDeploymentRequest` JSON shape

**`client.py`** — `PlatformClient` class:
- Config: constructor `host`/`token` args or `PLATFORMCTL_HOST`/`PLATFORMCTL_TOKEN` env vars
- Methods: `submit_job`, `get_job`, `list_jobs`, `register_model`, `promote_model`, `get_model`, `create_deployment`, `get_deployment`
- Raises `requests.HTTPError` on 4xx/5xx responses

**Design constraint:** No polling, no retry, no async. The SDK stays thin — control plane owns logic.

### Tests

`sdk/python/tests/` using `pytest` + `responses` library:
- All client methods covered with mocked HTTP responses
- Spec serialization and field defaults covered

### File structure

```
sdk/python/
  platformclient/
    __init__.py
    client.py
    specs.py
  tests/
    test_specs.py
    test_client.py
  pyproject.toml
```

---

## 6. E2E Tests

### A. Control plane Go integration test

**File:** `control-plane/internal/api/e2e_test.go`

- Real PostgreSQL via testcontainers (same pattern as existing integration tests in `jobs/store_test.go`)
- Mock MLflow server via `httptest.NewServer`
- No Kubernetes, no Ray, no Triton
- Drives internal status API to simulate state transitions:
  - Job: `PENDING → QUEUED → RUNNING → SUCCEEDED` (with `mlflow_run_id`)
  - Deployment: `pending → running` (with `serving_endpoint`)
- Asserts full metadata chain:
  1. Submit job → get `job_id` + `run_id`
  2. Drive to SUCCEEDED → verify `mlflow_run_id` set
  3. Register model → version `status=candidate`
  4. Promote to `staging` then `production` → alias resolves correctly
  5. Create deployment → `status=pending`
  6. Drive to running → verify `serving_endpoint` set
- Run: `go test ./internal/api/... -run TestE2E_FullLifecycle -v`

### B. Live stack test

`examples/demo-clients/demo.sh` with `DEMO_MODE=fast` IS the live E2E test.  
Exits non-zero on any failure. No separate test file needed.

---

## 7. Docs

### Architecture diagrams (`docs/architecture/`)

Four Mermaid-in-Markdown files:

| File | Content |
|------|---------|
| `overview.md` | Full system component diagram: all layers, ownership boundaries |
| `training-flow.md` | Sequence: submit → dispatcher → RayJob → Ray workers → MLflow → SUCCEEDED |
| `model-lifecycle.md` | State machine: candidate → staging → production → archived + promotion rules |
| `serving-flow.md` | Sequence: deploy create → operator → init-container → Triton pod → inference |

### Runbooks (`docs/runbooks/`)

| File | Content |
|------|---------|
| `local-setup.md` | `make local-up`, `make demo-images`, token seeding, health checks |
| `demo-walkthrough.md` | Step-by-step CLI commands for all 9 §17 requirements with expected output |

### Top-level `README.md`

- One-paragraph project summary
- Architecture table
- Quick-start: 4 commands (`make local-up`, start server, start operator, run demo)
- Link to demo walkthrough runbook
- Testing section

**Not in this phase:** `docs/api/` API reference — the handlers are the spec.

---

## Deliverable Checklist (maps to `BACKLOG.md` Phase 5)

| Deliverable | Section |
|-------------|---------|
| `platformctl` CLI: `train submit`, `train status` | §2 |
| `platformctl` CLI: `model register`, `model promote` | §2 |
| `platformctl` CLI: `deploy create`, `deploy status` | §2 |
| Python SDK: spec generation, submission, run metadata | §5 |
| Example training spec (`resnet.yaml`) | §3 |
| Example deployment spec (`resnet-prod.yaml`) | §3 |
| Architecture diagrams | §7 |
| Runbooks | §7 |
| Scripted end-to-end demo (9 §17 requirements) | §1 |
| End-to-end test: submit→train→register→promote→deploy→infer | §6 |
| Documentation cleanup (README) | §7 |
| Training runtime scripts + Docker images | §3 |
| `make local-up` full stack (kind + KubeRay) | §4 |
| `asciinema`-ready recording script | §1 |

---

## Build Order

1. **Demo script skeleton** — write `demo.sh` with all 9 acts as stubs (echo-only). This is the spec.
2. **CLI** — implement commands in the order the demo calls them: `train submit` → `train status` → `model register` → `model promote` → `deploy create` → `deploy status`
3. **Training runtime** — minimal script first (`DEMO_MODE=fast`), then ResNet50
4. **Infra** — `make cluster-up`, `make demo-images`, Triton image loading
5. **Fill in demo script** — replace stubs with real commands, add graceful degradation
6. **Control plane E2E test** — independent of the above, can be done in parallel
7. **SDK** — independent, after CLI
8. **Docs** — diagrams, runbooks, README last (everything they document exists)
