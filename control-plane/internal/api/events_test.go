// control-plane/internal/api/events_test.go
package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/api"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/events"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/jobs"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/testutil"
)

func setupEventsAPITest(t *testing.T) (http.Handler, *events.EventStore, string, string) {
	t.Helper()
	pool := testutil.SetupDB(t)
	ctx := context.Background()

	var tenantID string
	pool.QueryRow(ctx, `INSERT INTO tenants (name) VALUES ('events-tenant') RETURNING id::text`).Scan(&tenantID)

	plaintext := "eventstoken-xxxx"
	prefix := plaintext[:8]
	hash, _ := bcrypt.GenerateFromPassword([]byte(plaintext), bcrypt.MinCost)
	pool.Exec(ctx, `INSERT INTO api_tokens (tenant_id, token_hash, token_prefix) VALUES ($1, $2, $3)`,
		tenantID, string(hash), prefix)

	store := jobs.NewPostgresJobStore(pool)
	pub := &events.NoOpPublisher{}
	eventStore := events.NewEventStore(pool)
	handler := api.NewRouter(pool, store, pub, nil, nil, eventStore)
	return handler, eventStore, tenantID, plaintext
}

func TestEventsHandler_ListEmpty(t *testing.T) {
	handler, _, _, token := setupEventsAPITest(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	evts, ok := resp["events"]
	if !ok {
		t.Fatal("response missing 'events' field")
	}
	list, ok := evts.([]any)
	if !ok {
		t.Fatalf("expected events to be an array, got %T", evts)
	}
	if len(list) != 0 {
		t.Fatalf("expected empty list, got %d items", len(list))
	}
	if total, ok := resp["total"].(float64); !ok || total != 0 {
		t.Fatalf("expected total=0, got %v", resp["total"])
	}
}

func TestEventsHandler_ListWithEvents(t *testing.T) {
	handler, eventStore, tenantID, token := setupEventsAPITest(t)
	ctx := context.Background()

	// Insert a couple of events directly via the store.
	payload1, _ := json.Marshal(map[string]string{"from": "QUEUED", "to": "RUNNING"})
	payload2, _ := json.Marshal(map[string]string{"from": "RUNNING", "to": "SUCCEEDED"})

	jobID := "00000000-0000-0000-0000-000000000001"

	if err := eventStore.WriteEvent(ctx, events.PlatformEvent{
		TenantID:   tenantID,
		EntityType: "job",
		EntityID:   jobID,
		EventType:  "RUNNING",
		Payload:    payload1,
	}); err != nil {
		t.Fatalf("write event 1: %v", err)
	}
	if err := eventStore.WriteEvent(ctx, events.PlatformEvent{
		TenantID:   tenantID,
		EntityType: "job",
		EntityID:   jobID,
		EventType:  "SUCCEEDED",
		Payload:    payload2,
	}); err != nil {
		t.Fatalf("write event 2: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	list, ok := resp["events"].([]any)
	if !ok {
		t.Fatalf("expected events array, got %T", resp["events"])
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 events, got %d", len(list))
	}
	if total, ok := resp["total"].(float64); !ok || total != 2 {
		t.Fatalf("expected total=2, got %v", resp["total"])
	}
}

func TestEventsHandler_FilterByEntityType(t *testing.T) {
	handler, eventStore, tenantID, token := setupEventsAPITest(t)
	ctx := context.Background()

	jobID := "00000000-0000-0000-0000-000000000002"
	depID := "00000000-0000-0000-0000-000000000003"

	payload, _ := json.Marshal(map[string]string{"status": "RUNNING"})
	if err := eventStore.WriteEvent(ctx, events.PlatformEvent{
		TenantID: tenantID, EntityType: "job", EntityID: jobID,
		EventType: "RUNNING", Payload: payload,
	}); err != nil {
		t.Fatalf("write job event: %v", err)
	}
	if err := eventStore.WriteEvent(ctx, events.PlatformEvent{
		TenantID: tenantID, EntityType: "deployment", EntityID: depID,
		EventType: "running", Payload: payload,
	}); err != nil {
		t.Fatalf("write deployment event: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/events?entity_type=job", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)
	list := resp["events"].([]any)
	if len(list) != 1 {
		t.Fatalf("expected 1 job event, got %d", len(list))
	}
	if total, ok := resp["total"].(float64); !ok || total != 1 {
		t.Fatalf("expected total=1, got %v", resp["total"])
	}
}

func TestEventsHandler_RequiresAuth(t *testing.T) {
	handler, _, _, _ := setupEventsAPITest(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without auth, got %d", rec.Code)
	}
}
