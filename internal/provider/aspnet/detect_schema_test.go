package aspnet

import (
	"testing"

	"github.com/farhadamjady/archerik-extractor/internal/model"
	"github.com/farhadamjady/archerik-extractor/internal/provider"
	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/csharp"
	"github.com/farhadamjady/archerik-extractor/internal/query"
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

// field returns a schema's nested field by name, or nil.
func field(s *model.Schema, name string) *model.Schema {
	if s == nil {
		return nil
	}
	for i := range s.Nested {
		if s.Nested[i].Name == name {
			return &s.Nested[i]
		}
	}
	return nil
}

// TestResponseFromAnonymousObject proves the ASP.NET half of #67. `IActionResult`
// is the idiomatic action return precisely BECAUSE it is opaque — it lets one
// action return several status codes — so the payload exists only inside the
// method. Reading the declared type alone left every such endpoint with no
// contract; the anonymous object is now read as the body.
func TestResponseFromAnonymousObject(t *testing.T) {
	src := `
		public class ProductDto { public string Name { get; set; } public int Qty { get; set; } }
		[Route("api/products")]
		public class ProductsController : ControllerBase {
			[HttpGet("{id}")]
			public IActionResult Get(int id) {
				ProductDto product = _svc.Find(id);
				return Ok(new { data = product, total = 1, status = "ok" });
			}
		}`
	r := schemaFor(t, src)["GET /api/products/{id}"].Response
	if r == nil || r.Type != "object" {
		t.Fatalf("response = %+v, want the anonymous object as the body", r)
	}
	if r.Confidence != model.Confirmed {
		t.Errorf("member names are written in the source, want %q, got %q", model.Confirmed, r.Confidence)
	}
	if len(r.Nested) != 3 {
		t.Fatalf("fields = %d, want 3 (data, total, status); members are recovered from the separator text", len(r.Nested))
	}
	d := field(r, "data")
	if d == nil || d.Type != "ProductDto" || len(d.Nested) != 2 {
		t.Fatalf("data = %+v, want ProductDto expanded to 2 fields", d)
	}
	if d.Confidence != model.Likely {
		t.Errorf("data was recovered from the body, want %q, got %q", model.Likely, d.Confidence)
	}
	if n := field(r, "total"); n == nil || n.Type != "number" || n.Confidence != model.Confirmed {
		t.Errorf("total = %+v, want a confirmed number", n)
	}
	if s := field(r, "status"); s == nil || s.Type != "string" {
		t.Errorf("status = %+v, want string", s)
	}
}

// TestAnonymousObjectShorthandAndErrorResults locks two rules: a shorthand member
// (`new { product }`) names itself, and an error result is NEVER the contract —
// the same rule the Go provider learned in #58, since presenting a 404 envelope
// as the response is a confident wrong answer, worse than no answer.
func TestAnonymousObjectShorthandAndErrorResults(t *testing.T) {
	src := `
		public class ProductDto { public string Name { get; set; } }
		[Route("api/products")]
		public class ProductsController : ControllerBase {
			[HttpGet("{id}")]
			public IActionResult Get(int id) {
				var product = new ProductDto();
				if (product == null) { return NotFound(new { error = "missing" }); }
				return Ok(new { product });
			}
			[HttpDelete("{id}")]
			public IActionResult Remove(int id) { return NoContent(); }
		}`
	eps := schemaFor(t, src)

	r := eps["GET /api/products/{id}"].Response
	p := field(r, "product")
	if p == nil {
		t.Fatalf("response = %+v, want a shorthand member named product", r)
	}
	if p.Type != "ProductDto" {
		t.Errorf("product = %+v, want ProductDto (var followed to its initializer)", p)
	}
	if field(r, "error") != nil {
		t.Error("the NotFound envelope must never be emitted as the response contract")
	}

	if r := eps["DELETE /api/products/{id}"].Response; r != nil {
		t.Errorf("NoContent action = %+v, want no body", r)
	}
}
