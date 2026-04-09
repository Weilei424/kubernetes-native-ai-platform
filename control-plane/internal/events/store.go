// control-plane/internal/events/store.go
package events

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PlatformEvent mirrors the platform_events table row.
type PlatformEvent struct {
	ID         string          `json:"id"`
	TenantID   string          `json:"tenant_id"`
	EntityType string          `json:"entity_type"`
	EntityID   string          `json:"entity_id"`
	EventType  string          `json:"event_type"`
	Payload    json.RawMessage `json:"payload,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
}

// EventFilter constrains ListEvents queries.
type EventFilter struct {
	EntityType string
	EntityID   string
	Limit      int
	Offset     int
}

// EventStore persists and queries platform events in PostgreSQL.
type EventStore struct {
	db *pgxpool.Pool
}

// NewEventStore returns an EventStore backed by the given pool.
func NewEventStore(db *pgxpool.Pool) *EventStore {
	return &EventStore{db: db}
}

// WriteEvent inserts a single event into platform_events.
func (s *EventStore) WriteEvent(ctx context.Context, e PlatformEvent) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO platform_events (tenant_id, entity_type, entity_id, event_type, payload)
		VALUES ($1::uuid, $2, $3::uuid, $4, $5)`,
		e.TenantID, e.EntityType, e.EntityID, e.EventType, []byte(e.Payload),
	)
	if err != nil {
		return fmt.Errorf("write event: %w", err)
	}
	return nil
}

// ListEvents returns tenant-scoped events, optionally filtered by entity_type and entity_id.
// Returns the matching events and the total count before limit/offset.
func (s *EventStore) ListEvents(ctx context.Context, tenantID string, f EventFilter) ([]*PlatformEvent, int, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}

	args := []any{tenantID}
	where := "tenant_id = $1::uuid"
	i := 2
	if f.EntityType != "" {
		where += fmt.Sprintf(" AND entity_type = $%d", i)
		args = append(args, f.EntityType)
		i++
	}
	if f.EntityID != "" {
		where += fmt.Sprintf(" AND entity_id = $%d::uuid", i)
		args = append(args, f.EntityID)
		i++
	}

	var total int
	if err := s.db.QueryRow(ctx,
		fmt.Sprintf(`SELECT COUNT(*) FROM platform_events WHERE %s`, where),
		args...,
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count events: %w", err)
	}

	args = append(args, f.Limit, f.Offset)
	rows, err := s.db.Query(ctx, fmt.Sprintf(`
		SELECT id::text, tenant_id::text, entity_type, entity_id::text, event_type, payload, created_at
		FROM platform_events
		WHERE %s
		ORDER BY created_at DESC
		LIMIT $%d OFFSET $%d`, where, i, i+1),
		args...,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()

	var result []*PlatformEvent
	for rows.Next() {
		var ev PlatformEvent
		var payload []byte
		if err := rows.Scan(&ev.ID, &ev.TenantID, &ev.EntityType, &ev.EntityID,
			&ev.EventType, &payload, &ev.CreatedAt); err != nil {
			return nil, 0, err
		}
		ev.Payload = payload
		result = append(result, &ev)
	}
	return result, total, rows.Err()
}
