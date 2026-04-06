// control-plane/internal/api/models.go
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/auth"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/models"
)

// ModelsService is the interface the handler depends on (exported for test use).
type ModelsService interface {
	Register(ctx context.Context, tenantID string, req models.RegisterRequest) (*models.ModelVersion, error)
	GetModel(ctx context.Context, name, tenantID string) (*models.ModelRecord, []*models.ModelVersion, error)
	GetModelVersion(ctx context.Context, name string, version int, tenantID string) (*models.ModelVersion, error)
	Promote(ctx context.Context, name string, version int, alias, tenantID string) error
	ResolveAlias(ctx context.Context, name, alias, tenantID string) (*models.ModelVersion, error)
}

type modelsHandler struct {
	svc ModelsService
}

func (h *modelsHandler) handleRegister(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDFromContext(r.Context())

	var req models.RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if req.RunID == "" || req.ModelName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "run_id and model_name are required"})
		return
	}

	ver, err := h.svc.Register(r.Context(), tenantID, req)
	if err != nil {
		switch {
		case errors.Is(err, models.ErrRunNotFound):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		case errors.Is(err, models.ErrRunNotSucceeded), errors.Is(err, models.ErrRunNoMLflowID):
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		default:
			slog.Error("register model", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		}
		return
	}
	writeJSON(w, http.StatusCreated, map[string]interface{}{"version": ver})
}

func (h *modelsHandler) handleGetModel(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDFromContext(r.Context())
	name := chi.URLParam(r, "name")

	rec, versions, err := h.svc.GetModel(r.Context(), name, tenantID)
	if err != nil {
		if errors.Is(err, models.ErrModelNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "model not found"})
		} else {
			slog.Error("get model", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		}
		return
	}
	if versions == nil {
		versions = []*models.ModelVersion{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"model": rec, "versions": versions})
}

func (h *modelsHandler) handleGetModelVersion(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDFromContext(r.Context())
	name := chi.URLParam(r, "name")
	versionStr := chi.URLParam(r, "version")

	versionNum, err := strconv.Atoi(versionStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "version must be an integer"})
		return
	}

	ver, err := h.svc.GetModelVersion(r.Context(), name, versionNum, tenantID)
	if err != nil {
		if errors.Is(err, models.ErrVersionNotFound) || errors.Is(err, models.ErrModelNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "version not found"})
		} else {
			slog.Error("get model version", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"version": ver})
}

func (h *modelsHandler) handlePromote(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDFromContext(r.Context())
	name := chi.URLParam(r, "name")
	versionStr := chi.URLParam(r, "version")

	versionNum, err := strconv.Atoi(versionStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "version must be an integer"})
		return
	}

	var req models.PromoteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if req.Alias == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "alias is required"})
		return
	}

	err = h.svc.Promote(r.Context(), name, versionNum, req.Alias, tenantID)
	if err != nil {
		switch {
		case errors.Is(err, models.ErrModelNotFound), errors.Is(err, models.ErrVersionNotFound):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		case errors.Is(err, models.ErrVersionArchived):
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		default:
			slog.Error("promote model version", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *modelsHandler) handleResolveAlias(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDFromContext(r.Context())
	name := chi.URLParam(r, "name")
	alias := chi.URLParam(r, "alias")

	ver, err := h.svc.ResolveAlias(r.Context(), name, alias, tenantID)
	if err != nil {
		if errors.Is(err, models.ErrAliasNotFound) || errors.Is(err, models.ErrModelNotFound) || errors.Is(err, models.ErrVersionNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "alias not found"})
		} else {
			slog.Error("resolve model alias", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"version": ver})
}
