// control-plane/internal/api/router.go
package api

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/auth"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/events"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/jobs"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/observability"
)

// NewRouter builds and returns the chi router with all middleware and routes attached.
// The fourth argument is reserved for future use (e.g. a ray client) and may be nil.
func NewRouter(db *pgxpool.Pool, store jobs.Store, publisher events.Publisher, _ interface{}) http.Handler {
	r := chi.NewRouter()

	// Public routes — no auth required
	r.Get("/healthz", LivenessHandler)
	r.Get("/readyz", ReadinessHandler(db))

	logger := slog.Default()

	jh := &jobsHandler{store: store, publisher: publisher}

	// Protected routes — auth + logging middleware
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequestID)
		r.Use(observability.RequestLogger(logger))
		r.Use(auth.TokenAuth(auth.NewPostgresTokenStore(db)))

		r.Post("/v1/jobs", jh.handleSubmitJob)
		r.Get("/v1/jobs", jh.handleListJobs)
		r.Get("/v1/jobs/{id}", jh.handleGetJob)
		r.Get("/v1/jobs/{id}/runs/{run_id}", jh.handleGetRun)
	})

	return r
}
