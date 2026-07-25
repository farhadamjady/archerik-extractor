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
	dets := New().Detectors()
	if len(dets) != 1 || dets[0].Name() != "aspnet.rest" || dets[0].Protocol() != model.ProtoREST {
		t.Fatalf("unexpected detectors: %+v", dets)
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
