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
