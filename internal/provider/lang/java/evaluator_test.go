package java

import (
	"strings"
	"testing"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/resolve"
)

// fakeConfig is a minimal ConfigResolver for @Value tests.
type fakeConfig map[string]string

func (f fakeConfig) Resolve(expr string) (string, model.Confidence, bool) {
	// Substitute every ${key} like the real resolver does (mixed literals too).
	out, ok := expr, false
	for k, v := range f {
		if strings.Contains(out, "${"+k+"}") {
			out = strings.ReplaceAll(out, "${"+k+"}", v)
			ok = true
		}
	}
	if strings.Contains(out, "${") {
		return out, model.Uncertain, false
	}
	return out, model.Likely, ok
}
func (f fakeConfig) Candidates(string) []provider.ResolvedValue { return nil }

// evalTarget parses files, finds the single target(EXPR) call, and evaluates
// EXPR with an Evaluator over the built symbols + config.
func evalTarget(t *testing.T, cfg provider.ConfigResolver, srcs ...string) resolve.ValueSet {
	t.Helper()
	var files []*File
	for i, s := range srcs {
		pf, err := NewParser().Parse(string(rune('A'+i))+".java", []byte(s))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		files = append(files, pf.(*File))
	}
	idx := &provider.Index{Symbols: IndexSymbols(files), Config: cfg, Types: IndexTypes(files, nil)}
	ev := NewEvaluator(idx)

	// The expression under test is the first argument of a target(...) call in
	// the LAST file.
	arg := findTargetArg(files[len(files)-1].Root())
	if !arg.Valid() {
		t.Fatal("no target(...) call found")
	}
	return ev.Resolve(arg)
}

func findTargetArg(root Node) Node {
	var found Node
	root.Walk(func(n Node) bool {
		if found.Valid() {
			return false
		}
		if n.Type() == "method_invocation" && n.ChildByFieldName("name").Text() == "target" {
			args := n.ChildByFieldName("arguments")
			if args.NamedChildCount() > 0 {
				found = args.NamedChild(0)
				return false
			}
		}
		return true
	})
	return found
}

func vals(vs resolve.ValueSet) []string {
	out := make([]string, len(vs.Values))
	for i, v := range vs.Values {
		out[i] = v.S
	}
	return out
}

func TestEvalLiteral(t *testing.T) {
	vs := evalTarget(t, nil, `class C { void m() { target("http://x/api"); } }`)
	if vs.Kind != resolve.Exact || len(vs.Values) != 1 || vs.Values[0].S != "http://x/api" {
		t.Fatalf("literal = %+v", vs)
	}
	if vs.Values[0].Conf != model.Confirmed {
		t.Errorf("conf = %s, want confirmed", vs.Values[0].Conf)
	}
}

func TestEvalConcatConstantAndHole(t *testing.T) {
	// The acceptance case: "http://" + Const.HOST + "/u/" + id
	// HOST is a constant (confirmed); id is a param (hole) -> Template.
	vs := evalTarget(t, nil,
		`class Const { static final String HOST = "myhost"; }`,
		`class C { void m(String id) { target("http://" + Const.HOST + "/u/" + id); } }`)

	if vs.Kind != resolve.Template {
		t.Fatalf("kind = %v, want Template: %+v", vs.Kind, vs)
	}
	// Known prefix resolved, trailing param a hole.
	seg := vs.Segments
	if len(seg) != 2 || seg[0].Literal != "http://myhost/u/" || !seg[1].Hole {
		t.Errorf("segments = %+v, want [http://myhost/u/, hole]", seg)
	}
}

func TestEvalConcatAllResolved(t *testing.T) {
	vs := evalTarget(t, nil,
		`class T { static final String BASE = "http://svc"; static final String PATH = "/orders"; }`,
		`class C { void m() { target(T.BASE + T.PATH); } }`)
	if vs.Kind != resolve.Exact || len(vs.Values) != 1 || vs.Values[0].S != "http://svc/orders" {
		t.Errorf("all-resolved concat = %+v, want http://svc/orders", vs)
	}
}

func TestEvalValueField(t *testing.T) {
	cfg := fakeConfig{"base.url": "http://from-config"}
	vs := evalTarget(t, cfg,
		`class C { @Value("${base.url}") String baseUrl; void m() { target(baseUrl); } }`)
	if len(vals(vs)) != 1 || vs.Values[0].S != "http://from-config" {
		t.Fatalf("@Value field = %+v, want http://from-config", vs)
	}
	if vs.Values[0].Conf != model.Likely {
		t.Errorf("conf = %s, want likely (config)", vs.Values[0].Conf)
	}
}

func TestEvalStringFormat(t *testing.T) {
	vs := evalTarget(t, nil,
		`class H { static final String HOST = "h"; }`,
		`class C { void m() { target(String.format("http://%s/u", H.HOST)); } }`)
	if vs.Kind != resolve.Exact || vs.Values[0].S != "http://h/u" {
		t.Errorf("String.format = %+v, want http://h/u", vs)
	}
}

func TestEvalUnknownParam(t *testing.T) {
	vs := evalTarget(t, nil, `class C { void m(String url) { target(url); } }`)
	if vs.Kind != resolve.Unknown {
		t.Errorf("bare param = %v, want Unknown", vs.Kind)
	}
}

func TestEvalOpaqueCall(t *testing.T) {
	vs := evalTarget(t, nil, `class C { void m() { target(System.getenv("URL")); } }`)
	if vs.Kind != resolve.Unknown {
		t.Errorf("getenv = %v, want Unknown", vs.Kind)
	}
}
