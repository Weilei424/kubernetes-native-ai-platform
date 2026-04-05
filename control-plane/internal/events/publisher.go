// control-plane/internal/events/publisher.go
package events

import "context"

// Publisher publishes platform events. Implementations must be safe to call
// concurrently. Publish failures must not block the calling operation.
type Publisher interface {
	Publish(ctx context.Context, topic string, payload interface{}) error
}

// NoOpPublisher silently discards all events. Used in tests and when Kafka
// is not configured.
type NoOpPublisher struct{}

func (n *NoOpPublisher) Publish(_ context.Context, _ string, _ interface{}) error {
	return nil
}
