// Package resolve is the protocol- and language-neutral value lattice for
// recovering in-code target strings (URLs, topics, later gRPC/WS targets). A
// language evaluator (provider/lang/java, PR 14-15) walks an AST expression and
// produces a ValueSet; detectors turn that into edges. This package owns only
// the lattice and its algebra — it knows nothing about ASTs or frameworks.
package resolve

import (
	"sort"

	"github.com/farhadamjady/service-discovery/internal/model"
)

// maxValues caps candidate fan-out so concatenation/union can't explode
// (bounding, T5.3). Beyond it, an Exact set degrades to a Template or Unknown.
const maxValues = 32

// Kind is the lattice level of a ValueSet.
type Kind int

const (
	// Unknown: nothing recovered (method param, opaque call, getenv).
	Unknown Kind = iota
	// Exact: a finite set of concrete candidate strings.
	Exact
	// Template: a known shape with holes for the unresolved parts,
	// e.g. http://{?}/users/{id} — the host is a hole, the path is known.
	Template
)

// Value is one concrete candidate string with its confidence.
type Value struct {
	S    string
	Conf model.Confidence
}

// Segment is one piece of a Template: a literal run, or a hole.
type Segment struct {
	Literal string
	Hole    bool
}

// ValueSet is the result of evaluating an expression. Exactly one of Values
// (Kind==Exact) or Segments (Kind==Template) is populated.
type ValueSet struct {
	Kind     Kind
	Values   []Value
	Segments []Segment
}

// NewUnknown returns the bottom of the lattice.
func NewUnknown() ValueSet { return ValueSet{Kind: Unknown} }

// NewExact builds an Exact set giving every value the same confidence.
func NewExact(conf model.Confidence, values ...string) ValueSet {
	vs := make([]Value, len(values))
	for i, v := range values {
		vs[i] = Value{S: v, Conf: conf}
	}
	return canonical(vs)
}

// ExactValues builds an Exact set from values carrying their own confidence.
func ExactValues(values ...Value) ValueSet { return canonical(values) }

// NewTemplate builds a Template from segments, collapsing to Exact if it turns
// out to have no holes.
func NewTemplate(segs ...Segment) ValueSet {
	if !hasHole(segs) {
		lit := ""
		for _, s := range segs {
			lit += s.Literal
		}
		return NewExact(model.Confirmed, lit)
	}
	return ValueSet{Kind: Template, Segments: segs}
}

// Lit and Gap are Template segment constructors.
func Lit(s string) Segment { return Segment{Literal: s} }
func Gap() Segment         { return Segment{Hole: true} }

// Concat combines two sets as string concatenation. Exact×Exact is the
// (capped) cartesian product of concatenations; anything else becomes a Template
// with holes for the parts that aren't single concrete values.
func Concat(a, b ValueSet) ValueSet {
	if a.Kind == Exact && b.Kind == Exact && len(a.Values)*len(b.Values) <= maxValues {
		var out []Value
		for _, x := range a.Values {
			for _, y := range b.Values {
				out = append(out, Value{S: x.S + y.S, Conf: minConf(x.Conf, y.Conf)})
			}
		}
		return canonical(out)
	}
	return NewTemplate(append(asSegments(a), asSegments(b)...)...)
}

// Union merges alternatives (ternary / switch / reaching definitions). Concrete
// candidates from both sides are collected (deduped, capped). A Template unions
// cleanly only with itself or Unknown; other mixes degrade to Unknown.
func Union(a, b ValueSet) ValueSet {
	if a.Kind == Template || b.Kind == Template {
		switch {
		case a.Kind == Template && b.Kind == Unknown:
			return a
		case b.Kind == Template && a.Kind == Unknown:
			return b
		case a.Kind == Template && b.Kind == Template && sameSegments(a.Segments, b.Segments):
			return a
		default:
			return NewUnknown()
		}
	}
	vals := append(append([]Value{}, a.Values...), b.Values...)
	if len(vals) == 0 {
		return NewUnknown()
	}
	return canonical(vals)
}

// asSegments renders a value set as template segments: a single Exact value is a
// literal; alternatives and unknowns become a hole; a template keeps its shape.
func asSegments(vs ValueSet) []Segment {
	switch {
	case vs.Kind == Exact && len(vs.Values) == 1:
		return []Segment{Lit(vs.Values[0].S)}
	case vs.Kind == Template:
		return vs.Segments
	default:
		return []Segment{Gap()}
	}
}

// canonical dedupes by string (keeping the highest confidence), sorts for stable
// output, caps the set, and returns Unknown for an empty result.
func canonical(vals []Value) ValueSet {
	sort.Slice(vals, func(i, j int) bool {
		if vals[i].S != vals[j].S {
			return vals[i].S < vals[j].S
		}
		return confRank(vals[i].Conf) > confRank(vals[j].Conf)
	})
	var out []Value
	seen := map[string]bool{}
	for _, v := range vals {
		if seen[v.S] {
			continue
		}
		seen[v.S] = true
		out = append(out, v)
		if len(out) >= maxValues {
			break
		}
	}
	if len(out) == 0 {
		return NewUnknown()
	}
	return ValueSet{Kind: Exact, Values: out}
}

func hasHole(segs []Segment) bool {
	for _, s := range segs {
		if s.Hole {
			return true
		}
	}
	return false
}

func sameSegments(a, b []Segment) bool {
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

func confRank(c model.Confidence) int {
	switch c {
	case model.Confirmed:
		return 2
	case model.Likely:
		return 1
	default:
		return 0
	}
}

func minConf(a, b model.Confidence) model.Confidence {
	if confRank(a) <= confRank(b) {
		return a
	}
	return b
}
