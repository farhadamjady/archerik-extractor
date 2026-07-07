package spring

import (
	"fmt"
	"sort"
	"testing"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/java"
	"github.com/farhadamjady/service-discovery/internal/query"
)

// endpoints runs the REST detector over one Java source and returns its
// endpoints as sorted "VERB PATH" strings, exercising the real query engine.
func endpoints(t *testing.T, src string) []string {
	t.Helper()
	f, err := java.NewParser().Parse("C.java", []byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	svc := model.NewService("s", "s", "")
	if err := query.New().Run(f, []provider.Detector{restDetector{}}, &provider.Index{}, nil, svc); err != nil {
		t.Fatalf("run: %v", err)
	}
	model.Sort(svc)
	var out []string
	for _, e := range svc.Endpoints {
		out = append(out, fmt.Sprintf("%s %s", e.Method, e.Path))
	}
	sort.Strings(out)
	return out
}

func TestRESTEndpoints(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{
			name: "class base + method path, path variable preserved",
			src: `@RestController @RequestMapping("/api/v1")
				class UserController {
					@GetMapping("/users/{id}") String get() { return null; }
					@PostMapping("/users") String create() { return null; }
				}`,
			want: []string{"GET /api/v1/users/{id}", "POST /api/v1/users"},
		},
		{
			name: "no class mapping",
			src: `@RestController class OrderController {
					@DeleteMapping("/orders/{id}") void del() {}
				}`,
			want: []string{"DELETE /orders/{id}"},
		},
		{
			name: "method mapping with no path maps to base",
			src: `@RestController @RequestMapping("/health") class H {
					@GetMapping String ping() { return "ok"; }
				}`,
			want: []string{"GET /health"},
		},
		{
			name: "value= named attribute",
			src: `@RestController class C {
					@PutMapping(value = "/items/{id}") void put() {}
				}`,
			want: []string{"PUT /items/{id}"},
		},
		{
			name: "RequestMapping with explicit method",
			src: `@RestController class C {
					@RequestMapping(value = "/legacy", method = RequestMethod.POST) void l() {}
				}`,
			want: []string{"POST /legacy"},
		},
		{
			name: "RequestMapping with method array -> one endpoint per verb",
			src: `@RestController class C {
					@RequestMapping(value="/multi", method={RequestMethod.GET, RequestMethod.POST}) void m() {}
				}`,
			want: []string{"GET /multi", "POST /multi"},
		},
		{
			name: "non-handler methods ignored",
			src: `@RestController class C {
					@GetMapping("/a") String a() { return null; }
					private void helper() {}
				}`,
			want: []string{"GET /a"},
		},
		{
			name: "not a controller -> nothing",
			src: `@Service class NotAController {
					@GetMapping("/nope") String x() { return null; }
				}`,
			want: nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := endpoints(t, c.src)
			if fmt.Sprint(got) != fmt.Sprint(c.want) {
				t.Errorf("endpoints = %v, want %v", got, c.want)
			}
		})
	}
}

// TestRESTEdgeFields pins the orthogonal edge fields on a literal-path endpoint:
// protocol=rest, detection=annotation, confidence=confirmed.
func TestRESTEdgeFields(t *testing.T) {
	f, err := java.NewParser().Parse("C.java", []byte(
		`@RestController class C { @GetMapping("/x") String x() { return null; } }`))
	if err != nil {
		t.Fatal(err)
	}
	svc := model.NewService("s", "s", "")
	if err := query.New().Run(f, []provider.Detector{restDetector{}}, &provider.Index{}, nil, svc); err != nil {
		t.Fatal(err)
	}
	if len(svc.Endpoints) != 1 {
		t.Fatalf("got %d endpoints, want 1", len(svc.Endpoints))
	}
	e := svc.Endpoints[0]
	if e.Protocol != model.ProtoREST || e.Detection != model.DetectAnnotation || e.Confidence != model.Confirmed {
		t.Errorf("edge fields = (%s,%s,%s), want (rest,annotation,confirmed)", e.Protocol, e.Detection, e.Confidence)
	}
}

// TestRESTComputedPathUncertain: a non-literal mapping path (constant/placeholder)
// still emits an endpoint, flagged uncertain (the value resolver refines it later).
func TestRESTComputedPathUncertain(t *testing.T) {
	f, _ := java.NewParser().Parse("C.java", []byte(
		`@RestController class C { @GetMapping(PATHS.USERS) String x() { return null; } }`))
	svc := model.NewService("s", "s", "")
	if err := query.New().Run(f, []provider.Detector{restDetector{}}, &provider.Index{}, nil, svc); err != nil {
		t.Fatal(err)
	}
	if len(svc.Endpoints) != 1 {
		t.Fatalf("got %d endpoints, want 1", len(svc.Endpoints))
	}
	if svc.Endpoints[0].Confidence != model.Uncertain {
		t.Errorf("confidence = %s, want uncertain", svc.Endpoints[0].Confidence)
	}
}
