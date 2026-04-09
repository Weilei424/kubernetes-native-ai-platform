// control-plane/internal/api/events.go
package api

import (
	"net/http"
	"strconv"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/auth"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/events"
)

type eventsHandler struct {
	store *events.EventStore
}

func (h *eventsHandler) handleList(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDFromContext(r.Context())
	q := r.URL.Query()

	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))

	evts, total, err := h.store.ListEvents(r.Context(), tenantID, events.EventFilter{
		EntityType: q.Get("entity_type"),
		EntityID:   q.Get("entity_id"),
		Limit:      limit,
		Offset:     offset,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if evts == nil {
		evts = []*events.PlatformEvent{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": evts, "total": total})
}
