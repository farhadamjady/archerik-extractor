package spring

import (
	"sort"
	"strings"
	"testing"

	"github.com/farhadamjady/archerik-extractor/internal/model"
	"github.com/farhadamjady/archerik-extractor/internal/provider"
)

// buildLayered runs the config + deploy indexers over virtual files and returns
// the layered resolver, the way the pipeline assembles Index.Config.
func buildLayered(t *testing.T, env string, files map[string]string) provider.ConfigResolver {
	t.Helper()
	parsed := map[string]provider.ParsedFile{}
	for p, content := range files {
		kind := provider.KindDeployConfig
		if isSpringConfigPath(p) {
			kind = provider.KindSpringConfig
		}
		pf, err := (rawParser{kind: kind}).Parse(p, []byte(content))
		if err != nil {
			t.Fatalf("parse %s: %v", p, err)
		}
		parsed[p] = pf
	}
	idx := &provider.Index{}
	ic := &provider.IndexContext{Parsed: parsed, Environment: env}
	if err := (configIndexer{}).Index(ic, idx); err != nil {
		t.Fatalf("config index: %v", err)
	}
	if err := (deployIndexer{}).Index(ic, idx); err != nil {
		t.Fatalf("deploy index: %v", err)
	}
	return idx.Config
}

func isSpringConfigPath(p string) bool { return strings.Contains(p, "application") }

// TestDeployResolvesWhenSpringMisses is the milestone: a Feign URL whose value
// lives only in a Helm chart (values -> template env) resolves at likely.
func TestDeployResolvesWhenSpringMisses(t *testing.T) {
	c := buildLayered(t, "", map[string]string{
		"src/main/resources/application.yml": "spring:\n  application:\n    name: cart\n",
		"chart/values.yaml":                  "payment:\n  serviceUrl: http://payment:8080\n",
		"chart/templates/deployment.yaml": "" +
			"        - name: PAYMENT_SERVICE_URL\n" +
			"          value: {{ .Values.payment.serviceUrl }}\n",
	})

	// Spring property PAYMENT_SERVICE_URL is not in application.yml; it resolves
	// through the deploy layer (relaxed-bound: payment.service.url).
	v, conf, _, ok := c.Resolve("${PAYMENT_SERVICE_URL}")
	if !ok || v != "http://payment:8080" {
		t.Fatalf("resolve = (%q, ok=%v), want http://payment:8080", v, ok)
	}
	if conf != model.Likely {
		t.Errorf("confidence = %s, want likely (deploy layer)", conf)
	}
}

// TestSpringWinsOverDeploy: application.* takes precedence over the deploy layer.
func TestSpringWinsOverDeploy(t *testing.T) {
	c := buildLayered(t, "", map[string]string{
		"application.properties": "payment.url=http://from-spring",
		"values.yaml":            "payment:\n  url: http://from-helm\n",
	})
	if v, _, _, _ := c.Resolve("${payment.url}"); v != "http://from-spring" {
		t.Errorf("resolve = %q, want http://from-spring (Spring wins)", v)
	}
}

// TestDivergentOverlaysCandidates: with no environment selected, staging vs prod
// disagreement surfaces one candidate per distinct value.
func TestDivergentOverlaysCandidates(t *testing.T) {
	c := buildLayered(t, "", map[string]string{
		"values.yaml":         "payment:\n  url: http://base\n",
		"values-staging.yaml": "payment:\n  url: http://staging\n",
		"values-prod.yaml":    "payment:\n  url: http://prod\n",
	})
	cands := c.Candidates("${payment.url}")
	got := []string{}
	for _, rv := range cands {
		got = append(got, rv.Value)
	}
	sort.Strings(got)
	want := []string{"http://base", "http://prod", "http://staging"}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("candidates = %v, want %v", got, want)
	}
}

// TestEnvironmentCollapsesCandidates: selecting an environment picks that
// overlay and collapses to a single value.
func TestEnvironmentCollapsesCandidates(t *testing.T) {
	files := map[string]string{
		"values.yaml":         "payment:\n  url: http://base\n",
		"values-staging.yaml": "payment:\n  url: http://staging\n",
		"values-prod.yaml":    "payment:\n  url: http://prod\n",
	}
	c := buildLayered(t, "prod", files)
	cands := c.Candidates("${payment.url}")
	if len(cands) != 1 || cands[0].Value != "http://prod" {
		t.Errorf("candidates = %v, want single http://prod", cands)
	}
	if v, _, _, _ := c.Resolve("${payment.url}"); v != "http://prod" {
		t.Errorf("resolve = %q, want http://prod", v)
	}
}

// TestDotenvResolves: a .env value resolves through the deploy layer too.
func TestDotenvResolves(t *testing.T) {
	c := buildLayered(t, "", map[string]string{
		"deploy/.env": "ORDERS_TOPIC=orders.v1\n",
	})
	if v, _, _, ok := c.Resolve("${ORDERS_TOPIC}"); !ok || v != "orders.v1" {
		t.Errorf("resolve = (%q, ok=%v), want orders.v1", v, ok)
	}
}

// TestUnresolvableStaysUncertain: a value that lives only in a runtime source
// (no config, no deploy, no default) stays unresolved.
func TestUnresolvableStaysUncertain(t *testing.T) {
	c := buildLayered(t, "", map[string]string{
		"application.yml": "a: 1\n",
	})
	if _, conf, _, ok := c.Resolve("${config.server.only}"); ok || conf != model.Uncertain {
		t.Errorf("resolve = (conf=%s, ok=%v), want (uncertain, false)", conf, ok)
	}
}
