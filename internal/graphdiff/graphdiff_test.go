package graphdiff

import (
	"encoding/json"
	"testing"

	"github.com/farhadamjady/service-discovery/internal/model"
)

func svc(mut func(*model.Service)) *model.Service {
	s := model.NewService("orders", "orders", "repo")
	s.Endpoints = []model.Endpoint{{
		Method: "GET", Path: "/orders/{id}",
		Response: &model.Schema{Type: "Order", Required: model.ReqUnknown, Nested: []model.Schema{
			{Name: "id", Type: "string", Required: model.ReqRequired},
			{Name: "total", Type: "integer", Required: model.ReqOptional},
		}},
		Protocol: model.ProtoREST, Detection: model.DetectAnnotation, Confidence: model.Confirmed,
	}}
	s.OutboundDependencies = []model.Dependency{{
		TargetName: "payment-service", URL: "http://payment:8080",
		Protocol: model.ProtoREST, Detection: model.DetectFeign,
		Confidence: model.Likely, Resolved: true,
	}}
	s.KafkaProducers = []model.KafkaEdge{{
		Topic: "orders.v1", Resolved: true,
		Schema:   &model.Schema{Type: "OrderEvent", Required: model.ReqUnknown},
		Protocol: model.ProtoKafka, Detection: model.DetectKafka, Confidence: model.Confirmed,
	}}
	if mut != nil {
		mut(s)
	}
	model.Sort(s)
	return s
}

func TestIdenticalIsEmpty(t *testing.T) {
	d := Compute(svc(nil), svc(nil))
	if !d.Empty() {
		b, _ := json.Marshal(d)
		t.Fatalf("identical services must diff empty, got %s", b)
	}
}

func TestAddedDependency(t *testing.T) {
	head := svc(func(s *model.Service) {
		s.OutboundDependencies = append(s.OutboundDependencies, model.Dependency{
			TargetName: "shipping-service", Protocol: model.ProtoREST,
			Detection: model.DetectFeign, Confidence: model.Confirmed, Resolved: true,
		})
	})
	d := Compute(svc(nil), head)
	if len(d.Outbound.Added) != 1 || d.Outbound.Added[0].TargetName != "shipping-service" {
		t.Fatalf("added = %+v, want shipping-service", d.Outbound.Added)
	}
	if d.Summary.Added != 1 || d.Summary.Removed != 0 || d.Summary.Changed != 0 {
		t.Errorf("summary = %+v", d.Summary)
	}
}

func TestRemovedEndpointAndKafka(t *testing.T) {
	head := svc(func(s *model.Service) {
		s.Endpoints = nil
		s.KafkaProducers = nil
	})
	d := Compute(svc(nil), head)
	if len(d.Endpoints.Removed) != 1 || d.Endpoints.Removed[0].Path != "/orders/{id}" {
		t.Errorf("endpoint removed = %+v", d.Endpoints.Removed)
	}
	if len(d.KafkaProducers.Removed) != 1 || d.KafkaProducers.Removed[0].Topic != "orders.v1" {
		t.Errorf("kafka removed = %+v", d.KafkaProducers.Removed)
	}
	if d.Summary.Removed != 2 {
		t.Errorf("summary removed = %d, want 2", d.Summary.Removed)
	}
}

func TestSchemaFieldChanges(t *testing.T) {
	head := svc(func(s *model.Service) {
		// total: integer -> number; drop id; add note
		s.Endpoints[0].Response.Nested = []model.Schema{
			{Name: "total", Type: "number", Required: model.ReqOptional},
			{Name: "note", Type: "string", Required: model.ReqOptional},
		}
	})
	d := Compute(svc(nil), head)
	if len(d.Endpoints.Changed) != 1 {
		t.Fatalf("changed = %+v, want 1", d.Endpoints.Changed)
	}
	c := d.Endpoints.Changed[0]
	if c.Key != "GET /orders/{id}" || len(c.Changes) != 1 || c.Changes[0] != "response_schema" {
		t.Errorf("change = %+v", c)
	}
	got := map[string]string{}
	for _, fd := range c.SchemaDiff {
		got[fd.Field] = fd.Change
	}
	want := map[string]string{
		"response.id":    "removed",
		"response.note":  "added",
		"response.total": "type_changed",
	}
	for f, w := range want {
		if got[f] != w {
			t.Errorf("field %q = %q, want %q (all: %v)", f, got[f], w, got)
		}
	}
}

func TestConfidenceMovement(t *testing.T) {
	head := svc(func(s *model.Service) {
		s.OutboundDependencies[0].Confidence = model.Uncertain
		s.OutboundDependencies[0].Resolved = false
		s.OutboundDependencies[0].URL = ""
	})
	d := Compute(svc(nil), head)
	if len(d.Outbound.Changed) != 1 {
		t.Fatalf("changed = %+v", d.Outbound.Changed)
	}
	c := d.Outbound.Changed[0]
	has := map[string]bool{}
	for _, ch := range c.Changes {
		has[ch] = true
	}
	if !has["confidence"] || !has["resolved"] || !has["url"] {
		t.Errorf("changes = %v, want confidence+resolved+url", c.Changes)
	}
}

func TestKafkaSchemaAdded(t *testing.T) {
	base := svc(func(s *model.Service) { s.KafkaProducers[0].Schema = nil })
	d := Compute(base, svc(nil))
	if len(d.KafkaProducers.Changed) != 1 {
		t.Fatalf("changed = %+v", d.KafkaProducers.Changed)
	}
	sd := d.KafkaProducers.Changed[0].SchemaDiff
	if len(sd) != 1 || sd[0].Change != "added" || sd[0].Field != "(root)" {
		t.Errorf("schema diff = %+v, want root added", sd)
	}
}

// TestProducerConsumerSeparate: the same topic in different roles is two
// different identities — dropping the consumer does not touch the producer.
func TestProducerConsumerSeparate(t *testing.T) {
	base := svc(func(s *model.Service) {
		s.KafkaConsumers = []model.KafkaEdge{{Topic: "orders.v1", Resolved: true,
			Protocol: model.ProtoKafka, Detection: model.DetectKafka, Confidence: model.Confirmed}}
	})
	d := Compute(base, svc(nil))
	if len(d.KafkaConsumers.Removed) != 1 || len(d.KafkaProducers.Removed) != 0 {
		t.Errorf("consumer removed = %+v, producer removed = %+v",
			d.KafkaConsumers.Removed, d.KafkaProducers.Removed)
	}
}

// TestPathChangeIsRemoveAdd pins the no-rename decision.
func TestPathChangeIsRemoveAdd(t *testing.T) {
	head := svc(func(s *model.Service) { s.Endpoints[0].Path = "/v2/orders/{id}" })
	d := Compute(svc(nil), head)
	if len(d.Endpoints.Removed) != 1 || len(d.Endpoints.Added) != 1 || len(d.Endpoints.Changed) != 0 {
		t.Errorf("path change: removed=%d added=%d changed=%d, want 1/1/0",
			len(d.Endpoints.Removed), len(d.Endpoints.Added), len(d.Endpoints.Changed))
	}
}

// TestDeterministicJSON: the diff of the same pair marshals byte-identically.
func TestDeterministicJSON(t *testing.T) {
	head := svc(func(s *model.Service) {
		s.Endpoints[0].Confidence = model.Likely
		s.OutboundDependencies = append(s.OutboundDependencies,
			model.Dependency{TargetName: "b-svc", Detection: model.DetectFeign, Protocol: model.ProtoREST, Confidence: model.Confirmed},
			model.Dependency{TargetName: "a-svc", Detection: model.DetectFeign, Protocol: model.ProtoREST, Confidence: model.Confirmed},
		)
	})
	a, _ := json.Marshal(Compute(svc(nil), head))
	b, _ := json.Marshal(Compute(svc(nil), head))
	if string(a) != string(b) {
		t.Error("diff JSON not byte-stable")
	}
	// added entries sorted by identity key
	d := Compute(svc(nil), head)
	if d.Outbound.Added[0].TargetName != "a-svc" || d.Outbound.Added[1].TargetName != "b-svc" {
		t.Errorf("added not sorted: %+v", d.Outbound.Added)
	}
}
