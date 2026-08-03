package express

import (
	"testing"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/tsjs"
	"github.com/farhadamjady/service-discovery/internal/query"
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
