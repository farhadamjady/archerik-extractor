package spring

import (
	"sort"
	"testing"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/java"
	"github.com/farhadamjady/service-discovery/internal/query"
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

func TestRestTemplateTemplateHole(t *testing.T) {
	// base is a known local, id is a param -> Template with a hole.
	d := rtDep(t, `class C { RestTemplate rt; void m(String id) {
		String base = "http://svc";
		rt.getForObject(base + "/users/" + id, String.class);
	} }`)
	if d.Resolved || d.Confidence != model.Uncertain {
		t.Errorf("templated = %+v, want unresolved/uncertain", d)
	}
	if d.TargetName != "http://svc/users/{?}" {
		t.Errorf("target = %q, want http://svc/users/{?}", d.TargetName)
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
