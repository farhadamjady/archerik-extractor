package laravel

import (
	"fmt"
	"sort"
	"testing"

	"github.com/farhadamjady/archerik-extractor/internal/model"
	"github.com/farhadamjady/archerik-extractor/internal/provider"
	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/php"
	"github.com/farhadamjady/archerik-extractor/internal/query"
)

// endpointsConf is endpoints() plus each endpoint's confidence, which is the
// point of most of these cases: a path recovered by evaluation must not claim to
// be as certain as one that was written down.
func endpointsConf(t *testing.T, src string, idx *provider.Index) []string {
	t.Helper()
	f, err := php.NewParser().Parse("routes/test.php", []byte("<?php\n"+src))
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
		out = append(out, fmt.Sprintf("%s %s [%s]", e.Method, e.Path, e.Confidence))
	}
	sort.Strings(out)
	return out
}

func TestResolvedPaths(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{
			// The koel Subsonic block, reduced: routes/subsonic.php @ 2c7a5c0.
			// A top-level map, captured into a group closure with `use`, looped
			// over by KEY, interpolated into the path. None of the 51 real
			// endpoints appear anywhere in the source as a path.
			name: "foreach over a top-level array, key interpolated into the path",
			src: `$endpoints = [
					'ping' => PingController::class,
					'getLicense' => GetLicenseController::class,
				];
				Route::prefix('rest')
					->group(static function () use ($endpoints): void {
						foreach ($endpoints as $endpoint => $controller) {
							Route::match(['get', 'post'], "{$endpoint}{format?}", $controller)->where('format', '\.view');
						}
					});`,
			want: []string{
				"GET /rest/getLicense{format} [likely]",
				"GET /rest/ping{format} [likely]",
				"POST /rest/getLicense{format} [likely]",
				"POST /rest/ping{format} [likely]",
			},
		},
		{
			name: "foreach over a value list binds the value variable",
			src: `$slugs = ['alpha', 'beta'];
				foreach ($slugs as $slug) {
					Route::get("/tags/{$slug}", $h);
				}`,
			want: []string{"GET /tags/alpha [likely]", "GET /tags/beta [likely]"},
		},
		{
			name: "dot concatenation of a literal and a bound variable",
			src: `$base = 'admin';
				Route::get('/' . $base . '/stats', $h);`,
			want: []string{"GET /admin/stats [likely]"},
		},
		{
			name: "a literal path is still confirmed, not downgraded",
			src:  `Route::get('/users/{id}', $h);`,
			want: []string{"GET /users/{id} [confirmed]"},
		},
		{
			// The array is built at runtime, so the iteration set is unknown.
			// Emitting the loop body once, with an unresolved path, would invent
			// an endpoint that does not exist.
			name: "foreach over a non-literal collection emits nothing",
			src: `foreach ($this->registry->all() as $name) {
					Route::get("/x/{$name}", $h);
				}
				Route::get('/static', $h);`,
			want: []string{"GET /static [confirmed]"},
		},
		{
			// A positional entry has no key, so the key list is incomplete —
			// binding the two that do have keys would register a subset.
			name: "array with a keyless entry binds no keys",
			src: `$mixed = ['a' => A::class, B::class, 'c' => C::class];
				foreach ($mixed as $k => $v) {
					Route::get("/{$k}", $h);
				}
				Route::get('/static', $h);`,
			want: []string{"GET /static [confirmed]"},
		},
		{
			// Two top-level assignments to one name: there is no single value at
			// the point of use, so the name resolves to nothing.
			name: "a variable assigned twice is not resolved",
			src: `$p = 'first';
				$p = 'second';
				Route::get('/' . $p, $h);
				Route::get('/static', $h);`,
			want: []string{"GET /static [confirmed]"},
		},
		{
			// Legal to write and must not hang the extractor.
			name: "mutually referencing variables terminate",
			src: `$a = $b;
				$b = $a;
				Route::get('/' . $a, $h);
				Route::get('/static', $h);`,
			want: []string{"GET /static [confirmed]"},
		},
		{
			name: "interpolation of an unbound variable stays unresolved",
			src: `Route::get("/tenant/{$tenant}/users", $h);
				Route::get('/static', $h);`,
			want: []string{"GET /static [confirmed]"},
		},
		{
			name: "nested loops cross-product the path",
			src: `$vs = ['v1', 'v2'];
				$rs = ['users', 'orders'];
				foreach ($vs as $v) {
					foreach ($rs as $r) {
						Route::get("/{$v}/{$r}", $h);
					}
				}`,
			want: []string{
				"GET /v1/orders [likely]", "GET /v1/users [likely]",
				"GET /v2/orders [likely]", "GET /v2/users [likely]",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := endpointsConf(t, tc.src, &provider.Index{})
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// TestConfigLayer exercises the config()/env() chain end to end: a route path
// read from config/*.php, whose leaf is an env() call backed by .env.
func TestConfigLayer(t *testing.T) {
	idx := indexFrom(t, map[string]string{
		"config/app.php": `<?php
			return [
				'name' => 'Koel',
				'admin' => ['prefix' => env('ADMIN_PREFIX', 'backoffice')],
				'fallback' => env('MISSING_VAR', 'from-default'),
			];`,
		"config/services.php": `<?php
			return ['payment' => ['url' => env('PAYMENT_URL')]];`,
	}, map[string]string{
		".env": "PAYMENT_URL=https://pay.internal\n# comment\nexport ADMIN_PREFIX='ops'\n",
		// Placeholders, and the real .env must win over every one of them.
		".env.example": "PAYMENT_URL=http://localhost\nADMIN_PREFIX=changeme\nONLY_IN_EXAMPLE=x\n",
	})

	cfg := idx.Config
	if cfg == nil {
		t.Fatal("config indexer installed no resolver")
	}
	for _, tc := range []struct {
		key, want string
		wantOK    bool
	}{
		{"app.name", "Koel", true},
		{"app.admin.prefix", "ops", true},      // .env wins over the in-code default
		{"app.fallback", "from-default", true}, // no .env entry: the default is real
		{"services.payment.url", "https://pay.internal", true},
		{"PAYMENT_URL", "https://pay.internal", true}, // bare env var name
		{"ONLY_IN_EXAMPLE", "x", true},                // the example still fills gaps
		{"app.missing", "", false},
	} {
		got, conf, _, ok := cfg.Resolve(tc.key)
		if ok != tc.wantOK || got != tc.want {
			t.Errorf("Resolve(%q) = %q/%v, want %q/%v", tc.key, got, ok, tc.want, tc.wantOK)
		}
		// Everything reached through config is one indirection away.
		if ok && conf != model.Likely {
			t.Errorf("Resolve(%q) confidence = %v, want likely", tc.key, conf)
		}
	}
}

// TestConfigInRoutePath is the reason the config layer exists: a value read
// through config() is usable where a literal would be.
func TestConfigInRoutePath(t *testing.T) {
	idx := indexFrom(t, map[string]string{
		"config/app.php": `<?php return ['admin' => ['prefix' => env('ADMIN_PREFIX', 'backoffice')]];`,
	}, nil)

	got := endpointsConf(t, `Route::get('/' . config('app.admin.prefix') . '/stats', $h);`, idx)
	want := []string{"GET /backoffice/stats [likely]"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// indexFrom runs the real config indexer over the given files.
func indexFrom(t *testing.T, phpFiles, envFiles map[string]string) *provider.Index {
	t.Helper()
	parsed := map[string]provider.ParsedFile{}
	for p, src := range phpFiles {
		f, err := php.NewParser().Parse(p, []byte(src))
		if err != nil {
			t.Fatalf("parse %s: %v", p, err)
		}
		parsed[p] = f
	}
	for p, src := range envFiles {
		f, _ := rawParser{kind: provider.KindDeployConfig}.Parse(p, []byte(src))
		parsed[p] = f
	}
	idx := &provider.Index{}
	if err := (configIndexer{}).Index(&provider.IndexContext{Parsed: parsed}, idx); err != nil {
		t.Fatalf("index: %v", err)
	}
	return idx
}
