package aspnet

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/csharp"
	"github.com/farhadamjady/service-discovery/internal/query"
	"github.com/farhadamjady/service-discovery/internal/scan"
)

var _ provider.Provider = (*Provider)(nil)

func endpoints(t *testing.T, src string) []string {
	t.Helper()
	f, err := csharp.NewParser().Parse("C.cs", []byte(src))
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
			name: "[controller] token + verb paths + constraint stripping",
			src: `[ApiController]
				[Route("api/[controller]")]
				public class ProductsController : ControllerBase {
					[HttpGet] public IEnumerable<Product> GetAll() => null;
					[HttpGet("{id:int}")] public ActionResult<Product> GetById(int id) => null;
					[HttpPost] public IActionResult Create([FromBody] ProductDto dto) => Ok();
					[HttpDelete("{id}")] public IActionResult Delete(int id) => Ok();
				}`,
			want: []string{"DELETE /api/Products/{id}", "GET /api/Products", "GET /api/Products/{id}", "POST /api/Products"},
		},
		{
			name: "literal base route + method [Route]",
			src: `[Route("orders")]
				public class OrderController : ControllerBase {
					[HttpGet]
					[Route("open")]
					public IActionResult Open() => Ok();
				}`,
			want: []string{"GET /orders/open"},
		},
		{
			name: "non-controller class ignored",
			src: `public class ProductService {
					[HttpGet] public string Get() => "";
				}`,
			want: nil,
		},
		{
			name: "absolute method template overrides base",
			src: `[Route("api/[controller]")]
				public class PingController : ControllerBase {
					[HttpGet("/health")] public IActionResult Health() => Ok();
				}`,
			want: []string{"GET /health"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := endpoints(t, tc.src)
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDetectors(t *testing.T) {
	want := map[string]model.Protocol{
		"aspnet.rest":    model.ProtoREST,
		"aspnet.minimal": model.ProtoREST,
		"aspnet.client":  model.ProtoREST,
		"aspnet.kafka":   model.ProtoKafka,
	}
	dets := New().Detectors()
	if len(dets) != len(want) {
		t.Fatalf("got %d detectors, want %d", len(dets), len(want))
	}
	for _, d := range dets {
		if want[d.Name()] != d.Protocol() {
			t.Errorf("detector %q protocol %q", d.Name(), d.Protocol())
		}
	}
}

func TestMatch(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "Api.csproj", `<Project Sdk="Microsoft.NET.Sdk.Web"></Project>`)
	writeFile(t, root, "Controllers/ProductsController.cs", "using Microsoft.AspNetCore.Mvc;\npublic class ProductsController {}")
	m, score := New().Match(root, scan.NewOSFileTree(root, nil))
	if !m || score != 7 { // cs(1) + web sdk(3) + AspNetCore.Mvc using(3)
		t.Fatalf("matched=%v score=%d, want true/7", m, score)
	}
	javaRepo := t.TempDir()
	writeFile(t, javaRepo, "src/main/java/App.java", "class App {}")
	if m, _ := New().Match(javaRepo, scan.NewOSFileTree(javaRepo, nil)); m {
		t.Error("must not match a non-C# repo")
	}
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func minimalEndpoints(t *testing.T, src string) []string {
	t.Helper()
	f, err := csharp.NewParser().Parse("Program.cs", []byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	svc := model.NewService("s", "s", "")
	if err := query.New().Run(f, []provider.Detector{minimalDetector{}}, &provider.Index{}, nil, svc); err != nil {
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

func TestMinimalAPIs(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{
			name: "direct MapGet/MapPost on the app",
			src: `var app = builder.Build();
				app.MapGet("/todos", () => repo.All());
				app.MapPost("/todos", (Todo t) => repo.Add(t));`,
			want: []string{"GET /todos", "POST /todos"},
		},
		{
			name: "MapGroup prefix + constraint stripping",
			src: `var app = builder.Build();
				var group = app.MapGroup("/api/items");
				group.MapGet("/{id:int}", GetItem);
				group.MapDelete("/{id}", DeleteItem);`,
			want: []string{"DELETE /api/items/{id}", "GET /api/items/{id}"},
		},
		{
			name: "nested groups compose",
			src: `var api = app.MapGroup("/api");
				var todos = api.MapGroup("/todos");
				todos.MapGet("/{id}", h);`,
			want: []string{"GET /api/todos/{id}"},
		},
		{
			name: "chained group without a variable",
			src:  `app.MapGroup("/v1").MapGet("/ping", h);`,
			want: []string{"GET /v1/ping"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := minimalEndpoints(t, tc.src)
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMinimalGroupThroughBuilderChain(t *testing.T) {
	// The group variable is declared through a fluent chain — the MapGroup sits
	// deeper in the receiver chain (real pattern: filters/auth on the group).
	src := `var g = app.MapGroup("/api/contacts")
			.AddEndpointFilter(new F(2))
			.AddEndpointFilter(new F(3));
		g.MapPut("{id:int}", h);`
	got := minimalEndpoints(t, src)
	want := []string{"PUT /api/contacts/{id}"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
