package spring

import (
	"testing"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/java"
	"github.com/farhadamjady/service-discovery/internal/query"
)

// scanWith runs the given detector over sources with a full local index
// (types + symbols + optional config + adapters).
func scanWith(t *testing.T, det provider.Detector, cfg provider.ConfigResolver, adapters []provider.AdapterSpec, srcs ...string) *model.Service {
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
	idx := &provider.Index{
		Types:    java.IndexTypes(files),
		Symbols:  java.IndexSymbols(files),
		Config:   cfg,
		Adapters: adapters,
	}
	res := java.NewEvaluator(idx)
	svc := model.NewService("s", "s", "")
	for _, p := range sortedJavaPaths(parsed) {
		if err := query.New().Run(parsed[p], []provider.Detector{det}, idx, res, svc); err != nil {
			t.Fatalf("run: %v", err)
		}
	}
	model.Sort(svc)
	return svc
}

// --- #18 @HttpExchange ---

func TestHTTPExchangeClient(t *testing.T) {
	svc := scanWith(t, httpExchangeDetector{}, nil, nil, `
@HttpExchange(url = "http://payment:8080")
interface PaymentClient {
	@GetExchange("/pay/{id}") Payment get(@PathVariable String id);
	@PostExchange("/pay") Payment create(@RequestBody Payment p);
}`)
	if len(svc.OutboundDependencies) != 1 {
		t.Fatalf("deps = %+v, want 1 (methods dedup to the interface)", svc.OutboundDependencies)
	}
	d := svc.OutboundDependencies[0]
	if d.URL != "http://payment:8080" || d.Detection != model.DetectHTTPExchange || !d.Resolved {
		t.Errorf("dep = %+v, want url/httpexchange/resolved", d)
	}
}

func TestHTTPExchangeNameOnly(t *testing.T) {
	svc := scanWith(t, httpExchangeDetector{}, nil, nil, `
interface PaymentClient {
	@GetExchange("/pay") Payment get();
}`)
	if len(svc.OutboundDependencies) != 1 || svc.OutboundDependencies[0].TargetName != "PaymentClient" {
		t.Errorf("deps = %+v, want logical name PaymentClient", svc.OutboundDependencies)
	}
}

// --- #14 Spring Cloud Stream ---

func TestCloudStreamBindings(t *testing.T) {
	cfg := buildStore(t, nil, map[string]string{
		"application.yml": "spring:\n  cloud:\n    stream:\n      bindings:\n        orders-in-0:\n          destination: orders.v1\n",
	})
	svc := scanWith(t, cloudStreamDetector{}, cfg, nil, `
class StreamConfig {
	@Bean
	public Consumer<Order> orders() { return o -> {}; }
	@Bean
	public Supplier<Event> events() { return () -> null; }
}
class Order { private String id; }
class Event { private String kind; }`)
	if len(svc.KafkaConsumers) != 1 || svc.KafkaConsumers[0].Topic != "orders.v1" {
		t.Fatalf("consumers = %+v, want orders.v1 (bound destination)", svc.KafkaConsumers)
	}
	if svc.KafkaConsumers[0].Schema == nil || svc.KafkaConsumers[0].Schema.Type != "Order" {
		t.Errorf("consumer schema = %+v, want Order", svc.KafkaConsumers[0].Schema)
	}
	// supplier without an explicit destination -> default = binding name
	if len(svc.KafkaProducers) != 1 || svc.KafkaProducers[0].Topic != "events-out-0" {
		t.Errorf("producers = %+v, want default binding name events-out-0", svc.KafkaProducers)
	}
}

func TestCloudStreamGate(t *testing.T) {
	// No spring.cloud.stream config -> a plain Function bean emits nothing.
	cfg := buildStore(t, nil, map[string]string{"application.yml": "a: 1\n"})
	svc := scanWith(t, cloudStreamDetector{}, cfg, nil, `
class C { @Bean public Consumer<String> logger() { return s -> {}; } }`)
	if len(svc.KafkaConsumers) != 0 {
		t.Errorf("gate failed: %+v", svc.KafkaConsumers)
	}
}

// --- #15 adapter file ---

func TestAdapterWrapper(t *testing.T) {
	adapters := []provider.AdapterSpec{{Method: "callService", TargetArg: 0, Protocol: model.ProtoREST}}
	svc := scanWith(t, adapterDetector{}, nil, adapters, `
class C {
	PlatformClient pc;
	void m() { pc.callService("payment-service", "/pay"); }
}`)
	if len(svc.OutboundDependencies) != 1 {
		t.Fatalf("deps = %+v, want 1", svc.OutboundDependencies)
	}
	d := svc.OutboundDependencies[0]
	if d.TargetName != "payment-service" || d.Detection != model.DetectAdapter {
		t.Errorf("dep = %+v, want payment-service via adapter", d)
	}
}

// --- #17 meta-annotations ---

func TestMetaAnnotationController(t *testing.T) {
	svc := scanWith(t, restDetector{}, nil, nil, `
@RestController
@RequestMapping("/api")
@interface MyController {}`, `
@MyController
class UserController {
	@GetMapping("/users") String list() { return null; }
}`)
	if len(svc.Endpoints) != 1 {
		t.Fatalf("endpoints = %+v, want 1 (meta @RestController)", svc.Endpoints)
	}
	if svc.Endpoints[0].Path != "/api/users" {
		t.Errorf("path = %q, want /api/users (meta base + method)", svc.Endpoints[0].Path)
	}
}

func TestMetaAnnotationMethod(t *testing.T) {
	svc := scanWith(t, restDetector{}, nil, nil, `
@GetMapping("/ping")
@interface MyGet {}`, `
@RestController
class C {
	@MyGet String ping() { return "ok"; }
}`)
	if len(svc.Endpoints) != 1 || svc.Endpoints[0].Method != "GET" || svc.Endpoints[0].Path != "/ping" {
		t.Errorf("endpoints = %+v, want GET /ping via meta", svc.Endpoints)
	}
}

func TestPlainAnnotatedClassNotController(t *testing.T) {
	svc := scanWith(t, restDetector{}, nil, nil, `
@Service
class NotAController { @GetMapping("/nope") String x() { return null; } }`)
	if len(svc.Endpoints) != 0 {
		t.Errorf("non-controller emitted endpoints: %+v", svc.Endpoints)
	}
}
