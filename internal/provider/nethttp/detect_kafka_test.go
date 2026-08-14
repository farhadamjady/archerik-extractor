package nethttp

import (
	"testing"

	"github.com/farhadamjady/archerik-extractor/internal/model"
	"github.com/farhadamjady/archerik-extractor/internal/provider"
	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/golang"
	"github.com/farhadamjady/archerik-extractor/internal/query"
)

// kafkaFor runs the Kafka detector over one Go source with the struct type index
// built over it.
func kafkaFor(t *testing.T, src string) *model.Service {
	t.Helper()
	f, err := golang.NewParser().Parse("k.go", []byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	idx := &provider.Index{Types: buildTypeIndex([]*golang.File{f.(*golang.File)})}
	svc := model.NewService("s", "s", "")
	if err := query.New().Run(f, []provider.Detector{kafkaDetector{}}, idx, nil, svc); err != nil {
		t.Fatalf("run: %v", err)
	}
	model.Sort(svc)
	return svc
}

func kProducerTopics(svc *model.Service) []string {
	var out []string
	for _, p := range svc.KafkaProducers {
		out = append(out, p.Topic)
	}
	return out
}
func kConsumerTopics(svc *model.Service) []string {
	var out []string
	for _, c := range svc.KafkaConsumers {
		out = append(out, c.Topic)
	}
	return out
}

// TestSegmentioWriterReader proves K11 segmentio: kafka.Writer{Topic} is a
// producer, kafka.ReaderConfig{Topic} is a consumer.
func TestSegmentioWriterReader(t *testing.T) {
	src := `package main
import kafka "github.com/segmentio/kafka-go"
func setup() {
	w := &kafka.Writer{Topic: "orders-out", Balancer: &kafka.Hash{}}
	_ = w
	r := kafka.NewReader(kafka.ReaderConfig{Topic: "orders-in", GroupID: "g"})
	_ = r
}`
	svc := kafkaFor(t, src)
	if got := kProducerTopics(svc); len(got) != 1 || got[0] != "orders-out" {
		t.Errorf("producers = %v, want [orders-out]", got)
	}
	if got := kConsumerTopics(svc); len(got) != 1 || got[0] != "orders-in" {
		t.Errorf("consumers = %v, want [orders-in]", got)
	}
}

// TestSaramaProducerSchema proves K11 sarama: sarama.ProducerMessage{Topic,Value}
// is a producer, and the payload schema resolves from the value's json.Marshal
// source (traced through the local binding + ByteEncoder wrapper).
func TestSaramaProducerSchema(t *testing.T) {
	src := `package main
import "github.com/IBM/sarama"
type OrderEvent struct {
	OrderId string ` + "`json:\"order_id\"`" + `
	Total   int    ` + "`json:\"total\"`" + `
}
func publish(producer sarama.SyncProducer) {
	evt := OrderEvent{OrderId: "1", Total: 5}
	b, _ := json.Marshal(evt)
	producer.SendMessage(&sarama.ProducerMessage{Topic: "sarama-out", Value: sarama.ByteEncoder(b)})
}`
	svc := kafkaFor(t, src)
	if len(svc.KafkaProducers) != 1 {
		t.Fatalf("producers = %d, want 1", len(svc.KafkaProducers))
	}
	p := svc.KafkaProducers[0]
	if p.Topic != "sarama-out" || !p.Resolved || p.Confidence != model.Confirmed {
		t.Errorf("producer = %+v, want sarama-out/confirmed", p)
	}
	if p.Schema == nil || p.Schema.Type != "OrderEvent" {
		t.Fatalf("schema = %+v, want OrderEvent", p.Schema)
	}
	names := map[string]bool{}
	for _, f := range p.Schema.Nested {
		names[f.Name] = true
	}
	if !names["order_id"] || !names["total"] {
		t.Errorf("payload fields = %v, want json-tag order_id/total", names)
	}
}

// TestConfluentSubscribeTopics proves K11 confluent: SubscribeTopics([]string{…})
// emits one consumer edge per topic.
func TestConfluentSubscribeTopics(t *testing.T) {
	src := `package main
import "github.com/confluentinc/confluent-kafka-go/kafka"
func consume(c *kafka.Consumer) {
	c.SubscribeTopics([]string{"conf-a", "conf-b"}, nil)
}`
	svc := kafkaFor(t, src)
	topics := kConsumerTopics(svc)
	if len(topics) != 2 {
		t.Fatalf("consumers = %v, want [conf-a conf-b]", topics)
	}
	got := map[string]bool{}
	for _, tp := range topics {
		got[tp] = true
	}
	if !got["conf-a"] || !got["conf-b"] {
		t.Errorf("consumer topics = %v", topics)
	}
}

// TestSaramaConsumePartition proves the sarama consumer call form.
func TestSaramaConsumePartition(t *testing.T) {
	src := `package main
import "github.com/IBM/sarama"
func consume(c sarama.Consumer) {
	pc, _ := c.ConsumePartition("sarama-in", 0, sarama.OffsetNewest)
	_ = pc
}`
	svc := kafkaFor(t, src)
	if got := kConsumerTopics(svc); len(got) != 1 || got[0] != "sarama-in" {
		t.Errorf("consumers = %v, want [sarama-in]", got)
	}
}

// TestKafkaGate proves the import gate: a Kafka-shaped composite in a file that
// imports no Kafka client emits nothing (so a generic Writer/Message struct is
// not mistaken for Kafka).
func TestKafkaGate(t *testing.T) {
	src := `package main
func f() {
	m := &sarama.ProducerMessage{Topic: "nope", Value: nil}
	_ = m
}`
	svc := kafkaFor(t, src)
	if len(svc.KafkaProducers) != 0 || len(svc.KafkaConsumers) != 0 {
		t.Errorf("gate failed: producers=%v consumers=%v", svc.KafkaProducers, svc.KafkaConsumers)
	}
}
