package nestjs

import (
	"testing"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/tsjs"
	"github.com/farhadamjady/service-discovery/internal/query"
)

// kafkaFor runs the Kafka detector over one TS source and returns the service.
func kafkaFor(t *testing.T, src string) *model.Service {
	t.Helper()
	f, err := tsjs.NewParser().Parse("k.ts", []byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	svc := model.NewService("s", "s", "")
	if err := query.New().Run(f, []provider.Detector{kafkaDetector{}}, &provider.Index{}, nil, svc); err != nil {
		t.Fatalf("run: %v", err)
	}
	model.Sort(svc)
	return svc
}

func consumerTopics(svc *model.Service) []string {
	var out []string
	for _, c := range svc.KafkaConsumers {
		out = append(out, c.Topic)
	}
	return out
}
func producerTopics(svc *model.Service) []string {
	var out []string
	for _, p := range svc.KafkaProducers {
		out = append(out, p.Topic)
	}
	return out
}

// TestNestMessagePatternConsumer proves K8's consumer half: @MessagePattern and
// @EventPattern handlers emit consumed topics.
func TestNestMessagePatternConsumer(t *testing.T) {
	src := `
import { Controller } from '@nestjs/common';
import { MessagePattern, EventPattern } from '@nestjs/microservices';
@Controller()
class OrdersController {
	@MessagePattern('orders.get')
	get(msg) {}
	@EventPattern('orders.created')
	onCreated(evt) {}
}`
	svc := kafkaFor(t, src)
	topics := consumerTopics(svc)
	if len(topics) != 2 {
		t.Fatalf("consumers = %v, want 2", topics)
	}
	got := map[string]bool{}
	for _, tp := range topics {
		got[tp] = true
	}
	if !got["orders.get"] || !got["orders.created"] {
		t.Errorf("consumer topics = %v, want orders.get + orders.created", topics)
	}
	for _, c := range svc.KafkaConsumers {
		if !c.Resolved || c.Confidence != model.Confirmed || c.Detection != model.DetectKafka {
			t.Errorf("edge = %+v, want resolved/confirmed/kafka", c)
		}
	}
}

// TestKafkajsProducer proves K8's producer half: kafkajs producer.send({ topic })
// emits a produced topic.
func TestKafkajsProducer(t *testing.T) {
	src := `
async function run() {
	await producer.send({ topic: 'orders-out', messages: [{ value: 'x' }] });
}`
	svc := kafkaFor(t, src)
	if got := producerTopics(svc); len(got) != 1 || got[0] != "orders-out" {
		t.Fatalf("producers = %v, want [orders-out]", got)
	}
	if p := svc.KafkaProducers[0]; !p.Resolved || p.Confidence != model.Confirmed {
		t.Errorf("producer = %+v, want resolved/confirmed", p)
	}
}

// TestKafkajsSubscribeTopicsArray proves consumer.subscribe({ topics: [...] })
// fans out to one consumer edge per topic.
func TestKafkajsSubscribeTopicsArray(t *testing.T) {
	src := `
async function run() {
	await consumer.subscribe({ topics: ['a', 'b'], fromBeginning: true });
}`
	svc := kafkaFor(t, src)
	topics := consumerTopics(svc)
	if len(topics) != 2 {
		t.Fatalf("consumers = %v, want [a b]", topics)
	}
}

// TestClientKafkaEmit proves the gated ClientKafka.emit producer: it fires only
// when the file imports @nestjs/microservices.
func TestClientKafkaEmit(t *testing.T) {
	withImport := `
import { ClientKafka } from '@nestjs/microservices';
class Svc {
	constructor(private client: ClientKafka) {}
	publish(payload) { this.client.emit('user.created', payload); }
}`
	if got := producerTopics(kafkaFor(t, withImport)); len(got) != 1 || got[0] != "user.created" {
		t.Fatalf("producers = %v, want [user.created]", got)
	}

	// No microservices import -> a generic .emit() is NOT a Kafka producer.
	noImport := `
class Bus {
	fire(payload) { this.emitter.emit('user.created', payload); }
}`
	if got := producerTopics(kafkaFor(t, noImport)); len(got) != 0 {
		t.Errorf("producers = %v, want none (no @nestjs/microservices import)", got)
	}
}

// TestResSendNotKafka proves the ambiguous string-form send() is not matched, so
// res.send(...) in a controller doesn't become a Kafka producer.
func TestResSendNotKafka(t *testing.T) {
	src := `
import { MessagePattern } from '@nestjs/microservices';
class C {
	handle(res) { res.send('ok'); }
}`
	if got := producerTopics(kafkaFor(t, src)); len(got) != 0 {
		t.Errorf("producers = %v, want none (res.send is not Kafka)", got)
	}
}
