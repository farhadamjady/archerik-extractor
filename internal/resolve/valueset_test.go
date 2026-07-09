package resolve

import (
	"testing"

	"github.com/farhadamjady/service-discovery/internal/model"
)

func strings(vs ValueSet) []string {
	out := make([]string, len(vs.Values))
	for i, v := range vs.Values {
		out[i] = v.S
	}
	return out
}

func eq(a, b []string) bool {
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

func TestExactDedupSort(t *testing.T) {
	vs := NewExact(model.Confirmed, "b", "a", "b")
	if vs.Kind != Exact {
		t.Fatalf("kind = %v", vs.Kind)
	}
	if got := strings(vs); !eq(got, []string{"a", "b"}) {
		t.Errorf("values = %v, want [a b] (deduped, sorted)", got)
	}
}

func TestConcatCartesian(t *testing.T) {
	a := NewExact(model.Confirmed, "http://x", "http://y")
	b := NewExact(model.Confirmed, "/a", "/b")
	got := strings(Concat(a, b))
	want := []string{"http://x/a", "http://x/b", "http://y/a", "http://y/b"}
	if !eq(got, want) {
		t.Errorf("concat = %v, want %v", got, want)
	}
}

func TestConcatConfidenceIsMin(t *testing.T) {
	a := ExactValues(Value{S: "http://x", Conf: model.Likely})
	b := ExactValues(Value{S: "/a", Conf: model.Confirmed})
	got := Concat(a, b)
	if got.Values[0].Conf != model.Likely {
		t.Errorf("conf = %s, want likely (min of likely, confirmed)", got.Values[0].Conf)
	}
}

func TestConcatWithUnknownIsTemplate(t *testing.T) {
	got := Concat(NewUnknown(), NewExact(model.Confirmed, "/users/{id}"))
	if got.Kind != Template {
		t.Fatalf("kind = %v, want Template", got.Kind)
	}
	// host is a hole, the path is a literal
	if len(got.Segments) != 2 || !got.Segments[0].Hole || got.Segments[1].Literal != "/users/{id}" {
		t.Errorf("segments = %+v, want [hole, /users/{id}]", got.Segments)
	}
}

func TestConcatAllLiteralCollapsesToExact(t *testing.T) {
	// A template built from a single-value Exact on both sides has no holes.
	got := NewTemplate(Lit("http://x"), Lit("/a"))
	if got.Kind != Exact || !eq(strings(got), []string{"http://x/a"}) {
		t.Errorf("all-literal template should collapse to Exact http://x/a, got %+v", got)
	}
}

func TestUnionMerges(t *testing.T) {
	got := Union(NewExact(model.Confirmed, "a"), NewExact(model.Confirmed, "b"))
	if !eq(strings(got), []string{"a", "b"}) {
		t.Errorf("union = %v, want [a b]", strings(got))
	}
}

func TestUnionWithUnknownKeepsKnown(t *testing.T) {
	got := Union(NewExact(model.Confirmed, "a"), NewUnknown())
	if !eq(strings(got), []string{"a"}) {
		t.Errorf("union = %v, want [a] (unknown branch drops out)", strings(got))
	}
	if Union(NewUnknown(), NewUnknown()).Kind != Unknown {
		t.Error("unknown ∪ unknown should be Unknown")
	}
}

func TestConcatCapDegradesToTemplate(t *testing.T) {
	// Force the product past the cap; it must degrade, not explode.
	big := make([]string, 8)
	for i := range big {
		big[i] = "x"
	}
	a := NewExact(model.Confirmed, "a1", "a2", "a3", "a4", "a5", "a6", "a7")
	b := NewExact(model.Confirmed, "b1", "b2", "b3", "b4", "b5", "b6", "b7")
	got := Concat(a, b) // 49 > 32
	if got.Kind != Template {
		t.Errorf("kind = %v, want Template (product exceeds cap)", got.Kind)
	}
}
