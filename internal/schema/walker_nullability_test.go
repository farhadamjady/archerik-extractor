package schema

import (
	"testing"

	"github.com/farhadamjady/archerik-extractor/internal/model"
)

func ann(name string, named ...string) Annotation {
	a := Annotation{Name: name}
	if len(named) == 2 {
		a.Named = map[string]string{named[0]: named[1]}
	}
	return a
}

func TestRequiredness(t *testing.T) {
	types := fakeTypes{"Req": {Name: "Req", Fields: []FieldDef{
		field("primitive", "int"),                  // primitive -> required
		field("notNull", "String", ann("NotNull")), // @NotNull -> required
		field("jpTrue", "String", ann("JsonProperty", "required", "true")),
		field("nullable", "String", ann("Nullable")), // @Nullable -> optional
		field("opt", "Optional<String>"),             // Optional -> optional
		field("jpFalse", "String", ann("JsonProperty", "required", "false")),
		field("plain", "String"),                                     // no signal -> unknown
		{Name: "rec", Type: "String", Source: SourceRecordComponent}, // record component -> required
	}}}
	s := NewWalker(types).Type("Req")
	got := map[string]model.Requiredness{}
	for _, n := range s.Nested {
		got[n.Name] = n.Required
	}
	want := map[string]model.Requiredness{
		"primitive": model.ReqRequired,
		"notNull":   model.ReqRequired,
		"jpTrue":    model.ReqRequired,
		"rec":       model.ReqRequired,
		"nullable":  model.ReqOptional,
		"opt":       model.ReqOptional,
		"jpFalse":   model.ReqOptional,
		"plain":     model.ReqUnknown,
	}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("%s required = %q, want %q", name, got[name], w)
		}
	}
}

func TestNullability(t *testing.T) {
	types := fakeTypes{"N": {Name: "N", Fields: []FieldDef{
		field("prim", "int"),
		field("nn", "String", ann("NotNull")),
		field("nullable", "String", ann("Nullable")),
		field("opt", "Optional<String>"),
		field("plain", "String"),
	}}}
	s := NewWalker(types).Type("N")
	got := map[string]bool{}
	for _, n := range s.Nested {
		got[n.Name] = n.Nullable
	}
	if got["nullable"] != true || got["opt"] != true {
		t.Errorf("nullable/opt should be nullable: %+v", got)
	}
	if got["prim"] || got["nn"] || got["plain"] {
		t.Errorf("prim/notnull/plain should not be nullable: %+v", got)
	}
}

func TestRequiredAlwaysSet(t *testing.T) {
	// Even a scalar leaf and the root carry a requiredness value (never empty).
	s := NewWalker(nil).Type("String")
	if s.Required != model.ReqUnknown {
		t.Errorf("scalar root required = %q, want unknown", s.Required)
	}
}

func TestInheritedFields(t *testing.T) {
	types := fakeTypes{
		"Base":  {Name: "Base", Fields: []FieldDef{field("id", "String", ann("NotNull"))}},
		"Admin": {Name: "Admin", Super: "Base", Fields: []FieldDef{field("role", "String")}},
	}
	s := NewWalker(types).Type("Admin")
	got := map[string]model.Requiredness{}
	for _, n := range s.Nested {
		got[n.Name] = n.Required
	}
	if _, ok := got["id"]; !ok {
		t.Fatal("inherited field id missing")
	}
	if _, ok := got["role"]; !ok {
		t.Fatal("own field role missing")
	}
	if got["id"] != model.ReqRequired {
		t.Errorf("inherited @NotNull id required = %q, want required", got["id"])
	}
}

func TestSubclassOverridesInherited(t *testing.T) {
	// Admin redeclares id without @NotNull; the own field wins.
	types := fakeTypes{
		"Base":  {Name: "Base", Fields: []FieldDef{field("id", "String", ann("NotNull"))}},
		"Admin": {Name: "Admin", Super: "Base", Fields: []FieldDef{field("id", "String", ann("Nullable"))}},
	}
	s := NewWalker(types).Type("Admin")
	for _, n := range s.Nested {
		if n.Name == "id" && n.Required != model.ReqOptional {
			t.Errorf("overridden id required = %q, want optional (own wins)", n.Required)
		}
	}
}

func TestMergedSignalsAcrossFieldAndGetter(t *testing.T) {
	// @NotNull on the field, no annotation on the getter -> merged -> required.
	types := fakeTypes{"C": {Name: "C", Fields: []FieldDef{
		{Name: "name", Type: "String", Source: SourceField, Annotations: []Annotation{ann("NotNull")}},
		{Name: "name", Type: "String", Source: SourceGetter},
	}}}
	s := NewWalker(types).Type("C")
	if len(s.Nested) != 1 || s.Nested[0].Required != model.ReqRequired {
		t.Errorf("merged field+getter = %+v, want one required field", s.Nested)
	}
}

func TestDepthTruncation(t *testing.T) {
	// a -> b -> c -> d; at depth 2, d is truncated.
	types := fakeTypes{
		"A": {Name: "A", Fields: []FieldDef{field("b", "B")}},
		"B": {Name: "B", Fields: []FieldDef{field("c", "C")}},
		"C": {Name: "C", Fields: []FieldDef{field("d", "D")}},
		"D": {Name: "D", Fields: []FieldDef{field("x", "String")}},
	}
	s := NewWalker(types).Type("A")
	b := nestedByName(s)["b"]
	c := nestedByName(&b)["c"]
	if c.Type != "C" || !c.Truncated {
		t.Errorf("C at depth boundary = %+v, want truncated", c)
	}
	if len(c.Nested) != 0 {
		t.Errorf("truncated C should have no nested fields: %+v", c.Nested)
	}
}

func TestCycleTruncation(t *testing.T) {
	// Node has a field of its own type -> the recursion truncates on revisit.
	types := fakeTypes{"Node": {Name: "Node", Fields: []FieldDef{
		field("value", "String"),
		field("next", "Node"),
	}}}
	s := NewWalker(types).Type("Node")
	next := nestedByName(s)["next"]
	if !next.Truncated {
		t.Errorf("self-referential next should truncate: %+v", next)
	}
}
