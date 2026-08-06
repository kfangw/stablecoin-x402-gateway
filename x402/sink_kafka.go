package x402

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/twmb/franz-go/pkg/kgo"
)

// KafkaSink publishes settlement events to a Kafka topic. The record key is the
// entry ID (the settlement tx hash), which fixes the partition for a given
// settlement and gives consumers a stable idempotency key; the value is the
// entry as JSON. It speaks the Kafka protocol, so any Kafka-compatible broker
// (the demo stack uses Redpanda) works.
type KafkaSink struct {
	client *kgo.Client
	topic  string
}

// NewKafkaSink connects to the brokers and publishes to topic.
func NewKafkaSink(brokers []string, topic string) (*KafkaSink, error) {
	client, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.DefaultProduceTopic(topic),
	)
	if err != nil {
		return nil, fmt.Errorf("x402: kafka sink: %w", err)
	}
	return &KafkaSink{client: client, topic: topic}, nil
}

// Publish sends the entry synchronously and returns any produce error, so the
// outbox only marks the entry published once the broker has acknowledged it.
func (s *KafkaSink) Publish(ctx context.Context, e JournalEntry) error {
	value, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("x402: marshal settlement event: %w", err)
	}
	rec := &kgo.Record{Key: []byte(e.ID), Value: value}
	if err := s.client.ProduceSync(ctx, rec).FirstErr(); err != nil {
		return fmt.Errorf("x402: produce settlement %s: %w", e.ID, err)
	}
	return nil
}

// Close flushes buffered records and closes the client.
func (s *KafkaSink) Close() {
	s.client.Close()
}
