package laravel

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/farhadamjady/archerik-extractor/internal/model"
	"github.com/farhadamjady/archerik-extractor/internal/provider"
	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/php"
	"github.com/farhadamjady/archerik-extractor/internal/query"
)

// schemaFor runs the real class indexer over `files`, then the real route
// detector over `routes`, and renders the single resulting endpoint's contracts.
// Everything is exercised through the engine, so a case proves the whole path
// from a route registration to a class in another file.
func schemaFor(t *testing.T, routes string, files map[string]string) (req, resp string) {
	t.Helper()
	parsed := map[string]provider.ParsedFile{}
	for p, src := range files {
		f, err := php.NewParser().Parse(p, []byte(src))
		if err != nil {
			t.Fatalf("parse %s: %v", p, err)
		}
		parsed[p] = f
	}
	routeFile, err := php.NewParser().Parse("routes/api.php", []byte(routes))
	if err != nil {
		t.Fatalf("parse routes: %v", err)
	}
	parsed["routes/api.php"] = routeFile

	idx := &provider.Index{}
	if err := (classIndexer{}).Index(&provider.IndexContext{Parsed: parsed}, idx); err != nil {
		t.Fatalf("index: %v", err)
	}
	svc := model.NewService("s", "s", "")
	if err := query.New().Run(routeFile, []provider.Detector{routeDetector{}}, idx, nil, svc); err != nil {
		t.Fatalf("run: %v", err)
	}
	model.Sort(svc)
	if len(svc.Endpoints) == 0 {
		t.Fatal("no endpoint emitted")
	}
	e := svc.Endpoints[0]
	return render(e.Request), render(e.Response)
}

// render prints a schema compactly: type{field:type flags, ...}.
func render(s *model.Schema) string {
	if s == nil {
		return "-"
	}
	var b strings.Builder
	b.WriteString(s.Type)
	if s.Items != "" {
		b.WriteString("[" + s.Items + "]")
	}
	if s.Truncated {
		b.WriteString("!trunc")
	}
	var fs []string
	for _, f := range s.Nested {
		fs = append(fs, renderField(f))
	}
	sort.Strings(fs)
	b.WriteString("{" + strings.Join(fs, " ") + "}")
	return b.String()
}

func renderField(f model.Schema) string {
	s := f.Name + ":" + f.Type
	if f.Items != "" {
		s += "[" + f.Items + "]"
	}
	switch f.Required {
	case model.ReqRequired:
		s += "!"
	case model.ReqOptional:
		s += "?"
	}
	if f.Nullable {
		s += "~"
	}
	if len(f.Enum) > 0 {
		s += "(" + strings.Join(f.Enum, "|") + ")"
	}
	if len(f.Constraints) > 0 {
		var ks []string
		for k, v := range f.Constraints {
			ks = append(ks, k+"="+v)
		}
		sort.Strings(ks)
		s += "<" + strings.Join(ks, ",") + ">"
	}
	if len(f.Nested) > 0 {
		var ns []string
		for _, n := range f.Nested {
			ns = append(ns, renderField(n))
		}
		sort.Strings(ns)
		s += "{" + strings.Join(ns, " ") + "}"
	}
	return s
}

const routeToShow = `<?php
use App\Http\Controllers\UserController;
Route::get('users/{id}', [UserController::class, 'show']);`

func TestRequestFromFormRequest(t *testing.T) {
	cases := []struct {
		name  string
		rules string
		want  string
	}{
		{
			name:  "types, requiredness and constraints from the rule string",
			rules: `['email' => 'required|string|email|max:255', 'age' => 'sometimes|integer|min:18']`,
			want:  "StoreUserRequest{age:int?<minimum=18> email:string!<format=email,maxLength=255>}",
		},
		{
			// Laravel's other spelling; a Rule::unique() object constrains the
			// value's relationship to other data, not its shape.
			name:  "the array spelling, ignoring non-literal rule objects",
			rules: `['name' => ['required', 'string', Rule::unique('users', 'name')]]`,
			want:  "StoreUserRequest{name:string!}",
		},
		{
			name:  "nullable is distinct from optional",
			rules: `['bio' => 'sometimes|nullable|string']`,
			want:  "StoreUserRequest{bio:string?~}",
		},
		{
			// A field with no type rule is real and named; calling it a string
			// would be a guess.
			name:  "a field with no type rule stays untyped",
			rules: `['token' => 'required']`,
			want:  "StoreUserRequest{token:string!}",
		},
		{
			name:  "in: becomes an enum",
			rules: `['status' => 'required|string|in:draft,published']`,
			want:  "StoreUserRequest{status:string!(draft|published)}",
		},
		{
			// min/max mean different things per type, and the type rule need not
			// come first.
			name:  "min/max are typed constraints",
			rules: `['n' => 'max:9|integer|min:1', 's' => 'max:9|string', 't' => 'array|max:3']`,
			want:  "StoreUserRequest{n:int<maximum=9,minimum=1> s:string<maxLength=9> t:array<maxItems=3>}",
		},
		{
			name:  "dotted keys nest, wildcards become arrays",
			rules: `['author.name' => 'required|string', 'tags.*' => 'required|string']`,
			want:  "StoreUserRequest{author:object{name:string!} tags:array[string]!}",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := schemaFor(t, `<?php
				use App\Http\Controllers\UserController;
				Route::post('users', [UserController::class, 'store']);`,
				map[string]string{
					"app/Http/Controllers/UserController.php": `<?php
						namespace App\Http\Controllers;
						use App\Http\Requests\StoreUserRequest;
						class UserController {
							public function store(StoreUserRequest $request) { return null; }
						}`,
					"app/Http/Requests/StoreUserRequest.php": `<?php
						namespace App\Http\Requests;
						use Illuminate\Foundation\Http\FormRequest;
						class StoreUserRequest extends FormRequest {
							public function rules() { return ` + tc.rules + `; }
						}`,
				})
			if req != tc.want {
				t.Errorf("got  %s\nwant %s", req, tc.want)
			}
		})
	}
}

// TestRequestMergesParentRules covers the form laravel-realworld uses: a request
// that extends a base and merges its rules in. Reading only the literal half
// reports a contract that looks complete and is missing fields.
func TestRequestMergesParentRules(t *testing.T) {
	req, _ := schemaFor(t, `<?php
		use App\Http\Controllers\ArticleController;
		Route::post('articles', [ArticleController::class, 'store']);`,
		map[string]string{
			"app/Http/Controllers/ArticleController.php": `<?php
				namespace App\Http\Controllers;
				use App\Http\Requests\NewArticleRequest;
				class ArticleController {
					public function store(NewArticleRequest $request) { return null; }
				}`,
			"app/Http/Requests/BaseArticleRequest.php": `<?php
				namespace App\Http\Requests;
				use Illuminate\Foundation\Http\FormRequest;
				class BaseArticleRequest extends FormRequest {
					public function rules() { return ['body' => 'required|string']; }
				}`,
			"app/Http/Requests/NewArticleRequest.php": `<?php
				namespace App\Http\Requests;
				class NewArticleRequest extends BaseArticleRequest {
					public function rules() {
						return array_merge_recursive(parent::rules(), ['title' => 'required|string']);
					}
				}`,
		})
	want := "NewArticleRequest{body:string! title:string!}"
	if req != want {
		t.Errorf("got  %s\nwant %s", req, want)
	}
}

// TestRequestSubkeyWrapping covers validationData(): a FormRequest validating
// $this->input('user') describes the INNER object, and the real body is one
// level up. The Laravel instance of #63.
func TestRequestSubkeyWrapping(t *testing.T) {
	req, _ := schemaFor(t, `<?php
		use App\Http\Controllers\UserController;
		Route::post('users', [UserController::class, 'store']);`,
		map[string]string{
			"app/Http/Controllers/UserController.php": `<?php
				namespace App\Http\Controllers;
				use App\Http\Requests\NewUserRequest;
				class UserController {
					public function store(NewUserRequest $request) { return null; }
				}`,
			"app/Http/Requests/NewUserRequest.php": `<?php
				namespace App\Http\Requests;
				use Illuminate\Foundation\Http\FormRequest;
				class NewUserRequest extends FormRequest {
					public function rules() { return ['email' => 'required|string|email']; }
					public function validationData() { return Arr::wrap($this->input('user')); }
				}`,
		})
	want := "object{user:NewUserRequest{email:string!<format=email>}}"
	if req != want {
		t.Errorf("got  %s\nwant %s", req, want)
	}
}

func TestRequestFromInlineValidate(t *testing.T) {
	req, _ := schemaFor(t, `<?php
		use App\Http\Controllers\UserController;
		Route::post('users', [UserController::class, 'store']);`,
		map[string]string{
			"app/Http/Controllers/UserController.php": `<?php
				namespace App\Http\Controllers;
				class UserController {
					public function store($request) {
						$data = $request->validate(['email' => 'required|string|email']);
						return null;
					}
				}`,
		})
	want := "object{email:string!<format=email>}"
	if req != want {
		t.Errorf("got  %s\nwant %s", req, want)
	}
}

func TestResponseFromResource(t *testing.T) {
	files := map[string]string{
		"app/Http/Controllers/UserController.php": `<?php
			namespace App\Http\Controllers;
			use App\Http\Resources\UserResource;
			class UserController {
				public function show($id) { return new UserResource($u); }
			}`,
		"app/Http/Resources/BaseUserResource.php": `<?php
			namespace App\Http\Resources;
			use Illuminate\Http\Resources\Json\JsonResource;
			class BaseUserResource extends JsonResource {
				public function toArray($request) {
					return ['username' => $this->resource->username];
				}
			}`,
		"app/Http/Resources/UserResource.php": `<?php
			namespace App\Http\Resources;
			class UserResource extends BaseUserResource {
				public static $wrap = 'user';
				public function toArray($request) {
					return array_merge(parent::toArray($request), ['email' => $this->resource->email]);
				}
			}`,
	}
	_, resp := schemaFor(t, routeToShow, files)
	// $wrap nests the payload; parent::toArray contributes username.
	want := "object{user:UserResource{email:object username:object}}"
	if resp != want {
		t.Errorf("got  %s\nwant %s", resp, want)
	}
}

// TestResponseErrorBranchSkipped is the Laravel instance of #58: the guard
// clause returns first, and it is never the contract.
func TestResponseErrorBranchSkipped(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{
			name: "laravel signature, status is argument 1",
			body: `if ($bad) { return response()->json(['message' => 'nope', 'errors' => []], 422); }
			       return new UserResource($u);`,
		},
		{
			// pixelfed's controllers wrap json() with the status LAST. Reading a
			// fixed argument position finds the headers and misses the 404.
			name: "custom helper, status is argument 2",
			body: `if ($bad) { return $this->json(['error' => 'Record not found'], [], 404); }
			       return new UserResource($u);`,
		},
		{
			name: "setStatusCode on the chain",
			body: `if ($bad) { return response()->json(['error' => 'x'])->setStatusCode(500); }
			       return new UserResource($u);`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, resp := schemaFor(t, routeToShow, map[string]string{
				"app/Http/Controllers/UserController.php": `<?php
					namespace App\Http\Controllers;
					use App\Http\Resources\UserResource;
					class UserController {
						public function show($id) { ` + tc.body + ` }
					}`,
				"app/Http/Resources/UserResource.php": `<?php
					namespace App\Http\Resources;
					use Illuminate\Http\Resources\Json\JsonResource;
					class UserResource extends JsonResource {
						public function toArray($r) { return ['id' => $this->resource->id]; }
					}`,
			})
			want := "UserResource{id:object}"
			if resp != want {
				t.Errorf("got %s, want %s (the error envelope must never be the contract)", resp, want)
			}
		})
	}
}

// TestResponseSuccessStatusIsNotAnError guards the other direction: a 2xx status
// argument must not make the response look like an error branch.
func TestResponseSuccessStatusIsNotAnError(t *testing.T) {
	_, resp := schemaFor(t, routeToShow, map[string]string{
		"app/Http/Controllers/UserController.php": `<?php
			namespace App\Http\Controllers;
			class UserController {
				public function show($id) { return response()->json(['id' => 1], 201); }
			}`,
	})
	if want := "object{id:int}"; resp != want {
		t.Errorf("got %s, want %s", resp, want)
	}
}

func TestResponseCollections(t *testing.T) {
	resourceFile := `<?php
		namespace App\Http\Resources;
		use Illuminate\Http\Resources\Json\JsonResource;
		class ArticleResource extends JsonResource {
			public function toArray($r) { return ['slug' => $this->resource->slug]; }
		}`
	cases := []struct {
		name, handler, extra, want string
	}{
		{
			name:    "ResourceCollection with $collects",
			handler: `return new ArticlesCollection($x);`,
			extra: `<?php
				namespace App\Http\Resources;
				use Illuminate\Http\Resources\Json\ResourceCollection;
				class ArticlesCollection extends ResourceCollection {
					public static $wrap = 'articles';
					public $collects = ArticleResource::class;
				}`,
			want: "object{articles:array[ArticleResource]{slug:object}}",
		},
		{
			name:    "the ::collection factory every JsonResource inherits",
			handler: `return ArticleResource::collection($x);`,
			want:    "array[ArticleResource]{slug:object}",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string]string{
				"app/Http/Resources/ArticleResource.php": resourceFile,
				"app/Http/Controllers/UserController.php": `<?php
					namespace App\Http\Controllers;
					use App\Http\Resources\ArticleResource;
					use App\Http\Resources\ArticlesCollection;
					class UserController {
						public function show($id) { ` + tc.handler + ` }
					}`,
			}
			if tc.extra != "" {
				files["app/Http/Resources/ArticlesCollection.php"] = tc.extra
			}
			_, resp := schemaFor(t, routeToShow, files)
			if resp != tc.want {
				t.Errorf("got  %s\nwant %s", resp, tc.want)
			}
		})
	}
}

// TestNamespaceDisambiguation is the defect that cost laravel-blog every one of
// its API response contracts: three classes share the simple name
// `PostController`, and only the route file's `use` line says which is meant.
func TestNamespaceDisambiguation(t *testing.T) {
	files := map[string]string{
		// Sorts FIRST by path, so a simple-name index would pick this one.
		"app/Http/Controllers/Admin/PostController.php": `<?php
			namespace App\Http\Controllers\Admin;
			class PostController {
				public function show($id) { return view('admin.posts.show'); }
			}`,
		"app/Http/Controllers/Api/V1/PostController.php": `<?php
			namespace App\Http\Controllers\Api\V1;
			use App\Http\Resources\Post as PostResource;
			class PostController {
				public function show($id) { return new PostResource($p); }
			}`,
		"app/Http/Resources/Post.php": `<?php
			namespace App\Http\Resources;
			use Illuminate\Http\Resources\Json\JsonResource;
			class Post extends JsonResource {
				public function toArray($r) { return ['slug' => $this->resource->slug]; }
			}`,
		// A same-named model, to prove the ALIASED import is followed and not
		// the simple name `Post`.
		"app/Models/Post.php": `<?php
			namespace App\Models;
			class Post {}`,
	}
	_, resp := schemaFor(t, `<?php
		use App\Http\Controllers\Api\V1\PostController;
		Route::get('posts/{id}', [PostController::class, 'show']);`, files)
	want := "Post{slug:object}"
	if resp != want {
		t.Errorf("got %s, want %s", resp, want)
	}
}

func TestHandlerForms(t *testing.T) {
	controller := `<?php
		namespace App\Http\Controllers;
		class PingController {
			public function ping() { return ['pong' => true]; }
			public function __invoke() { return ['pong' => true]; }
		}`
	cases := []struct{ name, route string }{
		{"array form", `Route::get('p', [PingController::class, 'ping']);`},
		{"string form", `Route::get('p', 'App\Http\Controllers\PingController@ping');`},
		{"single-action controller", `Route::get('p', PingController::class);`},
		// firefly-iii registers its entire API this way.
		{"options array with a uses key", `Route::get('p', ['uses' => 'App\Http\Controllers\PingController@ping', 'as' => 'p']);`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, resp := schemaFor(t,
				"<?php\nuse App\\Http\\Controllers\\PingController;\n"+tc.route,
				map[string]string{"app/Http/Controllers/PingController.php": controller})
			if want := "object{pong:bool}"; resp != want {
				t.Errorf("got %s, want %s", resp, want)
			}
		})
	}
}

// TestResourceRegistrarSchemas: a resource route's actions are served by the
// like-named controller methods, so they carry contracts like any other route.
func TestResourceRegistrarSchemas(t *testing.T) {
	parsed := map[string]provider.ParsedFile{}
	add := func(p, src string) {
		f, err := php.NewParser().Parse(p, []byte(src))
		if err != nil {
			t.Fatalf("parse %s: %v", p, err)
		}
		parsed[p] = f
	}
	add("app/Http/Controllers/PostController.php", `<?php
		namespace App\Http\Controllers;
		class PostController {
			public function index() { return ['posts' => []]; }
			public function show($id) { return ['post' => 1]; }
		}`)
	routes := `<?php
		use App\Http\Controllers\PostController;
		Route::apiResource('posts', PostController::class)->only(['index', 'show']);`
	rf, err := php.NewParser().Parse("routes/api.php", []byte(routes))
	if err != nil {
		t.Fatal(err)
	}
	parsed["routes/api.php"] = rf

	idx := &provider.Index{}
	if err := (classIndexer{}).Index(&provider.IndexContext{Parsed: parsed}, idx); err != nil {
		t.Fatal(err)
	}
	svc := model.NewService("s", "s", "")
	if err := query.New().Run(rf, []provider.Detector{routeDetector{}}, idx, nil, svc); err != nil {
		t.Fatal(err)
	}
	model.Sort(svc)
	var got []string
	for _, e := range svc.Endpoints {
		got = append(got, fmt.Sprintf("%s %s -> %s", e.Method, e.Path, render(e.Response)))
	}
	want := []string{
		"GET /posts -> object{posts:array}",
		"GET /posts/{post} -> object{post:int}",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("got  %v\nwant %v", got, want)
	}
}

// TestModelAttributeTypes covers #73: a resource states `'slug' => $this->slug`,
// and every type behind those literal keys lives in the model — which declares
// no properties, because Eloquent builds them from the database at runtime. The
// three static sources are merged, strongest last.
func TestModelAttributeTypes(t *testing.T) {
	files := map[string]string{
		"app/Models/Article.php": `<?php
			namespace App\Models;
			use Illuminate\Database\Eloquent\Model;
			/**
			 * @property int $id
			 * @property string $title
			 * @property \Illuminate\Support\Carbon|null $created_at
			 */
			class Article extends Model {
				protected $casts = ['published' => 'boolean', 'score' => 'float'];
			}`,
		"database/migrations/2024_01_01_create_articles_table.php": `<?php
			return new class extends Migration {
				public function up() {
					Schema::create('articles', function (Blueprint $table) {
						$table->id();
						$table->string('slug');
						$table->text('body');
						$table->boolean('published');
						$table->integer('score');
						$table->timestamps();
					});
				}
			};`,
		"app/Http/Resources/ArticleResource.php": `<?php
			namespace App\Http\Resources;
			use Illuminate\Http\Resources\Json\JsonResource;
			class ArticleResource extends JsonResource {
				public function toArray($r) {
					return [
						'id' => $this->id,
						'slug' => $this->slug,
						'body' => $this->resource->body,
						'title' => $this->title,
						'published' => $this->published,
						'score' => $this->score,
						'created_at' => $this->created_at,
						'mystery' => $this->not_a_column,
					];
				}
			}`,
		"app/Http/Controllers/UserController.php": `<?php
			namespace App\Http\Controllers;
			use App\Http\Resources\ArticleResource;
			class UserController {
				public function show($id) { return new ArticleResource($a); }
			}`,
	}
	parsed := map[string]provider.ParsedFile{}
	for p, src := range files {
		f, err := php.NewParser().Parse(p, []byte(src))
		if err != nil {
			t.Fatalf("parse %s: %v", p, err)
		}
		parsed[p] = f
	}
	rf, err := php.NewParser().Parse("routes/api.php", []byte(routeToShow))
	if err != nil {
		t.Fatal(err)
	}
	parsed["routes/api.php"] = rf

	idx := &provider.Index{}
	for _, ix := range []provider.Indexer{classIndexer{}, modelIndexer{}} {
		if err := ix.Index(&provider.IndexContext{Parsed: parsed}, idx); err != nil {
			t.Fatal(err)
		}
	}
	svc := model.NewService("s", "s", "")
	if err := query.New().Run(rf, []provider.Detector{routeDetector{}}, idx, nil, svc); err != nil {
		t.Fatal(err)
	}
	model.Sort(svc)
	got := render(svc.Endpoints[0].Response)
	// score: the migration says integer, $casts says float — the cast wins,
	// because it is what json_encode actually sees.
	// created_at: only the docblock has it (Carbon -> string).
	// mystery: no source has it; the NAME survives, the type does not.
	want := "ArticleResource{body:string created_at:string id:int mystery:object published:bool score:float slug:string title:string}"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

// TestValueTypingWithoutAModel covers the sources that need no model at all: a
// PHP cast states the type outright, and a null-coalescing default or an
// agreeing ternary carries it too. pixelfed's resources are written this way and
// do not follow the <Model>Resource naming convention.
func TestValueTypingWithoutAModel(t *testing.T) {
	_, resp := schemaFor(t, routeToShow, map[string]string{
		"app/Http/Controllers/UserController.php": `<?php
			namespace App\Http\Controllers;
			class UserController {
				public function show($id) {
					return [
						'is_nsfw' => (bool) $this->is_nsfw,
						'count' => $this->cached_count ?? 0,
						'ratio' => (float) $x,
						'can_trend' => $this->can_trend === null ? true : (bool) $this->can_trend,
						'mixed' => $flag ? 'yes' : 3,
						'plain' => $this->whatever,
					];
				}
			}`,
	})
	want := "object{can_trend:bool count:int is_nsfw:bool mixed:object plain:object ratio:float}"
	if resp != want {
		t.Errorf("got  %s\nwant %s", resp, want)
	}
}
