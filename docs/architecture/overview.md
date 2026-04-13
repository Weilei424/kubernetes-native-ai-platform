# System Architecture Overview

This platform is a Kubernetes-native AI/ML control plane built around the required lifecycle:

`train -> track -> register -> promote -> deploy -> infer`

The design keeps orchestration and metadata ownership in Go, uses MLflow for experiment tracking and model registry, and uses Triton as the serving runtime.

## Business Context

- Purpose: demonstrate AI infrastructure and ML platform engineering with a complete model lifecycle.
- Primary users: platform engineers, ML engineers, and developers running the local portfolio demo.
- Core value: provide a single control plane for training submission, model registration, promotion, deployment, and inference.
- Operating model: CPU-first local development on kind/k3d before cloud-oriented variants.

## System Context

```mermaid
flowchart LR
    subgraph Clients["Client Layer"]
        CLI["platformctl CLI"]
        SDK["Python SDK"]
        DEMO["Demo Script / Example Specs"]
    end

    subgraph Control["Go Control Plane"]
        API["REST API"]
        AUTH["Token Auth"]
        SCHED["Scheduler\nadmission + quota + placement hints"]
        DISP["Dispatcher"]
        MODELS["Model Lifecycle Service"]
        DEPLOY["Deployment Service"]
        EVENTS["Events + Quota + Status APIs"]
    end

    subgraph Metadata["Metadata + Messaging"]
        PG["PostgreSQL\nplatform metadata"]
        KAFKA["Kafka\nplatform events"]
    end

    subgraph Training["Training + Tracking"]
        RAYJOB["RayJob CRD"]
        RAY["Ray / KubeRay"]
        MLFLOW["MLflow\ntracking + registry"]
        ART["MinIO / S3\nartifacts"]
    end

    subgraph Serving["Serving Plane"]
        OP["Operator"]
        LOADER["model-loader init container"]
        TRITON["Triton Inference Server"]
        SVC["Serving Service / Endpoint"]
    end

    subgraph Obs["Observability"]
        PROM["Prometheus"]
        GRAF["Grafana"]
    end

    CLI --> API
    SDK --> API
    DEMO --> CLI

    API --> AUTH
    API --> SCHED
    API --> MODELS
    API --> DEPLOY
    API --> EVENTS
    API --> PG
    API --> KAFKA
    API --> MLFLOW

    DISP --> RAYJOB
    SCHED --> DISP
    RAYJOB --> RAY
    RAY --> MLFLOW
    RAY --> ART

    OP --> RAYJOB
    OP --> API
    OP --> LOADER
    LOADER --> ART
    LOADER --> TRITON
    TRITON --> SVC

    API --> PROM
    OP --> PROM
    TRITON --> PROM
    PROM --> GRAF
```

## Lifecycle Flow

```mermaid
flowchart LR
    A["Train\nSubmit job through CLI / SDK"] --> B["Track\nRay job logs metrics and artifacts to MLflow"]
    B --> C["Register\nControl plane creates model version from succeeded run"]
    C --> D["Promote\nAlias moves through candidate -> staging -> production"]
    D --> E["Deploy\nDeployment record created for Triton"]
    E --> F["Infer\nClient calls Triton inference endpoint"]

    A -. "job + run metadata" .-> PG1["PostgreSQL"]
    B -. "run metadata + model artifact" .-> ML1["MLflow + MinIO"]
    E -. "deployment status + revision history" .-> PG1
```

## Local Development Topology

```mermaid
flowchart TB
    subgraph Laptop["Developer Workstation"]
        DEVCLI["platformctl / SDK / demo.sh"]
        CPSRV["control-plane\n(go run ./cmd/server)"]
        OPSRV["operator\n(go run ./cmd/operator)"]
    end

    subgraph Kind["kind / Kubernetes Cluster"]
        KAPI["Kubernetes API"]
        PG["PostgreSQL"]
        KAFKA["Kafka"]
        MINIO["MinIO"]
        MLFLOW["MLflow"]
        KUBERAY["KubeRay Operator"]
        TRAIN["Ray head + worker pods"]
        SERVE["Triton pod + Service"]
        PROM["Prometheus"]
        GRAF["Grafana"]
    end

    DEVCLI --> CPSRV
    CPSRV --> PG
    CPSRV --> KAFKA
    CPSRV --> MLFLOW
    CPSRV --> MINIO
    CPSRV --> KAPI

    OPSRV --> KAPI
    KUBERAY --> TRAIN
    TRAIN --> MLFLOW
    TRAIN --> MINIO
    OPSRV --> SERVE
    PROM --> GRAF
```

## Design Notes

- Control-plane ownership stays in Go. Validation, state transitions, scheduling, metadata persistence, and API behavior do not move into Python services.
- MLflow remains mandatory for the `track -> register -> promote` path.
- Triton remains the only serving runtime for the `deploy -> infer` path.
- PostgreSQL is the platform system of record for jobs, runs, models, deployments, revisions, and events.
- Kubernetes owns workload runtime state; the platform reconciles that state back into PostgreSQL through the operator.
