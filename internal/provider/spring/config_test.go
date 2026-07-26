package spring

import (
	"testing"

	"github.com/farhadamjady/service-discovery/internal/provider"
)

// buildStore runs the config indexer over a set of virtual config files and
// returns the resolver, so tests read merged results the way detectors will.
func buildStore(t *testing.T, profiles []string, files map[string]string) provider.ConfigResolver {
	t.Helper()
	parsed := map[string]provider.ParsedFile{}
	for p, content := range files {
		pf, err := (rawParser{kind: provider.KindSpringConfig}).Parse(p, []byte(content))
		if err != nil {
			t.Fatalf("parse %s: %v", p, err)
		}
		parsed[p] = pf
	}
	idx := &provider.Index{}
	ic := &provider.IndexContext{Parsed: parsed, Profiles: profiles}
	if err := (configIndexer{}).Index(ic, idx); err != nil {
		t.Fatalf("index: %v", err)
	}
	return idx.Config
}

// mustResolve resolves a bare key by wrapping it as a ${key} placeholder — the
// form detectors actually pass to the resolver.
func mustResolve(t *testing.T, c provider.ConfigResolver, key string) string {
	t.Helper()
	v, _, _, ok := c.Resolve("${" + key + "}")
	if !ok {
		t.Fatalf("key %q did not resolve", key)
	}
	return v
}

func TestFlattenYAML(t *testing.T) {
	c := buildStore(t, nil, map[string]string{
		"application.yml": `
payment:
  service:
    url: http://payment:8080
spring:
  application:
    name: cart
`,
	})
	if got := mustResolve(t, c, "payment.service.url"); got != "http://payment:8080" {
		t.Errorf("payment.service.url = %q", got)
	}
	if got := mustResolve(t, c, "spring.application.name"); got != "cart" {
		t.Errorf("spring.application.name = %q", got)
	}
}

func TestProperties(t *testing.T) {
	c := buildStore(t, nil, map[string]string{
		"application.properties": "orders.topic=orders.v1\n# a comment\npayment.url: http://p:8080\n",
	})
	if got := mustResolve(t, c, "orders.topic"); got != "orders.v1" {
		t.Errorf("orders.topic = %q", got)
	}
	// First ':' after the key is the separator; the URL's colon survives.
	if got := mustResolve(t, c, "payment.url"); got != "http://p:8080" {
		t.Errorf("payment.url = %q", got)
	}
}

func TestProfileListFlattening(t *testing.T) {
	c := buildStore(t, nil, map[string]string{
		"application.yml": "spring:\n  profiles:\n    active:\n      - prod\n      - kafka\n",
	})
	if got := mustResolve(t, c, "spring.profiles.active"); got != "prod,kafka" {
		t.Errorf("active list = %q, want prod,kafka", got)
	}
}

func TestActiveProfileOverride(t *testing.T) {
	files := map[string]string{
		"application.yml":      "spring:\n  profiles:\n    active: prod\npayment:\n  url: http://base\n",
		"application-prod.yml": "payment:\n  url: http://prod\n",
		"application-dev.yml":  "payment:\n  url: http://dev\n",
	}
	c := buildStore(t, nil, files)
	// Active profile (prod, from spring.profiles.active) overrides base.
	if got := mustResolve(t, c, "payment.url"); got != "http://prod" {
		t.Errorf("payment.url = %q, want http://prod (active profile wins)", got)
	}
}

func TestProfilesFlagOverridesActive(t *testing.T) {
	files := map[string]string{
		"application.yml":      "spring:\n  profiles:\n    active: prod\npayment:\n  url: http://base\n",
		"application-prod.yml": "payment:\n  url: http://prod\n",
		"application-dev.yml":  "payment:\n  url: http://dev\n",
	}
	// --profiles dev must win over spring.profiles.active: prod.
	c := buildStore(t, []string{"dev"}, files)
	if got := mustResolve(t, c, "payment.url"); got != "http://dev" {
		t.Errorf("payment.url = %q, want http://dev (--profiles override)", got)
	}
}

func TestNonActiveProfileFallback(t *testing.T) {
	files := map[string]string{
		"application.yml":         "spring:\n  profiles:\n    active: prod\n",
		"application-prod.yml":    "a: prod-only\n",
		"application-staging.yml": "b: staging-only\n",
	}
	c := buildStore(t, nil, files)
	// 'a' is in the active profile; 'b' lives only in a non-active profile but
	// is still resolvable via the fallback layer.
	if got := mustResolve(t, c, "a"); got != "prod-only" {
		t.Errorf("a = %q", got)
	}
	if got := mustResolve(t, c, "b"); got != "staging-only" {
		t.Errorf("b = %q (non-active fallback)", got)
	}
}

func TestCandidatesProvenance(t *testing.T) {
	c := buildStore(t, []string{"prod"}, map[string]string{
		"application.yml":      "x: base\n",
		"application-prod.yml": "x: prod\n",
	})
	cands := c.Candidates("${x}")
	if len(cands) != 1 {
		t.Fatalf("got %d candidates, want 1", len(cands))
	}
	got := cands[0]
	if got.Value != "prod" || got.Source != "application-prod.yml" || got.Origin != "prod" {
		t.Errorf("candidate = %+v, want value=prod source=application-prod.yml origin=prod", got)
	}
}

func TestUnresolvedKey(t *testing.T) {
	c := buildStore(t, nil, map[string]string{"application.yml": "a: 1\n"})
	if _, _, _, ok := c.Resolve("${missing}"); ok {
		t.Error("missing key should not resolve")
	}
	if c.Candidates("${missing}") != nil {
		t.Error("missing key should have no candidates")
	}
}
