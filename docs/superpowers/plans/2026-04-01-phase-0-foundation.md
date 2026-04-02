# Phase 0 — Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Establish the full repo directory tree, a local infrastructure stack running in kind (PostgreSQL, Kafka, MinIO, MLflow), and a Go control plane skeleton with health endpoints, DB-backed token auth, and base schema migrations.

**Architecture:** Two sequential layers. Layer 1 commits all Kubernetes manifests and Makefile targets for the local stack; verify it boots before starting Layer 2. Layer 2 commits the Go control plane — chi router, pgx/v5 storage, golang-migrate, and bcrypt token auth middleware. Unit tests use interface mocks (no live DB required). Integration verification uses the running Layer 1 stack.

**Tech Stack:** kind, kubectl, Go 1.22+, github.com/go-chi/chi/v5, github.com/jackc/pgx/v5, github.com/golang-migrate/migrate/v4, golang.org/x/crypto (bcrypt), log/slog (stdlib)

---

## File Map

### Layer 1 — Infrastructure

```
Makefile                                            # repo-root delegating targets
infra/local/Makefile                                # local-up, local-down, local-status
infra/local/kind-config.yaml                        # single control-plane node + NodePort mapping
infra/local/manifests/namespace.yaml
infra/local/manifests/postgres/secret.yaml
infra/local/manifests/postgres/configmap.yaml       # initdb: CREATE DATABASE mlflow
infra/local/manifests/postgres/statefulset.yaml
infra/local/manifests/postgres/service.yaml         # headless ClusterIP
infra/local/manifests/kafka/statefulset.yaml        # bitnami/kafka KRaft single-broker
infra/local/manifests/kafka/service.yaml            # headless ClusterIP
infra/local/manifests/minio/secret.yaml
infra/local/manifests/minio/deployment.yaml
infra/local/manifests/minio/service.yaml
infra/local/manifests/minio/pvc.yaml
infra/local/manifests/minio/bucket-job.yaml         # mc creates mlflow-artifacts bucket
infra/local/manifests/mlflow/deployment.yaml        # backed by postgres + minio
infra/local/manifests/mlflow/service.yaml           # NodePort 30000 → 5000
```

### Layer 2 — Control Plane

```
control-plane/go.mod
control-plane/go.sum
control-plane/cmd/server/main.go
control-plane/migrations/embed.go                   # //go:embed *.sql → var FS embed.FS
control-plane/migrations/001_create_tenants.up.sql
control-plane/migrations/001_create_tenants.down.sql
control-plane/migrations/002_create_projects.up.sql
control-plane/migrations/002_create_projects.down.sql
control-plane/migrations/003_create_api_tokens.up.sql
control-plane/migrations/003_create_api_tokens.down.sql
control-plane/migrations/004_create_platform_events.up.sql
control-plane/migrations/004_create_platform_events.down.sql
control-plane/internal/observability/logger.go      # NewLogger(level) *slog.Logger
control-plane/internal/observability/middleware.go  # RequestLogger(logger) middleware
control-plane/internal/storage/db.go                # Connect(), RunMigrations()
control-plane/internal/auth/store.go                # TokenStore interface
control-plane/internal/auth/postgres_store.go       # PostgresTokenStore — bcrypt lookup
control-plane/internal/auth/middleware.go           # TokenAuth(), TenantIDFromContext()
control-plane/internal/auth/middleware_test.go
control-plane/internal/api/health.go                # Pinger interface, LivenessHandler, ReadinessHandler
control-plane/internal/api/health_test.go
control-plane/internal/api/router.go                # NewRouter(db, logger)
```

---

## Layer 1: Infrastructure

---

### Task 1: Scaffold Repo Directory Tree

**Files:**
- Create: all placeholder directories listed in the spec with `.gitkeep`

- [ ] **Step 1: Create placeholder directories**

Run from repo root:

```bash
mkdir -p \
  infra/local/manifests/postgres \
  infra/local/manifests/kafka \
  infra/local/manifests/minio \
  infra/local/manifests/mlflow \
  infra/helm \
  infra/terraform \
  infra/kubernetes \
  infra/argocd \
  control-plane/cmd/server \
  control-plane/internal/api \
  control-plane/internal/auth \
  control-plane/internal/storage \
  control-plane/internal/observability \
  control-plane/migrations \
  operator \
  sdk/python \
  cli \
  training-runtime/base-images \
  training-runtime/wrappers \
  training-runtime/examples \
  serving/triton \
  serving/model-bundles \
  serving/deployment-templates \
  observability/prometheus \
  observability/grafana \
  observability/dashboards \
  examples/training-specs \
  examples/deployment-specs \
  examples/demo-clients \
  docs/architecture \
  docs/api \
  docs/runbooks
```

- [ ] **Step 2: Add .gitkeep to leaf placeholder dirs**

```bash
find operator sdk/python cli training-runtime serving observability examples \
     infra/helm infra/terraform infra/kubernetes infra/argocd \
     docs/architecture docs/api docs/runbooks \
     -type d -empty -exec touch {}/.gitkeep \;
```

- [ ] **Step 3: Commit**

```bash
git add .
git commit -m "chore: scaffold repo directory tree"
```

---

### Task 2: Kind Cluster Config + Root Makefile

**Files:**
- Create: `infra/local/kind-config.yaml`
- Create: `Makefile` (repo root)

- [ ] **Step 1: Write kind-config.yaml**

```yaml
# infra/local/kind-config.yaml
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
  - role: control-plane
    extraPortMappings:
      - containerPort: 30000
        hostPort: 5000
        protocol: TCP
        listenAddress: "127.0.0.1"
```

The `extraPortMappings` entry maps the MLflow NodePort (30000) to `localhost:5000` on the WSL2 host.

- [ ] **Step 2: Write root Makefile**

```makefile
# Makefile (repo root)
.PHONY: local-up local-down local-status

local-up:
	$(MAKE) -C infra/local local-up

local-down:
	$(MAKE) -C infra/local local-down

local-status:
	$(MAKE) -C infra/local local-status
```

- [ ] **Step 3: Commit**

```bash
git add infra/local/kind-config.yaml Makefile
git commit -m "chore: add kind cluster config and root Makefile"
```

---

### Task 3: Namespace + PostgreSQL Manifests

**Files:**
- Create: `infra/local/manifests/namespace.yaml`
- Create: `infra/local/manifests/postgres/secret.yaml`
- Create: `infra/local/manifests/postgres/configmap.yaml`
- Create: `infra/local/manifests/postgres/statefulset.yaml`
- Create: `infra/local/manifests/postgres/service.yaml`

- [ ] **Step 1: Write namespace.yaml**

```yaml
# infra/local/manifests/namespace.yaml
apiVersion: v1
kind: Namespace
metadata:
  name: aiplatform
```

- [ ] **Step 2: Write postgres/secret.yaml**

Credentials are plain dev values — never use these outside local.

```yaml
# infra/local/manifests/postgres/secret.yaml
apiVersion: v1
kind: Secret
metadata:
  name: postgres-credentials
  namespace: aiplatform
type: Opaque
stringData:
  username: aiplatform
  password: aiplatform
```

- [ ] **Step 3: Write postgres/configmap.yaml**

Creates the `mlflow` database on first Postgres startup (the `aiplatform` database is created automatically via `POSTGRES_DB`).

```yaml
# infra/local/manifests/postgres/configmap.yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: postgres-initdb
  namespace: aiplatform
data:
  init.sql: |
    CREATE DATABASE mlflow;
```

- [ ] **Step 4: Write postgres/statefulset.yaml**

```yaml
# infra/local/manifests/postgres/statefulset.yaml
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: postgres
  namespace: aiplatform
spec:
  serviceName: postgres
  replicas: 1
  selector:
    matchLabels:
      app: postgres
  template:
    metadata:
      labels:
        app: postgres
    spec:
      containers:
        - name: postgres
          image: postgres:16
          env:
            - name: POSTGRES_USER
              valueFrom:
                secretKeyRef:
                  name: postgres-credentials
                  key: username
            - name: POSTGRES_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: postgres-credentials
                  key: password
            - name: POSTGRES_DB
              value: aiplatform
            - name: PGDATA
              value: /var/lib/postgresql/data/pgdata
          ports:
            - containerPort: 5432
          volumeMounts:
            - name: data
              mountPath: /var/lib/postgresql/data
            - name: initdb
              mountPath: /docker-entrypoint-initdb.d
      volumes:
        - name: initdb
          configMap:
            name: postgres-initdb
  volumeClaimTemplates:
    - metadata:
        name: data
      spec:
        accessModes: ["ReadWriteOnce"]
        resources:
          requests:
            storage: 5Gi
```

Note: `PGDATA` is set to a subdirectory to avoid ownership conflicts with kind's local-path-provisioner.

- [ ] **Step 5: Write postgres/service.yaml**

Headless service — DNS `postgres.aiplatform.svc.cluster.local` resolves directly to the pod IP.

```yaml
# infra/local/manifests/postgres/service.yaml
apiVersion: v1
kind: Service
metadata:
  name: postgres
  namespace: aiplatform
spec:
  clusterIP: None
  selector:
    app: postgres
  ports:
    - port: 5432
      targetPort: 5432
```

- [ ] **Step 6: Commit**

```bash
git add infra/local/manifests/
git commit -m "chore: add namespace and postgres manifests"
```

---

### Task 4: Kafka Manifests

**Files:**
- Create: `infra/local/manifests/kafka/statefulset.yaml`
- Create: `infra/local/manifests/kafka/service.yaml`

- [ ] **Step 1: Write kafka/statefulset.yaml**

Single broker in KRaft mode (no ZooKeeper). Uses `bitnami/kafka:3.7` which handles KRaft configuration via env vars.

```yaml
# infra/local/manifests/kafka/statefulset.yaml
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: kafka
  namespace: aiplatform
spec:
  serviceName: kafka
  replicas: 1
  selector:
    matchLabels:
      app: kafka
  template:
    metadata:
      labels:
        app: kafka
    spec:
      containers:
        - name: kafka
          image: bitnami/kafka:3.7
          env:
            - name: KAFKA_CFG_NODE_ID
              value: "1"
            - name: KAFKA_CFG_PROCESS_ROLES
              value: broker,controller
            - name: KAFKA_CFG_LISTENERS
              value: PLAINTEXT://:9092,CONTROLLER://:9093
            - name: KAFKA_CFG_ADVERTISED_LISTENERS
              value: PLAINTEXT://kafka:9092
            - name: KAFKA_CFG_CONTROLLER_QUORUM_VOTERS
              value: 1@kafka:9093
            - name: KAFKA_CFG_CONTROLLER_LISTENER_NAMES
              value: CONTROLLER
            - name: KAFKA_CFG_INTER_BROKER_LISTENER_NAME
              value: PLAINTEXT
            - name: ALLOW_PLAINTEXT_LISTENER
              value: "yes"
            - name: KAFKA_KRAFT_CLUSTER_ID
              value: "MkU3OTlkNmItMWE4OS00Zjk5"
          ports:
            - containerPort: 9092
              name: plaintext
          volumeMounts:
            - name: data
              mountPath: /bitnami/kafka
  volumeClaimTemplates:
    - metadata:
        name: data
      spec:
        accessModes: ["ReadWriteOnce"]
        resources:
          requests:
            storage: 5Gi
```

- [ ] **Step 2: Write kafka/service.yaml**

```yaml
# infra/local/manifests/kafka/service.yaml
apiVersion: v1
kind: Service
metadata:
  name: kafka
  namespace: aiplatform
spec:
  clusterIP: None
  selector:
    app: kafka
  ports:
    - name: plaintext
      port: 9092
      targetPort: 9092
    - name: controller
      port: 9093
      targetPort: 9093
```

- [ ] **Step 3: Commit**

```bash
git add infra/local/manifests/kafka/
git commit -m "chore: add kafka KRaft manifests"
```

---

### Task 5: MinIO Manifests

**Files:**
- Create: `infra/local/manifests/minio/secret.yaml`
- Create: `infra/local/manifests/minio/pvc.yaml`
- Create: `infra/local/manifests/minio/deployment.yaml`
- Create: `infra/local/manifests/minio/service.yaml`
- Create: `infra/local/manifests/minio/bucket-job.yaml`

- [ ] **Step 1: Write minio/secret.yaml**

```yaml
# infra/local/manifests/minio/secret.yaml
apiVersion: v1
kind: Secret
metadata:
  name: minio-credentials
  namespace: aiplatform
type: Opaque
stringData:
  root-user: minioadmin
  root-password: minioadmin
```

- [ ] **Step 2: Write minio/pvc.yaml**

```yaml
# infra/local/manifests/minio/pvc.yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: minio-data
  namespace: aiplatform
spec:
  accessModes: ["ReadWriteOnce"]
  resources:
    requests:
      storage: 10Gi
```

- [ ] **Step 3: Write minio/deployment.yaml**

```yaml
# infra/local/manifests/minio/deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: minio
  namespace: aiplatform
spec:
  replicas: 1
  selector:
    matchLabels:
      app: minio
  template:
    metadata:
      labels:
        app: minio
    spec:
      containers:
        - name: minio
          image: minio/minio:latest
          args: ["server", "/data", "--console-address", ":9001"]
          env:
            - name: MINIO_ROOT_USER
              valueFrom:
                secretKeyRef:
                  name: minio-credentials
                  key: root-user
            - name: MINIO_ROOT_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: minio-credentials
                  key: root-password
          ports:
            - containerPort: 9000
              name: api
            - containerPort: 9001
              name: console
          volumeMounts:
            - name: data
              mountPath: /data
      volumes:
        - name: data
          persistentVolumeClaim:
            claimName: minio-data
```

- [ ] **Step 4: Write minio/service.yaml**

```yaml
# infra/local/manifests/minio/service.yaml
apiVersion: v1
kind: Service
metadata:
  name: minio
  namespace: aiplatform
spec:
  selector:
    app: minio
  ports:
    - name: api
      port: 9000
      targetPort: 9000
    - name: console
      port: 9001
      targetPort: 9001
```

- [ ] **Step 5: Write minio/bucket-job.yaml**

The job retries until MinIO is reachable, then creates the `mlflow-artifacts` bucket.

```yaml
# infra/local/manifests/minio/bucket-job.yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: minio-create-bucket
  namespace: aiplatform
spec:
  template:
    spec:
      restartPolicy: OnFailure
      containers:
        - name: mc
          image: minio/mc:latest
          command:
            - /bin/sh
            - -c
            - |
              until mc alias set local http://minio:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD"; do
                echo "Waiting for MinIO..."; sleep 3
              done
              mc mb local/mlflow-artifacts --ignore-existing
              echo "Bucket ready."
          env:
            - name: MINIO_ROOT_USER
              valueFrom:
                secretKeyRef:
                  name: minio-credentials
                  key: root-user
            - name: MINIO_ROOT_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: minio-credentials
                  key: root-password
```

- [ ] **Step 6: Commit**

```bash
git add infra/local/manifests/minio/
git commit -m "chore: add minio manifests and bucket init job"
```

---

### Task 6: MLflow Manifests

**Files:**
- Create: `infra/local/manifests/mlflow/deployment.yaml`
- Create: `infra/local/manifests/mlflow/service.yaml`

- [ ] **Step 1: Write mlflow/deployment.yaml**

MLflow uses Postgres as its backend store and MinIO (S3-compatible) as the artifact store. The `AWS_*` variables are how MLflow/boto3 authenticates to MinIO.

```yaml
# infra/local/manifests/mlflow/deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: mlflow
  namespace: aiplatform
spec:
  replicas: 1
  selector:
    matchLabels:
      app: mlflow
  template:
    metadata:
      labels:
        app: mlflow
    spec:
      containers:
        - name: mlflow
          image: ghcr.io/mlflow/mlflow:v2.11.3
          command:
            - mlflow
            - server
            - --backend-store-uri
            - postgresql://$(POSTGRES_USER):$(POSTGRES_PASSWORD)@postgres:5432/mlflow
            - --default-artifact-root
            - s3://mlflow-artifacts/
            - --host
            - "0.0.0.0"
            - --port
            - "5000"
          env:
            - name: MLFLOW_S3_ENDPOINT_URL
              value: http://minio:9000
            - name: AWS_ACCESS_KEY_ID
              valueFrom:
                secretKeyRef:
                  name: minio-credentials
                  key: root-user
            - name: AWS_SECRET_ACCESS_KEY
              valueFrom:
                secretKeyRef:
                  name: minio-credentials
                  key: root-password
            - name: POSTGRES_USER
              valueFrom:
                secretKeyRef:
                  name: postgres-credentials
                  key: username
            - name: POSTGRES_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: postgres-credentials
                  key: password
          ports:
            - containerPort: 5000
```

- [ ] **Step 2: Write mlflow/service.yaml**

NodePort 30000 maps to `localhost:5000` via the kind `extraPortMappings` entry in `kind-config.yaml`.

```yaml
# infra/local/manifests/mlflow/service.yaml
apiVersion: v1
kind: Service
metadata:
  name: mlflow
  namespace: aiplatform
spec:
  type: NodePort
  selector:
    app: mlflow
  ports:
    - port: 5000
      targetPort: 5000
      nodePort: 30000
```

- [ ] **Step 3: Commit**

```bash
git add infra/local/manifests/mlflow/
git commit -m "chore: add mlflow manifests"
```

---

### Task 7: infra/local/Makefile

**Files:**
- Create: `infra/local/Makefile`

- [ ] **Step 1: Write infra/local/Makefile**

```makefile
# infra/local/Makefile
CLUSTER_NAME := aiplatform-local
NAMESPACE    := aiplatform

.PHONY: local-up local-down local-status

local-up:
	@echo "==> Creating kind cluster $(CLUSTER_NAME)..."
	kind create cluster --name $(CLUSTER_NAME) --config kind-config.yaml
	@echo "==> Applying namespace..."
	kubectl apply -f manifests/namespace.yaml
	@echo "==> Deploying PostgreSQL..."
	kubectl apply -f manifests/postgres/
	kubectl rollout status statefulset/postgres -n $(NAMESPACE) --timeout=180s
	@echo "==> Deploying Kafka..."
	kubectl apply -f manifests/kafka/
	kubectl rollout status statefulset/kafka -n $(NAMESPACE) --timeout=180s
	@echo "==> Deploying MinIO..."
	kubectl apply -f manifests/minio/pvc.yaml
	kubectl apply -f manifests/minio/secret.yaml
	kubectl apply -f manifests/minio/deployment.yaml
	kubectl apply -f manifests/minio/service.yaml
	kubectl rollout status deployment/minio -n $(NAMESPACE) --timeout=180s
	kubectl apply -f manifests/minio/bucket-job.yaml
	kubectl wait --for=condition=complete job/minio-create-bucket -n $(NAMESPACE) --timeout=120s
	@echo "==> Deploying MLflow..."
	kubectl apply -f manifests/mlflow/
	kubectl rollout status deployment/mlflow -n $(NAMESPACE) --timeout=180s
	@echo ""
	@echo "==> Stack is ready."
	@$(MAKE) local-status

local-down:
	kind delete cluster --name $(CLUSTER_NAME)

local-status:
	@echo "==> Pods in namespace $(NAMESPACE):"
	kubectl get pods -n $(NAMESPACE)
	@echo ""
	@echo "==> Endpoints:"
	@echo "  MLflow UI:    http://localhost:5000 (via kind NodePort)"
	@echo "  PostgreSQL:   kubectl port-forward svc/postgres -n $(NAMESPACE) 5432:5432"
	@echo "                DSN: postgres://aiplatform:aiplatform@localhost:5432/aiplatform"
	@echo "  MinIO API:    kubectl port-forward svc/minio -n $(NAMESPACE) 9000:9000"
	@echo "  Kafka:        kubectl port-forward svc/kafka -n $(NAMESPACE) 9092:9092"
```

- [ ] **Step 2: Commit**

```bash
git add infra/local/Makefile
git commit -m "chore: add local-up / local-down / local-status Makefile"
```

---

### Task 8: Verify Layer 1

**Prerequisites:** Docker Desktop running, WSL2 integration enabled, `kind` and `kubectl` installed in WSL2.

- [ ] **Step 1: Spin up the stack**

Run from repo root:

```bash
make local-up
```

Expected: command completes without error. Final output shows `make local-status` table.

- [ ] **Step 2: Verify all pods are Running**

```bash
kubectl get pods -n aiplatform
```

Expected output (all pods `Running`, job `Completed`):
```
NAME                      READY   STATUS      RESTARTS   AGE
kafka-0                   1/1     Running     0          2m
minio-<hash>              1/1     Running     0          2m
minio-create-bucket-<id>  0/1     Completed   0          2m
mlflow-<hash>             1/1     Running     0          1m
postgres-0                1/1     Running     0          3m
```

- [ ] **Step 3: Verify MLflow UI is reachable**

Open `http://localhost:5000` in a browser. Expected: MLflow Experiments UI loads.

- [ ] **Step 4: Verify PostgreSQL databases exist**

```bash
kubectl port-forward svc/postgres -n aiplatform 5432:5432 &
psql "postgres://aiplatform:aiplatform@localhost:5432/aiplatform" -c "\l"
```

Expected: `aiplatform` and `mlflow` databases both listed. Kill the port-forward after.

- [ ] **Step 5: Commit Layer 1 verification note**

No code change needed. Layer 1 is complete.

---

## Layer 2: Control Plane

---

### Task 9: Go Module Init + Dependencies

**Files:**
- Create: `control-plane/go.mod`

- [ ] **Step 1: Init the module**

```bash
cd control-plane
go mod init github.com/Weilei424/kubernetes-native-ai-platform/control-plane
```

- [ ] **Step 2: Add dependencies**

```bash
go get github.com/go-chi/chi/v5@latest
go get github.com/golang-migrate/migrate/v4@latest
go get github.com/golang-migrate/migrate/v4/database/postgres@latest
go get github.com/golang-migrate/migrate/v4/source/iofs@latest
go get github.com/jackc/pgx/v5@latest
go get golang.org/x/crypto@latest
go mod tidy
```

- [ ] **Step 3: Verify the module builds**

```bash
go build ./...
```

Expected: no output (nothing to build yet, but module is valid).

- [ ] **Step 4: Commit**

```bash
git add control-plane/go.mod control-plane/go.sum
git commit -m "chore: init control-plane Go module"
```

---

### Task 10: Structured Logger + Request Logger Middleware

**Files:**
- Create: `control-plane/internal/observability/logger.go`
- Create: `control-plane/internal/observability/middleware.go`

- [ ] **Step 1: Write logger.go**

```go
// control-plane/internal/observability/logger.go
package observability

import (
	"log/slog"
	"os"
	"strings"
)

// NewLogger returns a JSON slog.Logger at the requested level.
// level is case-insensitive: "debug", "info", "warn", "error". Defaults to info.
func NewLogger(level string) *slog.Logger {
	var l slog.Level
	switch strings.ToLower(level) {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l}))
}
```

- [ ] **Step 2: Write middleware.go**

Logs method, path, status, latency, and request ID (injected by chi's `middleware.RequestID`) on every request.

```go
// control-plane/internal/observability/middleware.go
package observability

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

// RequestLogger returns a chi-compatible middleware that logs each request.
func RequestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			logger.Info("request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.Status(),
				"latency_ms", time.Since(start).Milliseconds(),
				"request_id", middleware.GetReqID(r.Context()),
			)
		})
	}
}
```

- [ ] **Step 3: Build to confirm no errors**

```bash
cd control-plane
go build ./internal/observability/...
```

Expected: no output.

- [ ] **Step 4: Commit**

```bash
git add control-plane/internal/observability/
git commit -m "feat: add structured JSON logger and request logger middleware"
```

---

### Task 11: Storage Package

**Files:**
- Create: `control-plane/internal/storage/db.go`

- [ ] **Step 1: Write db.go**

`Connect` opens and pings a pgx pool. `RunMigrations` runs all pending migrations from the embedded FS (defined in Task 12).

```go
// control-plane/internal/storage/db.go
package storage

import (
	"context"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/migrations"
)

// Connect returns a live pgxpool.Pool or an error.
func Connect(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("pgxpool.New: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return pool, nil
}

// RunMigrations applies all pending up-migrations from the embedded SQL files.
// Returns nil if there are no new migrations to apply.
func RunMigrations(dsn string) error {
	d, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return fmt.Errorf("iofs.New: %w", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", d, dsn)
	if err != nil {
		return fmt.Errorf("migrate.New: %w", err)
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("migrate.Up: %w", err)
	}
	return nil
}
```

- [ ] **Step 2: Commit**

```bash
git add control-plane/internal/storage/
git commit -m "feat: add pgx storage package with migration runner"
```

---

### Task 12: Migration SQL Files

**Files:**
- Create: `control-plane/migrations/embed.go`
- Create: `control-plane/migrations/001_create_tenants.up.sql`
- Create: `control-plane/migrations/001_create_tenants.down.sql`
- Create: `control-plane/migrations/002_create_projects.up.sql`
- Create: `control-plane/migrations/002_create_projects.down.sql`
- Create: `control-plane/migrations/003_create_api_tokens.up.sql`
- Create: `control-plane/migrations/003_create_api_tokens.down.sql`
- Create: `control-plane/migrations/004_create_platform_events.up.sql`
- Create: `control-plane/migrations/004_create_platform_events.down.sql`

- [ ] **Step 1: Write migrations/embed.go**

```go
// control-plane/migrations/embed.go
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
```

- [ ] **Step 2: Write migration 001 — tenants**

```sql
-- control-plane/migrations/001_create_tenants.up.sql
CREATE TABLE tenants (
  id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  name       TEXT        NOT NULL UNIQUE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

```sql
-- control-plane/migrations/001_create_tenants.down.sql
DROP TABLE IF EXISTS tenants;
```

- [ ] **Step 3: Write migration 002 — projects**

```sql
-- control-plane/migrations/002_create_projects.up.sql
CREATE TABLE projects (
  id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id  UUID        NOT NULL REFERENCES tenants(id),
  name       TEXT        NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, name)
);
```

```sql
-- control-plane/migrations/002_create_projects.down.sql
DROP TABLE IF EXISTS projects;
```

- [ ] **Step 4: Write migration 003 — api_tokens**

```sql
-- control-plane/migrations/003_create_api_tokens.up.sql
CREATE TABLE api_tokens (
  id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   UUID        NOT NULL REFERENCES tenants(id),
  token_hash  TEXT        NOT NULL UNIQUE,
  description TEXT,
  expires_at  TIMESTAMPTZ,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

```sql
-- control-plane/migrations/003_create_api_tokens.down.sql
DROP TABLE IF EXISTS api_tokens;
```

- [ ] **Step 5: Write migration 004 — platform_events**

```sql
-- control-plane/migrations/004_create_platform_events.up.sql
CREATE TABLE platform_events (
  id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   UUID        NOT NULL REFERENCES tenants(id),
  entity_type TEXT        NOT NULL,
  entity_id   UUID        NOT NULL,
  event_type  TEXT        NOT NULL,
  payload     JSONB,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX ON platform_events (tenant_id, entity_type, entity_id);
```

```sql
-- control-plane/migrations/004_create_platform_events.down.sql
DROP INDEX IF EXISTS platform_events_tenant_id_entity_type_entity_id_idx;
DROP TABLE IF EXISTS platform_events;
```

- [ ] **Step 6: Build to confirm embed compiles**

```bash
cd control-plane
go build ./migrations/... ./internal/storage/...
```

Expected: no output.

- [ ] **Step 7: Commit**

```bash
git add control-plane/migrations/
git commit -m "feat: add base schema migrations (tenants, projects, api_tokens, platform_events)"
```

---

### Task 13: Auth Token Store

**Files:**
- Create: `control-plane/internal/auth/store.go`
- Create: `control-plane/internal/auth/postgres_store.go`

- [ ] **Step 1: Write store.go**

```go
// control-plane/internal/auth/store.go
package auth

import "context"

// TokenStore looks up a plaintext bearer token and returns the owning tenant ID.
// Returns an error if the token is not found or is expired.
type TokenStore interface {
	FindToken(ctx context.Context, plaintext string) (tenantID string, err error)
}
```

- [ ] **Step 2: Write postgres_store.go**

Scans all non-expired tokens and bcrypt-compares each one. Acceptable for v1 with a small number of API tokens.

```go
// control-plane/internal/auth/postgres_store.go
package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

// PostgresTokenStore implements TokenStore against the api_tokens table.
type PostgresTokenStore struct {
	db *pgxpool.Pool
}

// NewPostgresTokenStore creates a PostgresTokenStore backed by the given pool.
func NewPostgresTokenStore(db *pgxpool.Pool) *PostgresTokenStore {
	return &PostgresTokenStore{db: db}
}

// FindToken scans api_tokens for a row whose token_hash matches the plaintext
// via bcrypt. Returns the tenant_id of the first matching, non-expired token.
func (s *PostgresTokenStore) FindToken(ctx context.Context, plaintext string) (string, error) {
	rows, err := s.db.Query(ctx,
		`SELECT tenant_id::text, token_hash, expires_at FROM api_tokens`)
	if err != nil {
		return "", fmt.Errorf("query api_tokens: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var tenantID, hash string
		var expiresAt *time.Time
		if err := rows.Scan(&tenantID, &hash, &expiresAt); err != nil {
			continue
		}
		if expiresAt != nil && time.Now().After(*expiresAt) {
			continue
		}
		if bcrypt.CompareHashAndPassword([]byte(hash), []byte(plaintext)) == nil {
			return tenantID, nil
		}
	}
	return "", fmt.Errorf("token not found")
}
```

- [ ] **Step 3: Build**

```bash
cd control-plane
go build ./internal/auth/...
```

Expected: no output.

- [ ] **Step 4: Commit**

```bash
git add control-plane/internal/auth/store.go control-plane/internal/auth/postgres_store.go
git commit -m "feat: add TokenStore interface and postgres implementation"
```

---

### Task 14: Auth Middleware (TDD)

**Files:**
- Create: `control-plane/internal/auth/middleware.go`
- Create: `control-plane/internal/auth/middleware_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// control-plane/internal/auth/middleware_test.go
package auth_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/auth"
)

type mockStore struct {
	tenantID string
	err      error
}

func (m *mockStore) FindToken(_ context.Context, _ string) (string, error) {
	return m.tenantID, m.err
}

func TestTokenAuth_NoHeader(t *testing.T) {
	store := &mockStore{err: fmt.Errorf("not found")}
	handler := auth.TokenAuth(store)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/anything", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestTokenAuth_InvalidToken(t *testing.T) {
	store := &mockStore{err: fmt.Errorf("token not found")}
	handler := auth.TokenAuth(store)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/anything", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestTokenAuth_ValidToken(t *testing.T) {
	store := &mockStore{tenantID: "tenant-abc"}
	var gotTenantID string
	handler := auth.TokenAuth(store)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTenantID = auth.TenantIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/anything", nil)
	req.Header.Set("Authorization", "Bearer valid-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if gotTenantID != "tenant-abc" {
		t.Fatalf("expected tenant-abc in context, got %q", gotTenantID)
	}
}
```

- [ ] **Step 2: Run the tests — verify they fail**

```bash
cd control-plane
go test ./internal/auth/...
```

Expected: compilation error — `auth.TokenAuth` and `auth.TenantIDFromContext` are not defined yet.

- [ ] **Step 3: Write middleware.go**

```go
// control-plane/internal/auth/middleware.go
package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

type contextKey string

const tenantIDKey contextKey = "tenant_id"

// TokenAuth returns a middleware that validates the Bearer token in the
// Authorization header using the provided TokenStore. On success it injects
// the tenant ID into the request context. On failure it returns 401.
func TokenAuth(store TokenStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := extractBearer(r)
			if token == "" {
				writeUnauthorized(w, "missing or malformed Authorization header")
				return
			}

			tenantID, err := store.FindToken(r.Context(), token)
			if err != nil {
				writeUnauthorized(w, "invalid or expired token")
				return
			}

			ctx := context.WithValue(r.Context(), tenantIDKey, tenantID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// TenantIDFromContext extracts the tenant ID injected by TokenAuth.
// Returns "" if not present.
func TenantIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(tenantIDKey).(string)
	return v
}

func extractBearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(h, "Bearer ")
}

func writeUnauthorized(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	json.NewEncoder(w).Encode(map[string]string{"error": msg}) //nolint:errcheck
}
```

- [ ] **Step 4: Run the tests — verify they pass**

```bash
cd control-plane
go test ./internal/auth/... -v
```

Expected:
```
--- PASS: TestTokenAuth_NoHeader (0.00s)
--- PASS: TestTokenAuth_InvalidToken (0.00s)
--- PASS: TestTokenAuth_ValidToken (0.00s)
PASS
```

- [ ] **Step 5: Commit**

```bash
git add control-plane/internal/auth/middleware.go control-plane/internal/auth/middleware_test.go
git commit -m "feat: add token auth middleware with tests"
```

---

### Task 15: Health Handlers (TDD)

**Files:**
- Create: `control-plane/internal/api/health.go`
- Create: `control-plane/internal/api/health_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// control-plane/internal/api/health_test.go
package api_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/api"
)

type mockPinger struct{ err error }

func (m *mockPinger) Ping(_ context.Context) error { return m.err }

func TestLivenessHandler(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	api.LivenessHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestReadinessHandler_DBUp(t *testing.T) {
	handler := api.ReadinessHandler(&mockPinger{})

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestReadinessHandler_DBDown(t *testing.T) {
	handler := api.ReadinessHandler(&mockPinger{err: errors.New("connection refused")})

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}
```

- [ ] **Step 2: Run the tests — verify they fail**

```bash
cd control-plane
go test ./internal/api/...
```

Expected: compilation error — `api.LivenessHandler`, `api.ReadinessHandler`, `api.Pinger` not defined.

- [ ] **Step 3: Write health.go**

```go
// control-plane/internal/api/health.go
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// Pinger is satisfied by *pgxpool.Pool and any mock that checks DB connectivity.
type Pinger interface {
	Ping(ctx context.Context) error
}

// LivenessHandler always returns 200. Used as the /healthz liveness probe.
func LivenessHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"}) //nolint:errcheck
}

// ReadinessHandler returns a handler that pings the DB.
// Returns 200 if the DB is reachable, 503 otherwise.
func ReadinessHandler(db Pinger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		w.Header().Set("Content-Type", "application/json")

		if err := db.Ping(ctx); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{"status": "unavailable", "error": err.Error()}) //nolint:errcheck
			return
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"}) //nolint:errcheck
	}
}
```

- [ ] **Step 4: Run the tests — verify they pass**

```bash
cd control-plane
go test ./internal/api/... -v
```

Expected:
```
--- PASS: TestLivenessHandler (0.00s)
--- PASS: TestReadinessHandler_DBUp (0.00s)
--- PASS: TestReadinessHandler_DBDown (0.00s)
PASS
```

- [ ] **Step 5: Commit**

```bash
git add control-plane/internal/api/health.go control-plane/internal/api/health_test.go
git commit -m "feat: add health handlers (liveness, readiness) with tests"
```

---

### Task 16: Router + Main Entrypoint

**Files:**
- Create: `control-plane/internal/api/router.go`
- Create: `control-plane/cmd/server/main.go`

- [ ] **Step 1: Write router.go**

```go
// control-plane/internal/api/router.go
package api

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/auth"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/observability"
)

// NewRouter builds and returns the chi router with all middleware and routes attached.
func NewRouter(db *pgxpool.Pool, logger *slog.Logger) http.Handler {
	r := chi.NewRouter()

	// Public routes — no auth required
	r.Get("/healthz", LivenessHandler)
	r.Get("/readyz", ReadinessHandler(db))

	// Protected routes — auth + logging middleware
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequestID)
		r.Use(observability.RequestLogger(logger))
		r.Use(auth.TokenAuth(auth.NewPostgresTokenStore(db)))
		// Phase 1+ handlers are registered here
	})

	return r
}
```

- [ ] **Step 2: Write cmd/server/main.go**

```go
// control-plane/cmd/server/main.go
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/api"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/observability"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/storage"
)

func main() {
	logger := observability.NewLogger(os.Getenv("LOG_LEVEL"))
	slog.SetDefault(logger)

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		slog.Error("DATABASE_URL environment variable is required")
		os.Exit(1)
	}

	pool, err := storage.Connect(context.Background(), dbURL)
	if err != nil {
		slog.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := storage.RunMigrations(dbURL); err != nil {
		slog.Error("failed to run migrations", "error", err)
		os.Exit(1)
	}
	slog.Info("migrations applied")

	port := os.Getenv("SERVER_PORT")
	if port == "" {
		port = "8080"
	}

	r := api.NewRouter(pool, logger)
	slog.Info("server starting", "port", port)
	if err := http.ListenAndServe(fmt.Sprintf(":%s", port), r); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
```

- [ ] **Step 3: Build the binary**

```bash
cd control-plane
go build ./cmd/server/...
```

Expected: no output. Binary produced at `control-plane/server` (or `server.exe`).

- [ ] **Step 4: Run all unit tests**

```bash
cd control-plane
go test ./internal/...
```

Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add control-plane/internal/api/router.go control-plane/cmd/server/main.go
git commit -m "feat: add chi router and server entrypoint"
```

---

### Task 17: Verify Layer 2 Against Live Stack

**Prerequisites:** Layer 1 stack running (`make local-up` completed). Port-forward Postgres to localhost.

- [ ] **Step 1: Port-forward PostgreSQL**

```bash
kubectl port-forward svc/postgres -n aiplatform 5432:5432 &
PF_PID=$!
```

- [ ] **Step 2: Run the server locally**

```bash
cd control-plane
DATABASE_URL="postgres://aiplatform:aiplatform@localhost:5432/aiplatform" \
SERVER_PORT=8080 \
LOG_LEVEL=debug \
go run ./cmd/server/
```

Expected JSON log lines: `"migrations applied"` and `"server starting"`.

- [ ] **Step 3: Verify /healthz**

In a new terminal:

```bash
curl -s http://localhost:8080/healthz | jq .
```

Expected:
```json
{"status":"ok"}
```

- [ ] **Step 4: Verify /readyz**

```bash
curl -s http://localhost:8080/readyz | jq .
```

Expected:
```json
{"status":"ok"}
```

- [ ] **Step 5: Verify auth rejects unauthenticated requests**

```bash
curl -s -o /dev/null -w "%{http_code}" http://localhost:8080/v1/anything
```

Expected: `401`

- [ ] **Step 6: Verify migrations ran**

```bash
psql "postgres://aiplatform:aiplatform@localhost:5432/aiplatform" \
  -c "\dt"
```

Expected tables: `schema_migrations`, `tenants`, `projects`, `api_tokens`, `platform_events`.

- [ ] **Step 7: Verify auth accepts a valid token**

Insert a test tenant and bcrypt-hashed token, then confirm a request passes auth. Run in psql:

Generate the bcrypt hash with a small Go script:

```bash
cd control-plane
cat > /tmp/hashtoken.go << 'EOF'
package main

import (
	"fmt"
	"golang.org/x/crypto/bcrypt"
)

func main() {
	h, _ := bcrypt.GenerateFromPassword([]byte("test-secret-token"), bcrypt.DefaultCost)
	fmt.Println(string(h))
}
EOF
go run /tmp/hashtoken.go
```

Copy the printed hash, then insert into DB:

```bash
psql "postgres://aiplatform:aiplatform@localhost:5432/aiplatform" << 'SQL'
INSERT INTO tenants (name) VALUES ('test-tenant') RETURNING id;
SQL
# Copy the returned UUID, then:
psql "postgres://aiplatform:aiplatform@localhost:5432/aiplatform" \
  -c "INSERT INTO api_tokens (tenant_id, token_hash, description) VALUES ('<uuid>', '<hash>', 'dev token')"
```

Then test:

```bash
curl -s -o /dev/null -w "%{http_code}" \
  -H "Authorization: Bearer test-secret-token" \
  http://localhost:8080/v1/anything
```

Expected: `404` (route not found — but not 401, meaning auth passed).

- [ ] **Step 8: Stop the server and port-forward**

```bash
kill $PF_PID
# Ctrl-C the server
```

- [ ] **Step 9: Final commit**

```bash
git add .
git commit -m "feat: Phase 0 foundation complete — local stack + control plane skeleton"
```

---

## Phase 0 Success Criteria Checklist

- [ ] `make local-up` completes without errors on WSL2 + Docker Desktop
- [ ] All pods in `aiplatform` namespace reach `Running`/`Completed`
- [ ] MLflow UI accessible at `http://localhost:5000`
- [ ] `GET /healthz` → `200 {"status":"ok"}`
- [ ] `GET /readyz` → `200 {"status":"ok"}` when DB is up
- [ ] `GET /v1/*` without token → `401`
- [ ] `GET /v1/*` with valid token → `404` (not 401)
- [ ] All four migrations applied cleanly
- [ ] `go test ./internal/...` passes with no live DB
