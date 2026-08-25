package laravel

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/farhadamjady/archerik-extractor/internal/model"
	"github.com/farhadamjady/archerik-extractor/internal/provider"
	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/php"
	"github.com/farhadamjady/archerik-extractor/internal/query"
	"github.com/farhadamjady/archerik-extractor/internal/scan"
)

var _ provider.Provider = (*Provider)(nil)

// endpoints runs the real query engine + PHP parser over one route file with no
// mount prefix, so a case exercises only what it declares.
func endpoints(t *testing.T, src string) []string {
	t.Helper()
	return endpointsWith(t, "routes/test.php", src, &provider.Index{})
}

func endpointsWith(t *testing.T, path, src string, idx *provider.Index) []string {
	t.Helper()
	f, err := php.NewParser().Parse(path, []byte("<?php\n"+src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	svc := model.NewService("s", "s", "")
	if err := query.New().Run(f, []provider.Detector{routeDetector{}}, idx, nil, svc); err != nil {
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

func TestRoutes(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{
			name: "verbs, both quote styles, param normalization",
			src: `Route::get('/users/{id}', [UserController::class, 'show']);
				Route::post("/users", [UserController::class, 'store']);
				Route::delete('/users/{id?}', $h);
				Route::any('/health', $h);`,
			want: []string{"* /health", "DELETE /users/{id}", "GET /users/{id}", "POST /users"},
		},
		{
			name: "builder chain before and after the verb",
			src: `Route::middleware('auth')->get('/me', $h);
				Route::post('/login', $h)->name('login')->withoutMiddleware('x');`,
			want: []string{"GET /me", "POST /login"},
		},
		{
			name: "match registers one endpoint per verb",
			src:  `Route::match(['get', 'post'], '/search', $h);`,
			want: []string{"GET /search", "POST /search"},
		},
		{
			name: "chained group prefix, nested, incl. arrow-fn closure",
			src: `Route::prefix('v1')->group(function () {
					Route::get('/orders', $h);
					Route::middleware('auth:api')->prefix('admin')->group(fn () => Route::delete('/orders/{order}', $h));
				});`,
			want: []string{"DELETE /v1/admin/orders/{order}", "GET /v1/orders"},
		},
		{
			name: "array-form group attributes",
			src: `Route::group(['prefix' => 'admin', 'middleware' => ['auth']], function () {
					Route::get('/stats', $h);
				});`,
			want: []string{"GET /admin/stats"},
		},
		{
			name: "group without a prefix contributes nothing",
			src: `Route::middleware('auth')->group(function () {
					Route::get('/profile', $h);
				});`,
			want: []string{"GET /profile"},
		},
		{
			name: "non-Route facades and repositories are not routes",
			src: `Cache::get('key');
				$this->repo->delete($id);
				Storage::disk('s3')->put('/file', $c);
				Route::get('/real', $h);`,
			want: []string{"GET /real"},
		},
		{
			// Found on laravel/framework @ 12.x
			// (src/Illuminate/Auth/Middleware/RedirectIfAuthenticated.php:71).
			// The facade also exposes methods that return something other than a
			// registrar: getRoutes() gives a RouteCollection, and ->get('GET')
			// on it is a LOOKUP by verb. Rooting at `Route::` is not enough to
			// call something a registration — this emitted `GET /GET`.
			name: "non-registering facade methods do not register",
			src: `$routes = Route::getRoutes()->get('GET');
				$c = Route::getRoutes();
				Route::current()->parameter('id');
				Route::get('/real', $h);`,
			want: []string{"GET /real"},
		},
		{
			name: "interpolated path is skipped, not guessed",
			src: `Route::get("/tenant/{$tenant}/users", $h);
				Route::get('/static', $h);`,
			want: []string{"GET /static"},
		},
		{
			// Found on guillaumebriday/laravel-blog @ c88e307 (routes/auth.php):
			// Route::redirect binds every verb to the framework's redirect
			// controller, and was missed entirely by the verb-only detector.
			name: "framework-controller registrars: redirect and view",
			src: `Route::redirect('/.well-known/change-password', '/settings/password');
				Route::permanentRedirect('/old', '/new');
				Route::view('/welcome', 'welcome');`,
			want: []string{"* /.well-known/change-password", "* /old", "GET /welcome"},
		},
		{
			name: "root path inside a group",
			src: `Route::prefix('api/v2')->group(function () {
					Route::get('/', $h);
				});`,
			want: []string{"GET /api/v2"},
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

func TestResourceRegistrars(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{
			name: "apiResource expands to the five actions (update on PUT+PATCH)",
			src:  `Route::apiResource('posts', PostController::class);`,
			want: []string{
				"DELETE /posts/{post}", "GET /posts", "GET /posts/{post}",
				"PATCH /posts/{post}", "POST /posts", "PUT /posts/{post}",
			},
		},
		{
			name: "resource adds the two form routes",
			src:  `Route::resource('photos', PhotoController::class);`,
			want: []string{
				"DELETE /photos/{photo}", "GET /photos", "GET /photos/create",
				"GET /photos/{photo}", "GET /photos/{photo}/edit",
				"PATCH /photos/{photo}", "POST /photos", "PUT /photos/{photo}",
			},
		},
		{
			name: "->only() whitelists",
			src:  `Route::apiResource('users', UserController::class)->only(['index', 'show']);`,
			want: []string{"GET /users", "GET /users/{user}"},
		},
		{
			name: "->except() blacklists",
			src:  `Route::apiResource('users', UserController::class)->except(['destroy', 'update', 'store']);`,
			want: []string{"GET /users", "GET /users/{user}"},
		},
		{
			name: "nested resource nests the parent parameter",
			src:  `Route::apiResource('posts.comments', CommentController::class)->only(['index', 'show']);`,
			want: []string{"GET /posts/{post}/comments", "GET /posts/{post}/comments/{comment}"},
		},
		{
			name: "plural registrar + irregular singular + dashes",
			src: `Route::apiResources(['companies' => CompanyController::class])->only(['show']);
				Route::apiResource('blog-posts', BlogPostController::class)->only(['show']);
				Route::apiResource('people', PersonController::class)->only(['show']);`,
			want: []string{"GET /blog-posts/{blog_post}", "GET /companies/{company}", "GET /people/{person}"},
		},
		{
			// Found on guillaumebriday/laravel-blog @ c88e307: the resource
			// parameter is Str::singular(name), and `media` singularizes to
			// `medium` — the controllers bind `Media $medium`. A wrong parameter
			// name is a wrong endpoint identity (verb + path), not a cosmetic slip.
			name: "irregular resource parameter: media -> medium",
			src:  `Route::apiResource('media', MediaController::class)->only(['show', 'destroy']);`,
			want: []string{"DELETE /media/{medium}", "GET /media/{medium}"},
		},
		{
			// `->only('destroy')` (a bare string, not an array) is as common as the
			// array form in real route files.
			name: "action filter passed as a bare string",
			src:  `Route::apiResource('comments', CommentController::class)->only('destroy');`,
			want: []string{"DELETE /comments/{comment}"},
		},
		{
			name: "resource inside a prefixed group",
			src: `Route::prefix('v1')->group(function () {
					Route::apiResource('orders', OrderController::class)->only(['index']);
				});`,
			want: []string{"GET /v1/orders"},
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

// TestMountPrefix locks the cross-file composition: routes/api.php is served
// under a prefix declared in bootstrap/app.php or RouteServiceProvider, never in
// the route file itself.
func TestMountPrefix(t *testing.T) {
	cases := []struct {
		name    string
		sources map[string]string
		want    []string
	}{
		{
			name: "framework default: api.php under /api, web.php at the root",
			sources: map[string]string{
				"routes/api.php": `Route::get('/users', $h);`,
				"routes/web.php": `Route::get('/home', $h);`,
			},
			want: []string{"GET /api/users", "GET /home"},
		},
		{
			name: "laravel 11 bootstrap/app.php apiPrefix",
			sources: map[string]string{
				"bootstrap/app.php": `return Application::configure(basePath: dirname(__DIR__))
					->withRouting(
						web: __DIR__.'/../routes/web.php',
						api: __DIR__.'/../routes/api.php',
						apiPrefix: 'api/v2',
					)->create();`,
				"routes/api.php": `Route::get('/users', $h);`,
			},
			want: []string{"GET /api/v2/users"},
		},
		{
			name: "laravel <=10 RouteServiceProvider group(base_path(...))",
			sources: map[string]string{
				"app/Providers/RouteServiceProvider.php": `class RouteServiceProvider {
					public function boot(): void {
						Route::middleware('api')->prefix('api')->group(base_path('routes/api.php'));
						Route::prefix('internal')->group(base_path('routes/internal.php'));
					}
				}`,
				"routes/api.php":      `Route::get('/users', $h);`,
				"routes/internal.php": `Route::get('/metrics', $h);`,
			},
			want: []string{"GET /api/users", "GET /internal/metrics"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parsed := map[string]provider.ParsedFile{}
			for p, src := range tc.sources {
				f, err := php.NewParser().Parse(p, []byte("<?php\n"+src))
				if err != nil {
					t.Fatalf("parse %s: %v", p, err)
				}
				parsed[p] = f
			}
			idx := &provider.Index{}
			if err := (prefixIndexer{}).Index(&provider.IndexContext{Parsed: parsed}, idx); err != nil {
				t.Fatalf("index: %v", err)
			}
			svc := model.NewService("s", "s", "")
			var paths []string
			for p := range parsed {
				paths = append(paths, p)
			}
			sort.Strings(paths)
			for _, p := range paths {
				if err := query.New().Run(parsed[p], []provider.Detector{routeDetector{}}, idx, nil, svc); err != nil {
					t.Fatalf("run %s: %v", p, err)
				}
			}
			model.Sort(svc)
			var got []string
			for _, e := range svc.Endpoints {
				got = append(got, e.Method+" "+e.Path)
			}
			sort.Strings(got)
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDetectors(t *testing.T) {
	want := map[string]model.Protocol{
		"laravel.route":  model.ProtoREST,
		"laravel.client": model.ProtoREST,
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

// TestParsersCoverFileSpec pins the routing invariant: every kind the FileSpec
// collects has a parser.
func TestParsersCoverFileSpec(t *testing.T) {
	p := New()
	parsers := p.Parsers()
	for _, g := range p.FileSpec().Groups {
		if _, ok := parsers[g.Kind]; !ok {
			t.Errorf("kind %d collected but has no parser", g.Kind)
		}
	}
}

func TestMatch(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "composer.json", `{"require":{"laravel/framework":"^11.0"}}`)
	writeFile(t, root, "artisan", "#!/usr/bin/env php")
	writeFile(t, root, "routes/api.php", "<?php Route::get('/x', $h);")
	m, score := New().Match(root, scan.NewOSFileTree(root, nil))
	if !m || score != 8 { // php(1) + laravel/framework(3) + artisan(2) + routes(2)
		t.Fatalf("laravel repo: matched=%v score=%d, want true/8", m, score)
	}

	// Lumen ships a different router — decline rather than half-read it.
	lumen := t.TempDir()
	writeFile(t, lumen, "composer.json", `{"require":{"laravel/lumen-framework":"^10.0"}}`)
	writeFile(t, lumen, "artisan", "#!/usr/bin/env php")
	writeFile(t, lumen, "routes/web.php", "<?php $router->get('/x', $h);")
	if m, _ := New().Match(lumen, scan.NewOSFileTree(lumen, nil)); m {
		t.Error("Lumen must not match")
	}

	// A plain PHP repo (no framework marker) must not match.
	plain := t.TempDir()
	writeFile(t, plain, "index.php", "<?php echo 'hi';")
	writeFile(t, plain, "composer.json", `{"require":{"symfony/console":"^7.0"}}`)
	if m, _ := New().Match(plain, scan.NewOSFileTree(plain, nil)); m {
		t.Error("a non-Laravel PHP repo must not match")
	}
}

// TestMatchNeedsUsageNotJustDependency covers docs/CROSS-STACK-CHECKS.md Check 2:
// a dependency string is not a usage. Modeled on the Express test of the same
// name.
func TestMatchNeedsUsageNotJustDependency(t *testing.T) {
	// A Laravel *package* — the case that motivated the gate. It requires the
	// framework to build against and registers no routes of its own, so it must
	// not be claimed as a service.
	pkg := t.TempDir()
	writeFile(t, pkg, "composer.json",
		`{"name":"acme/laravel-widgets","require-dev":{"laravel/framework":"^11.0"}}`)
	writeFile(t, pkg, "src/WidgetServiceProvider.php",
		"<?php\nclass WidgetServiceProvider extends ServiceProvider {\n  public function boot() { $this->publishes([]); }\n}")
	if m, score := New().Match(pkg, scan.NewOSFileTree(pkg, nil)); m {
		t.Errorf("laravel package matched (score %d); the dependency alone must not match", score)
	}

	// The same package with the framework INSTALLED. vendor/ carries Laravel's
	// own route declarations, so a usage scan that reads it would match every
	// PHP repo that ever ran `composer install`.
	installed := t.TempDir()
	writeFile(t, installed, "composer.json",
		`{"name":"acme/laravel-widgets","require-dev":{"laravel/framework":"^11.0"}}`)
	writeFile(t, installed, "src/WidgetServiceProvider.php", "<?php\nclass WidgetServiceProvider {}")
	writeFile(t, installed, "vendor/laravel/framework/src/Illuminate/Foundation/stubs/routes.php",
		"<?php Route::get('/', function () { return view('welcome'); });")
	if m, score := New().Match(installed, scan.NewOSFileTree(installed, nil)); m {
		t.Errorf("vendored framework routes matched (score %d); vendor/ is not this service's usage", score)
	}

	// Nor is a throwaway route spun up by a package's own test suite.
	testonly := t.TempDir()
	writeFile(t, testonly, "composer.json",
		`{"name":"acme/laravel-widgets","require-dev":{"laravel/framework":"^11.0"}}`)
	writeFile(t, testonly, "src/Widget.php", "<?php\nclass Widget {}")
	writeFile(t, testonly, "tests/Feature/WidgetTest.php",
		"<?php Route::get('/__test', fn () => 'ok');")
	if m, score := New().Match(testonly, scan.NewOSFileTree(testonly, nil)); m {
		t.Errorf("test-suite route matched (score %d); tests/ is excluded from extraction", score)
	}

	// `Route::class` in a stock config/app.php aliases array registers nothing.
	aliasonly := t.TempDir()
	writeFile(t, aliasonly, "composer.json", `{"require":{"laravel/framework":"^11.0"}}`)
	writeFile(t, aliasonly, "artisan", "#!/usr/bin/env php")
	writeFile(t, aliasonly, "config/app.php",
		"<?php\nreturn ['aliases' => ['Route' => Illuminate\\Support\\Facades\\Route::class]];")
	if m, score := New().Match(aliasonly, scan.NewOSFileTree(aliasonly, nil)); m {
		t.Errorf("Route::class alias matched (score %d); it is a reference, not a registration", score)
	}

	// A tool that only INSPECTS the route table (a route-lister, a debug bar)
	// calls the facade without registering anything. It serves no routes.
	inspector := t.TempDir()
	writeFile(t, inspector, "composer.json", `{"require":{"laravel/framework":"^11.0"}}`)
	writeFile(t, inspector, "src/RouteLister.php",
		"<?php\nclass RouteLister {\n  public function all() { return Route::getRoutes()->get('GET'); }\n  public function known($n) { return Route::has($n); }\n}")
	if m, score := New().Match(inspector, scan.NewOSFileTree(inspector, nil)); m {
		t.Errorf("route inspector matched (score %d); reading the route table is not serving routes", score)
	}

	// laravel-zero is a CLI framework with an artisan-like entry point and no
	// HTTP router — a look-alike that must not read as Laravel.
	zero := t.TempDir()
	writeFile(t, zero, "composer.json", `{"require":{"laravel-zero/framework":"^11.0"}}`)
	writeFile(t, zero, "artisan", "#!/usr/bin/env php")
	writeFile(t, zero, "app/Commands/BuildCommand.php", "<?php\nclass BuildCommand extends Command {}")
	if m, score := New().Match(zero, scan.NewOSFileTree(zero, nil)); m {
		t.Errorf("laravel-zero matched (score %d); it has no HTTP router", score)
	}

	// Usage in a real route file still matches, and the chained registration
	// form counts as usage just as the detector reads it.
	chained := t.TempDir()
	writeFile(t, chained, "composer.json", `{"require":{"laravel/framework":"^11.0"}}`)
	writeFile(t, chained, "artisan", "#!/usr/bin/env php")
	writeFile(t, chained, "routes/api.php",
		"<?php Route::prefix('v1')->group(function () { Route::get('/users', $h); });")
	m, score := New().Match(chained, scan.NewOSFileTree(chained, nil))
	if !m || score != 8 {
		t.Errorf("chained registration: matched=%v score=%d, want true/8", m, score)
	}

	// Usage without a declared dependency: a monorepo service whose composer.json
	// lives in a parent directory. artisan alone carries it.
	monorepo := t.TempDir()
	writeFile(t, monorepo, "artisan", "#!/usr/bin/env php")
	writeFile(t, monorepo, "routes/web.php", "<?php Route::get('/health', $h);")
	m, score = New().Match(monorepo, scan.NewOSFileTree(monorepo, nil))
	if !m || score != 5 { // php(1) + artisan(2) + routes(2)
		t.Errorf("usage without dependency: matched=%v score=%d, want true/5", m, score)
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
