// operator/internal/observability/metrics.go
package observability

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	// ReconcileDuration tracks how long each reconcile loop takes.
	ReconcileDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "operator_reconcile_duration_seconds",
		Help:    "Duration of operator reconcile loops in seconds.",
		Buckets: prometheus.DefBuckets,
	}, []string{"reconciler"})

	// ReconcileErrors counts reconcile iterations that returned an error.
	ReconcileErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "operator_reconcile_errors_total",
		Help: "Total number of operator reconcile errors.",
	}, []string{"reconciler"})
)

// MetricsHandler returns an HTTP handler for the /metrics endpoint.
func MetricsHandler() http.Handler {
	return promhttp.Handler()
}
