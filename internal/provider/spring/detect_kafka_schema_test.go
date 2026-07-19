package spring

import (
	"testing"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/java"
	"github.com/farhadamjady/service-discovery/internal/query"
	"github.com/farhadamjady/service-discovery/internal/schema"
)

// kafkaScanWith runs the Kafka detector with a TypeIndex and a SchemaSources
// built from the given contract files (path -> content).
func kafkaScanWith(t *testing.T, contracts map[string]string, srcs ...string) *model.Service {
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
	sources := schema.NewSources()
	for p, body := range contracts {
		name, sch := parseContract(p, []byte(body))
		sources.Add(name, sch)
	}
	idx := &provider.Index{Types: java.IndexTypes(files, nil), Schemas: sources}
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

func TestKafkaSchemaFromAvroFile(t *testing.T) {
	avsc := `{"type":"record","name":"OrderEvent","fields":[{"name":"id","type":"string"}]}`
	svc := kafkaScanWith(t, map[string]string{"OrderEvent.avsc": avsc},
		`class C {
			KafkaTemplate<String, OrderEvent> kt;
			void m() { kt.send("orders", null); }
		}`)
	if len(svc.KafkaProducers) != 1 {
		t.Fatalf("producers = %+v", svc.KafkaProducers)
	}
	sc := svc.KafkaProducers[0].Schema
	if sc == nil || sc.Type != "OrderEvent" || sc.Confidence != model.Confirmed {
		t.Errorf("avro schema = %+v, want OrderEvent/confirmed", sc)
	}
}

func TestKafkaSchemaFromInCodeDTO(t *testing.T) {
	// No contract file; the payload type is a repo DTO -> in-code schema (likely).
	svc := kafkaScanWith(t, nil,
		`class OrderEvent { private String id; private int total; }`,
		`class C {
			KafkaTemplate<String, OrderEvent> kt;
			void m() { kt.send("orders", null); }
		}`)
	sc := svc.KafkaProducers[0].Schema
	if sc == nil || sc.Type != "OrderEvent" || sc.Confidence != model.Likely {
		t.Errorf("in-code schema = %+v, want OrderEvent/likely", sc)
	}
	if len(sc.Nested) != 2 {
		t.Errorf("OrderEvent should have 2 fields: %+v", sc.Nested)
	}
}

func TestKafkaSchemaSafeFail(t *testing.T) {
	// Unknown payload type: the edge is kept, the schema is dropped (nil).
	svc := kafkaScanWith(t, nil, `class C {
		KafkaTemplate<String, MysteryPayload> kt;
		void m() { kt.send("orders", null); }
	}`)
	if len(svc.KafkaProducers) != 1 {
		t.Fatalf("edge should be kept: %+v", svc.KafkaProducers)
	}
	if svc.KafkaProducers[0].Schema != nil {
		t.Errorf("schema should be nil (safe-fail): %+v", svc.KafkaProducers[0].Schema)
	}
	if svc.KafkaProducers[0].Topic != "orders" {
		t.Errorf("topic should still resolve: %+v", svc.KafkaProducers[0])
	}
}

func TestKafkaConsumerSchema(t *testing.T) {
	avsc := `{"type":"record","name":"OrderEvent","fields":[{"name":"id","type":"string"}]}`
	svc := kafkaScanWith(t, map[string]string{"OrderEvent.avsc": avsc},
		`class C {
			@KafkaListener(topics = "orders") void handle(OrderEvent event) {}
		}`)
	if len(svc.KafkaConsumers) != 1 || svc.KafkaConsumers[0].Schema == nil {
		t.Fatalf("consumer schema = %+v", svc.KafkaConsumers)
	}
	if svc.KafkaConsumers[0].Schema.Type != "OrderEvent" {
		t.Errorf("consumer schema type = %q, want OrderEvent", svc.KafkaConsumers[0].Schema.Type)
	}
}

func TestKafkaStringPayloadNoSchema(t *testing.T) {
	// A String-valued template has no structural schema, but the edge is emitted.
	svc := kafkaScanWith(t, nil, `class C {
		KafkaTemplate<String, String> kt;
		void m() { kt.send("orders", "msg"); }
	}`)
	if len(svc.KafkaProducers) != 1 || svc.KafkaProducers[0].Schema != nil {
		t.Errorf("string payload = %+v, want edge with nil schema", svc.KafkaProducers)
	}
}
