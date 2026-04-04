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
