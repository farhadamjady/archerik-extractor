package spring

import (
	"sort"
	"testing"

	"github.com/farhadamjady/archerik-extractor/internal/model"
	"github.com/farhadamjady/archerik-extractor/internal/provider"
	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/java"
	"github.com/farhadamjady/archerik-extractor/internal/query"
)

// httpDeps runs a detector that resolves in-code targets: it builds the value
// evaluator over the parsed files (+ optional config) and returns the outbound
// dependencies, sorted.
func httpDeps(t *testing.T, det provider.Detector, cfg provider.ConfigResolver, srcs ...string) []model.Dependency {
	t.Helper()
	var files []*java.File
	parsed := map[string]provider.ParsedFile{}
	for i, s := range srcs {
		name := string(rune('A'+i)) + ".java"
		pf, err := java.NewParser().Parse(name, []byte(s))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		files = append(files, pf.(*java.File))
		parsed[name] = pf
	}
	idx := &provider.Index{Symbols: java.IndexSymbols(files), Config: cfg}
	res := java.NewEvaluator(idx)

	svc := model.NewService("s", "s", "")
	for _, p := range sortedJavaPaths(parsed) {
		if err := query.New().Run(parsed[p], []provider.Detector{det}, idx, res, svc); err != nil {
			t.Fatalf("run: %v", err)
		}
	}
	model.Sort(svc)
	return svc.OutboundDependencies
}

func rtDep(t *testing.T, src string) model.Dependency {
	t.Helper()
	deps := httpDeps(t, restTemplateDetector{}, nil, src)
	if len(deps) != 1 {
		t.Fatalf("got %d deps, want 1: %+v", len(deps), deps)
	}
	return deps[0]
}

func TestRestTemplateLiteral(t *testing.T) {
	d := rtDep(t, `class C { RestTemplate rt; void m() {
		rt.getForObject("http://payment/api", String.class);
	} }`)
	if d.URL != "http://payment/api" || !d.Resolved || d.Confidence != model.Confirmed {
		t.Errorf("literal = %+v, want resolved/confirmed", d)
	}
	if d.Protocol != model.ProtoREST || d.Detection != model.DetectRestTemplate {
		t.Errorf("edge fields = (%s,%s)", d.Protocol, d.Detection)
	}
}

func TestRestTemplateConstantConcat(t *testing.T) {
	d := rtDep(t, `class Api { static final String BASE = "http://svc"; }
	class C { RestTemplate rt; void m() {
		rt.exchange(Api.BASE + "/users", null, null, String.class);
	} }`)
	if d.URL != "http://svc/users" || !d.Resolved || d.Confidence != model.Confirmed {
		t.Errorf("constant concat = %+v, want http://svc/users confirmed", d)
	}
}

func TestRestTemplateKnownHostTemplate(t *testing.T) {
	// base is a known local, id is a runtime param. The SERVICE is known —
	// only a path value is missing — so the edge is resolved at likely,
	// like a path variable on an endpoint.
	d := rtDep(t, `class C { RestTemplate rt; void m(String id) {
		String base = "http://svc";
		rt.getForObject(base + "/users/" + id, String.class);
	} }`)
	if !d.Resolved || d.Confidence != model.Likely {
		t.Errorf("templated = %+v, want resolved/likely (host known)", d)
	}
	if d.TargetName != "svc" {
		t.Errorf("target = %q, want host-only svc", d.TargetName)
	}
	if d.URL != "http://svc/users/{?}" {
		t.Errorf("url = %q, want the templated shape http://svc/users/{?}", d.URL)
	}
}

// An absolute-URL target collapses to its authority so every path variant of one
// service shares a node; the full URL stays in url. Mirrors the real
// SelimHorri/hoangtien2k3 RestTemplate shape (http://USER-SERVICE/user-service/
// api/users/{id}) that was fanning out into a node per path.
func TestRestTemplateURLTargetIsHostOnly(t *testing.T) {
	d := rtDep(t, `class C { RestTemplate rt; void m(String id) {
		rt.getForObject("http://AUTH-SERVICE:8088/api/manager/user/1", String.class);
	} }`)
	if d.TargetName != "AUTH-SERVICE:8088" {
		t.Errorf("target = %q, want authority AUTH-SERVICE:8088 (raw casing, port kept)", d.TargetName)
	}
	if d.URL != "http://AUTH-SERVICE:8088/api/manager/user/1" || !d.Resolved {
		t.Errorf("url/resolved = {url:%q resolved:%v}, want full URL resolved", d.URL, d.Resolved)
	}
}

// A bare path ("/orders" — host set by a WebClient baseUrl bean elsewhere) is
// NOT a target: the call is emitted but anonymous, with the path in url, so it
// never leaks in as a phantom "/orders" service node.
func TestRestTemplateBarePathIsAnonymous(t *testing.T) {
	d := rtDep(t, `class C { RestTemplate rt; void m() {
		rt.getForObject("/orders", String.class);
	} }`)
	if d.Resolved || d.Confidence != model.Uncertain {
		t.Errorf("bare path = %+v, want unresolved/uncertain", d)
	}
	if d.TargetName != "" {
		t.Errorf("target_name = %q, want empty (a path names no service)", d.TargetName)
	}
	if d.URL != "/orders" {
		t.Errorf("url = %q, want the bare path /orders", d.URL)
	}
}

func TestRestTemplateUnknownHostStaysUncertain(t *testing.T) {
	// The HOST itself is the hole -> which service is called is unknown. The
	// partial shape is kept in url (edge stays distinct, backend can show it) but
	// target_name stays empty — a runtime host is not a resolvable node label.
	d := rtDep(t, `class C { RestTemplate rt; void m(String host) {
		rt.getForObject("http://" + host + "/users", String.class);
	} }`)
	if d.Resolved || d.Confidence != model.Uncertain {
		t.Errorf("host-hole = %+v, want unresolved/uncertain", d)
	}
	if d.TargetName != "" {
		t.Errorf("target_name = %q, want empty (host is a runtime hole)", d.TargetName)
	}
	if d.URL != "http://{?}/users" {
		t.Errorf("url = %q, want the partial shape http://{?}/users", d.URL)
	}
}

// A fully unresolvable target (an opaque variable, no literal segments — e.g. an
// OAuth userInfo url passed in as a parameter) is still emitted, but ANONYMOUS:
// the source identifier ("uri") is NOT a call target, so it must not become the
// target_name. The backend renders the empty case as a runtime-unknown node.
func TestRestTemplateUnknownTargetIsAnonymous(t *testing.T) {
	d := rtDep(t, `class C { RestTemplate rt; void m(String uri) {
		rt.getForObject(uri, String.class);
	} }`)
	if d.Resolved || d.Confidence != model.Uncertain {
		t.Errorf("unknown target = %+v, want unresolved/uncertain", d)
	}
	if d.TargetName != "" || d.URL != "" {
		t.Errorf("opaque target = {name:%q url:%q}, want both empty (no variable-name label)", d.TargetName, d.URL)
	}
}

func TestRestTemplateTernaryCandidates(t *testing.T) {
	deps := httpDeps(t, restTemplateDetector{}, nil, `class C { RestTemplate rt; void m(boolean f) {
		rt.getForObject(f ? "http://a" : "http://b", String.class);
	} }`)
	if len(deps) != 2 {
		t.Fatalf("got %d deps, want 2 candidates: %+v", len(deps), deps)
	}
	var urls []string
	group := deps[0].CandidateGroup
	for _, d := range deps {
		urls = append(urls, d.URL)
		if !d.Conditional || d.CandidateGroup != group || d.Confidence != model.Likely {
			t.Errorf("candidate = %+v, want conditional/shared-group/likely", d)
		}
	}
	sort.Strings(urls)
	if urls[0] != "http://a" || urls[1] != "http://b" {
		t.Errorf("candidate urls = %v", urls)
	}
}

func TestRestTemplateValueField(t *testing.T) {
	cfg := buildStore(t, nil, map[string]string{
		"application.yml": "payment:\n  url: http://payment:8080\n",
	})
	deps := httpDeps(t, restTemplateDetector{}, cfg, `class C {
		@Value("${payment.url}") String base;
		RestTemplate rt;
		void m() { rt.getForObject(base + "/pay", String.class); }
	}`)
	if len(deps) != 1 || deps[0].URL != "http://payment:8080/pay" {
		t.Fatalf("value-field concat = %+v, want http://payment:8080/pay", deps)
	}
	if deps[0].Confidence != model.Likely {
		t.Errorf("confidence = %s, want likely (config-sourced)", deps[0].Confidence)
	}
}

// The URL host is a runtime Eureka instance host/port, so the URL alone
// resolves only to an anonymous uncertain edge — but the target service is
// statically known: the getApplication argument, a @Value field resolved via
// config. We emit that logical name (like a service-discovery @FeignClient),
// resolved/likely, and NOT the anonymous edge.
func TestRestTemplateEurekaDiscoveryTarget(t *testing.T) {
	cfg := buildStore(t, nil, map[string]string{
		"application.yml": "com:\n  amdocs:\n    external:\n      application:\n        name: PROFILE-SERVICE\n",
	})
	src := `import com.netflix.discovery.EurekaClient;
	class C {
		@Value("${com.amdocs.external.application.name}") String applicationName;
		EurekaClient client;
		RestTemplate rt;
		void m() {
			InstanceInfo instanceInfo = client.getApplication(applicationName).getInstances().get(0);
			String url = "http://" + instanceInfo.getHostName() + ":" + instanceInfo.getPort() + "/profile";
			rt.exchange(url, null, null, Object.class);
		}
	}`
	deps := httpDeps(t, restTemplateDetector{}, cfg, src)
	if len(deps) != 1 {
		t.Fatalf("got %d deps, want 1: %+v", len(deps), deps)
	}
	d := deps[0]
	if d.TargetName != "PROFILE-SERVICE" || !d.Resolved || d.Confidence != model.Likely {
		t.Errorf("discovery target = %+v, want PROFILE-SERVICE resolved/likely", d)
	}
	if d.Protocol != model.ProtoREST || d.Detection != model.DetectRestTemplate {
		t.Errorf("edge fields = (%s,%s)", d.Protocol, d.Detection)
	}
	if d.URL != "" {
		t.Errorf("url = %q, want empty (logical-name edge, backend maps it)", d.URL)
	}
}

// getApplication is a generic method name; without the Netflix discovery import
// it must NOT be treated as a registry lookup — the edge stays an honest
// anonymous uncertain (host is a runtime hole), never a phantom "X" target.
func TestRestTemplateGetApplicationUngatedWithoutEureka(t *testing.T) {
	d := rtDep(t, `class C {
		Repo client;
		RestTemplate rt;
		void m() {
			Thing instanceInfo = client.getApplication("X").getInstances().get(0);
			String url = "http://" + instanceInfo.getHostName() + "/profile";
			rt.exchange(url, null, null, Object.class);
		}
	}`)
	if d.Resolved || d.Confidence != model.Uncertain || d.TargetName != "" {
		t.Errorf("ungated getApplication = %+v, want anonymous uncertain", d)
	}
}

func TestNonRestTemplateMethodIgnored(t *testing.T) {
	deps := httpDeps(t, restTemplateDetector{}, nil, `class C { java.util.Map m; void f() {
		m.put("key", "value");
		m.getForObject("x");
	} }`)
	// put is not in the set; getForObject is, but "x" resolves to a literal edge.
	if len(deps) != 1 || deps[0].URL != "x" {
		t.Errorf("deps = %+v, want only the getForObject edge", deps)
	}
}
