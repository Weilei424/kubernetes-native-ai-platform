// control-plane/internal/events/kafka.go
package events

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	kafka "github.com/segmentio/kafka-go"
)

// KafkaPublisher publishes events to Kafka topics using segmentio/kafka-go.
// Publish failures are returned to the caller for logging; they must not
// block state transitions (caller is responsible for the fire-and-forget pattern).
type KafkaPublisher struct {
	writer *kafka.Writer
}

// NewKafkaPublisher creates a KafkaPublisher connected to the given broker address.
func NewKafkaPublisher(brokerAddr string) *KafkaPublisher {
	return &KafkaPublisher{
		writer: &kafka.Writer{
			Addr:         kafka.TCP(brokerAddr),
			Balancer:     &kafka.LeastBytes{},
			WriteTimeout: 5 * time.Second,
		},
	}
}

// Close shuts down the underlying writer.
func (p *KafkaPublisher) Close() error {
	return p.writer.Close()
}

// Publish serializes payload as JSON and writes it to the given topic.
func (p *KafkaPublisher) Publish(ctx context.Context, topic string, payload interface{}) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	return p.writer.WriteMessages(ctx, kafka.Message{
		Topic: topic,
		Value: b,
	})
}
