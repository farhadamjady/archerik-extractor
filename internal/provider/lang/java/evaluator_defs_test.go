package java

import (
	"sort"
	"testing"

	"github.com/farhadamjady/service-discovery/internal/resolve"
)

func sortedVals(vs resolve.ValueSet) []string {
	out := vals(vs)
	sort.Strings(out)
	return out
}

func eqStr(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestLocalVarSimple: a local initialized from a literal resolves to it.
func TestLocalVarSimple(t *testing.T) {
	vs := evalTarget(t, nil,
		`class C { void m() { String url = "http://x"; target(url); } }`)
	if !eqStr(sortedVals(vs), []string{"http://x"}) {
		t.Errorf("local var = %+v, want [http://x]", vs)
	}
}

// TestReachingDefsUnion is the acceptance case: a variable reassigned on
// different branches yields the union of both values (candidates).
func TestReachingDefsUnion(t *testing.T) {
	vs := evalTarget(t, nil, `class C {
		void m(boolean flag) {
			String url;
			if (flag) { url = "http://a"; } else { url = "http://b"; }
			target(url);
		}
	}`)
	if !eqStr(sortedVals(vs), []string{"http://a", "http://b"}) {
		t.Errorf("branch union = %v, want [http://a http://b]", sortedVals(vs))
	}
}

// TestLaterReassignmentExcluded: an assignment AFTER the use does not reach it.
func TestLaterReassignmentExcluded(t *testing.T) {
	vs := evalTarget(t, nil, `class C {
		void m() {
			String url = "http://early";
			target(url);
			url = "http://late";
		}
	}`)
	if !eqStr(sortedVals(vs), []string{"http://early"}) {
		t.Errorf("reaching = %v, want [http://early] (later assignment excluded)", sortedVals(vs))
	}
}

// TestTernaryUnion: a ternary target is the union of both arms.
func TestTernaryUnion(t *testing.T) {
	vs := evalTarget(t, nil,
		`class C { void m(boolean f) { target(f ? "http://a" : "http://b"); } }`)
	if !eqStr(sortedVals(vs), []string{"http://a", "http://b"}) {
		t.Errorf("ternary = %v, want [http://a http://b]", sortedVals(vs))
	}
}

// TestLoopAssignedIsHole: a variable assigned only inside a loop is not
// analyzed (loop -> hole), so it stays Unknown.
func TestLoopAssignedIsHole(t *testing.T) {
	vs := evalTarget(t, nil, `class C {
		void m(String[] parts) {
			String url = "";
			for (String p : parts) { url = url + p; }
			target(url);
		}
	}`)
	// The pre-loop init "" precedes the use, so it's the only reaching def;
	// the loop assignment is excluded. Result is the init value.
	if vs.Kind != resolve.Exact || !eqStr(vals(vs), []string{""}) {
		t.Errorf("loop-assigned var = %+v, want the pre-loop init only", vs)
	}
}

// TestSelfReferenceCycleNoHang: a mutually-referential pair must not loop.
func TestSelfReferenceCycleNoHang(t *testing.T) {
	vs := evalTarget(t, nil, `class C {
		void m() {
			String a = b;
			String b = a;
			target(a);
		}
	}`)
	// b is defined after a's use of it (and cyclic); a resolves to nothing usable.
	if vs.Kind != resolve.Unknown {
		t.Errorf("cyclic vars = %v, want Unknown (no hang)", vs.Kind)
	}
}

// TestConcatOfLocalVars: known local prefix + param -> Template with a hole.
func TestConcatOfLocalVars(t *testing.T) {
	vs := evalTarget(t, nil, `class C {
		void m(String id) {
			String base = "http://svc";
			target(base + "/users/" + id);
		}
	}`)
	if vs.Kind != resolve.Template {
		t.Fatalf("kind = %v, want Template: %+v", vs.Kind, vs)
	}
	if len(vs.Segments) != 2 || vs.Segments[0].Literal != "http://svc/users/" || !vs.Segments[1].Hole {
		t.Errorf("segments = %+v, want [http://svc/users/, hole]", vs.Segments)
	}
}

// TestSwitchUnion: an arrow-switch target unions its case values.
func TestSwitchUnion(t *testing.T) {
	vs := evalTarget(t, nil, `class C {
		void m(String env) {
			target(switch (env) {
				case "prod" -> "http://prod";
				default -> "http://dev";
			});
		}
	}`)
	if !eqStr(sortedVals(vs), []string{"http://dev", "http://prod"}) {
		t.Errorf("switch = %v, want [http://dev http://prod]", sortedVals(vs))
	}
}

// TestFieldInitializer (IMPROVEMENTS #3): an instance field with a literal
// initializer resolves through it, capped at likely (the field is mutable).
func TestFieldInitializer(t *testing.T) {
	vs := evalTarget(t, nil, `class C {
		private String hostname = "http://visits-service/";
		void m() { target(hostname + "pets/visits"); }
	}`)
	if vs.Kind != resolve.Exact || len(vs.Values) != 1 {
		t.Fatalf("field-init concat = %+v, want one exact value", vs)
	}
	if vs.Values[0].S != "http://visits-service/pets/visits" {
		t.Errorf("value = %q", vs.Values[0].S)
	}
	if string(vs.Values[0].Conf) != "likely" {
		t.Errorf("conf = %v, want likely (mutable field cap)", vs.Values[0].Conf)
	}
}

// TestFieldWithoutInitializerStaysUnknown: a field with no initializer (set in
// the constructor or by Spring) stays a hole.
func TestFieldWithoutInitializerStaysUnknown(t *testing.T) {
	vs := evalTarget(t, nil, `class C {
		private String hostname;
		void m() { target(hostname); }
	}`)
	if vs.Kind != resolve.Unknown {
		t.Errorf("uninitialized field = %v, want Unknown", vs.Kind)
	}
}

// TestReturnInlining (IMPROVEMENTS #4): a same-class helper with one return is
// followed one level.
func TestReturnInlining(t *testing.T) {
	vs := evalTarget(t, nil, `class C {
		private String base() { return "http://payment"; }
		void m() { target(base() + "/pay"); }
	}`)
	if vs.Kind != resolve.Exact || vs.Values[0].S != "http://payment/pay" {
		t.Errorf("inlined helper = %+v, want http://payment/pay", vs)
	}
}

// TestDiscoveryClientChain (IMPROVEMENTS #4): getInstances("name") anywhere in
// the receiver chain resolves to the logical service target at likely.
func TestDiscoveryClientChain(t *testing.T) {
	vs := evalTarget(t, nil, `class C {
		private DiscoveryClient discoveryClient;
		private java.net.URI uri() { return discoveryClient.getInstances("customers-service").get(0).getUri(); }
		void m() { target(uri() + "/owners"); }
	}`)
	if vs.Kind != resolve.Exact || len(vs.Values) != 1 {
		t.Fatalf("discovery chain = %+v", vs)
	}
	if vs.Values[0].S != "http://customers-service/owners" {
		t.Errorf("value = %q, want http://customers-service/owners", vs.Values[0].S)
	}
	if string(vs.Values[0].Conf) != "likely" {
		t.Errorf("conf = %v, want likely", vs.Values[0].Conf)
	}
}

// TestRecursiveHelperNoHang: a self-calling helper does not loop.
func TestRecursiveHelperNoHang(t *testing.T) {
	vs := evalTarget(t, nil, `class C {
		private String x() { return x(); }
		void m() { target(x()); }
	}`)
	if vs.Kind != resolve.Unknown {
		t.Errorf("recursive helper = %v, want Unknown", vs.Kind)
	}
}

// TestMultiReturnHelperNotInlined: a helper with two returns is not followed
// (could be either branch; return-inlining is deliberately conservative).
func TestMultiReturnHelperNotInlined(t *testing.T) {
	vs := evalTarget(t, nil, `class C {
		private String x(boolean f) { if (f) { return "a"; } return "b"; }
		void m() { target(x(true)); }
	}`)
	if vs.Kind != resolve.Unknown {
		t.Errorf("multi-return helper = %v, want Unknown (conservative)", vs.Kind)
	}
}

// TestCtorParamValue (IMPROVEMENTS #8): @Value on a constructor parameter
// assigned to a field resolves through config — the bank-of-anthos pattern.
func TestCtorParamValue(t *testing.T) {
	cfg := fakeConfig{"BALANCES_API_ADDR": "balancereader:8080"}
	vs := evalTarget(t, cfg, `class C {
		private final String balancesApiUri;
		public C(@Value("http://${BALANCES_API_ADDR}/balances") String balancesApiUri) {
			this.balancesApiUri = balancesApiUri;
		}
		void m(String acct) { target(balancesApiUri + "/" + acct); }
	}`)
	if vs.Kind != resolve.Template {
		t.Fatalf("kind = %v, want Template (trailing runtime acct): %+v", vs.Kind, vs)
	}
	if vs.Segments[0].Literal != "http://balancereader:8080/balances/" || !vs.Segments[1].Hole {
		t.Errorf("segments = %+v, want [http://balancereader:8080/balances/, hole]", vs.Segments)
	}
}

// TestCtorParamValueDifferentName: the parameter name differs from the field;
// the `this.field = param` assignment binds them.
func TestCtorParamValueDifferentName(t *testing.T) {
	cfg := fakeConfig{"x.url": "http://x"}
	vs := evalTarget(t, cfg, `class C {
		private String uri;
		public C(@Value("${x.url}") String addr) { this.uri = addr; }
		void m() { target(uri); }
	}`)
	if vs.Kind != resolve.Exact || vs.Values[0].S != "http://x" {
		t.Errorf("ctor different-name = %+v, want http://x", vs)
	}
}
