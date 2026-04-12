// control-plane/internal/api/internal.go
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/deployments"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/events"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/jobs"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/observability"
)

// NewInternalRouter builds the internal-only HTTP handler for operator callbacks.
// This router has no auth middleware — it must be bound to an internal-only port.
// deploymentStore and eventStore may be nil; their features are only active when non-nil.
func NewInternalRouter(store jobs.Store, publisher events.Publisher, deploymentStore deployments.Store, eventStore *events.EventStore) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(observability.RequestLogger(slog.Default()))

	h := &internalHandler{store: store, publisher: publisher, deploymentStore: deploymentStore, eventStore: eventStore}
	r.Patch("/internal/v1/jobs/{id}/status", h.handleUpdateJobStatus)
	if deploymentStore != nil {
		r.Get("/internal/v1/deployments", h.handleListPendingDeployments)
		r.Patch("/internal/v1/deployments/{id}/status", h.handleUpdateDeploymentStatus)
	}
	return r
}

type internalHandler struct {
	store           jobs.Store
	publisher       events.Publisher
	deploymentStore deployments.Store
	eventStore      *events.EventStore
}

func (h *internalHandler) handleUpdateJobStatus(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "id")

	var req jobs.StatusUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	// Fetch current job to know the from-status (no tenant filter — internal endpoint)
	job, err := h.store.GetJobByID(r.Context(), jobID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found"})
		return
	}

	// Capture the current run's started_at before transitioning so we can observe
	// training_run_duration_seconds when the run reaches a terminal state.
	var currentRun *jobs.TrainingRun
	if req.Status == "SUCCEEDED" || req.Status == "FAILED" {
		if run, runErr := h.store.GetRunByJobID(r.Context(), jobID); runErr == nil {
			currentRun = run
		}
	}

	if err := h.store.TransitionJobStatus(r.Context(), jobID, job.Status, req.Status, req.FailureReason); err != nil {
		observability.FromContext(r.Context()).Error("internal: transition status", "job_id", jobID, "from", job.Status, "to", req.Status, "error", err)
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}

	// Emit run duration and completion outcome when a run reaches a terminal state.
	if currentRun != nil && currentRun.StartedAt != nil {
		observability.TrainingRunDuration.Observe(time.Since(*currentRun.StartedAt).Seconds())
	}
	switch req.Status {
	case "SUCCEEDED":
		observability.TrainingRunCompletions.WithLabelValues("succeeded").Inc()
	case "FAILED":
		observability.TrainingRunCompletions.WithLabelValues("failed").Inc()
	}

	if h.eventStore != nil {
		payload, _ := json.Marshal(map[string]any{
			"from":           job.Status,
			"to":             req.Status,
			"failure_reason": req.FailureReason,
			"timestamp":      time.Now().UTC().Format(time.RFC3339),
		})
		if err := h.eventStore.WriteEvent(r.Context(), events.PlatformEvent{
			TenantID: job.TenantID, EntityType: "job", EntityID: jobID,
			EventType: req.Status, Payload: payload,
		}); err != nil {
			observability.FromContext(r.Context()).Warn("internal: write job event", "job_id", jobID, "error", err)
		}
	}

	if req.MLflowRunID != nil {
		if err := h.store.SetMLflowRunID(r.Context(), jobID, *req.MLflowRunID); err != nil {
			observability.FromContext(r.Context()).Warn("internal: set mlflow run id", "job_id", jobID, "error", err)
		}
	}

	// After a successful FAILED transition, check if we should automatically retry.
	if req.Status == "FAILED" {
		newCount, incErr := h.store.IncrementRetryCount(r.Context(), jobID)
		if incErr != nil {
			observability.FromContext(r.Context()).Warn("internal: increment retry count", "job_id", jobID, "error", incErr)
		} else if newCount <= job.MaxRetries {
			if _, runErr := h.store.CreateRetryRun(r.Context(), jobID, job.TenantID); runErr != nil {
				observability.FromContext(r.Context()).Error("internal: create retry run", "job_id", jobID, "error", runErr)
			} else if transErr := h.store.TransitionJobStatus(r.Context(), jobID, "FAILED", "QUEUED", nil); transErr != nil {
				observability.FromContext(r.Context()).Error("internal: re-queue job for retry", "job_id", jobID, "error", transErr)
			} else {
				observability.TrainingRunRetries.Inc()
				observability.FromContext(r.Context()).Info("internal: job re-queued for retry", "job_id", jobID, "retry_count", newCount)
			}
		}
	}

	topic := statusToTopic(req.Status)
	evt := jobs.JobEvent{
		JobID:         jobID,
		TenantID:      job.TenantID,
		Status:        req.Status,
		Timestamp:     time.Now().UTC().Format(time.RFC3339),
		FailureReason: req.FailureReason,
	}
	if err := h.publisher.Publish(r.Context(), topic, evt); err != nil {
		observability.FromContext(r.Context()).Warn("internal: publish event", "topic", topic, "job_id", jobID, "error", err)
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": req.Status})
}

func (h *internalHandler) handleListPendingDeployments(w http.ResponseWriter, r *http.Request) {
	deps, err := h.deploymentStore.ListPendingDeployments(r.Context())
	if err != nil {
		observability.FromContext(r.Context()).Error("internal: list pending deployments", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if deps == nil {
		deps = []*deployments.Deployment{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"deployments": deps})
}

func (h *internalHandler) handleUpdateDeploymentStatus(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var req deployments.UpdateStatusRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if req.Status == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "status is required"})
		return
	}

	// Enforce the deployment state machine before writing to the store.
	current, err := h.deploymentStore.GetDeployment(r.Context(), id)
	if err != nil {
		if errors.Is(err, deployments.ErrDeploymentNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "deployment not found"})
		} else {
			observability.FromContext(r.Context()).Error("internal: get deployment for transition", "id", id, "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		}
		return
	}
	if !deployments.ValidTransition(current.Status, req.Status) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "invalid status transition: " + current.Status + " → " + req.Status,
		})
		return
	}

	if err := h.deploymentStore.UpdateDeploymentStatus(r.Context(), id, req.Status, req.ServingEndpoint, req.FailureReason); err != nil {
		if errors.Is(err, deployments.ErrDeploymentNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "deployment not found"})
		} else {
			observability.FromContext(r.Context()).Error("internal: update deployment status", "id", id, "status", req.Status, "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		}
		return
	}

	if h.eventStore != nil {
		payload, _ := json.Marshal(map[string]any{
			"from":      current.Status,
			"to":        req.Status,
			"timestamp": time.Now().UTC().Format(time.RFC3339),
		})
		if err := h.eventStore.WriteEvent(r.Context(), events.PlatformEvent{
			TenantID: current.TenantID, EntityType: "deployment", EntityID: id,
			EventType: req.Status, Payload: payload,
		}); err != nil {
			observability.FromContext(r.Context()).Warn("internal: write deployment event", "deployment_id", id, "error", err)
		}
	}

	// Update deployment_count gauge. "deleted" is excluded from the gauge to match
	// the startup snapshot (CountByStatus excludes deleted rows), preventing the
	// series from appearing at runtime then vanishing after process restart.
	observability.DeploymentCount.WithLabelValues(current.Status).Dec()
	if req.Status != "deleted" {
		observability.DeploymentCount.WithLabelValues(req.Status).Inc()
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": req.Status})
}

func statusToTopic(status string) string {
	switch status {
	case "RUNNING":
		return "platform.job.running"
	case "SUCCEEDED":
		return "platform.job.succeeded"
	case "FAILED":
		return "platform.job.failed"
	default:
		return "platform.job." + status
	}
}
