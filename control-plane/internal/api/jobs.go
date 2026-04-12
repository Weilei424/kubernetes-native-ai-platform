// control-plane/internal/api/jobs.go
package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/auth"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/events"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/jobs"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/observability"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/scheduler"
)

type jobsHandler struct {
	store     jobs.Store
	publisher events.Publisher
}

func (h *jobsHandler) handleSubmitJob(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDFromContext(r.Context())

	var req jobs.JobSubmitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	// Verify the project exists and belongs to this tenant before anything else.
	if err := h.store.ProjectBelongsToTenant(r.Context(), req.ProjectID, tenantID); err != nil {
		if errors.Is(err, jobs.ErrProjectNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
			return
		}
		observability.FromContext(r.Context()).Error("check project ownership", "project_id", req.ProjectID, "tenant_id", tenantID, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	// Admission check
	admReq := scheduler.AdmissionRequest{
		Image:        req.Runtime.Image,
		NumWorkers:   req.Resources.NumWorkers,
		WorkerCPU:    req.Resources.WorkerCPU,
		WorkerMemory: req.Resources.WorkerMemory,
		HeadCPU:      req.Resources.HeadCPU,
		HeadMemory:   req.Resources.HeadMemory,
	}
	if err := scheduler.Admit(admReq); err != nil {
		observability.JobAdmissionFailures.WithLabelValues("invalid_spec").Inc()
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}

	// Quota check at submission time (pre-check; dispatcher re-checks before promotion)
	cpuQuota, memQuota, err := h.store.GetTenantQuota(r.Context(), tenantID)
	if err != nil {
		observability.FromContext(r.Context()).Error("get tenant quota", "tenant_id", tenantID, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	activeJobs, err := h.store.ListNonTerminalJobs(r.Context(), tenantID)
	if err != nil {
		observability.FromContext(r.Context()).Error("list active jobs", "tenant_id", tenantID, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	activeResources := make([]scheduler.JobResources, 0, len(activeJobs))
	for _, aj := range activeJobs {
		activeResources = append(activeResources, scheduler.JobResources{
			NumWorkers: aj.NumWorkers, WorkerCPU: aj.WorkerCPU,
			WorkerMemory: aj.WorkerMemory, HeadCPU: aj.HeadCPU, HeadMemory: aj.HeadMemory,
		})
	}
	quotaErr := scheduler.CheckQuota(
		scheduler.TenantQuota{CPUMillicores: cpuQuota, MemoryBytes: memQuota},
		activeResources,
		scheduler.JobResources{
			NumWorkers: req.Resources.NumWorkers, WorkerCPU: req.Resources.WorkerCPU,
			WorkerMemory: req.Resources.WorkerMemory, HeadCPU: req.Resources.HeadCPU,
			HeadMemory: req.Resources.HeadMemory,
		},
	)
	if quotaErr != nil {
		observability.JobAdmissionFailures.WithLabelValues("quota_exceeded").Inc()
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": quotaErr.Error()})
		return
	}

	env := req.Runtime.Env
	if env == nil {
		env = map[string]string{}
	}
	args := req.Runtime.Args
	if args == nil {
		args = []string{}
	}

	job := &jobs.TrainingJob{
		TenantID: tenantID, ProjectID: req.ProjectID, Name: req.Name,
		Status: "PENDING", Image: req.Runtime.Image,
		Command: req.Runtime.Command, Args: args, Env: env,
		NumWorkers: req.Resources.NumWorkers, WorkerCPU: req.Resources.WorkerCPU,
		WorkerMemory: req.Resources.WorkerMemory, HeadCPU: req.Resources.HeadCPU,
		HeadMemory: req.Resources.HeadMemory,
	}
	run := &jobs.TrainingRun{TenantID: tenantID, Status: "PENDING"}

	if err := h.store.CreateJobWithRun(r.Context(), job, run); err != nil {
		observability.FromContext(r.Context()).Error("create job with run", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	// Publish PENDING event (best-effort)
	evt := jobs.JobEvent{JobID: job.ID, TenantID: tenantID, Status: "PENDING"}
	if err := h.publisher.Publish(r.Context(), "platform.job.pending", evt); err != nil {
		observability.FromContext(r.Context()).Warn("publish job.pending", "job_id", job.ID, "error", err)
	}

	writeJSON(w, http.StatusAccepted, map[string]string{"job_id": job.ID, "run_id": run.ID})
}

func (h *jobsHandler) handleListJobs(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDFromContext(r.Context())
	list, err := h.store.ListJobs(r.Context(), tenantID)
	if err != nil {
		observability.FromContext(r.Context()).Error("list jobs", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if list == nil {
		list = []*jobs.TrainingJob{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"jobs": list})
}

func (h *jobsHandler) handleGetJob(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDFromContext(r.Context())
	jobID := chi.URLParam(r, "id")

	job, err := h.store.GetJob(r.Context(), jobID, tenantID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found"})
		return
	}
	run, _ := h.store.GetRunByJobID(r.Context(), jobID)
	writeJSON(w, http.StatusOK, map[string]interface{}{"job": job, "run": run})
}

func (h *jobsHandler) handleGetRun(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDFromContext(r.Context())
	jobID := chi.URLParam(r, "id")
	runID := chi.URLParam(r, "run_id")

	run, err := h.store.GetRun(r.Context(), jobID, runID, tenantID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "run not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"run": run})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}
