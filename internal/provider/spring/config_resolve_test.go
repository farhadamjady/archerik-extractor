package spring

import (
	"fmt"
	"testing"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
)

// store builds a resolver from flat key=value lines via a .properties file
// (properties keep ${...} verbatim, no YAML quoting needed).
func store(t *testing.T, kv map[string]string) provider.ConfigResolver {
	t.Helper()
	var b string
	for k, v := range kv {
		b += k + "=" + v + "\n"
	}
	return buildStore(t, nil, map[string]string{"application.properties": b})
}

func resolveExpr(t *testing.T, c provider.ConfigResolver, expr string) (string, model.Confidence, bool) {
	t.Helper()
	return c.Resolve(expr)
}

func TestResolveChain(t *testing.T) {
	c := store(t, map[string]string{"a": "${b}", "b": "${c}", "c": "final"})
	v, conf, ok := resolveExpr(t, c, "${a}")
	if !ok || v != "final" {
		t.Fatalf("resolve ${a} = (%q, ok=%v), want final", v, ok)
	}
	if conf != model.Likely {
		t.Errorf("confidence = %s, want likely (config indirection)", conf)
	}
}

func TestResolveMixedLiteralAndPlaceholders(t *testing.T) {
	c := store(t, map[string]string{"host": "svc", "port": "8080"})
	v, _, ok := resolveExpr(t, c, "http://${host}:${port}/api")
	if !ok || v != "http://svc:8080/api" {
		t.Errorf("resolve = (%q, ok=%v), want http://svc:8080/api", v, ok)
	}
}

func TestResolveDefault(t *testing.T) {
	c := store(t, map[string]string{"present": "yes"})

	// missing key falls back to the default
	if v, conf, ok := resolveExpr(t, c, "${missing:fallback}"); !ok || v != "fallback" || conf != model.Likely {
		t.Errorf("default = (%q, %s, ok=%v), want (fallback, likely, true)", v, conf, ok)
	}
	// present key ignores the default
	if v, _, ok := resolveExpr(t, c, "${present:fallback}"); !ok || v != "yes" {
		t.Errorf("present-with-default = (%q, ok=%v), want yes", v, ok)
	}
	// default may contain colons (first ':' splits)
	if v, _, ok := resolveExpr(t, c, "${db.url:jdbc:postgresql://h/db}"); !ok || v != "jdbc:postgresql://h/db" {
		t.Errorf("colon default = (%q, ok=%v), want jdbc:postgresql://h/db", v, ok)
	}
}

func TestResolveUnresolved(t *testing.T) {
	c := store(t, map[string]string{"a": "1"})
	v, conf, ok := resolveExpr(t, c, "${nope}")
	if ok {
		t.Fatal("unknown placeholder should not resolve")
	}
	if conf != model.Uncertain {
		t.Errorf("confidence = %s, want uncertain", conf)
	}
	if v != "${nope}" {
		t.Errorf("partial value = %q, want the placeholder left visible", v)
	}
}

func TestResolveCycleNoHang(t *testing.T) {
	c := store(t, map[string]string{"a": "${b}", "b": "${a}"})
	if _, _, ok := resolveExpr(t, c, "${a}"); ok {
		t.Error("a<->b cycle must not resolve (and must not hang)")
	}
}

func TestResolveDepthCap(t *testing.T) {
	// A chain longer than the depth cap must fail closed, not loop.
	kv := map[string]string{}
	for i := 0; i < maxPlaceholderDepth+2; i++ {
		kv[fmt.Sprintf("k%d", i)] = fmt.Sprintf("${k%d}", i+1)
	}
	kv[fmt.Sprintf("k%d", maxPlaceholderDepth+2)] = "bottom"
	c := store(t, kv)
	if _, _, ok := resolveExpr(t, c, "${k0}"); ok {
		t.Error("chain deeper than the cap should not resolve")
	}
}

func TestNonActiveProfileResolvesLikely(t *testing.T) {
	c := buildStore(t, nil, map[string]string{
		"application.yml":         "spring:\n  profiles:\n    active: prod\n",
		"application-staging.yml": "only.here: http://staging\n",
	})
	// Value lives only in a non-active profile; still resolvable, capped likely.
	v, conf, ok := c.Resolve("${only.here}")
	if !ok || v != "http://staging" {
		t.Fatalf("fallback resolve = (%q, ok=%v)", v, ok)
	}
	if conf != model.Likely {
		t.Errorf("confidence = %s, want likely", conf)
	}
}

func TestConfigDependenciesTracked(t *testing.T) {
	c := store(t, map[string]string{"a": "${b}", "b": "final"})
	// resolve two expressions: one chained, one missing-with-default.
	c.Resolve("${a}")
	c.Resolve("${x:def}")

	r, ok := c.(interface {
		Dependencies() []model.ConfigDep
	})
	if !ok {
		t.Fatal("resolver does not report dependencies")
	}
	deps := r.Dependencies()

	got := map[string]model.ConfigDep{}
	for _, d := range deps {
		got[d.Key] = d
	}
	// a and b resolved; x resolved via default.
	for _, k := range []string{"a", "b", "x"} {
		d, ok := got[k]
		if !ok {
			t.Errorf("missing config dependency %q", k)
			continue
		}
		if !d.Resolved {
			t.Errorf("dependency %q marked unresolved", k)
		}
	}
	// deterministic, sorted by key
	for i := 1; i < len(deps); i++ {
		if deps[i-1].Key > deps[i].Key {
			t.Errorf("dependencies not sorted: %q before %q", deps[i-1].Key, deps[i].Key)
		}
	}
}
