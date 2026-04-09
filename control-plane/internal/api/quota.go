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
		"memory_quota": fmt.Sprintf("%dB", memQuota),
		"cpu_used":     fmt.Sprintf("%dm", cpuUsed),
		"memory_used":  fmt.Sprintf("%dB", memUsed),
		"running_jobs": runningJobs,
	})
}
