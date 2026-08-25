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

// deps runs the real engine over one PHP file and renders each outbound edge as
// "target url [confidence] via", so a case pins the whole contract of the edge
// rather than just that something was found.
func deps(t *testing.T, src string, idx *provider.Index) []string {
	t.Helper()
	f, err := php.NewParser().Parse("app/Service.php", []byte("<?php\n"+src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	svc := model.NewService("s", "s", "")
	if err := query.New().Run(f, []provider.Detector{clientDetector{}}, idx, nil, svc); err != nil {
		t.Fatalf("run: %v", err)
	}
	model.Sort(svc)
	var out []string
	for _, d := range svc.OutboundDependencies {
		target := d.TargetName
		if target == "" {
			target = "(anonymous)"
		}
		url := d.URL
		if url == "" {
			url = "-"
		}
		s := fmt.Sprintf("%s %s [%s]", target, url, d.Confidence)
		if d.ResolvedVia != "" {
			s += " via " + d.ResolvedVia
		}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func TestHTTPClient(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{
			name: "direct facade call with a literal URL",
			src:  `Http::get('https://api.example.com/v1/ping');`,
			want: []string{"api.example.com https://api.example.com/v1/ping [confirmed]"},
		},
		{
			// koel app/Services/SongStorages/DropboxStorage.php @ 2c7a5c0.
			name: "builder chain, URL on the verb at the end",
			src: `$response = Http::asForm()
					->withBasicAuth($this->config['app_key'], $this->config['app_secret'])
					->post('https://api.dropboxapi.com/oauth2/token', ['grant_type' => 'refresh_token']);`,
			want: []string{"api.dropboxapi.com https://api.dropboxapi.com/oauth2/token [confirmed]"},
		},
		{
			name: "send() takes the verb first, the URL second",
			src:  `Http::send('GET', 'https://api.example.com/thing');`,
			want: []string{"api.example.com https://api.example.com/thing [confirmed]"},
		},
		{
			// The honesty rule: a dependency with an unknown target is still the
			// fact that this service calls something, so it is emitted.
			name: "dynamic URL is emitted as an anonymous uncertain edge",
			src:  `Http::withUserAgent($ua)->get($url);`,
			want: []string{"(anonymous) - [uncertain]"},
		},
		{
			name: "a port is kept in the target",
			src:  `Http::get('http://payments.internal:8080/charge');`,
			want: []string{"payments.internal:8080 http://payments.internal:8080/charge [confirmed]"},
		},
		{
			// A relative URL is a real path but names no service.
			name: "relative URL resolves no target",
			src:  `Http::get('/internal/ping');`,
			want: []string{"(anonymous) /internal/ping [uncertain]"},
		},
		{
			// The Http facade exposes plenty of methods that are not requests.
			// Same trap as Route::getRoutes()->get() (#70).
			name: "non-request facade methods emit nothing",
			src: `Http::fake();
				$root = Http::getFacadeRoot();
				Http::assertNothingSent();`,
			want: nil,
		},
		{
			name: "an unrelated facade named otherwise is not the Http facade",
			src:  `Cache::get('key'); $this->repo->get($id); Storage::get('/f');`,
			want: nil,
		},
		{
			name: "fully qualified facade is the same facade",
			src:  `\Illuminate\Support\Facades\Http::get('https://api.example.com/x');`,
			want: []string{"api.example.com https://api.example.com/x [confirmed]"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := deps(t, tc.src, &provider.Index{})
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// TestClientURLThroughConfig is the payoff of the config layer: koel's
// DoctorCommand calls its own API through config('app.url'), and the target only
// exists after config/app.php -> env('APP_URL') -> the env file are followed.
func TestClientURLThroughConfig(t *testing.T) {
	idx := indexFrom(t, map[string]string{
		"config/app.php": `<?php return ['url' => env('APP_URL', 'http://localhost')];`,
	}, map[string]string{
		".env": "APP_URL=https://koel.example.com\n",
	})

	got := deps(t, `Http::get(config('app.url') . '/api/ping');`, idx)
	want := []string{"koel.example.com https://koel.example.com/api/ping [likely] via .env"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// TestClientProvenanceNamesExampleEnv pins the distinction that matters when a
// repo ships no real .env: the value is still emitted, but resolved_via says it
// came from the example, so a reader is not told the service dials localhost.
func TestClientProvenanceNamesExampleEnv(t *testing.T) {
	idx := indexFrom(t, map[string]string{
		"config/app.php": `<?php return ['url' => env('APP_URL', 'http://fallback')];`,
	}, map[string]string{
		".env.example": "APP_URL=http://localhost:8000\n",
	})

	got := deps(t, `Http::get(config('app.url') . '/api/ping');`, idx)
	want := []string{"localhost:8000 http://localhost:8000/api/ping [likely] via .env.example"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestGuzzleClient(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{
			name: "base_uri on an imported Client",
			src: `use GuzzleHttp\Client;
				$c = new Client(['base_uri' => 'https://api.stripe.com', 'timeout' => 5]);`,
			want: []string{"api.stripe.com https://api.stripe.com [confirmed]"},
		},
		{
			name: "fully qualified Client needs no import",
			src:  `$c = new \GuzzleHttp\Client(['base_uri' => 'https://api.stripe.com']);`,
			want: []string{"api.stripe.com https://api.stripe.com [confirmed]"},
		},
		{
			// koel's SafeHttp builds a Client with handlers and timeouts only.
			// It declares no target, so it is not an edge.
			name: "a Client with no base_uri declares no target",
			src: `use GuzzleHttp\Client;
				$c = new Client(['handler' => $stack, 'timeout' => 10]);`,
			want: nil,
		},
		{
			// `Client` is far too common a class name to claim without the import.
			name: "bare Client without the Guzzle import is not Guzzle",
			src:  `$c = new Client(['base_uri' => 'https://api.example.com']);`,
			want: nil,
		},
		{
			// firefly-iii app/Jobs/DownloadExchangeRates.php: the client carries
			// no base_uri and the URL arrives at the call site.
			name: "request on a variable proved to hold a Guzzle client",
			src: `use GuzzleHttp\Client;
				class Job {
					public function handle() {
						$client = new Client();
						$res = $client->get('https://rates.example.com/latest.json');
					}
				}`,
			want: []string{"rates.example.com https://rates.example.com/latest.json [confirmed]"},
		},
		{
			name: "request() takes the verb first, the URL second",
			src: `use GuzzleHttp\Client;
				class Job {
					public function handle() {
						$client = new Client();
						$client->request('POST', 'https://hooks.example.com/notify', []);
					}
				}`,
			want: []string{"hooks.example.com https://hooks.example.com/notify [confirmed]"},
		},
		{
			// A dynamic URL on a proved client is still an outbound call.
			name: "unresolvable URL on a proved client is still an edge",
			src: `use GuzzleHttp\Client;
				class Job {
					public function handle() {
						$client = new Client();
						$client->get(sprintf('%s/%s.json', $base, $code));
					}
				}`,
			want: []string{"(anonymous) - [uncertain]"},
		},
		{
			// The whole point of requiring proof: these are not HTTP.
			name: "get() on an unproven receiver is not a client call",
			src: `class S {
					public function handle() {
						$cache->get('key');
						$collection->get(0);
						$this->repo->request('x', 'y');
					}
				}`,
			want: nil,
		},
		{
			// koel app/Services/ApplicationInformationService.php: the client is
			// INJECTED, not constructed. Across five benchmark repos there is not
			// one `$this->client = new Client()`, so the declared type is the only
			// evidence there is.
			name: "promoted constructor property typed as a Guzzle client",
			src: `use GuzzleHttp\Client;
				class S {
					public function __construct(private readonly Client $client) {}
					public function go() { return $this->client->get('https://api.github.com/repos/x/y/tags'); }
				}`,
			want: []string{"api.github.com https://api.github.com/repos/x/y/tags [confirmed]"},
		},
		{
			name: "typed property declaration",
			src: `use GuzzleHttp\Client;
				class S {
					private Client $http;
					public function go() { return $this->http->get('https://api.example.com/x'); }
				}`,
			want: []string{"api.example.com https://api.example.com/x [confirmed]"},
		},
		{
			// The type is what makes the property form safe: without it every
			// `$this->cache->get(...)` in the app becomes an HTTP edge.
			name: "an untyped or non-Guzzle property is not a client",
			src: `use GuzzleHttp\Client;
				class S {
					private $cache;
					private CacheRepository $store;
					public function go() {
						$this->cache->get('https://api.example.com/x');
						$this->store->get('https://api.example.com/y');
					}
				}`,
			want: nil,
		},
		{
			// A client proved in one method must not vouch for a same-named
			// variable in another.
			name: "proof does not leak across methods",
			src: `use GuzzleHttp\Client;
				class S {
					public function a() { $client = new Client(); }
					public function b() { $client = $this->cache; $client->get('https://api.example.com/x'); }
				}`,
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := deps(t, tc.src, &provider.Index{})
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
