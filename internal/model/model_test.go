package model

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestNewServiceContract pins the empty output contract: all slices emit as []
// (never null), field order is stable, and the exact byte shape is the one the
// backend depends on. This is the golden the CLI's empty-run test builds on (PR 4).
func TestNewServiceContract(t *testing.T) {
	svc := NewService("svc-1", "cart", "github.com/acme/cart")

	got, err := json.Marshal(svc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	const want = `{"service_id":"svc-1","service_name":"cart","repository":"github.com/acme/cart",` +
		`"endpoints":[],"outbound_dependencies":[],"kafka_producers":[],"kafka_consumers":[],` +
		`"databases_used":[],"config_dependencies":[]}`

	if string(got) != want {
		t.Errorf("contract JSON drift:\n got: %s\nwant: %s", got, want)
	}
}

// TestMarshalByteStable guards the byte-stability invariant: the same graph must
// marshal identically every time. Marshaling here is deterministic by construction;
// this test exists so a future field or map iteration that breaks it fails loudly.
func TestMarshalByteStable(t *testing.T) {
	build := func() *Service {
		svc := NewService("id", "name", "repo")
		svc.Endpoints = append(svc.Endpoints,
			Endpoint{Method: "GET", Path: "/b", Protocol: ProtoREST, Detection: DetectAnnotation, Confidence: Confirmed},
			Endpoint{Method: "GET", Path: "/a", Protocol: ProtoREST, Detection: DetectAnnotation, Confidence: Confirmed},
		)
		Sort(svc)
		return svc
	}
	a, _ := json.Marshal(build())
	b, _ := json.Marshal(build())
	if string(a) != string(b) {
		t.Errorf("marshal not byte-stable:\n a: %s\n b: %s", a, b)
	}
}

// TestSortDeterministic checks that Sort imposes a total order independent of
// insertion order, and is idempotent (re-sorting a sorted graph is a no-op).
func TestSortDeterministic(t *testing.T) {
	mk := func() *Service {
		svc := NewService("id", "name", "repo")
		svc.Endpoints = []Endpoint{
			{Method: "POST", Path: "/a"},
			{Method: "GET", Path: "/b"},
			{Method: "GET", Path: "/a"},
		}
		svc.OutboundDependencies = []Dependency{
			{TargetName: "b-svc", Detection: DetectFeign},
			{TargetName: "a-svc", Detection: DetectWebClient},
			{TargetName: "a-svc", Detection: DetectFeign},
		}
		return svc
	}

	svc := mk()
	Sort(svc)

	wantEP := []string{"GET /a", "GET /b", "POST /a"}
	for i, w := range wantEP {
		if got := EndpointKey(svc.Endpoints[i]); got != w {
			t.Errorf("endpoint[%d] = %q, want %q", i, got, w)
		}
	}
	wantDep := []string{"a-svc|feign", "a-svc|webclient", "b-svc|feign"}
	for i, w := range wantDep {
		if got := DependencyKey(svc.OutboundDependencies[i]); got != w {
			t.Errorf("dependency[%d] = %q, want %q", i, got, w)
		}
	}

	// Idempotence: sorting again changes nothing.
	before, _ := json.Marshal(svc)
	Sort(svc)
	after, _ := json.Marshal(svc)
	if string(before) != string(after) {
		t.Errorf("Sort not idempotent:\n before: %s\n after:  %s", before, after)
	}
}

// TestSortNestedSchema verifies nested schema fields are ordered by wire name,
// recursively — so schema output is byte-stable regardless of field discovery order.
func TestSortNestedSchema(t *testing.T) {
	svc := NewService("id", "name", "repo")
	svc.Endpoints = []Endpoint{{
		Method: "GET", Path: "/x",
		Response: &Schema{Type: "object", Nested: []Schema{
			{Name: "zeta", Type: "string"},
			{Name: "alpha", Type: "object", Nested: []Schema{
				{Name: "y", Type: "int"},
				{Name: "x", Type: "int"},
			}},
		}},
	}}
	Sort(svc)

	top := svc.Endpoints[0].Response.Nested
	if top[0].Name != "alpha" || top[1].Name != "zeta" {
		t.Errorf("top-level nested not sorted: %q, %q", top[0].Name, top[1].Name)
	}
	inner := top[0].Nested
	if inner[0].Name != "x" || inner[1].Name != "y" {
		t.Errorf("inner nested not sorted: %q, %q", inner[0].Name, inner[1].Name)
	}
}

// TestRequiredAlwaysEmitted locks the no-omitempty decision: a schema field always
// carries "required" so the backend can tell an unsignaled "unknown" apart from a
// deliberate "optional". A zero-value Requiredness must still appear in the JSON.
func TestRequiredAlwaysEmitted(t *testing.T) {
	for _, s := range []Schema{
		{Type: "string"},                        // zero-value Requiredness ("")
		{Type: "string", Required: ReqUnknown},  // explicit unknown
		{Type: "string", Required: ReqOptional}, // optional
	} {
		b, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), `"required":`) {
			t.Errorf(`"required" missing from schema JSON: %s`, b)
		}
	}
}
