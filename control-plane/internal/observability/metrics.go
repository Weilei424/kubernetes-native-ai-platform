// control-plane/internal/observability/metrics.go
package observability

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
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

	// TrainingRunCompletions counts terminal training run outcomes by result.
	// Use result="succeeded" and result="failed" to compute success rate in Grafana.
	TrainingRunCompletions = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "training_run_completions_total",
		Help: "Total number of training runs that reached a terminal state.",
	}, []string{"result"})

	// DeploymentCount tracks deployments by status.
	DeploymentCount = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "deployment_count",
		Help: "Number of deployments by status.",
	}, []string{"status"})
)

// SyncDeploymentCountGauge resets DeploymentCount and sets it from a status→count map.
// Call this at process start (after DB is ready) so the gauge reflects pre-existing
// deployments rather than starting at zero and drifting negative on the first Dec.
func SyncDeploymentCountGauge(counts map[string]int64) {
	DeploymentCount.Reset()
	for status, count := range counts {
		DeploymentCount.WithLabelValues(status).Set(float64(count))
	}
}

// PrometheusMiddleware records HTTP request duration for each handler.
func PrometheusMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		HTTPRequestDuration.WithLabelValues(
			r.Method,
			routePattern(r),
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

// routePattern returns the chi route pattern for the request (e.g. "/v1/jobs/{id}"),
// falling back to r.URL.Path if the chi context is not available.
func routePattern(r *http.Request) string {
	if rctx := chi.RouteContext(r.Context()); rctx != nil {
		if p := rctx.RoutePattern(); p != "" {
			return p
		}
	}
	return r.URL.Path
}
