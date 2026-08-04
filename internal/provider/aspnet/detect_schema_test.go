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

// TestMinimalFluentSchemas proves #60(a): Minimal API endpoints get request/
// response bodies from the fluent .Accepts<T>()/.Produces<T>() metadata chained
// onto Map*, with .Produces(statusOnly) carrying no body.
func TestMinimalFluentSchemas(t *testing.T) {
	src := `
		public class ContactDto { public string Name { get; set; } }
		public class ContactForCreationDto { public string Name { get; set; } }
		public static class Api {
			public static void Register(IEndpointRouteBuilder app) {
				app.MapGet("/contacts", GetAll).Produces<IEnumerable<ContactDto>>();
				app.MapGet("/contacts/{id}", GetOne).Produces<ContactDto>().Produces(404);
				app.MapPost("/contacts", Create).Accepts<ContactForCreationDto>("application/json").Produces(201);
			}
		}`
	f, err := csharp.NewParser().Parse("C.cs", []byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	idx := &provider.Index{Types: buildTypeIndex([]*csharp.File{f.(*csharp.File)})}
	svc := model.NewService("s", "s", "")
	if err := query.New().Run(f, []provider.Detector{minimalDetector{}}, idx, nil, svc); err != nil {
		t.Fatalf("run: %v", err)
	}
	eps := map[string]model.Endpoint{}
	for _, e := range svc.Endpoints {
		eps[e.Method+" "+e.Path] = e
	}

	if r := eps["GET /contacts"].Response; r == nil || r.Type != "array" || r.Items != "ContactDto" {
		t.Errorf("GET /contacts response = %+v, want array of ContactDto", r)
	}
	if r := eps["GET /contacts/{id}"].Response; r == nil || r.Type != "ContactDto" {
		t.Errorf("GET /contacts/{id} response = %+v, want ContactDto", r)
	}
	post := eps["POST /contacts"]
	if post.Request == nil || post.Request.Type != "ContactForCreationDto" {
		t.Errorf("POST request = %+v, want ContactForCreationDto", post.Request)
	}
	if post.Response != nil {
		t.Errorf("POST response = %+v, want nil (Produces(201) is a status, no body)", post.Response)
	}
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
