# Phase 4: Observability and Reliability — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add production-grade structured logging, Prometheus metrics, Grafana dashboards, job retry logic, deployment rollback, events/quota APIs, operator hardening, and a failure test suite.

**Architecture:** Observability-first — instrumentation is woven in before new behaviors (retry, rollback) are added so every operation is visible from day one. Structured logging uses `log/slog` context propagation. Metrics are registered centrally in `observability/metrics.go` and consumed at the call site. Grafana dashboards are provisioned automatically via kubernetes manifests consistent with the existing local stack pattern.

**Tech Stack:** `prometheus/client_golang`, `log/slog` (already in use), chi middleware, PostgreSQL (platform_events dual-write), controller-runtime leader election, kubernetes manifests for Prometheus + Grafana.

**Module path:** `github.com/Weilei424/kubernetes-native-ai-platform/control-plane`

---

## File Map

### New files
| File | Purpose |
|---|---|
| `control-plane/internal/observability/metrics.go` | Prometheus metric registrations + Prometheus middleware |
| `control-plane/internal/events/store.go` | `WriteEvent` + `ListEvents` against `platform_events` table |
| `control-plane/internal/api/events.go` | `GET /v1/events` handler |
| `control-plane/internal/api/quota.go` | `GET /v1/quota` handler |
| `control-plane/migrations/013_job_retry.up.sql` | Add `retry_count`, `max_retries` to `training_jobs` |
| `control-plane/migrations/013_job_retry.down.sql` | Drop those columns |
| `operator/internal/config/config.go` | Operator config struct with local/prod profiles |
| `operator/config/local.yaml` | Local profile defaults |
| `operator/config/prod.yaml` | Prod profile defaults |
| `infra/local/manifests/prometheus/configmap.yaml` | Prometheus scrape config |
| `infra/local/manifests/prometheus/deployment.yaml` | Prometheus deployment |
| `infra/local/manifests/prometheus/service.yaml` | Prometheus NodePort service |
| `infra/local/manifests/grafana/configmap-datasource.yaml` | Grafana datasource provisioning |
| `infra/local/manifests/grafana/configmap-dashboards.yaml` | Grafana dashboard provisioning pointer |
| `infra/local/manifests/grafana/configmap-dashboard-files.yaml` | Four dashboard JSON files as ConfigMap |
| `infra/local/manifests/grafana/deployment.yaml` | Grafana deployment |
| `infra/local/manifests/grafana/service.yaml` | Grafana NodePort service |

### Modified files
| File | Change |
|---|---|
| `control-plane/internal/observability/middleware.go` | Inject logger with request_id into context |
| `control-plane/internal/observability/logger.go` | Add `FromContext` helper |
| `control-plane/internal/api/router.go` | Add `/metrics`, events, quota, rollback routes |
| `control-plane/internal/api/deployments.go` | Add `Rollback` to interface + `handleRollback` |
| `control-plane/internal/api/internal.go` | Dual-write to `platform_events` on state transitions |
| `control-plane/internal/deployments/model.go` | Add `RollbackRequest` type |
| `control-plane/internal/deployments/store.go` | Add `CreateRevision`, `GetRevision`, `GetCurrentRevisionNumber`, `RollbackDeployment` |
| `control-plane/internal/deployments/service.go` | Add `Rollback` method |
| `control-plane/internal/jobs/model.go` | Add `RetryCount`, `MaxRetries` to `TrainingJob` |
| `control-plane/internal/jobs/statemachine.go` | Add `FAILED → QUEUED` retry transition |
| `control-plane/internal/jobs/store.go` | Add `IncrementRetryCount`, `CreateRetryRun` to interface + impl; update all SELECT queries to include retry columns; update `scanJob` |
| `control-plane/internal/api/jobs.go` | Emit `job_admission_failures_total` on quota rejection |
| `control-plane/cmd/server/main.go` | Register Prometheus metrics, expose `/metrics` |
| `operator/cmd/operator/main.go` | Wire config struct, leader election, namespace, metrics port |
| `infra/local/Makefile` | Apply Prometheus + Grafana manifests in `local-up` |

---

## Task 1: Structured logging — context propagation

**Files:**
- Modify: `control-plane/internal/observability/logger.go`
- Modify: `control-plane/internal/observability/middleware.go`
- Test: `control-plane/internal/observability/` (manual curl verification)

- [ ] **Step 1: Write the failing test**

Create `control-plane/internal/observability/logger_test.go`:

```go
package observability_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/observability"
)

func TestFromContext_ReturnsLoggerWithRequestID(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(observability.RequestLogger(logger))
	r.Get("/test", func(w http.ResponseWriter, r *http.Request) {
		l := observability.FromContext(r.Context())
		l.Info("hello", "key", "val")
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	var entry map[string]any
	// Find the "hello" log line
	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		if err := json.Unmarshal(line, &entry); err == nil {
			if entry["msg"] == "hello" {
				break
			}
		}
	}
	if entry["msg"] != "hello" {
		t.Fatal("hello log line not found")
	}
	if entry["request_id"] == nil || entry["request_id"] == "" {
		t.Errorf("expected request_id in log entry, got: %v", entry)
	}
}

func TestFromContext_FallsBackToDefault(t *testing.T) {
	l := observability.FromContext(context.Background())
	if l == nil {
		t.Fatal("expected non-nil logger from empty context")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd control-plane && go test ./internal/observability/... -run TestFromContext -v
```
Expected: FAIL — `observability.FromContext` undefined.

- [ ] **Step 3: Add `FromContext` and context key to `logger.go`; inject into middleware**

Replace `control-plane/internal/observability/logger.go` with:

```go
// control-plane/internal/observability/logger.go
package observability

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

type contextKey struct{}

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

// WithLogger returns a new context carrying the given logger.
func WithLogger(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, contextKey{}, logger)
}

// FromContext returns the logger stored in ctx, or slog.Default() if none.
func FromContext(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(contextKey{}).(*slog.Logger); ok && l != nil {
		return l
	}
	return slog.Default()
}
```

Replace `control-plane/internal/observability/middleware.go` with:

```go
// control-plane/internal/observability/middleware.go
package observability

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

// RequestLogger returns a chi-compatible middleware that logs each request
// and injects a request-scoped logger into the context.
func RequestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			reqID := middleware.GetReqID(r.Context())
			reqLogger := logger.With("request_id", reqID)
			r = r.WithContext(WithLogger(r.Context(), reqLogger))

			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)

			reqLogger.Info("request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.Status(),
				"latency_ms", time.Since(start).Milliseconds(),
			)
		})
	}
}
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
cd control-plane && go test ./internal/observability/... -run TestFromContext -v
```
Expected: PASS.

- [ ] **Step 5: Verify the full package still builds**

```bash
cd control-plane && go build ./... && go vet ./...
```

- [ ] **Step 6: Commit**

```
control-plane/internal/observability/logger.go — add WithLogger/FromContext context helpers
control-plane/internal/observability/middleware.go — inject request-scoped logger into context
control-plane/internal/observability/logger_test.go — test FromContext propagation
```

---

## Task 2: Prometheus metrics — register and expose `/metrics`

**Files:**
- Create: `control-plane/internal/observability/metrics.go`
- Modify: `control-plane/cmd/server/main.go`
- Modify: `control-plane/internal/api/router.go`
- Test: `control-plane/internal/observability/metrics_test.go`

- [ ] **Step 1: Write the failing test**

Create `control-plane/internal/observability/metrics_test.go`:

```go
package observability_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/observability"
)

func TestPrometheusMiddleware_RecordsLatency(t *testing.T) {
	handler := observability.PrometheusMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/jobs", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	// No panic = metric recorded successfully.
}

func TestMetricsHandler_Responds(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	observability.MetricsHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "go_") {
		t.Error("expected prometheus metrics in response body")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd control-plane && go test ./internal/observability/... -run TestPrometheus -run TestMetrics -v
```
Expected: FAIL — `PrometheusMiddleware` and `MetricsHandler` undefined.

- [ ] **Step 3: Add `prometheus/client_golang` dependency**

```bash
cd control-plane && go get github.com/prometheus/client_golang@latest
```

- [ ] **Step 4: Create `control-plane/internal/observability/metrics.go`**

```go
// control-plane/internal/observability/metrics.go
package observability

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	// HTTPRequestDuration tracks API handler latency.
	HTTPRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "Duration of HTTP requests in seconds.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "path", "status_code"})

	// JobQueueDepth is the number of PENDING + QUEUED jobs across all tenants.
	JobQueueDepth = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "job_queue_depth",
		Help: "Number of jobs in PENDING or QUEUED state.",
	})

	// JobAdmissionFailures counts jobs rejected at submission time.
	JobAdmissionFailures = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "job_admission_failures_total",
		Help: "Total number of jobs rejected at admission.",
	}, []string{"reason"})

	// TrainingRunDuration tracks the wall-clock duration of training runs.
	TrainingRunDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "training_run_duration_seconds",
		Help:    "Duration of completed training runs in seconds.",
		Buckets: []float64{10, 30, 60, 120, 300, 600, 1800, 3600},
	})

	// TrainingRunRetries counts automatic retries triggered by the platform.
	TrainingRunRetries = promauto.NewCounter(prometheus.CounterOpts{
		Name: "training_run_retry_total",
		Help: "Total number of automatic training run retries.",
	})

	// DeploymentCount tracks deployments by status.
	DeploymentCount = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "deployment_count",
		Help: "Number of deployments by status.",
	}, []string{"status"})
)

// PrometheusMiddleware records HTTP request duration for each handler.
func PrometheusMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		HTTPRequestDuration.WithLabelValues(
			r.Method,
			r.URL.Path,
			strconv.Itoa(rw.status),
		).Observe(time.Since(start).Seconds())
	})
}

// MetricsHandler returns the promhttp handler for the /metrics endpoint.
func MetricsHandler() http.Handler {
	return promhttp.Handler()
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (sw *statusWriter) WriteHeader(status int) {
	sw.status = status
	sw.ResponseWriter.WriteHeader(status)
}
```

- [ ] **Step 5: Add `/metrics` route to router and wire middleware in `main.go`**

In `control-plane/internal/api/router.go`, add the metrics handler and middleware. Replace `NewRouter` with:

```go
func NewRouter(db *pgxpool.Pool, store jobs.Store, publisher events.Publisher, modelsSvc ModelsService, deploymentsSvc DeploymentsService) http.Handler {
	r := chi.NewRouter()

	r.Get("/healthz", LivenessHandler)
	r.Get("/readyz", ReadinessHandler(db))
	r.Handle("/metrics", observability.MetricsHandler())

	logger := slog.Default()

	jh := &jobsHandler{store: store, publisher: publisher}

	var mh *modelsHandler
	if modelsSvc != nil {
		mh = &modelsHandler{svc: modelsSvc}
	}

	var dh *deploymentsHandler
	if deploymentsSvc != nil {
		dh = &deploymentsHandler{svc: deploymentsSvc}
	}

	r.Group(func(r chi.Router) {
		r.Use(middleware.RequestID)
		r.Use(observability.RequestLogger(logger))
		r.Use(observability.PrometheusMiddleware)
		r.Use(auth.TokenAuth(auth.NewPostgresTokenStore(db)))

		r.Post("/v1/jobs", jh.handleSubmitJob)
		r.Get("/v1/jobs", jh.handleListJobs)
		r.Get("/v1/jobs/{id}", jh.handleGetJob)
		r.Get("/v1/jobs/{id}/runs/{run_id}", jh.handleGetRun)

		if mh != nil {
			r.Post("/v1/models", mh.handleRegister)
			r.Get("/v1/models/{name}", mh.handleGetModel)
			r.Get("/v1/models/{name}/versions/{version}", mh.handleGetModelVersion)
			r.Post("/v1/models/{name}/versions/{version}/promote", mh.handlePromote)
			r.Get("/v1/models/{name}/alias/{alias}", mh.handleResolveAlias)
		}

		if dh != nil {
			r.Post("/v1/deployments", dh.handleCreate)
			r.Get("/v1/deployments/{id}", dh.handleGet)
			r.Delete("/v1/deployments/{id}", dh.handleDelete)
		}
	})

	return r
}
```

- [ ] **Step 6: Run the tests**

```bash
cd control-plane && go test ./internal/observability/... -v
```
Expected: all PASS.

```bash
cd control-plane && go build ./... && go vet ./...
```

- [ ] **Step 7: Commit**

```
control-plane/internal/observability/metrics.go — Prometheus metric definitions + middleware
control-plane/internal/observability/metrics_test.go — test PrometheusMiddleware and MetricsHandler
control-plane/internal/api/router.go — add /metrics route + PrometheusMiddleware
control-plane/go.mod / go.sum — add prometheus/client_golang
```

---

## Task 3: Wire queue-depth and admission-failure metrics

**Files:**
- Modify: `control-plane/internal/jobs/dispatcher.go`
- Modify: `control-plane/internal/api/jobs.go`
- Test: existing unit tests must still pass

- [ ] **Step 1: Add queue-depth gauge updates in the dispatcher tick**

In `control-plane/internal/jobs/dispatcher.go`, add the import and update at the top of `tick`:

```go
import (
    // existing imports …
    "github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/observability"
)
```

At the start of `tick`, after `tenantIDs` are fetched, count PENDING/QUEUED rows and set the gauge. The simplest approach is to set it inside `promoteOldestPending` after each promotion, but for an accurate total, add a helper call at the end of `tick`:

In the `tick` method, at the very end after the per-tenant loop:

```go
func (d *Dispatcher) tick(ctx context.Context) {
	d.retryQueuedWithoutRayJob(ctx)

	tenantIDs, err := d.store.GetTenantIDsWithPendingJobs(ctx)
	if err != nil {
		slog.Error("dispatcher: list pending tenants", "error", err)
		return
	}
	for _, tenantID := range tenantIDs {
		d.promoteOldestPending(ctx, tenantID)
	}

	// Update queue depth metric: count of jobs in PENDING or QUEUED state.
	depth, err := d.store.CountQueuedJobs(ctx)
	if err != nil {
		slog.Warn("dispatcher: count queued jobs for metric", "error", err)
	} else {
		observability.JobQueueDepth.Set(float64(depth))
	}
}
```

- [ ] **Step 2: Add `CountQueuedJobs` to the jobs `Store` interface and `PostgresJobStore`**

In `control-plane/internal/jobs/store.go`, add to the `Store` interface:

```go
// CountQueuedJobs returns the total number of PENDING + QUEUED jobs across all tenants.
CountQueuedJobs(ctx context.Context) (int, error)
```

Add implementation on `PostgresJobStore`:

```go
func (s *PostgresJobStore) CountQueuedJobs(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM training_jobs WHERE status IN ('PENDING', 'QUEUED')`).Scan(&count)
	return count, err
}
```

- [ ] **Step 3: Emit `job_admission_failures_total` in the jobs handler**

In `control-plane/internal/api/jobs.go`, find the quota check rejection (where `quotaErr != nil`) and add after writing the 422 response:

```go
import (
    // existing …
    "github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/observability"
)
```

In `handleSubmitJob`, when `quotaErr != nil`:

```go
if quotaErr != nil {
    observability.JobAdmissionFailures.WithLabelValues("quota_exceeded").Inc()
    writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": quotaErr.Error()})
    return
}
```

Also emit on admission validation failure (the `scheduler.Admit` rejection):

```go
if err := scheduler.Admit(admReq); err != nil {
    observability.JobAdmissionFailures.WithLabelValues("invalid_spec").Inc()
    writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
    return
}
```

- [ ] **Step 4: Verify tests still pass**

```bash
cd control-plane && go test ./... && go vet ./...
```
Expected: all PASS.

- [ ] **Step 5: Commit**

```
control-plane/internal/jobs/store.go — add CountQueuedJobs to interface + PostgresJobStore
control-plane/internal/jobs/dispatcher.go — set JobQueueDepth gauge each tick
control-plane/internal/api/jobs.go — emit JobAdmissionFailures counter on quota and spec rejection
```

---

## Task 4: Prometheus + Grafana local stack wiring

**Files:**
- Create: `infra/local/manifests/prometheus/configmap.yaml`
- Create: `infra/local/manifests/prometheus/deployment.yaml`
- Create: `infra/local/manifests/prometheus/service.yaml`
- Create: `infra/local/manifests/grafana/configmap-datasource.yaml`
- Create: `infra/local/manifests/grafana/configmap-dashboards.yaml`
- Create: `infra/local/manifests/grafana/configmap-dashboard-files.yaml`
- Create: `infra/local/manifests/grafana/deployment.yaml`
- Create: `infra/local/manifests/grafana/service.yaml`
- Modify: `infra/local/Makefile`

Note: The control plane runs on the host machine. Inside the kind cluster, `host.docker.internal` resolves to the host. Prometheus scrapes the control plane at `host.docker.internal:8080`.

- [ ] **Step 1: Create Prometheus ConfigMap**

Create `infra/local/manifests/prometheus/configmap.yaml`:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: prometheus-config
  namespace: aiplatform
data:
  prometheus.yml: |
    global:
      scrape_interval: 15s

    scrape_configs:
      - job_name: control-plane
        static_configs:
          - targets: ['host.docker.internal:8080']

      - job_name: operator
        static_configs:
          - targets: ['host.docker.internal:8082']

      - job_name: triton
        kubernetes_sd_configs:
          - role: pod
            namespaces:
              names: [aiplatform]
        relabel_configs:
          - source_labels: [__meta_kubernetes_pod_label_app]
            action: keep
            regex: triton
          - source_labels: [__meta_kubernetes_pod_ip]
            target_label: __address__
            replacement: $1:8002
```

- [ ] **Step 2: Create Prometheus Deployment + Service**

Create `infra/local/manifests/prometheus/deployment.yaml`:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: prometheus
  namespace: aiplatform
spec:
  replicas: 1
  selector:
    matchLabels:
      app: prometheus
  template:
    metadata:
      labels:
        app: prometheus
    spec:
      serviceAccountName: default
      containers:
        - name: prometheus
          image: prom/prometheus:v2.51.0
          args:
            - '--config.file=/etc/prometheus/prometheus.yml'
            - '--storage.tsdb.retention.time=2h'
          ports:
            - containerPort: 9090
          volumeMounts:
            - name: config
              mountPath: /etc/prometheus
      volumes:
        - name: config
          configMap:
            name: prometheus-config
```

Create `infra/local/manifests/prometheus/service.yaml`:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: prometheus
  namespace: aiplatform
spec:
  type: NodePort
  selector:
    app: prometheus
  ports:
    - port: 9090
      targetPort: 9090
      nodePort: 30090
```

- [ ] **Step 3: Create Grafana ConfigMaps**

Create `infra/local/manifests/grafana/configmap-datasource.yaml`:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: grafana-datasource
  namespace: aiplatform
data:
  prometheus.yaml: |
    apiVersion: 1
    datasources:
      - name: Prometheus
        type: prometheus
        url: http://prometheus:9090
        isDefault: true
```

Create `infra/local/manifests/grafana/configmap-dashboards.yaml`:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: grafana-dashboard-provider
  namespace: aiplatform
data:
  default.yaml: |
    apiVersion: 1
    providers:
      - name: default
        type: file
        options:
          path: /var/lib/grafana/dashboards
```

- [ ] **Step 4: Create dashboard JSON ConfigMap**

Create `infra/local/manifests/grafana/configmap-dashboard-files.yaml` with all four dashboards:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: grafana-dashboards
  namespace: aiplatform
data:
  control-plane.json: |
    {
      "title": "Control Plane",
      "uid": "control-plane",
      "schemaVersion": 38,
      "panels": [
        {
          "type": "timeseries",
          "title": "API Request Rate",
          "gridPos": {"x":0,"y":0,"w":12,"h":8},
          "targets": [{"expr": "rate(http_request_duration_seconds_count[1m])", "legendFormat": "{{method}} {{path}}"}]
        },
        {
          "type": "timeseries",
          "title": "API p99 Latency",
          "gridPos": {"x":12,"y":0,"w":12,"h":8},
          "targets": [{"expr": "histogram_quantile(0.99, rate(http_request_duration_seconds_bucket[5m]))", "legendFormat": "p99 {{path}}"}]
        },
        {
          "type": "timeseries",
          "title": "API Error Rate (5xx)",
          "gridPos": {"x":0,"y":8,"w":12,"h":8},
          "targets": [{"expr": "rate(http_request_duration_seconds_count{status_code=~\"5..\"}[1m])", "legendFormat": "errors"}]
        },
        {
          "type": "stat",
          "title": "Active Deployments",
          "gridPos": {"x":12,"y":8,"w":6,"h":4},
          "targets": [{"expr": "sum(deployment_count{status=\"running\"})"}]
        }
      ]
    }
  training.json: |
    {
      "title": "Training",
      "uid": "training",
      "schemaVersion": 38,
      "panels": [
        {
          "type": "timeseries",
          "title": "Job Queue Depth",
          "gridPos": {"x":0,"y":0,"w":12,"h":8},
          "targets": [{"expr": "job_queue_depth", "legendFormat": "queue depth"}]
        },
        {
          "type": "timeseries",
          "title": "Admission Failures",
          "gridPos": {"x":12,"y":0,"w":12,"h":8},
          "targets": [{"expr": "rate(job_admission_failures_total[5m])", "legendFormat": "{{reason}}"}]
        },
        {
          "type": "timeseries",
          "title": "Run Duration (p50 / p95)",
          "gridPos": {"x":0,"y":8,"w":12,"h":8},
          "targets": [
            {"expr": "histogram_quantile(0.50, rate(training_run_duration_seconds_bucket[10m]))", "legendFormat": "p50"},
            {"expr": "histogram_quantile(0.95, rate(training_run_duration_seconds_bucket[10m]))", "legendFormat": "p95"}
          ]
        },
        {
          "type": "timeseries",
          "title": "Auto-Retries",
          "gridPos": {"x":12,"y":8,"w":12,"h":8},
          "targets": [{"expr": "rate(training_run_retry_total[5m])", "legendFormat": "retries/s"}]
        }
      ]
    }
  triton-serving.json: |
    {
      "title": "Triton Serving",
      "uid": "triton-serving",
      "schemaVersion": 38,
      "panels": [
        {
          "type": "timeseries",
          "title": "Inference Request Rate",
          "gridPos": {"x":0,"y":0,"w":12,"h":8},
          "targets": [{"expr": "rate(nv_inference_request_success[1m])", "legendFormat": "{{model}}"}]
        },
        {
          "type": "timeseries",
          "title": "Inference p99 Latency (ms)",
          "gridPos": {"x":12,"y":0,"w":12,"h":8},
          "targets": [{"expr": "nv_inference_request_duration_us / 1000", "legendFormat": "{{model}}"}]
        },
        {
          "type": "timeseries",
          "title": "Inference Failure Rate",
          "gridPos": {"x":0,"y":8,"w":12,"h":8},
          "targets": [{"expr": "rate(nv_inference_request_failure[1m])", "legendFormat": "{{model}}"}]
        }
      ]
    }
  resource.json: |
    {
      "title": "Resource Consumption",
      "uid": "resource",
      "schemaVersion": 38,
      "panels": [
        {
          "type": "timeseries",
          "title": "Deployments by Status",
          "gridPos": {"x":0,"y":0,"w":12,"h":8},
          "targets": [{"expr": "deployment_count", "legendFormat": "{{status}}"}]
        },
        {
          "type": "timeseries",
          "title": "Job Queue Depth",
          "gridPos": {"x":12,"y":0,"w":12,"h":8},
          "targets": [{"expr": "job_queue_depth", "legendFormat": "depth"}]
        }
      ]
    }
```

- [ ] **Step 5: Create Grafana Deployment + Service**

Create `infra/local/manifests/grafana/deployment.yaml`:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: grafana
  namespace: aiplatform
spec:
  replicas: 1
  selector:
    matchLabels:
      app: grafana
  template:
    metadata:
      labels:
        app: grafana
    spec:
      containers:
        - name: grafana
          image: grafana/grafana:10.4.0
          ports:
            - containerPort: 3000
          env:
            - name: GF_AUTH_ANONYMOUS_ENABLED
              value: "true"
            - name: GF_AUTH_ANONYMOUS_ORG_ROLE
              value: Admin
          volumeMounts:
            - name: datasource
              mountPath: /etc/grafana/provisioning/datasources
            - name: dashboard-provider
              mountPath: /etc/grafana/provisioning/dashboards
            - name: dashboard-files
              mountPath: /var/lib/grafana/dashboards
      volumes:
        - name: datasource
          configMap:
            name: grafana-datasource
        - name: dashboard-provider
          configMap:
            name: grafana-dashboard-provider
        - name: dashboard-files
          configMap:
            name: grafana-dashboards
```

Create `infra/local/manifests/grafana/service.yaml`:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: grafana
  namespace: aiplatform
spec:
  type: NodePort
  selector:
    app: grafana
  ports:
    - port: 3000
      targetPort: 3000
      nodePort: 30300
```

- [ ] **Step 6: Update the Makefile to deploy Prometheus + Grafana**

In `infra/local/Makefile`, add after the MLflow rollout block and before the status output:

```makefile
	@echo "==> Deploying Prometheus..."
	kubectl apply -f manifests/prometheus/
	kubectl rollout status deployment/prometheus -n $(NAMESPACE) --timeout=120s
	@echo "==> Deploying Grafana..."
	kubectl apply -f manifests/grafana/
	kubectl rollout status deployment/grafana -n $(NAMESPACE) --timeout=120s
```

Add to `local-status`:

```makefile
	@echo "  Prometheus:   http://localhost:30090"
	@echo "  Grafana:      http://localhost:30300 (admin, no password)"
```

- [ ] **Step 7: Verify manifests are valid YAML**

```bash
find infra/local/manifests/prometheus infra/local/manifests/grafana -name '*.yaml' \
  | xargs -I{} sh -c 'echo "==> {}" && python3 -c "import sys,yaml; yaml.safe_load_all(open(sys.argv[1]))" {}'
```
Expected: no errors.

- [ ] **Step 8: Commit**

```
infra/local/manifests/prometheus/ — Prometheus ConfigMap, Deployment, Service
infra/local/manifests/grafana/ — Grafana Deployments, Services, dashboard + datasource ConfigMaps
infra/local/Makefile — add Prometheus and Grafana to local-up and local-status
```

---

## Task 5: Migration 013 — job retry columns + model update

**Files:**
- Create: `control-plane/migrations/013_job_retry.up.sql`
- Create: `control-plane/migrations/013_job_retry.down.sql`
- Modify: `control-plane/internal/jobs/model.go`
- Modify: `control-plane/internal/jobs/store.go`

- [ ] **Step 1: Create the migration files**

Create `control-plane/migrations/013_job_retry.up.sql`:

```sql
ALTER TABLE training_jobs
  ADD COLUMN retry_count INT NOT NULL DEFAULT 0,
  ADD COLUMN max_retries INT NOT NULL DEFAULT 3;
```

Create `control-plane/migrations/013_job_retry.down.sql`:

```sql
ALTER TABLE training_jobs
  DROP COLUMN retry_count,
  DROP COLUMN max_retries;
```

- [ ] **Step 2: Add fields to `TrainingJob` struct**

In `control-plane/internal/jobs/model.go`, add to `TrainingJob`:

```go
RetryCount int
MaxRetries int
```

- [ ] **Step 3: Update all SELECT queries in `store.go` to include the new columns**

Every query that selects from `training_jobs` uses the same column list. Update every occurrence of the SELECT column list to include `retry_count, max_retries` before the closing SELECT or at the end. The column list currently ends with `rayjob_name, created_at, updated_at`. Change it to `rayjob_name, retry_count, max_retries, created_at, updated_at`.

The affected queries are in: `GetJob`, `GetJobByID`, `ListJobs`, `ListActiveJobs`, `ListNonTerminalJobs`, `GetOldestPendingJob`, `GetQueuedJobsWithoutRayJob`.

For example, `GetJob`:

```go
func (s *PostgresJobStore) GetJob(ctx context.Context, id, tenantID string) (*TrainingJob, error) {
	row := s.db.QueryRow(ctx, `
		SELECT id::text, tenant_id::text, project_id::text, name, status,
		       image, command, args, env, num_workers,
		       worker_cpu, worker_memory, head_cpu, head_memory,
		       rayjob_name, retry_count, max_retries, created_at, updated_at
		FROM training_jobs
		WHERE id = $1 AND tenant_id = $2`,
		id, tenantID,
	)
	return scanJob(row)
}
```

Apply the same change to `GetJobByID`, `ListJobs`, `ListActiveJobs`, `ListNonTerminalJobs`, `GetOldestPendingJob`, `GetQueuedJobsWithoutRayJob`.

Also update `scanJob` to scan the new fields. Change `scanJob` to add `&j.RetryCount, &j.MaxRetries` after `&rayJobName`:

```go
func scanJob(row scannable) (*TrainingJob, error) {
	var j TrainingJob
	var envJSON []byte
	var rayJobName *string
	err := row.Scan(
		&j.ID, &j.TenantID, &j.ProjectID, &j.Name, &j.Status,
		&j.Image, &j.Command, &j.Args, &envJSON, &j.NumWorkers,
		&j.WorkerCPU, &j.WorkerMemory, &j.HeadCPU, &j.HeadMemory,
		&rayJobName, &j.RetryCount, &j.MaxRetries, &j.CreatedAt, &j.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	j.RayJobName = rayJobName
	if len(envJSON) > 0 {
		if err := json.Unmarshal(envJSON, &j.Env); err != nil {
			return nil, fmt.Errorf("unmarshal env: %w", err)
		}
	}
	if j.Env == nil {
		j.Env = map[string]string{}
	}
	return &j, nil
}
```

- [ ] **Step 4: Verify build**

```bash
cd control-plane && go build ./... && go vet ./...
```
Expected: no errors.

- [ ] **Step 5: Run existing tests (migration will apply automatically in integration tests)**

```bash
cd control-plane && go test ./...
```
Expected: all PASS (integration tests run migrations automatically).

- [ ] **Step 6: Commit**

```
control-plane/migrations/013_job_retry.up.sql — add retry_count + max_retries to training_jobs
control-plane/migrations/013_job_retry.down.sql — rollback migration
control-plane/internal/jobs/model.go — add RetryCount, MaxRetries fields to TrainingJob
control-plane/internal/jobs/store.go — update all SELECT queries + scanJob for new columns
```

---

## Task 6: Job retry — state machine + store methods + internal handler trigger

**Files:**
- Modify: `control-plane/internal/jobs/statemachine.go`
- Modify: `control-plane/internal/jobs/store.go`
- Modify: `control-plane/internal/api/internal.go`
- Test: `control-plane/internal/jobs/statemachine_test.go` (add case)
- Test: `control-plane/internal/api/internal_test.go` (add retry test)

- [ ] **Step 1: Write the failing state machine test**

Open `control-plane/internal/jobs/statemachine_test.go` and add:

```go
func TestValidateTransition_FailedToQueued_Allowed(t *testing.T) {
	if err := ValidateTransition("FAILED", "QUEUED"); err != nil {
		t.Errorf("expected FAILED→QUEUED to be valid, got: %v", err)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd control-plane && go test ./internal/jobs/... -run TestValidateTransition_FailedToQueued -v
```
Expected: FAIL.

- [ ] **Step 3: Add the transition to `statemachine.go`**

In `control-plane/internal/jobs/statemachine.go`, update `validTransitions`:

```go
var validTransitions = map[string]map[string]bool{
	"PENDING": {"QUEUED": true, "CANCELLED": true},
	"QUEUED":  {"RUNNING": true, "FAILED": true, "CANCELLED": true},
	"RUNNING": {"SUCCEEDED": true, "FAILED": true, "CANCELLED": true, "QUEUED": true},
	"FAILED":  {"QUEUED": true},
}
```

Note: `RUNNING → QUEUED` already existed for pod eviction/recovery. `FAILED → QUEUED` is new for the retry path.

- [ ] **Step 4: Run the state machine test**

```bash
cd control-plane && go test ./internal/jobs/... -run TestValidateTransition -v
```
Expected: all PASS.

- [ ] **Step 5: Add `IncrementRetryCount` and `CreateRetryRun` to the Store interface**

In `control-plane/internal/jobs/store.go`, add to the `Store` interface:

```go
// IncrementRetryCount increments retry_count for the given job and returns the new count.
IncrementRetryCount(ctx context.Context, jobID string) (newCount int, err error)
// CreateRetryRun inserts a new training_run for the given job (used on automatic retry).
CreateRetryRun(ctx context.Context, jobID, tenantID string) (*TrainingRun, error)
```

Add implementations on `PostgresJobStore`:

```go
func (s *PostgresJobStore) IncrementRetryCount(ctx context.Context, jobID string) (int, error) {
	var newCount int
	err := s.db.QueryRow(ctx,
		`UPDATE training_jobs SET retry_count = retry_count + 1, updated_at = now()
		 WHERE id = $1 RETURNING retry_count`,
		jobID,
	).Scan(&newCount)
	return newCount, err
}

func (s *PostgresJobStore) CreateRetryRun(ctx context.Context, jobID, tenantID string) (*TrainingRun, error) {
	run := &TrainingRun{}
	err := s.db.QueryRow(ctx, `
		INSERT INTO training_runs (job_id, tenant_id, status)
		VALUES ($1, $2, 'QUEUED')
		RETURNING id::text, job_id::text, tenant_id::text, status,
		          mlflow_run_id, started_at, finished_at, failure_reason,
		          created_at, updated_at`,
		jobID, tenantID,
	).Scan(
		&run.ID, &run.JobID, &run.TenantID, &run.Status,
		&run.MLflowRunID, &run.StartedAt, &run.FinishedAt, &run.FailureReason,
		&run.CreatedAt, &run.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("create retry run: %w", err)
	}
	return run, nil
}
```

- [ ] **Step 6: Add the retry trigger in the internal handler**

In `control-plane/internal/api/internal.go`, update `handleUpdateJobStatus` to trigger retry after a FAILED transition. Replace the Kafka publish block and the final `writeJSON` with:

```go
	// After a successful FAILED transition, check if we should retry.
	if req.Status == "FAILED" {
		newCount, incErr := h.store.IncrementRetryCount(r.Context(), jobID)
		if incErr != nil {
			slog.Warn("internal: increment retry count", "job_id", jobID, "error", incErr)
		} else if newCount <= job.MaxRetries {
			// Re-queue the job for retry.
			if _, runErr := h.store.CreateRetryRun(r.Context(), jobID, job.TenantID); runErr != nil {
				slog.Error("internal: create retry run", "job_id", jobID, "error", runErr)
			} else if transErr := h.store.TransitionJobStatus(r.Context(), jobID, "FAILED", "QUEUED", nil); transErr != nil {
				slog.Error("internal: re-queue job for retry", "job_id", jobID, "error", transErr)
			} else {
				observability.TrainingRunRetries.Inc()
				slog.Info("internal: job re-queued for retry", "job_id", jobID, "retry_count", newCount)
			}
		}
	}

	topic := statusToTopic(req.Status)
	evt := jobs.JobEvent{ /* … same as before … */ }
	// … publish + writeJSON unchanged …
```

The full updated `handleUpdateJobStatus` after the `TransitionJobStatus` call and the MLflowRunID block, replace the final section with the retry check inserted before the Kafka publish.

- [ ] **Step 7: Write an integration test for the retry trigger**

In `control-plane/internal/api/internal_test.go`, add:

```go
func TestInternalHandler_RetryOnFailed(t *testing.T) {
	// Requires a real DB — use testutil.NewTestDB(t)
	db := testutil.NewTestDB(t)
	store := jobs.NewPostgresJobStore(db)
	// Create tenant + project
	tenantID, projectID := testutil.CreateTenantAndProject(t, db)
	// Create a job in RUNNING state
	job := &jobs.TrainingJob{
		TenantID: tenantID, ProjectID: projectID, Name: "retry-test",
		Status: "RUNNING", Image: "img:1", Command: []string{"train"},
		NumWorkers: 1, WorkerCPU: "1", WorkerMemory: "1Gi",
		HeadCPU: "1", HeadMemory: "1Gi",
	}
	run := &jobs.TrainingRun{TenantID: tenantID, Status: "RUNNING"}
	if err := store.CreateJobWithRun(context.Background(), job, run); err != nil {
		t.Fatal(err)
	}
	// Manually set to RUNNING
	if err := store.TransitionJobStatus(context.Background(), job.ID, "PENDING", "QUEUED", nil); err != nil {
		t.Fatal(err)
	}
	if err := store.TransitionJobStatus(context.Background(), job.ID, "QUEUED", "RUNNING", nil); err != nil {
		t.Fatal(err)
	}

	h := api.NewInternalRouter(store, &events.NoOpPublisher{}, nil)
	reason := "pod failed"
	body, _ := json.Marshal(jobs.StatusUpdateRequest{Status: "FAILED", FailureReason: &reason})
	req := httptest.NewRequest(http.MethodPatch, "/internal/v1/jobs/"+job.ID+"/status", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Job should now be QUEUED (re-queued for retry).
	updated, err := store.GetJobByID(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != "QUEUED" {
		t.Errorf("expected QUEUED after retry trigger, got %s", updated.Status)
	}
	if updated.RetryCount != 1 {
		t.Errorf("expected retry_count=1, got %d", updated.RetryCount)
	}
}
```

- [ ] **Step 8: Run tests**

```bash
cd control-plane && go test ./internal/jobs/... ./internal/api/... -v
```
Expected: all PASS.

- [ ] **Step 9: Commit**

```
control-plane/internal/jobs/statemachine.go — add FAILED→QUEUED retry transition
control-plane/internal/jobs/store.go — add IncrementRetryCount, CreateRetryRun
control-plane/internal/api/internal.go — trigger retry on FAILED status update
control-plane/internal/api/internal_test.go — TestInternalHandler_RetryOnFailed
```

---

## Task 7: Deployment rollback — store methods + service + handler

**Files:**
- Modify: `control-plane/internal/deployments/model.go`
- Modify: `control-plane/internal/deployments/store.go`
- Modify: `control-plane/internal/deployments/service.go`
- Modify: `control-plane/internal/api/deployments.go`
- Modify: `control-plane/internal/api/router.go`
- Test: `control-plane/internal/deployments/store_test.go`
- Test: `control-plane/internal/deployments/service_test.go`

- [ ] **Step 1: Add `RollbackRequest` to model.go and extend the Store interface**

In `control-plane/internal/deployments/model.go`, add:

```go
// RollbackRequest is the body for POST /v1/deployments/:id/rollback.
// Revision is optional; 0 means "roll back to the previous revision".
type RollbackRequest struct {
	Revision int `json:"revision"`
}
```

In `control-plane/internal/deployments/store.go`, add to the `Store` interface:

```go
// GetCurrentRevisionNumber returns the highest revision_number for the given deployment.
GetCurrentRevisionNumber(ctx context.Context, deploymentID string) (int, error)
// GetRevision returns a specific revision for a deployment.
GetRevision(ctx context.Context, deploymentID string, revisionNumber int) (*DeploymentRevision, error)
// RollbackDeployment atomically creates revision N+1 mirroring targetModelVersionID,
// updates the deployment's model_version_id, sets status to 'pending', and clears serving_endpoint.
RollbackDeployment(ctx context.Context, deploymentID, targetModelVersionID string) (*Deployment, error)
```

- [ ] **Step 2: Write failing store tests**

In `control-plane/internal/deployments/store_test.go`, add:

```go
func TestStore_RollbackDeployment(t *testing.T) {
	db := testutil.NewTestDB(t)
	store := deployments.NewPostgresDeploymentStore(db)
	tenantID, projectID, modelRecordID, modelVersionID := testutil.CreateModelFixtures(t, db)

	d := &deployments.Deployment{
		TenantID: tenantID, ProjectID: projectID,
		ModelRecordID: modelRecordID, ModelVersionID: modelVersionID,
		Name: "rollback-test", Namespace: "default",
		Status: "running", DesiredReplicas: 1,
	}
	if err := store.CreateDeployment(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	// Simulate a second revision (v2 model version) created separately.
	// Roll back to revision 1 (original modelVersionID).
	rolled, err := store.RollbackDeployment(context.Background(), d.ID, modelVersionID)
	if err != nil {
		t.Fatalf("RollbackDeployment: %v", err)
	}
	if rolled.Status != "pending" {
		t.Errorf("expected pending, got %s", rolled.Status)
	}
	rev, err := store.GetCurrentRevisionNumber(context.Background(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rev != 2 {
		t.Errorf("expected revision 2 after rollback, got %d", rev)
	}
}
```

- [ ] **Step 3: Run to confirm failure**

```bash
cd control-plane && go test ./internal/deployments/... -run TestStore_RollbackDeployment -v
```
Expected: FAIL — methods undefined.

- [ ] **Step 4: Implement store methods on `PostgresDeploymentStore`**

Add to `control-plane/internal/deployments/store.go`:

```go
func (s *PostgresDeploymentStore) GetCurrentRevisionNumber(ctx context.Context, deploymentID string) (int, error) {
	var n int
	err := s.db.QueryRow(ctx,
		`SELECT COALESCE(MAX(revision_number), 0) FROM deployment_revisions WHERE deployment_id = $1::uuid`,
		deploymentID,
	).Scan(&n)
	return n, err
}

func (s *PostgresDeploymentStore) GetRevision(ctx context.Context, deploymentID string, revisionNumber int) (*DeploymentRevision, error) {
	var rev DeploymentRevision
	err := s.db.QueryRow(ctx, `
		SELECT id::text, deployment_id::text, revision_number, model_version_id::text, status, created_at
		FROM deployment_revisions
		WHERE deployment_id = $1::uuid AND revision_number = $2`,
		deploymentID, revisionNumber,
	).Scan(&rev.ID, &rev.DeploymentID, &rev.RevisionNumber, &rev.ModelVersionID, &rev.Status, &rev.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrDeploymentNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get revision: %w", err)
	}
	return &rev, nil
}

func (s *PostgresDeploymentStore) RollbackDeployment(ctx context.Context, deploymentID, targetModelVersionID string) (*Deployment, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// Get next revision number.
	var nextRev int
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(MAX(revision_number), 0) + 1 FROM deployment_revisions WHERE deployment_id = $1::uuid`,
		deploymentID,
	).Scan(&nextRev); err != nil {
		return nil, fmt.Errorf("get next revision: %w", err)
	}

	// Insert new revision.
	if _, err := tx.Exec(ctx, `
		INSERT INTO deployment_revisions (deployment_id, revision_number, model_version_id, status)
		VALUES ($1::uuid, $2, $3::uuid, 'active')`,
		deploymentID, nextRev, targetModelVersionID,
	); err != nil {
		return nil, fmt.Errorf("insert rollback revision: %w", err)
	}

	// Update the deployment.
	var d Deployment
	if err := tx.QueryRow(ctx, `
		UPDATE deployments
		SET model_version_id = $1::uuid, status = 'pending', serving_endpoint = NULL, updated_at = now()
		WHERE id = $2::uuid
		RETURNING id::text, tenant_id::text, project_id::text, model_record_id::text,
		          model_version_id::text, name, namespace, status, desired_replicas,
		          COALESCE(serving_endpoint, ''), created_at, updated_at`,
		targetModelVersionID, deploymentID,
	).Scan(
		&d.ID, &d.TenantID, &d.ProjectID, &d.ModelRecordID,
		&d.ModelVersionID, &d.Name, &d.Namespace, &d.Status, &d.DesiredReplicas,
		&d.ServingEndpoint, &d.CreatedAt, &d.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrDeploymentNotFound
		}
		return nil, fmt.Errorf("update deployment for rollback: %w", err)
	}

	return &d, tx.Commit(ctx)
}
```

- [ ] **Step 5: Add `Rollback` to the service**

In `control-plane/internal/deployments/service.go`, add:

```go
// Rollback rolls a deployment back to a specific revision (or the previous one if revision == 0).
// It creates a new revision that mirrors the target and sets the deployment to pending.
func (s *Service) Rollback(ctx context.Context, id, tenantID string, revision int) (*Deployment, error) {
	d, err := s.store.GetDeployment(ctx, id)
	if err != nil {
		return nil, err
	}
	if d.TenantID != tenantID {
		return nil, ErrDeploymentNotFound
	}

	current, err := s.store.GetCurrentRevisionNumber(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get current revision: %w", err)
	}

	target := revision
	if target == 0 {
		target = current - 1
	}
	if target < 1 {
		return nil, fmt.Errorf("already at revision 1, nothing to roll back to")
	}

	targetRev, err := s.store.GetRevision(ctx, id, target)
	if err != nil {
		return nil, fmt.Errorf("get target revision %d: %w", target, err)
	}

	return s.store.RollbackDeployment(ctx, id, targetRev.ModelVersionID)
}
```

- [ ] **Step 6: Add `Rollback` to the `DeploymentsService` interface and handler**

In `control-plane/internal/api/deployments.go`, add `Rollback` to the interface:

```go
type DeploymentsService interface {
	Create(ctx context.Context, tenantID string, req deployments.CreateDeploymentRequest) (*deployments.Deployment, error)
	Get(ctx context.Context, id, tenantID string) (*deployments.Deployment, error)
	Delete(ctx context.Context, id, tenantID string) error
	Rollback(ctx context.Context, id, tenantID string, revision int) (*deployments.Deployment, error)
}
```

Add handler:

```go
func (h *deploymentsHandler) handleRollback(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDFromContext(r.Context())
	id := chi.URLParam(r, "id")

	var req deployments.RollbackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err.Error() != "EOF" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	dep, err := h.svc.Rollback(r.Context(), id, tenantID, req.Revision)
	if err != nil {
		switch {
		case errors.Is(err, deployments.ErrDeploymentNotFound):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "deployment not found"})
		default:
			slog.Error("rollback deployment", "id", id, "error", err)
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"deployment": dep})
}
```

- [ ] **Step 7: Register the rollback route in router**

In `control-plane/internal/api/router.go`, in the `if dh != nil` block add:

```go
r.Post("/v1/deployments/{id}/rollback", dh.handleRollback)
```

- [ ] **Step 8: Run all tests**

```bash
cd control-plane && go test ./internal/deployments/... ./internal/api/... -v
```
Expected: all PASS.

- [ ] **Step 9: Commit**

```
control-plane/internal/deployments/model.go — add RollbackRequest type
control-plane/internal/deployments/store.go — add GetCurrentRevisionNumber, GetRevision, RollbackDeployment
control-plane/internal/deployments/store_test.go — TestStore_RollbackDeployment
control-plane/internal/deployments/service.go — add Rollback method
control-plane/internal/api/deployments.go — add Rollback to interface + handleRollback handler
control-plane/internal/api/router.go — register POST /v1/deployments/{id}/rollback
```

---

## Task 8: Events dual-write + events store + Events API

**Files:**
- Create: `control-plane/internal/events/store.go`
- Modify: `control-plane/internal/api/internal.go`
- Create: `control-plane/internal/api/events.go`
- Modify: `control-plane/internal/api/router.go`
- Modify: `control-plane/internal/api/internal.go` (add events store)
- Test: `control-plane/internal/api/events_test.go`

- [ ] **Step 1: Create the events store**

Create `control-plane/internal/events/store.go`:

```go
// control-plane/internal/events/store.go
package events

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PlatformEvent mirrors the platform_events table row.
type PlatformEvent struct {
	ID         string          `json:"id"`
	TenantID   string          `json:"tenant_id"`
	EntityType string          `json:"entity_type"`
	EntityID   string          `json:"entity_id"`
	EventType  string          `json:"event_type"`
	Payload    json.RawMessage `json:"payload,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
}

// EventFilter constrains ListEvents queries.
type EventFilter struct {
	EntityType string
	EntityID   string
	Limit      int
	Offset     int
}

// EventStore persists and queries platform events in PostgreSQL.
type EventStore struct {
	db *pgxpool.Pool
}

// NewEventStore returns an EventStore backed by the given pool.
func NewEventStore(db *pgxpool.Pool) *EventStore {
	return &EventStore{db: db}
}

// WriteEvent inserts a single event into platform_events.
func (s *EventStore) WriteEvent(ctx context.Context, e PlatformEvent) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO platform_events (tenant_id, entity_type, entity_id, event_type, payload)
		VALUES ($1::uuid, $2, $3::uuid, $4, $5)`,
		e.TenantID, e.EntityType, e.EntityID, e.EventType, []byte(e.Payload),
	)
	if err != nil {
		return fmt.Errorf("write event: %w", err)
	}
	return nil
}

// ListEvents returns tenant-scoped events, optionally filtered by entity_type and entity_id.
// Returns the matching events and the total count before limit/offset.
func (s *EventStore) ListEvents(ctx context.Context, tenantID string, f EventFilter) ([]*PlatformEvent, int, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}

	args := []any{tenantID}
	where := "tenant_id = $1::uuid"
	i := 2
	if f.EntityType != "" {
		where += fmt.Sprintf(" AND entity_type = $%d", i)
		args = append(args, f.EntityType)
		i++
	}
	if f.EntityID != "" {
		where += fmt.Sprintf(" AND entity_id = $%d::uuid", i)
		args = append(args, f.EntityID)
		i++
	}

	var total int
	if err := s.db.QueryRow(ctx,
		fmt.Sprintf(`SELECT COUNT(*) FROM platform_events WHERE %s`, where),
		args...,
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count events: %w", err)
	}

	args = append(args, f.Limit, f.Offset)
	rows, err := s.db.Query(ctx, fmt.Sprintf(`
		SELECT id::text, tenant_id::text, entity_type, entity_id::text, event_type, payload, created_at
		FROM platform_events
		WHERE %s
		ORDER BY created_at DESC
		LIMIT $%d OFFSET $%d`, where, i, i+1),
		args...,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()

	var result []*PlatformEvent
	for rows.Next() {
		var ev PlatformEvent
		var payload []byte
		if err := rows.Scan(&ev.ID, &ev.TenantID, &ev.EntityType, &ev.EntityID,
			&ev.EventType, &payload, &ev.CreatedAt); err != nil {
			return nil, 0, err
		}
		ev.Payload = payload
		result = append(result, &ev)
	}
	return result, total, rows.Err()
}
```

- [ ] **Step 2: Add dual-write to internal handler**

In `control-plane/internal/api/internal.go`, update `internalHandler` to include the event store and write events on transitions.

Add `eventStore *events.EventStore` field to `internalHandler`:

```go
type internalHandler struct {
	store           jobs.Store
	publisher       events.Publisher
	deploymentStore deployments.Store
	eventStore      *events.EventStore
}
```

Update `NewInternalRouter` signature:

```go
func NewInternalRouter(store jobs.Store, publisher events.Publisher, deploymentStore deployments.Store, eventStore *events.EventStore) http.Handler {
	r := chi.NewRouter()
	h := &internalHandler{store: store, publisher: publisher, deploymentStore: deploymentStore, eventStore: eventStore}
	// … routes unchanged …
}
```

In `handleUpdateJobStatus`, after the successful `TransitionJobStatus` call (and before the retry logic), add:

```go
	if h.eventStore != nil {
		payload, _ := json.Marshal(map[string]any{
			"from": job.Status, "to": req.Status,
			"failure_reason": req.FailureReason,
			"timestamp":      time.Now().UTC().Format(time.RFC3339),
		})
		if err := h.eventStore.WriteEvent(r.Context(), events.PlatformEvent{
			TenantID: job.TenantID, EntityType: "job", EntityID: jobID,
			EventType: req.Status, Payload: payload,
		}); err != nil {
			slog.Warn("internal: write job event", "job_id", jobID, "error", err)
		}
	}
```

Similarly in `handleUpdateDeploymentStatus`, after `UpdateDeploymentStatus` succeeds:

```go
	if h.eventStore != nil {
		payload, _ := json.Marshal(map[string]any{
			"from": current.Status, "to": req.Status,
			"timestamp": time.Now().UTC().Format(time.RFC3339),
		})
		if err := h.eventStore.WriteEvent(r.Context(), events.PlatformEvent{
			TenantID: current.TenantID, EntityType: "deployment", EntityID: id,
			EventType: req.Status, Payload: payload,
		}); err != nil {
			slog.Warn("internal: write deployment event", "deployment_id", id, "error", err)
		}
	}
```

- [ ] **Step 3: Update `main.go` to wire the event store**

In `control-plane/cmd/server/main.go`, add:

```go
eventStore := events.NewEventStore(pool)
internalHandler := api.NewInternalRouter(store, publisher, deploymentStore, eventStore)
```

Also update `NewRouter` call to pass `eventStore` (added in the next step).

- [ ] **Step 4: Create the events API handler**

Create `control-plane/internal/api/events.go`:

```go
// control-plane/internal/api/events.go
package api

import (
	"net/http"
	"strconv"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/auth"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/events"
)

type eventsHandler struct {
	store *events.EventStore
}

func (h *eventsHandler) handleList(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDFromContext(r.Context())
	q := r.URL.Query()

	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))

	evts, total, err := h.store.ListEvents(r.Context(), tenantID, events.EventFilter{
		EntityType: q.Get("entity_type"),
		EntityID:   q.Get("entity_id"),
		Limit:      limit,
		Offset:     offset,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if evts == nil {
		evts = []*events.PlatformEvent{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": evts, "total": total})
}
```

- [ ] **Step 5: Update router to wire events store + route**

In `control-plane/internal/api/router.go`, update `NewRouter` signature:

```go
func NewRouter(db *pgxpool.Pool, store jobs.Store, publisher events.Publisher,
	modelsSvc ModelsService, deploymentsSvc DeploymentsService,
	eventStore *events.EventStore) http.Handler {
```

Add the events handler and route inside the auth group:

```go
eh := &eventsHandler{store: eventStore}
// …
r.Get("/v1/events", eh.handleList)
```

Update `main.go` to pass `eventStore` to `NewRouter`:

```go
r := api.NewRouter(pool, store, publisher, modelsSvc, deploymentsSvc, eventStore)
```

- [ ] **Step 6: Write test for events handler**

Create `control-plane/internal/api/events_test.go`:

```go
package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/api"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/auth"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/events"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/testutil"
)

func TestEventsHandler_ListEmpty(t *testing.T) {
	db := testutil.NewTestDB(t)
	eventStore := events.NewEventStore(db)
	tenantID, _ := testutil.CreateTenantAndProject(t, db)
	token := testutil.CreateToken(t, db, tenantID)

	router := api.NewRouter(db, nil, nil, nil, nil, eventStore)
	req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"events"`) {
		t.Error("expected events key in response")
	}
}
```

- [ ] **Step 7: Run tests**

```bash
cd control-plane && go test ./internal/events/... ./internal/api/... -v
```
Expected: all PASS.

- [ ] **Step 8: Fix any compilation errors in existing test files that call `NewInternalRouter` or `NewRouter` with old signatures**

```bash
cd control-plane && go build ./... 2>&1 | grep -i "too many\|too few\|cannot use"
```
Fix call sites in test files to pass the new parameters (use `nil` for `eventStore` where not needed).

- [ ] **Step 9: Commit**

```
control-plane/internal/events/store.go — WriteEvent + ListEvents against platform_events
control-plane/internal/api/internal.go — dual-write events on job + deployment transitions
control-plane/internal/api/events.go — GET /v1/events handler
control-plane/internal/api/events_test.go — list events integration test
control-plane/internal/api/router.go — add eventStore param + /v1/events route
control-plane/cmd/server/main.go — wire eventStore
```

---

## Task 9: Quota API

**Files:**
- Create: `control-plane/internal/api/quota.go`
- Modify: `control-plane/internal/api/router.go`
- Modify: `control-plane/internal/jobs/store.go`
- Test: `control-plane/internal/api/quota_test.go`

- [ ] **Step 1: Add `GetRunningResourceUsage` to jobs Store**

In `control-plane/internal/jobs/store.go`, add to the `Store` interface:

```go
// GetRunningResourceUsage returns the sum of cpu_request and memory_request
// for all RUNNING training_runs for the tenant.
GetRunningResourceUsage(ctx context.Context, tenantID string) (cpuMillicores, memoryBytes int64, runningJobs int, err error)
```

Add implementation:

```go
func (s *PostgresJobStore) GetRunningResourceUsage(ctx context.Context, tenantID string) (int64, int64, int, error) {
	var cpu, mem int64
	var count int
	err := s.db.QueryRow(ctx, `
		SELECT
			COALESCE(SUM(
				(SELECT SUM(CAST(regexp_replace(worker_cpu,'[^0-9]','','g') AS BIGINT) * num_workers +
				            CAST(regexp_replace(head_cpu,'[^0-9]','','g') AS BIGINT))
				 FROM training_jobs tj2 WHERE tj2.id = tr.job_id)
			), 0),
			COALESCE(SUM(
				(SELECT SUM(CAST(regexp_replace(worker_memory,'[^0-9]','','g') AS BIGINT) * num_workers +
				            CAST(regexp_replace(head_memory,'[^0-9]','','g') AS BIGINT))
				 FROM training_jobs tj2 WHERE tj2.id = tr.job_id)
			), 0),
			COUNT(*)
		FROM training_runs tr
		WHERE tr.tenant_id = $1::uuid AND tr.status = 'RUNNING'`,
		tenantID,
	).Scan(&cpu, &mem, &count)
	return cpu, mem, count, err
}
```

Note: The resource strings (e.g. `"500m"`, `"2Gi"`) are stored as TEXT. Parsing them in SQL via regex is approximate. The quota handler should note this is an approximation; precise quota accounting is handled by the scheduler.

- [ ] **Step 2: Create quota handler**

Create `control-plane/internal/api/quota.go`:

```go
// control-plane/internal/api/quota.go
package api

import (
	"fmt"
	"net/http"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/auth"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/jobs"
)

type quotaHandler struct {
	store jobs.Store
}

func (h *quotaHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDFromContext(r.Context())

	cpuQuota, memQuota, err := h.store.GetTenantQuota(r.Context(), tenantID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	cpuUsed, memUsed, runningJobs, err := h.store.GetRunningResourceUsage(r.Context(), tenantID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"cpu_quota":    fmt.Sprintf("%dm", cpuQuota),
		"memory_quota": fmt.Sprintf("%d", memQuota),
		"cpu_used":     fmt.Sprintf("%dm", cpuUsed),
		"memory_used":  fmt.Sprintf("%d", memUsed),
		"running_jobs": runningJobs,
	})
}
```

- [ ] **Step 3: Register route in router**

In `control-plane/internal/api/router.go`, add inside the auth group:

```go
qh := &quotaHandler{store: store}
r.Get("/v1/quota", qh.handleGet)
```

- [ ] **Step 4: Write test**

Create `control-plane/internal/api/quota_test.go`:

```go
package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/api"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/testutil"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/events"
)

func TestQuotaHandler_HappyPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	store := jobs.NewPostgresJobStore(db)
	tenantID, _ := testutil.CreateTenantAndProject(t, db)
	token := testutil.CreateToken(t, db, tenantID)
	eventStore := events.NewEventStore(db)

	router := api.NewRouter(db, store, &events.NoOpPublisher{}, nil, nil, eventStore)
	req := httptest.NewRequest(http.MethodGet, "/v1/quota", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, key := range []string{"cpu_quota", "memory_quota", "running_jobs"} {
		if !strings.Contains(body, key) {
			t.Errorf("expected %q in response body", key)
		}
	}
}
```

- [ ] **Step 5: Run tests**

```bash
cd control-plane && go test ./internal/api/... ./internal/jobs/... -v
```
Expected: all PASS.

- [ ] **Step 6: Commit**

```
control-plane/internal/jobs/store.go — add GetRunningResourceUsage
control-plane/internal/api/quota.go — GET /v1/quota handler
control-plane/internal/api/quota_test.go — happy path test
control-plane/internal/api/router.go — register /v1/quota route
```

---

## Task 10: Operator config struct + hardening

**Files:**
- Create: `operator/internal/config/config.go`
- Create: `operator/config/local.yaml`
- Create: `operator/config/prod.yaml`
- Modify: `operator/cmd/operator/main.go`

- [ ] **Step 1: Create the operator config package**

Create `operator/internal/config/config.go`:

```go
// operator/internal/config/config.go
package config

import (
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds all operator tunable values.
type Config struct {
	Env             string        `yaml:"env"`              // "local" or "prod"
	Namespace       string        `yaml:"namespace"`         // "" = cluster-wide
	LeaderElection  bool          `yaml:"leader_election"`
	WebhookEnabled  bool          `yaml:"webhook_enabled"`
	MetricsPort     int           `yaml:"metrics_port"`
	RetryBaseDelay  time.Duration `yaml:"retry_base_delay"`
	RetryMaxDelay   time.Duration `yaml:"retry_max_delay"`
	PollInterval    time.Duration `yaml:"poll_interval"`
}

// Default returns sensible defaults (local profile).
func Default() Config {
	return Config{
		Env:            "local",
		Namespace:      "aiplatform",
		LeaderElection: false,
		WebhookEnabled: false,
		MetricsPort:    8082,
		RetryBaseDelay: 5 * time.Second,
		RetryMaxDelay:  30 * time.Second,
		PollInterval:   10 * time.Second,
	}
}

// ProdDefaults returns prod profile defaults.
func ProdDefaults() Config {
	return Config{
		Env:            "prod",
		Namespace:      "",
		LeaderElection: true,
		WebhookEnabled: true,
		MetricsPort:    8082,
		RetryBaseDelay: 5 * time.Second,
		RetryMaxDelay:  5 * time.Minute,
		PollInterval:   10 * time.Second,
	}
}

// Load reads a YAML config file and merges it over the default config.
// If path is empty, returns the default config.
func Load(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		return cfg, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return cfg, err
	}
	defer f.Close()
	if err := yaml.NewDecoder(f).Decode(&cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}
```

- [ ] **Step 2: Create config YAML files**

Create `operator/config/local.yaml`:

```yaml
env: local
namespace: aiplatform
leader_election: false
webhook_enabled: false
metrics_port: 8082
retry_base_delay: 5s
retry_max_delay: 30s
poll_interval: 10s
```

Create `operator/config/prod.yaml`:

```yaml
env: prod
namespace: ""
leader_election: true
webhook_enabled: true
metrics_port: 8082
retry_base_delay: 5s
retry_max_delay: 5m
poll_interval: 10s
```

- [ ] **Step 3: Add `gopkg.in/yaml.v3` to operator module**

```bash
cd operator && go get gopkg.in/yaml.v3
```

- [ ] **Step 4: Update operator `main.go` to read config and apply it**

Replace `operator/cmd/operator/main.go` with:

```go
// operator/cmd/operator/main.go
package main

import (
	"flag"
	"log/slog"
	"net/http"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"

	opcfg "github.com/Weilei424/kubernetes-native-ai-platform/operator/internal/config"
	"github.com/Weilei424/kubernetes-native-ai-platform/operator/internal/reconciler"
)

func main() {
	configPath := flag.String("config", "", "Path to operator config YAML file")
	flag.Parse()

	cfg, err := opcfg.Load(*configPath)
	if err != nil {
		slog.Error("failed to load config", "path", *configPath, "error", err)
		os.Exit(1)
	}
	slog.Info("operator config loaded", "env", cfg.Env, "namespace", cfg.Namespace,
		"leader_election", cfg.LeaderElection)

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		slog.Error("unable to add client-go scheme", "error", err)
		os.Exit(1)
	}

	mgrOpts := ctrl.Options{
		Scheme: scheme,
	}
	if cfg.LeaderElection {
		mgrOpts.LeaderElection = true
		mgrOpts.LeaderElectionID = "ai-platform-operator-leader"
		mgrOpts.LeaderElectionNamespace = cfg.Namespace
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), mgrOpts)
	if err != nil {
		slog.Error("unable to start manager", "error", err)
		os.Exit(1)
	}

	controlPlaneURL := os.Getenv("CONTROL_PLANE_INTERNAL_URL")
	if controlPlaneURL == "" {
		controlPlaneURL = "http://control-plane:8081"
	}

	httpClient := &http.Client{Timeout: 10 * time.Second}

	// RayJob reconciler.
	rjr := &reconciler.RayJobReconciler{
		Client:          mgr.GetClient(),
		ControlPlaneURL: controlPlaneURL,
		HTTPClient:      httpClient,
	}
	if err := rjr.SetupWithManager(mgr); err != nil {
		slog.Error("unable to set up rayjob reconciler", "error", err)
		os.Exit(1)
	}

	// Deployment reconciler.
	dr := &reconciler.DeploymentReconciler{
		Client:          mgr.GetClient(),
		ControlPlaneURL: controlPlaneURL,
		HTTPClient:      httpClient,
		MinioEndpoint:   os.Getenv("MINIO_ENDPOINT"),
		PollInterval:    cfg.PollInterval,
	}
	if err := mgr.Add(dr); err != nil {
		slog.Error("unable to add deployment reconciler", "error", err)
		os.Exit(1)
	}

	slog.Info("operator starting", "control_plane_url", controlPlaneURL)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		slog.Error("operator stopped", "error", err)
		os.Exit(1)
	}
}
```

- [ ] **Step 5: Verify operator builds**

```bash
cd operator && go build ./... && go vet ./...
```
Expected: no errors.

- [ ] **Step 6: Run operator tests**

```bash
cd operator && go test ./...
```
Expected: all PASS.

- [ ] **Step 7: Commit**

```
operator/internal/config/config.go — Config struct with Load, Default, ProdDefaults
operator/config/local.yaml — local profile defaults
operator/config/prod.yaml — prod profile defaults
operator/cmd/operator/main.go — read config flag, apply LeaderElection + PollInterval
operator/go.mod / go.sum — add gopkg.in/yaml.v3
```

---

## Task 11: Failure test suite

**Files:**
- Modify: `control-plane/internal/api/jobs_test.go` (quota exceeded test)
- Modify: `control-plane/internal/api/deployments_test.go` (invalid deployment test)
- Modify: `operator/internal/reconciler/deployment_reconciler_test.go` (Triton readiness failure)
- Create: `control-plane/internal/api/failure_test.go` (bad image + missing artifact integration)

- [ ] **Step 1: Write the quota-exceeded failure test**

In `control-plane/internal/api/jobs_test.go`, add:

```go
func TestJobsAPI_QuotaExceeded(t *testing.T) {
	db := testutil.NewTestDB(t)
	store := jobs.NewPostgresJobStore(db)
	eventStore := events.NewEventStore(db)
	tenantID, projectID := testutil.CreateTenantAndProject(t, db)
	token := testutil.CreateToken(t, db, tenantID)

	// Set quota to 1 CPU, 1Gi memory (very small).
	_, err := db.Exec(context.Background(),
		`UPDATE tenants SET cpu_quota = 1000, memory_quota = 1073741824 WHERE id = $1::uuid`, tenantID)
	if err != nil {
		t.Fatal(err)
	}

	router := api.NewRouter(db, store, &events.NoOpPublisher{}, nil, nil, eventStore)
	body := fmt.Sprintf(`{
		"name":"big-job","project_id":%q,
		"runtime":{"image":"train:1","command":["train"]},
		"resources":{"num_workers":10,"worker_cpu":"4","worker_memory":"16Gi","head_cpu":"2","head_memory":"8Gi"}
	}`, projectID)

	req := httptest.NewRequest(http.MethodPost, "/v1/jobs", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "quota") {
		t.Errorf("expected quota error in body, got: %s", rec.Body.String())
	}

	// Verify no training_run was created.
	var count int
	db.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM training_runs WHERE tenant_id = $1::uuid`, tenantID).Scan(&count)
	if count != 0 {
		t.Errorf("expected 0 training_runs, got %d", count)
	}
}
```

- [ ] **Step 2: Write the invalid-deployment (non-production model) failure test**

In `control-plane/internal/api/deployments_test.go`, add a test that uses a real service (not a mock) to verify the service rejects a non-production model version. This test already partially exists in `service_test.go`; add an API-level integration test:

```go
func TestDeploymentsAPI_InvalidModelVersion(t *testing.T) {
	db := testutil.NewTestDB(t)
	// Create a model version in "candidate" status (not production).
	tenantID, projectID, modelVersionID := testutil.CreateCandidateModelVersion(t, db)
	token := testutil.CreateToken(t, db, tenantID)
	_ = projectID

	deployStore := deployments.NewPostgresDeploymentStore(db)
	modelStore := models.NewPostgresModelStore(db)
	svc := deployments.NewService(deployStore, modelStore)
	eventStore := events.NewEventStore(db)

	router := api.NewRouter(db, nil, &events.NoOpPublisher{}, nil, svc, eventStore)
	body := fmt.Sprintf(`{"model_name":"test-model","model_version":1,"name":"dep1","namespace":"default","replicas":1}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/deployments", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d: %s", rec.Code, rec.Body.String())
	}
	// No deployment record created.
	var count int
	db.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM deployments WHERE tenant_id = $1::uuid`, tenantID).Scan(&count)
	if count != 0 {
		t.Errorf("expected 0 deployments, got %d", count)
	}
	_ = modelVersionID
}
```

Note: `testutil.CreateCandidateModelVersion` must be added to the testutil package if it doesn't exist; it creates tenant + model_record + model_version with status "candidate".

- [ ] **Step 3: Write the Triton readiness failure test**

In `operator/internal/reconciler/deployment_reconciler_test.go`, add:

```go
func TestDeploymentReconciler_TritonReadinessFail_StaysProvisioning(t *testing.T) {
	// Simulate: pod is Running but Triton readiness endpoint returns 503.
	// The reconciler should NOT advance the deployment to "running".
	readinessCalls := 0
	fakeControlPlane := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/deployments") {
			// Return one provisioning deployment.
			json.NewEncoder(w).Encode(map[string]any{
				"deployments": []map[string]any{{
					"id": "dep-1", "name": "test", "namespace": "default",
					"status": "provisioning", "desired_replicas": 1,
					"artifact_uri": "s3://bucket/model", "model_name": "resnet",
				}},
			})
		} else if r.Method == http.MethodPatch {
			// Capture the status update. Should NOT be called with "running".
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			if body["status"] == "running" {
				t.Errorf("reconciler advanced to 'running' despite readiness failure")
			}
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer fakeControlPlane.Close()

	fakeTriton := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		readinessCalls++
		w.WriteHeader(http.StatusServiceUnavailable) // Triton not ready.
	}))
	defer fakeTriton.Close()

	// The reconciler checks readiness by calling ServingEndpoint/v2/health/ready.
	// With a fake k8s client returning a Running pod whose IP points to fakeTriton,
	// the reconciler will call fakeTriton and see 503.
	// This test verifies no "running" PATCH is sent.

	// (Use reconciler.MapPodPhase and the HTTP round-trip helpers already tested
	// in deployment_reconciler_http_test.go to set up the scenario.)

	r := &reconciler.DeploymentReconciler{
		Client:          fake.NewFakeClient(),
		ControlPlaneURL: fakeControlPlane.URL,
		HTTPClient:      fakeTriton.Client(),
		PollInterval:    50 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_ = r.Start(ctx) // runs until timeout.

	if readinessCalls == 0 {
		t.Error("expected reconciler to call Triton readiness endpoint")
	}
}
```

- [ ] **Step 4: Write the bad-image / retry-exhaustion test**

Create `control-plane/internal/api/failure_test.go`:

```go
package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/api"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/events"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/jobs"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/testutil"
)

// TestRetryExhaustion verifies that a job that repeatedly FAILs
// stops retrying after max_retries attempts.
func TestRetryExhaustion(t *testing.T) {
	db := testutil.NewTestDB(t)
	store := jobs.NewPostgresJobStore(db)
	eventStore := events.NewEventStore(db)
	tenantID, projectID := testutil.CreateTenantAndProject(t, db)

	// Create a job directly.
	job := &jobs.TrainingJob{
		TenantID: tenantID, ProjectID: projectID, Name: "bad-image-job",
		Status: "RUNNING", Image: "does-not-exist:latest",
		Command: []string{"train"}, NumWorkers: 1,
		WorkerCPU: "1", WorkerMemory: "1Gi", HeadCPU: "1", HeadMemory: "1Gi",
	}
	run := &jobs.TrainingRun{TenantID: tenantID, Status: "RUNNING"}
	if err := store.CreateJobWithRun(context.Background(), job, run); err != nil {
		t.Fatal(err)
	}
	// Manually advance to RUNNING.
	store.TransitionJobStatus(context.Background(), job.ID, "PENDING", "QUEUED", nil)
	store.TransitionJobStatus(context.Background(), job.ID, "QUEUED", "RUNNING", nil)

	internalRouter := api.NewInternalRouter(store, &events.NoOpPublisher{}, nil, eventStore)
	reason := "ImagePullBackOff: does-not-exist:latest"

	// Simulate 3 consecutive FAILED reports (max_retries = 3).
	for i := 0; i < 3; i++ {
		body, _ := json.Marshal(jobs.StatusUpdateRequest{Status: "FAILED", FailureReason: &reason})
		req := httptest.NewRequest(http.MethodPatch, "/internal/v1/jobs/"+job.ID+"/status", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		internalRouter.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("iteration %d: expected 200, got %d", i+1, rec.Code)
		}
		// Re-advance to RUNNING for next failure (simulates dispatcher + operator cycle).
		if i < 2 {
			store.TransitionJobStatus(context.Background(), job.ID, "QUEUED", "RUNNING", nil)
		}
	}

	// After 3 retries, 4th FAILED should leave job permanently FAILED.
	store.TransitionJobStatus(context.Background(), job.ID, "QUEUED", "RUNNING", nil)
	body, _ := json.Marshal(jobs.StatusUpdateRequest{Status: "FAILED", FailureReason: &reason})
	req := httptest.NewRequest(http.MethodPatch, "/internal/v1/jobs/"+job.ID+"/status", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	internalRouter.ServeHTTP(rec, req)

	updated, _ := store.GetJobByID(context.Background(), job.ID)
	if updated.Status != "FAILED" {
		t.Errorf("expected FAILED after retry exhaustion, got %s", updated.Status)
	}
	if updated.RetryCount != 3 {
		t.Errorf("expected retry_count=3, got %d", updated.RetryCount)
	}
}
```

- [ ] **Step 5: Run the full test suite**

```bash
cd control-plane && go test ./... -v 2>&1 | tail -30
cd operator && go test ./... -v 2>&1 | tail -20
```
Expected: all PASS. Fix any failures before committing.

- [ ] **Step 6: Commit**

```
control-plane/internal/api/jobs_test.go — TestJobsAPI_QuotaExceeded failure test
control-plane/internal/api/deployments_test.go — TestDeploymentsAPI_InvalidModelVersion failure test
control-plane/internal/api/failure_test.go — TestRetryExhaustion failure test
operator/internal/reconciler/deployment_reconciler_test.go — TestDeploymentReconciler_TritonReadinessFail_StaysProvisioning
```

---

## Task 12: Final integration verification

- [ ] **Step 1: Run full control-plane test suite**

```bash
cd control-plane && go test ./... && go vet ./...
```
Expected: all PASS, no vet errors.

- [ ] **Step 2: Run full operator test suite**

```bash
cd operator && go test ./... && go vet ./...
```
Expected: all PASS, no vet errors.

- [ ] **Step 3: Update BACKLOG.md**

Mark all Phase 4 checklist items as `[x]` in `docs/planning/BACKLOG.md`.

- [ ] **Step 4: Final commit**

```
docs/planning/BACKLOG.md — mark Phase 4 items complete
```

---

## Self-Review Notes

**Spec coverage check:**
- Structured logging (correlation IDs, entity IDs) → Task 1 ✓
- Prometheus metrics (API latency, queue depth, admission failures, run duration, retries, deployment count) → Tasks 2+3 ✓
- Training run duration metric → `TrainingRunDuration` registered in metrics.go; wiring into the internal handler (emit on SUCCEEDED/FAILED with duration from started_at) is intentionally left for Task 6 inline since `started_at` is available on the training_run.
- Grafana dashboards (4 JSON, provisioned) → Task 4 ✓
- Job retry (migration, state machine, store, handler trigger) → Tasks 5+6 ✓
- Deployment rollback (store, service, handler, route) → Task 7 ✓
- Events dual-write + store + API → Task 8 ✓
- Quota API → Task 9 ✓
- Operator config + hardening (local/prod profiles, leader election) → Task 10 ✓
- Failure tests (quota, invalid deployment, retry exhaustion, Triton readiness) → Task 11 ✓
- Missing artifact failure test: the missing artifact scenario surfaces in the operator (init container failure). A unit test using a fake pod phase `Init:Error` maps to a known pod status; `MapPodPhase` already handles this. Add one more case to the existing `TestMapPodPhase` table in `operator/internal/reconciler/deployment_reconciler_test.go` for `Init:Error → failed`. Add to Task 11 Step 3.

**Type consistency check:**
- `events.PlatformEvent` defined in Task 8 and used in `internal.go` dual-write — consistent ✓
- `events.EventFilter` defined and used in `ListEvents` — consistent ✓
- `DeploymentsService.Rollback` signature in `api/deployments.go` matches `deployments.Service.Rollback` — consistent ✓
- `Store.IncrementRetryCount` returns `(int, error)`, used in `internal.go` as `newCount, incErr := h.store.IncrementRetryCount(...)` — consistent ✓
- `job.MaxRetries` referenced in `internal.go` retry check — field added to `TrainingJob` in Task 5 ✓
- `NewRouter` new signature (adds `eventStore *events.EventStore`) must be updated in all existing test files that call it — noted in Task 8 Step 8 ✓
- `NewInternalRouter` new signature (adds `eventStore *events.EventStore`) — noted in Task 8, and call site in `main.go` updated ✓
