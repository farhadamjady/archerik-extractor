package spring

import (
	"testing"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/java"
	"github.com/farhadamjady/service-discovery/internal/query"
)

// restEndpoints runs the REST detector with a TypeIndex over all sources and
// returns the endpoints (so request/response schemas are attached).
func restEndpoints(t *testing.T, srcs ...string) []model.Endpoint {
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
	idx := &provider.Index{Types: java.IndexTypes(files)}
	svc := model.NewService("s", "s", "")
	for _, p := range sortedJavaPaths(parsed) {
		if err := query.New().Run(parsed[p], []provider.Detector{restDetector{}}, idx, nil, svc); err != nil {
			t.Fatalf("run: %v", err)
		}
	}
	model.Sort(svc)
	return svc.Endpoints
}

func TestRESTRequestResponseSchema(t *testing.T) {
	eps := restEndpoints(t,
		`class Order { private String id; private int total; }`,
		`@RestController class OrderController {
			@PostMapping("/orders")
			Order create(@RequestBody @Valid Order body) { return null; }
		}`)
	if len(eps) != 1 {
		t.Fatalf("got %d endpoints, want 1", len(eps))
	}
	e := eps[0]
	if e.Request == nil || e.Request.Type != "Order" {
		t.Errorf("request = %+v, want Order", e.Request)
	}
	if e.Response == nil || e.Response.Type != "Order" {
		t.Errorf("response = %+v, want Order", e.Response)
	}
	if len(e.Response.Nested) != 2 {
		t.Errorf("Order should have 2 fields, got %+v", e.Response.Nested)
	}
}

func TestRESTVoidNoResponseSchema(t *testing.T) {
	eps := restEndpoints(t, `@RestController class C {
		@DeleteMapping("/x/{id}") void del(@PathVariable String id) {}
	}`)
	if len(eps) != 1 || eps[0].Response != nil {
		t.Errorf("void handler should have no response schema: %+v", eps)
	}
}

func TestRESTListResponseSchema(t *testing.T) {
	eps := restEndpoints(t,
		`class User { private String name; }`,
		`@RestController class C { @GetMapping("/users") java.util.List<User> all() { return null; } }`)
	if len(eps) != 1 || eps[0].Response == nil || eps[0].Response.Type != "array" || eps[0].Response.Items != "User" {
		t.Errorf("list response = %+v, want array items=User", eps[0].Response)
	}
}
