package spring

import (
	"sort"
	"testing"

	"github.com/farhadamjady/archerik-extractor/internal/model"
	"github.com/farhadamjady/archerik-extractor/internal/provider"
	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/java"
	"github.com/farhadamjady/archerik-extractor/internal/query"
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
	idx := &provider.Index{Symbols: java.IndexSymbols(files), Config: cfg, Types: java.IndexTypes(files, nil)}
	// Populate the NewTopic-bean index so producer topic resolution has the
	// same inputs it gets from the real pipeline.
	_ = kafkaTopicIndexer{}.Index(&provider.IndexContext{Parsed: parsed}, idx)
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

// TestKafkaStreamsTopology: builder.stream/table consume, KStream.to produces
// — gated on the org.apache.kafka.streams import.
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

// TestKafkaProducerNewTopicBeanHeader reproduces a real-world case: the
// idiomatic Spring producer injects a NewTopic bean and sends a Message whose
// KafkaHeaders.TOPIC header is `topic.name()` — the destination is never a
// literal at the send() call site. The topic must resolve through the bean's
// TopicBuilder.name(@Value) and the config layer, at likely confidence.
func TestKafkaProducerNewTopicBeanHeader(t *testing.T) {
	cfg := buildStore(t, nil, map[string]string{
		"application.properties": "spring.kafka.topic.name=order_topics\n",
	})
	svc := kafkaScan(t, cfg,
		`class OrderProducer {
			private NewTopic topic;
			private KafkaTemplate<String, OrderEvent> kafkaTemplate;
			public OrderProducer(NewTopic topic, KafkaTemplate<String, OrderEvent> kafkaTemplate) {
				this.topic = topic;
				this.kafkaTemplate = kafkaTemplate;
			}
			void sendMessage(OrderEvent e) {
				Message<OrderEvent> message = MessageBuilder.withPayload(e)
					.setHeader(KafkaHeaders.TOPIC, topic.name())
					.build();
				kafkaTemplate.send(message);
			}
		}`,
		`@Configuration class KafkaTopicConfig {
			@Value("${spring.kafka.topic.name}") private String orderTopic;
			@Bean public NewTopic orderTopic() { return TopicBuilder.name(orderTopic).build(); }
		}`)
	if got := topics(svc.KafkaProducers); len(got) != 1 || got[0] != "order_topics" {
		t.Fatalf("producer topic = %v, want [order_topics]", got)
	}
	if e := svc.KafkaProducers[0]; !e.Resolved || e.Confidence != model.Likely {
		t.Errorf("producer edge = %+v, want resolved/likely", e)
	}
}

// TestKafkaProducerMessageNoBeanUncertain: the same Message form, but no NewTopic
// bean to resolve — the producer edge is still emitted, honestly uncertain
// (never dropped), rather than fabricated.
func TestKafkaProducerMessageNoBeanUncertain(t *testing.T) {
	svc := kafkaScan(t, nil, `class P {
		private NewTopic topic;
		private KafkaTemplate<String, String> kt;
		void m() {
			Message<String> message = MessageBuilder.withPayload("x")
				.setHeader(KafkaHeaders.TOPIC, topic.name()).build();
			kt.send(message);
		}
	}`)
	if len(svc.KafkaProducers) != 1 {
		t.Fatalf("producers = %+v, want 1 (honest uncertain) edge", svc.KafkaProducers)
	}
	if e := svc.KafkaProducers[0]; e.Resolved || e.Topic != "" || e.Confidence != model.Uncertain {
		t.Errorf("edge = %+v, want unresolved/empty/uncertain", e)
	}
}

// TestKafkaProducerCrossClassWrapper reproduces a real-world case: a thin
// EventProducer wrapper sends with a topic PARAM whose call sites live in
// OTHER classes with resolvable constants. The topic must resolve through the
// repo-wide call-site index, capped likely (crosses a class boundary).
func TestKafkaProducerCrossClassWrapper(t *testing.T) {
	svc := kafkaScan(t, nil,
		`class KafkaConstant { public static final String PROFILE_TOPIC = "profile-onboarded"; }`,
		`class EventProducer {
			private final KafkaTemplate<String, String> kafkaTemplate;
			public void send(String topic, String message) { kafkaTemplate.send(topic, message); }
		}`,
		`class EventConsumer {
			private final EventProducer eventProducer;
			void handle(String result) { eventProducer.send(KafkaConstant.PROFILE_TOPIC, result); }
		}`)
	if got := topics(svc.KafkaProducers); len(got) != 1 || got[0] != "profile-onboarded" {
		t.Fatalf("producer topics = %v, want [profile-onboarded]", got)
	}
	if e := svc.KafkaProducers[0]; !e.Resolved || e.Confidence != model.Likely {
		t.Errorf("edge = %+v, want resolved/likely", e)
	}
}

// TestKafkaProducerWrapperUnrelatedReceiver: a same-named send() on an unrelated
// type must not contribute values to the wrapper's parameter.
func TestKafkaProducerWrapperUnrelatedReceiver(t *testing.T) {
	svc := kafkaScan(t, nil,
		`class EventProducer {
			private final KafkaTemplate<String, String> kafkaTemplate;
			public void send(String topic, String message) { kafkaTemplate.send(topic, message); }
		}`,
		`class MailSender { void m(JavaMailSender mail) { mail.send("not-a-topic", "x"); } }`)
	if len(svc.KafkaProducers) != 1 {
		t.Fatalf("producers = %+v, want 1", svc.KafkaProducers)
	}
	if e := svc.KafkaProducers[0]; e.Resolved || e.Topic != "" || e.Confidence != model.Uncertain {
		t.Errorf("edge = %+v, want honest unresolved (mail.send must not leak in)", e)
	}
}

// TestKafkaOutboxProducer reproduces a real-world case: the service produces
// by writing an OutBox row; the topic pattern lives in a Debezium connector
// JSON and is joined with the resolvable aggregate-type argument.
func TestKafkaOutboxProducer(t *testing.T) {
	svcObj := kafkaScanOutbox(t, []string{"${routedByValue}.events"},
		`class Topics { public static final String ORDER = "ORDER"; }`,
		`class OrderHandler {
			void persist(OutBoxRepository repo, Object payload) {
				repo.save(OutBox.builder().aggregateType(Topics.ORDER).payload(payload).build());
			}
		}`)
	if got := topics(svcObj.KafkaProducers); len(got) != 1 || got[0] != "ORDER.events" {
		t.Fatalf("outbox producer topics = %v, want [ORDER.events]", got)
	}
	if e := svcObj.KafkaProducers[0]; !e.Resolved || e.Confidence != model.Likely {
		t.Errorf("edge = %+v, want resolved/likely", e)
	}
}

// TestKafkaOutboxNoConnectorNoEdge: without connector JSONs in the repo,
// aggregateType() calls emit nothing (the gate keeps unrelated builders silent).
func TestKafkaOutboxNoConnectorNoEdge(t *testing.T) {
	svcObj := kafkaScan(t, nil,
		`class OrderHandler { void m(B b) { b.aggregateType("ORDER"); } }`)
	if len(svcObj.KafkaProducers) != 0 {
		t.Errorf("no connector -> no outbox edges, got %+v", svcObj.KafkaProducers)
	}
}

// kafkaScanOutbox is kafkaScan with outbox route patterns injected.
func kafkaScanOutbox(t *testing.T, routes []string, srcs ...string) *model.Service {
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
	idx := &provider.Index{Symbols: java.IndexSymbols(files), Types: java.IndexTypes(files, nil), OutboxRoutes: routes}
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

// TestKafkaOutboxSetterStyle: the setter form (`outbox.setAggregateType(X)`,
// uuhnaut69 customer/inventory style) resolves like the builder form.
func TestKafkaOutboxSetterStyle(t *testing.T) {
	svcObj := kafkaScanOutbox(t, []string{"${routedByValue}.events"},
		`class H {
			static final String CUSTOMER = "CUSTOMER";
			void m(OutBox outbox) { outbox.setAggregateType(CUSTOMER); }
		}`)
	if got := topics(svcObj.KafkaProducers); len(got) != 1 || got[0] != "CUSTOMER.events" {
		t.Fatalf("setter outbox topics = %v, want [CUSTOMER.events]", got)
	}
}
