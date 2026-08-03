package spring

import (
	"testing"

	"github.com/farhadamjady/service-discovery/internal/model"
)

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
