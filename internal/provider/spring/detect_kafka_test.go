package spring

import (
	"sort"
	"testing"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/java"
	"github.com/farhadamjady/service-discovery/internal/query"
)

// kafkaScan runs the Kafka detector over sources (+ optional config) and returns
// the produced and consumed topic sets.
func kafkaScan(t *testing.T, cfg provider.ConfigResolver, srcs ...string) *model.Service {
	t.Helper()
	var files []*java.File
	parsed := map[string]provider.ParsedFile{}
	for i, s := range srcs {
		name := string(rune('A'+i)) + ".java"
		pf, err := java.NewParser().Parse(name, []byte(s))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		files = append(files, pf.(*java.File))
		parsed[name] = pf
	}
	idx := &provider.Index{Symbols: java.IndexSymbols(files), Config: cfg}
	res := java.NewEvaluator(idx)
	svc := model.NewService("s", "s", "")
	for _, p := range sortedJavaPaths(parsed) {
		if err := query.New().Run(parsed[p], []provider.Detector{kafkaDetector{}}, idx, res, svc); err != nil {
			t.Fatalf("run: %v", err)
		}
	}
	model.Sort(svc)
	return svc
}

func topics(edges []model.KafkaEdge) []string {
	out := make([]string, len(edges))
	for i, e := range edges {
		out[i] = e.Topic
	}
	sort.Strings(out)
	return out
}

func TestKafkaProducerLiteral(t *testing.T) {
	svc := kafkaScan(t, nil, `class C {
		KafkaTemplate<String, String> kafkaTemplate;
		void m() { kafkaTemplate.send("orders", "payload"); }
	}`)
	if len(svc.KafkaProducers) != 1 {
		t.Fatalf("producers = %+v, want 1", svc.KafkaProducers)
	}
	e := svc.KafkaProducers[0]
	if e.Topic != "orders" || !e.Resolved || e.Confidence != model.Confirmed {
		t.Errorf("producer = %+v, want orders/resolved/confirmed", e)
	}
	if e.Protocol != model.ProtoKafka || e.Detection != model.DetectKafka {
		t.Errorf("edge fields = (%s,%s)", e.Protocol, e.Detection)
	}
}

func TestKafkaProducerConstantTopic(t *testing.T) {
	svc := kafkaScan(t, nil,
		`class Topics { static final String ORDERS = "orders.v1"; }`,
		`class C { KafkaTemplate<String,String> kt; void m() { kt.send(Topics.ORDERS, "p"); } }`)
	if got := topics(svc.KafkaProducers); len(got) != 1 || got[0] != "orders.v1" {
		t.Errorf("constant topic = %v, want [orders.v1]", got)
	}
}

func TestKafkaNonTemplateSenderIgnored(t *testing.T) {
	// emailService.send(...) is not a KafkaTemplate — no producer edge.
	svc := kafkaScan(t, nil, `class C {
		EmailService emailService;
		void m() { emailService.send("hello", "body"); }
	}`)
	if len(svc.KafkaProducers) != 0 {
		t.Errorf("non-KafkaTemplate send should not produce an edge: %+v", svc.KafkaProducers)
	}
}

func TestKafkaProducerUnresolvedStillEmitted(t *testing.T) {
	// topic from an opaque call -> edge is still emitted (uncertain).
	svc := kafkaScan(t, nil, `class C {
		KafkaTemplate<String,String> kt;
		void m() { kt.send(computeTopic(), "p"); }
	}`)
	if len(svc.KafkaProducers) != 1 || svc.KafkaProducers[0].Resolved {
		t.Fatalf("producers = %+v, want 1 unresolved edge", svc.KafkaProducers)
	}
	if svc.KafkaProducers[0].Confidence != model.Uncertain {
		t.Errorf("confidence = %s, want uncertain", svc.KafkaProducers[0].Confidence)
	}
}

func TestKafkaConsumerLiteral(t *testing.T) {
	svc := kafkaScan(t, nil, `class C {
		@KafkaListener(topics = "orders") void handle(String msg) {}
	}`)
	if got := topics(svc.KafkaConsumers); len(got) != 1 || got[0] != "orders" {
		t.Errorf("consumer = %v, want [orders]", got)
	}
	if len(svc.KafkaProducers) != 0 {
		t.Errorf("no producers expected: %+v", svc.KafkaProducers)
	}
}

func TestKafkaConsumerPlaceholder(t *testing.T) {
	cfg := buildStore(t, nil, map[string]string{
		"application.yml": "orders:\n  topic: orders.v2\n",
	})
	svc := kafkaScan(t, cfg, `class C {
		@KafkaListener(topics = "${orders.topic}") void handle(String msg) {}
	}`)
	if got := topics(svc.KafkaConsumers); len(got) != 1 || got[0] != "orders.v2" {
		t.Errorf("placeholder consumer = %v, want [orders.v2]", got)
	}
	if svc.KafkaConsumers[0].Confidence != model.Likely {
		t.Errorf("confidence = %s, want likely (config)", svc.KafkaConsumers[0].Confidence)
	}
}

func TestKafkaConsumerTopicArray(t *testing.T) {
	svc := kafkaScan(t, nil, `class C {
		@KafkaListener(topics = {"orders", "shipments"}) void handle(String msg) {}
	}`)
	if got := topics(svc.KafkaConsumers); len(got) != 2 || got[0] != "orders" || got[1] != "shipments" {
		t.Errorf("array topics = %v, want [orders shipments]", got)
	}
}

// TestKafkaStreamsTopology (IMPROVEMENTS #5): builder.stream/table consume,
// KStream.to produces — gated on the org.apache.kafka.streams import.
func TestKafkaStreamsTopology(t *testing.T) {
	svc := kafkaScan(t, nil, `
import org.apache.kafka.streams.StreamsBuilder;
class OrderApp {
	KStream<Long, Order> stream(StreamsBuilder builder) {
		KStream<Long, Order> s = builder.stream("payment-orders");
		s.join(builder.stream("stock-orders")).to("orders");
		return s;
	}
	KTable<Long, Order> table(StreamsBuilder builder) {
		return builder.stream("orders").toTable();
	}
}`)
	if got := topics(svc.KafkaConsumers); len(got) != 3 || got[0] != "orders" || got[1] != "payment-orders" || got[2] != "stock-orders" {
		t.Errorf("streams consumers = %v, want [orders payment-orders stock-orders]", got)
	}
	if got := topics(svc.KafkaProducers); len(got) != 1 || got[0] != "orders" {
		t.Errorf("streams producers = %v, want [orders]", got)
	}
}

// TestNoStreamsImportNoMatch: without the streams import, stream()/to() calls
// (java.util streams, other libs) never produce Kafka edges.
func TestNoStreamsImportNoMatch(t *testing.T) {
	svc := kafkaScan(t, nil, `class C {
		void m(java.util.List<String> xs) {
			xs.stream().map(String::trim);
			someBuilder.to("nowhere");
		}
	}`)
	if len(svc.KafkaConsumers) != 0 || len(svc.KafkaProducers) != 0 {
		t.Errorf("non-streams file must not emit kafka edges: %+v %+v", svc.KafkaConsumers, svc.KafkaProducers)
	}
}
