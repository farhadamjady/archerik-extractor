package aspnet

import (
	"testing"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/csharp"
	"github.com/farhadamjady/service-discovery/internal/query"
)

func schemaFor(t *testing.T, src string) map[string]model.Endpoint {
	t.Helper()
	f, err := csharp.NewParser().Parse("C.cs", []byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	idx := &provider.Index{Types: buildTypeIndex([]*csharp.File{f.(*csharp.File)})}
	svc := model.NewService("s", "s", "")
	if err := query.New().Run(f, []provider.Detector{restDetector{}}, idx, nil, svc); err != nil {
		t.Fatalf("run: %v", err)
	}
	out := map[string]model.Endpoint{}
	for _, e := range svc.Endpoints {
		out[e.Method+" "+e.Path] = e
	}
	return out
}

// TestPropertyVisibility locks in System.Text.Json's public-instance-only rule:
// non-public / static / [JsonIgnore] properties invent no phantom wire field,
// while [JsonInclude] forces a non-public one in.
func TestPropertyVisibility(t *testing.T) {
	f, err := csharp.NewParser().Parse("D.cs", []byte(`
		public class Dto {
			public string Name { get; set; }
			private int Secret { get; set; }
			protected string Helper { get; set; }
			internal string Pkg { get; set; }
			public static string Version { get; set; }
			[JsonIgnore] public string Hidden { get; set; }
			[JsonInclude] internal string Forced { get; set; }
		}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	td, ok := buildTypeIndex([]*csharp.File{f.(*csharp.File)}).Lookup("Dto")
	if !ok {
		t.Fatal("Dto not indexed")
	}
	got := map[string]bool{}
	for _, fld := range td.Fields {
		got[fld.Name] = true
	}
	for _, want := range []string{"Name", "Forced"} {
		if !got[want] {
			t.Errorf("missing serializable property %q; fields=%v", want, got)
		}
	}
	for _, bad := range []string{"Secret", "Helper", "Pkg", "Version", "Hidden"} {
		if got[bad] {
			t.Errorf("phantom property %q should be excluded; fields=%v", bad, got)
		}
	}
}

func TestActionResponseAndRequestSchema(t *testing.T) {
	src := `
		public class ProductDto { public string Name { get; set; } public int Stock { get; set; } }
		public record ProductEnvelope(ProductDto Product);
		public class CreateProduct { public string Name { get; set; } }
		[Route("products")]
		public class ProductsController : ControllerBase {
			[HttpGet("{id}")] public Task<ProductEnvelope> Get(int id) => null;
			[HttpPost] public Task<ProductEnvelope> Create([FromBody] CreateProduct cmd) => null;
			[HttpDelete("{id}")] public Task Delete(int id) => null;
		}`
	eps := schemaFor(t, src)

	get := eps["GET /products/{id}"]
	if get.Response == nil || get.Response.Type != "ProductEnvelope" {
		t.Fatalf("GET response = %+v, want ProductEnvelope", get.Response)
	}
	post := eps["POST /products"]
	if post.Request == nil || post.Request.Type != "CreateProduct" {
		t.Fatalf("POST request = %+v, want CreateProduct", post.Request)
	}
	// bare Task -> no response body
	if del := eps["DELETE /products/{id}"]; del.Response != nil {
		t.Errorf("DELETE response = %+v, want nil (bare Task)", del.Response)
	}
}
