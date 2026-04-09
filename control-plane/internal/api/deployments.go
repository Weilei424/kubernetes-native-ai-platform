// control-plane/internal/api/deployments.go
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/auth"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/deployments"
)

// DeploymentsService is the interface the handler depends on (exported for test use).
type DeploymentsService interface {
	Create(ctx context.Context, tenantID string, req deployments.CreateDeploymentRequest) (*deployments.Deployment, error)
	Get(ctx context.Context, id, tenantID string) (*deployments.Deployment, error)
	Delete(ctx context.Context, id, tenantID string) error
}

type deploymentsHandler struct {
	svc DeploymentsService
}

func (h *deploymentsHandler) handleCreate(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDFromContext(r.Context())

	var req deployments.CreateDeploymentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if req.ModelName == "" || req.Name == "" || req.ModelVersion == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "model_name, name, and model_version are required"})
		return
	}

	dep, err := h.svc.Create(r.Context(), tenantID, req)
	if err != nil {
		switch {
		case errors.Is(err, deployments.ErrModelNotFound), errors.Is(err, deployments.ErrVersionNotFound):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		case errors.Is(err, deployments.ErrModelVersionNotProduction):
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		case errors.Is(err, deployments.ErrDuplicateDeploymentName):
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		default:
			slog.Error("create deployment", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		}
		return
	}
	writeJSON(w, http.StatusCreated, map[string]interface{}{"deployment": dep})
}

func (h *deploymentsHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDFromContext(r.Context())
	id := chi.URLParam(r, "id")

	dep, err := h.svc.Get(r.Context(), id, tenantID)
	if err != nil {
		if errors.Is(err, deployments.ErrDeploymentNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "deployment not found"})
		} else {
			slog.Error("get deployment", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"deployment": dep})
}

func (h *deploymentsHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDFromContext(r.Context())
	id := chi.URLParam(r, "id")

	if err := h.svc.Delete(r.Context(), id, tenantID); err != nil {
		if errors.Is(err, deployments.ErrDeploymentNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "deployment not found"})
		} else {
			slog.Error("delete deployment", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
