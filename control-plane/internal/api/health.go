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
