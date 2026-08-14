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
