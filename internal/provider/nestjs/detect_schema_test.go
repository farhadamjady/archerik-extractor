package nestjs

import (
	"testing"

	"github.com/farhadamjady/archerik-extractor/internal/model"
	"github.com/farhadamjady/archerik-extractor/internal/provider"
	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/tsjs"
	"github.com/farhadamjady/archerik-extractor/internal/query"
)

// schemaFor parses one source, indexes its types, runs the REST detector, and
// returns the endpoints keyed by "VERB path".
func schemaFor(t *testing.T, src string) map[string]model.Endpoint {
	t.Helper()
	f, err := tsjs.NewParser().Parse("c.ts", []byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	idx := &provider.Index{Types: buildTypeIndex([]*tsjs.File{f.(*tsjs.File)})}
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

func TestResponseAndRequestSchema(t *testing.T) {
	src := `
		export class UserDto {
			readonly name: string;
			readonly age: number;
			readonly tags: string[];
		}
		export interface UserRO { user: UserDto; token: string; }
		@Controller('users')
		export class UsersController {
			@Get(':id') findOne(): Promise<UserRO> { return null; }
			@Post() create(@Body() dto: UserDto) {}
		}`
	eps := schemaFor(t, src)

	get := eps["GET /users/{id}"]
	if get.Response == nil || get.Response.Type != "UserRO" {
		t.Fatalf("GET response = %+v, want UserRO", get.Response)
	}
	// UserRO.user nests UserDto -> {name, age, tags[]}
	var user *model.Schema
	for i := range get.Response.Nested {
		if get.Response.Nested[i].Name == "user" {
			user = &get.Response.Nested[i]
		}
	}
	if user == nil || user.Type != "UserDto" || len(user.Nested) != 3 {
		t.Fatalf("UserRO.user = %+v, want nested UserDto{name,age,tags}", user)
	}

	post := eps["POST /users"]
	if post.Request == nil || post.Request.Type != "UserDto" {
		t.Fatalf("POST request = %+v, want UserDto", post.Request)
	}
	for _, f := range post.Request.Nested {
		if f.Name == "tags" && (f.Type != "array" || f.Items != "string") {
			t.Errorf("tags = %+v, want array of string", f)
		}
	}
}

// TestBodyKeyWrapping proves #63: @Body('article') binds a sub-property, so the
// request body is { article: <Dto> }, not the bare Dto. A key-less @Body() stays
// unwrapped.
func TestBodyKeyWrapping(t *testing.T) {
	src := `
		export class CreateArticleDto { readonly title: string; readonly body: string; }
		@Controller('articles')
		export class ArticlesController {
			@Post() create(@Body('article') dto: CreateArticleDto) {}
			@Put() replace(@Body() dto: CreateArticleDto) {}
		}`
	eps := schemaFor(t, src)

	post := eps["POST /articles"].Request
	if post == nil || post.Type != "object" || len(post.Nested) != 1 {
		t.Fatalf("POST request = %+v, want object wrapping one key", post)
	}
	if post.Nested[0].Name != "article" || post.Nested[0].Type != "CreateArticleDto" {
		t.Fatalf("wrapper field = %+v, want article: CreateArticleDto", post.Nested[0])
	}
	if len(post.Nested[0].Nested) != 2 {
		t.Errorf("wrapped Dto should keep its 2 fields, got %d", len(post.Nested[0].Nested))
	}

	put := eps["PUT /articles"].Request
	if put == nil || put.Type != "CreateArticleDto" {
		t.Fatalf("PUT request = %+v, want unwrapped CreateArticleDto (key-less @Body)", put)
	}
}

// TestResponseInferredFromServiceReturn proves #62: a handler with no return
// annotation that delegates `return this.svc.method(...)` gets its response from
// the service method's declared return type (constructor param-property resolves
// the field type; the method-return index resolves the method).
func TestResponseInferredFromServiceReturn(t *testing.T) {
	src := `
		export class ArticleEntity { readonly slug: string; readonly title: string; }
		export class ArticleService {
			async create(dto): Promise<ArticleEntity> { return null; }
		}
		@Controller('articles')
		export class ArticlesController {
			constructor(private readonly articleService: ArticleService) {}
			@Post() async create(@Body('article') dto: any) {
				return await this.articleService.create(dto);
			}
		}`
	eps := schemaFor(t, src)
	r := eps["POST /articles"].Response
	if r == nil || r.Type != "ArticleEntity" {
		t.Fatalf("POST response = %+v, want ArticleEntity (inferred from ArticleService.create)", r)
	}
	if len(r.Nested) != 2 {
		t.Errorf("ArticleEntity should expand to 2 fields, got %d", len(r.Nested))
	}
}

// TestNullableAliasAndUnion proves #61(a)+(b): a nullable type ALIAS
// (`type NullableType<T> = T | null`) and a direct `| null` union both resolve to
// the base DTO with nullable set — instead of a fieldless "NullableType" phantom
// or the literal "User | null" type.
func TestNullableAliasAndUnion(t *testing.T) {
	src := `
		export type NullableType<T> = T | null;
		export class User { readonly id: string; readonly name: string; }
		@Controller('users')
		export class UsersController {
			@Get('alias') viaAlias(): Promise<NullableType<User>> { return null; }
			@Get('union') viaUnion(): Promise<User | null> { return null; }
		}`
	eps := schemaFor(t, src)

	for _, path := range []string{"GET /users/alias", "GET /users/union"} {
		r := eps[path].Response
		if r == nil || r.Type != "User" {
			t.Fatalf("%s response = %+v, want User", path, r)
		}
		if !r.Nullable {
			t.Errorf("%s response should be nullable", path)
		}
		if len(r.Nested) != 2 {
			t.Errorf("%s response fields = %d, want 2 (User expanded, not a phantom)", path, len(r.Nested))
		}
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

// TestResponseFromReturnedObjectLiteral proves #64: a handler with no return
// annotation that assembles its response inline (`return { data: profile }` — the
// envelope pattern, where the wire shape exists only as an object literal) gets
// that literal as its response. The KEYS come off the AST, so the object is
// confirmed; each value is chased local -> service method -> declared return type
// and marked likely, having been reached through indirection rather than declared.
func TestResponseFromReturnedObjectLiteral(t *testing.T) {
	src := `
		export class User { readonly id: string; readonly name: string; }
		export class UsersService {
			async getProfile(id: number): Promise<User> { return null; }
		}
		@Controller('users')
		export class UsersController {
			constructor(private readonly userService: UsersService) {}
			@Get('me') async getProfile(@Req() req) {
				const profile = await this.userService.getProfile(req.user.id);
				return { data: profile, total: 1, label: 'me' };
			}
		}`
	r := schemaFor(t, src)["GET /users/me"].Response
	if r == nil || r.Type != "object" {
		t.Fatalf("response = %+v, want an object (the returned literal)", r)
	}
	if r.Confidence != model.Confirmed {
		t.Errorf("literal keys are written in the source, so the object is %q, got %q", model.Confirmed, r.Confidence)
	}
	if len(r.Nested) != 3 {
		t.Fatalf("fields = %d, want 3 (data, total, label)", len(r.Nested))
	}

	data := field(r, "data")
	if data == nil || data.Type != "User" {
		t.Fatalf("data = %+v, want User (chased through the local to UsersService.getProfile)", data)
	}
	if data.Confidence != model.Likely {
		t.Errorf("data resolved through indirection, want %q, got %q", model.Likely, data.Confidence)
	}
	if len(data.Nested) != 2 {
		t.Errorf("User should expand to 2 fields, got %d", len(data.Nested))
	}
	if n := field(r, "total"); n == nil || n.Type != "number" || n.Confidence != model.Confirmed {
		t.Errorf("total = %+v, want a confirmed number", n)
	}
	if s := field(r, "label"); s == nil || s.Type != "string" || s.Confidence != model.Confirmed {
		t.Errorf("label = %+v, want a confirmed string", s)
	}
}

// TestObjectLiteralUnresolvableValueStillNamed proves the honesty rule on the
// envelope path: a value that cannot be typed (an untyped service method, a
// spread that hides the key set) never deletes the field — the name ships with an
// uncertain type, and a spread downgrades the object's own confidence because the
// key list is no longer known to be complete.
func TestObjectLiteralUnresolvableValueStillNamed(t *testing.T) {
	src := `
		export class UsersService {
			async searchUsers(text) { return null; }
			async raw(): Promise<any> { return null; }
		}
		@Controller('users')
		export class UsersController {
			constructor(private readonly userService: UsersService) {}
			@Get('search') async search(@Query('t') t: string) {
				const found = await this.userService.searchUsers(t);
				return { data: found.items, ...extra };
			}
			@Get('raw') async rawly() {
				const r = await this.userService.raw();
				return { data: r };
			}
		}`
	eps := schemaFor(t, src)

	// `Promise<any>` IS a declared type, but it resolves to an opaque object. Being
	// chased to a declaration that says nothing must not upgrade the confidence.
	if d := field(eps["GET /users/raw"].Response, "data"); d == nil || d.Confidence != model.Uncertain {
		t.Errorf("data from Promise<any> = %+v, want uncertain (an unresolved type is not evidence)", d)
	}

	r := eps["GET /users/search"].Response
	if r == nil {
		t.Fatal("response = nil, want the literal with its named field")
	}
	if r.Confidence != model.Uncertain {
		t.Errorf("a spread hides keys, so the object is %q, got %q", model.Uncertain, r.Confidence)
	}
	d := field(r, "data")
	if d == nil {
		t.Fatal("data field dropped; an unresolvable value must still ship its name")
	}
	if d.Type != "object" || d.Confidence != model.Uncertain {
		t.Errorf("data = %+v, want an uncertain object (searchUsers declares no return type)", d)
	}
}

// TestDeclaredReturnBeatsLiteral locks precedence: a declared return type is
// still authoritative, and delegation (#62) is still preferred over a literal
// returned elsewhere in the same handler.
func TestDeclaredReturnBeatsLiteral(t *testing.T) {
	src := `
		export class User { readonly id: string; }
		export class UsersService { async find(): Promise<User> { return null; } }
		@Controller('users')
		export class UsersController {
			constructor(private readonly svc: UsersService) {}
			@Get('a') async declared(): Promise<User> { return { data: 1 }; }
			@Get('b') async delegating(@Query('q') q: string) {
				if (!q) { return { error: 'missing' }; }
				return this.svc.find();
			}
		}`
	eps := schemaFor(t, src)
	if r := eps["GET /users/a"].Response; r == nil || r.Type != "User" {
		t.Errorf("declared return = %+v, want User (annotation wins over the literal)", r)
	}
	if r := eps["GET /users/b"].Response; r == nil || r.Type != "User" {
		t.Errorf("delegating handler = %+v, want User (#62 wins over the guard-clause literal)", r)
	}
}
