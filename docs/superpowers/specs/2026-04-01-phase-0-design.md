# Phase 0 — Foundation: Design Spec

**Date:** 2026-04-01
**Status:** Approved

---

## Overview

Phase 0 establishes the complete repo structure, a fully running local infrastructure stack inside a kind cluster, and a Go control plane skeleton that handles requests, authenticates them via database-backed tokens, and persists metadata.

Delivery follows a **layered bootstrap** approach: infrastructure first (Layer 1), then control plane (Layer 2). Each layer is independently verifiable before the next begins.

---

## Section 1: Repo Layout

The full directory tree is scaffolded in Layer 1. Placeholder directories receive a `.gitkeep`. No structural churn in later phases.

```
infra/
  local/
    kind-config.yaml          # kind cluster definition (1 control-plane node)
    Makefile                  # local-up, local-down, local-status targets
    manifests/
      namespace.yaml          # aiplatform namespace
      postgres/               # StatefulSet, Service, PVC, ConfigMap, Secret
      kafka/                  # StatefulSet, Service (plain manifests, no Strimzi)
      minio/                  # Deployment, Service, PVC
      mlflow/                 # Deployment, Service (NodePort for browser access)
  helm/                       # (Phase 3+) Helm charts for platform components
  terraform/                  # (Phase 5) cloud infra
  kubernetes/                 # (Phase 1+) operator manifests, CRDs
  argocd/                     # (Phase 5) GitOps apps

control-plane/
  cmd/server/main.go          # entrypoint
  internal/
    api/                      # HTTP handlers and routing (chi)
    auth/                     # token middleware + DB validation
    storage/                  # PostgreSQL access layer (pgx/v5, raw SQL)
    observability/            # structured logger (log/slog), metrics stubs
  migrations/                 # golang-migrate SQL files (up + down)
  go.mod
  go.sum

operator/                     # (Phase 1+) placeholder
sdk/python/                   # (Phase 5) placeholder
cli/                          # (Phase 5) placeholder
training-runtime/             # (Phase 1+) placeholder
serving/                      # (Phase 3+) placeholder
observability/                # (Phase 4+) placeholder
examples/                     # placeholder
docs/
  planning/                   # BACKLOG.md, IMPLEMENTATION_PLAN.md, ARCHITECTURE_NOTES.md
  architecture/               # placeholder
  api/                        # placeholder
  runbooks/                   # placeholder
  superpowers/                # specs and plans (this file lives here)
```

---

## Section 2: Local Infrastructure Stack (Layer 1)

All local dependencies run as Kubernetes workloads inside a single-node kind cluster.

**Prerequisites (documented in README):**
- Docker Desktop for Windows installed and running
- WSL2 integration enabled for the active distro in Docker Desktop settings
- `kind` and `kubectl` installed inside WSL2

### `make local-up` sequence

1. Create kind cluster from `kind-config.yaml` (single control-plane node)
2. Apply `namespace.yaml` — creates `aiplatform` namespace
3. Deploy **PostgreSQL** — single-node StatefulSet, PVC, ClusterIP Service, credentials in a Kubernetes Secret
4. Deploy **Kafka** — single-broker StatefulSet, ClusterIP Service (plain manifests; no Strimzi operator until Phase 1)
5. Deploy **MinIO** — Deployment, PVC, ClusterIP Service; post-deploy Job creates `mlflow-artifacts` bucket
6. Deploy **MLflow** — Deployment backed by in-cluster Postgres (separate DB) and MinIO; ClusterIP Service + NodePort for browser access
7. Poll each Service until ready before returning

### Supporting targets

| Target | Action |
|---|---|
| `make local-down` | Delete the kind cluster entirely (clean slate) |
| `make local-status` | `kubectl get pods -n aiplatform` + print Postgres DSN, MinIO endpoint, MLflow UI URL |

---

## Section 3: Control Plane Skeleton (Layer 2)

**Go module path:** `github.com/Weilei424/kubernetes-native-ai-platform/control-plane`
Module rooted at `control-plane/`.

### Entrypoint (`cmd/server/main.go`)

- Reads config from environment variables: `DATABASE_URL`, `SERVER_PORT`, `LOG_LEVEL`
- Initializes structured logger (`log/slog`, JSON output)
- Opens `pgx/v5` connection pool, runs `golang-migrate` migrations on startup
- Constructs chi router, attaches middleware chain, starts HTTP server

### Router structure

```
GET  /healthz        # public — liveness probe
GET  /readyz         # public — readiness probe (checks DB)
/v1/*                # protected by auth middleware
```

### Middleware stack (applied to `/v1`)

1. **Request ID** — generates UUID, injects into context and `X-Request-ID` response header
2. **Structured logger** — logs method, path, status code, latency, request ID on every request
3. **Auth** — extracts `Authorization: Bearer <token>`, bcrypt-compares against `api_tokens.token_hash`, rejects with 401 if missing/invalid/expired, injects `tenant_id` into context on success

### Health endpoints

| Endpoint | Behaviour |
|---|---|
| `GET /healthz` | Always `200 OK` — confirms process is alive |
| `GET /readyz` | Pings DB; `200 OK` if connected, `503 Service Unavailable` if not |

### Internal packages

| Package | Responsibility |
|---|---|
| `internal/api` | chi router setup, handler registration |
| `internal/auth` | token middleware, bcrypt comparison, DB lookup |
| `internal/storage` | `pgx/v5` pool init, query helpers (raw SQL, no ORM) |
| `internal/observability` | `log/slog` logger init, request logging middleware, metrics stubs |

---

## Section 4: Base DB Schema

Four migrations applied via `golang-migrate` on control plane startup. All migrations include a corresponding `.down.sql`.

### `001_create_tenants`

```sql
CREATE TABLE tenants (
  id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name       TEXT NOT NULL UNIQUE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

### `002_create_projects`

```sql
CREATE TABLE projects (
  id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id  UUID NOT NULL REFERENCES tenants(id),
  name       TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, name)
);
```

### `003_create_api_tokens`

```sql
CREATE TABLE api_tokens (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   UUID NOT NULL REFERENCES tenants(id),
  token_hash  TEXT NOT NULL UNIQUE,  -- bcrypt hash; plaintext never stored
  description TEXT,
  expires_at  TIMESTAMPTZ,           -- NULL = no expiry
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

### `004_create_platform_events`

```sql
CREATE TABLE platform_events (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   UUID NOT NULL REFERENCES tenants(id),
  entity_type TEXT NOT NULL,
  entity_id   UUID NOT NULL,
  event_type  TEXT NOT NULL,
  payload     JSONB,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX ON platform_events (tenant_id, entity_type, entity_id);
```

---

## Success Criteria

- `make local-up` completes without errors on WSL2 + Docker Desktop
- All pods in `aiplatform` namespace reach `Running` state
- `GET /healthz` returns `200 OK`
- `GET /readyz` returns `200 OK` when DB is up, `503` when DB is unreachable
- A request to `GET /v1/*` without a valid token returns `401 Unauthorized`
- A request with a valid token returns the tenant ID in context (logged)
- All four migrations apply cleanly against the in-cluster Postgres

---

## Key Decisions

- **Everything-in-kind:** all local deps run as Kubernetes workloads; no Docker Compose split
- **chi router:** idiomatic, infra-aligned, minimal deps
- **golang-migrate:** standard Go migration tooling, SQL files, up/down support
- **Database-backed tokens:** bcrypt-hashed in `api_tokens` table; supports per-tenant revocation from day one
- **pgx/v5, raw SQL:** no ORM; control planes benefit from explicit query control
- **log/slog:** stdlib structured logger; zero additional dependency
- **Plain Kafka manifests:** no Strimzi operator in Phase 0; added in Phase 1 when event publishing begins
