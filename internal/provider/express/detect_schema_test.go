package express

import (
	"testing"

	"github.com/farhadamjady/archerik-extractor/internal/model"
	"github.com/farhadamjady/archerik-extractor/internal/provider"
	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/tsjs"
	"github.com/farhadamjady/archerik-extractor/internal/query"
)

// schemaFor parses one TypeScript Express source, builds the TS type index over
// it, runs the route detector, and returns endpoints keyed by "VERB /path".
func schemaFor(t *testing.T, src string) map[string]model.Endpoint {
	t.Helper()
	f, err := tsjs.NewParser().Parse("app.ts", []byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	idx := &provider.Index{Types: buildTypeIndex([]*tsjs.File{f.(*tsjs.File)})}
	svc := model.NewService("s", "s", "")
	if err := query.New().Run(f, []provider.Detector{routeDetector{}}, idx, nil, svc); err != nil {
		t.Fatalf("run: %v", err)
	}
	out := map[string]model.Endpoint{}
	for _, e := range svc.Endpoints {
		out[e.Method+" "+e.Path] = e
	}
	return out
}

// TestGenericTypedHandler proves H7: Request<Params, ResBody, ReqBody> yields the
// request body (CreateUserDto) and Response<Body> yields the response body
// (UserRO), each expanded through the walker and capped at `likely`.
func TestGenericTypedHandler(t *testing.T) {
	src := `
interface CreateUserDto {
	name: string;
	age: number;
}
interface UserRO {
	id: string;
	name: string;
}
app.post('/users', (req: Request<{}, UserRO, CreateUserDto>, res: Response<UserRO>) => {
	res.json({ id: '1', name: req.body.name });
});
`
	post := schemaFor(t, src)["POST /users"]
	if post.Request == nil || post.Request.Type != "CreateUserDto" {
		t.Fatalf("request = %+v, want CreateUserDto", post.Request)
	}
	if post.Request.Confidence != model.Likely {
		t.Errorf("request confidence = %q, want likely (capped)", post.Request.Confidence)
	}
	reqFields := map[string]string{}
	for _, f := range post.Request.Nested {
		reqFields[f.Name] = f.Type
	}
	if reqFields["name"] != "string" || reqFields["age"] != "number" {
		t.Errorf("request fields = %v, want name:string age:number", reqFields)
	}
	if post.Response == nil || post.Response.Type != "UserRO" {
		t.Fatalf("response = %+v, want UserRO", post.Response)
	}
	if post.Response.Confidence != model.Likely {
		t.Errorf("response confidence = %q, want likely (capped)", post.Response.Confidence)
	}
}

// TestResJsonReturn proves the res.json(x) response path: with no Response<>
// generic, the response type is inferred from the local variable passed to
// res.json, and the request from a `req.body as Foo` cast.
func TestResJsonReturn(t *testing.T) {
	src := `
interface CreateUserDto {
	name: string;
}
interface UserRO {
	id: string;
}
app.post('/users', (req: Request, res: Response) => {
	const dto = req.body as CreateUserDto;
	const result: UserRO = save(dto);
	res.json(result);
});
`
	post := schemaFor(t, src)["POST /users"]
	if post.Request == nil || post.Request.Type != "CreateUserDto" {
		t.Fatalf("request = %+v, want CreateUserDto (from cast)", post.Request)
	}
	if post.Response == nil || post.Response.Type != "UserRO" {
		t.Fatalf("response = %+v, want UserRO (from res.json local)", post.Response)
	}
	if post.Response.Confidence != model.Likely {
		t.Errorf("response confidence = %q, want likely", post.Response.Confidence)
	}
}

// TestPlainJSNoSchema proves the TypeScript gate: a .js handler carries no types,
// so no request/response schema is emitted (the route itself still is).
func TestPlainJSNoSchema(t *testing.T) {
	src := `app.post('/users', (req, res) => { res.json({ ok: true }); });`
	f, err := tsjs.NewParser().Parse("app.js", []byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	idx := &provider.Index{Types: buildTypeIndex([]*tsjs.File{f.(*tsjs.File)})}
	svc := model.NewService("s", "s", "")
	if err := query.New().Run(f, []provider.Detector{routeDetector{}}, idx, nil, svc); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(svc.Endpoints) != 1 {
		t.Fatalf("endpoints = %d, want 1", len(svc.Endpoints))
	}
	if e := svc.Endpoints[0]; e.Request != nil || e.Response != nil {
		t.Errorf("plain JS should have no schema, got req=%+v resp=%+v", e.Request, e.Response)
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

// TestResponseFromResJsonObjectLiteral proves the Express half of #67: a handler
// that states its response inline — `res.status(200).json({ … })`, the dominant
// idiom in plain-JS Express apps, where no named type exists anywhere in the
// file — now yields the literal's keys as the contract. The chained form is
// covered on purpose: `res.status(200).json(x)` is far more common than a bare
// `res.json(x)`, and the receiver there is a call, not `res`.
func TestResponseFromResJsonObjectLiteral(t *testing.T) {
	src := `
		class User { id: string; name: string; }
		app.get('/users/:id', (req, res) => {
			const user = new User();
			res.status(200).json({ data: user, total: 1, status: 'ok' });
		});`
	r := schemaFor(t, src)["GET /users/{id}"].Response
	if r == nil || r.Type != "object" {
		t.Fatalf("response = %+v, want the returned literal as an object", r)
	}
	// Express caps its weaker inference at likely, literal keys included.
	if r.Confidence != model.Likely {
		t.Errorf("confidence = %q, want %q (Express caps inference)", r.Confidence, model.Likely)
	}
	if len(r.Nested) != 3 {
		t.Fatalf("fields = %d, want 3 (data, total, status)", len(r.Nested))
	}
	if d := field(r, "data"); d == nil || d.Type != "User" || len(d.Nested) != 2 {
		t.Errorf("data = %+v, want User expanded to 2 fields", d)
	}
	if n := field(r, "total"); n == nil || n.Type != "number" {
		t.Errorf("total = %+v, want number", n)
	}
	if s := field(r, "status"); s == nil || s.Type != "string" {
		t.Errorf("status = %+v, want string", s)
	}
}

// TestResJsonLiteralKeepsUnresolvableKeys proves the honesty rule on the Express
// path, and is the plain-JS case: no types exist, so no value resolves — but
// every key still ships. This is the only contract such a file contains.
func TestResJsonLiteralKeepsUnresolvableKeys(t *testing.T) {
	src := `
		app.post('/room/send_text', function (req, res) {
			var result = doWork(req.body);
			res.status(200).json({ Result: true, data: result, meta: { code: 200 } });
		});`
	r := schemaFor(t, src)["POST /room/send_text"].Response
	if r == nil || len(r.Nested) != 3 {
		t.Fatalf("response = %+v, want 3 named fields even with nothing typed", r)
	}
	if b := field(r, "Result"); b == nil || b.Type != "boolean" {
		t.Errorf("Result = %+v, want boolean", b)
	}
	if d := field(r, "data"); d == nil || d.Confidence != model.Uncertain {
		t.Errorf("data = %+v, want an uncertain field that still keeps its name", d)
	}
	if m := field(r, "meta"); m == nil || m.Type != "object" || field(m, "code") == nil {
		t.Errorf("meta = %+v, want a nested object carrying code", m)
	}
}

// TestNamedTypeBeatsResJsonLiteral locks precedence: a declared Response<T>
// generic still wins over a literal in the same handler.
func TestNamedTypeBeatsResJsonLiteral(t *testing.T) {
	src := `
		class UserRO { token: string; }
		app.get('/me', (req: Request, res: Response<UserRO>) => {
			res.json({ data: 1 });
		});`
	r := schemaFor(t, src)["GET /me"].Response
	if r == nil || r.Type != "UserRO" {
		t.Errorf("response = %+v, want UserRO (the declared generic wins)", r)
	}
}
