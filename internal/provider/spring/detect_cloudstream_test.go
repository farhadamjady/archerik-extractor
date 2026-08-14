package spring

import (
	"testing"

	"github.com/farhadamjady/archerik-extractor/internal/model"
)

// TestFunctionComposition proves K3: with spring.cloud.function.definition=a|b,
// the composed pipeline exposes ONE input (from the first bean a) and ONE output
// (from the last bean b) on the composite binding name — not four per-bean
// bindings, and the intermediate type is not a topic.
func TestFunctionComposition(t *testing.T) {
	cfg := buildStore(t, nil, map[string]string{
		"application.yml": "spring:\n" +
			"  cloud:\n" +
			"    function:\n" +
			"      definition: a|b\n" +
			"    stream:\n" +
			"      bindings:\n" +
			"        a|b-in-0:\n" +
			"          destination: in.topic\n" +
			"        a|b-out-0:\n" +
			"          destination: out.topic\n",
	})
	svc := scanWith(t, cloudStreamDetector{}, cfg, nil, `
class Cfg {
	@Bean public Function<In, Mid> a() { return x -> null; }
	@Bean public Function<Mid, Out> b() { return x -> null; }
}
class In { private String x; }
class Mid { private String y; }
class Out { private String z; }`)

	if len(svc.KafkaConsumers) != 1 {
		t.Fatalf("consumers = %+v, want 1 (composite input)", svc.KafkaConsumers)
	}
	if c := svc.KafkaConsumers[0]; c.Topic != "in.topic" || c.Schema == nil || c.Schema.Type != "In" {
		t.Errorf("consumer = %+v, want in.topic / In", c)
	}
	if len(svc.KafkaProducers) != 1 {
		t.Fatalf("producers = %+v, want 1 (composite output)", svc.KafkaProducers)
	}
	if p := svc.KafkaProducers[0]; p.Topic != "out.topic" || p.Schema == nil || p.Schema.Type != "Out" {
		t.Errorf("producer = %+v, want out.topic / Out", p)
	}
	// The intermediate Mid type is internal — it must not surface as a topic.
	for _, e := range append(svc.KafkaConsumers, svc.KafkaProducers...) {
		if e.Schema != nil && e.Schema.Type == "Mid" {
			t.Errorf("intermediate Mid leaked as a topic: %+v", e)
		}
	}
}

// TestCompositionInactiveBean proves a bean absent from the definition emits
// nothing (only definition-named functions are active).
func TestCompositionInactiveBean(t *testing.T) {
	cfg := buildStore(t, nil, map[string]string{
		"application.yml": "spring:\n" +
			"  cloud:\n" +
			"    function:\n" +
			"      definition: a\n" +
			"    stream:\n" +
			"      bindings:\n" +
			"        a-out-0:\n" +
			"          destination: a.topic\n",
	})
	svc := scanWith(t, cloudStreamDetector{}, cfg, nil, `
class Cfg {
	@Bean public Supplier<Out> a() { return () -> null; }
	@Bean public Supplier<Other> unused() { return () -> null; }
}
class Out { private String z; }
class Other { private String w; }`)

	if len(svc.KafkaProducers) != 1 || svc.KafkaProducers[0].Topic != "a.topic" {
		t.Fatalf("producers = %+v, want only a.topic (unused bean inactive)", svc.KafkaProducers)
	}
}

// TestStreamBridgeCompositeProducer proves K2: streamBridge.send(binding, data)
// resolves the binding's destination through config, and a composite
// (comma-separated) destination fans out to one producer edge per destination —
// three here — each carrying the payload DTO's schema.
func TestStreamBridgeCompositeProducer(t *testing.T) {
	cfg := buildStore(t, nil, map[string]string{
		"application.yml": "spring:\n  cloud:\n    stream:\n      bindings:\n        orders-out-0:\n          destination: orders.a,orders.b,orders.c\n",
	})
	svc := scanWith(t, cloudStreamDetector{}, cfg, nil, `
class OrderProducer {
	private StreamBridge streamBridge;
	public void publish(OrderEvent event) {
		streamBridge.send("orders-out-0", event);
	}
}
class OrderEvent { private String id; private String status; }`)

	if len(svc.KafkaProducers) != 3 {
		t.Fatalf("producers = %d, want 3 (composite destination)", len(svc.KafkaProducers))
	}
	topics := map[string]bool{}
	for _, p := range svc.KafkaProducers {
		topics[p.Topic] = true
		if p.Detection != model.DetectCloudStream || !p.Resolved {
			t.Errorf("edge = %+v, want cloudstream/resolved", p)
		}
		if p.Schema == nil || p.Schema.Type != "OrderEvent" {
			t.Errorf("schema = %+v, want OrderEvent", p.Schema)
		}
	}
	for _, want := range []string{"orders.a", "orders.b", "orders.c"} {
		if !topics[want] {
			t.Errorf("missing destination %q; got %v", want, topics)
		}
	}
}

// TestStreamBridgeDefaultDestination proves the fallback: with no binding config,
// the destination defaults to the binding name (Spring's dynamic-destination
// behavior). Gated on cloud-stream config existing at all.
func TestStreamBridgeDefaultDestination(t *testing.T) {
	cfg := buildStore(t, nil, map[string]string{
		"application.yml": "spring:\n  cloud:\n    stream:\n      bindings:\n        other-out-0:\n          destination: x\n",
	})
	svc := scanWith(t, cloudStreamDetector{}, cfg, nil, `
class P {
	private StreamBridge streamBridge;
	public void go() { streamBridge.send("audit-out-0", new AuditEvent()); }
}
class AuditEvent { private String who; }`)

	if len(svc.KafkaProducers) != 1 || svc.KafkaProducers[0].Topic != "audit-out-0" {
		t.Fatalf("producers = %+v, want default binding name audit-out-0", svc.KafkaProducers)
	}
}

// TestStreamBridgeReceiverGuard proves send() on a non-StreamBridge receiver is
// not a producer.
func TestStreamBridgeReceiverGuard(t *testing.T) {
	cfg := buildStore(t, nil, map[string]string{
		"application.yml": "spring:\n  cloud:\n    stream:\n      bindings:\n        x-out-0:\n          destination: x\n",
	})
	svc := scanWith(t, cloudStreamDetector{}, cfg, nil, `
class Mailer {
	private SmtpClient client;
	public void go() { client.send("hello", "body"); }
}`)
	if len(svc.KafkaProducers) != 0 {
		t.Errorf("producers = %+v, want 0 (client is not StreamBridge)", svc.KafkaProducers)
	}
}

// TestStreamBridgeGate proves the cloud-stream config gate applies to the
// imperative producer too: no spring.cloud.stream config -> nothing.
func TestStreamBridgeGate(t *testing.T) {
	cfg := buildStore(t, nil, map[string]string{"application.yml": "a: 1\n"})
	svc := scanWith(t, cloudStreamDetector{}, cfg, nil, `
class P {
	private StreamBridge streamBridge;
	public void go() { streamBridge.send("orders-out-0", new E()); }
}
class E { private String id; }`)
	if len(svc.KafkaProducers) != 0 {
		t.Errorf("gate failed: %+v", svc.KafkaProducers)
	}
}
