package schema

import (
	"testing"

	"github.com/farhadamjady/service-discovery/internal/model"
)

// fakeTypes is an in-memory TypeSource for walker tests.
type fakeTypes map[string]*TypeDef

func (f fakeTypes) Lookup(name string) (*TypeDef, bool) {
	if d, ok := f[name]; ok {
		return d, true
	}
	return nil, false
}

func field(name, typ string, anns ...Annotation) FieldDef {
	return FieldDef{Name: name, Type: typ, Source: SourceField, Annotations: anns}
}

// nestedByName maps a schema's nested fields by name.
func nestedByName(s *model.Schema) map[string]model.Schema {
	m := map[string]model.Schema{}
	for _, n := range s.Nested {
		m[n.Name] = n
	}
	return m
}

func TestWalkScalar(t *testing.T) {
	w := NewWalker(nil)
	s := w.Type("String")
	if s.Type != "string" || s.Confidence != model.Confirmed {
		t.Errorf("String = %+v, want string/confirmed", s)
	}
	if w.Type("int").Type != "integer" || w.Type("boolean").Type != "boolean" {
		t.Error("scalar mapping wrong")
	}
}

func TestWalkDTOFields(t *testing.T) {
	types := fakeTypes{"User": {Name: "User", Fields: []FieldDef{
		field("name", "String"),
		field("age", "int"),
	}}}
	s := NewWalker(types).Type("User")
	if s.Type != "User" || s.Confidence != model.Confirmed {
		t.Fatalf("User = %+v", s)
	}
	n := nestedByName(s)
	if n["name"].Type != "string" || n["age"].Type != "integer" {
		t.Errorf("fields = %+v", s.Nested)
	}
}

func TestWalkListUnwrap(t *testing.T) {
	s := NewWalker(nil).Type("List<User>")
	if s.Type != "array" || s.Items != "User" {
		t.Errorf("List<User> = %+v, want array items=User", s)
	}
}

func TestWalkOptionalUnwrap(t *testing.T) {
	s := NewWalker(nil).Type("Optional<String>")
	if s.Type != "string" {
		t.Errorf("Optional<String> = %+v, want string", s)
	}
}

func TestWalkPageUnwrap(t *testing.T) {
	types := fakeTypes{"Invoice": {Name: "Invoice", Fields: []FieldDef{field("id", "String")}}}
	s := NewWalker(types).Type("Page<Invoice>")
	if s.Type != "Invoice" {
		t.Errorf("Page<Invoice> = %+v, want Invoice", s)
	}
}

func TestWalkResponseEntityUnwrap(t *testing.T) {
	types := fakeTypes{"User": {Name: "User", Fields: []FieldDef{field("name", "String")}}}
	s := NewWalker(types).Type("ResponseEntity<User>")
	if s.Type != "User" || len(s.Nested) != 1 {
		t.Errorf("ResponseEntity<User> = %+v, want User with 1 field", s)
	}
}

func TestWalkMap(t *testing.T) {
	s := NewWalker(nil).Type("Map<String, Order>")
	if s.Type != "map" || s.KeyType != "String" || s.ValueType != "Order" {
		t.Errorf("Map = %+v, want map key=String value=Order", s)
	}
}

func TestWalkNestedIsLeafAtDepth1(t *testing.T) {
	// Address is a DTO field of User; at the default depth it is a leaf (named
	// but not expanded) — recursion is PR 21.
	types := fakeTypes{
		"User":    {Name: "User", Fields: []FieldDef{field("address", "Address")}},
		"Address": {Name: "Address", Fields: []FieldDef{field("city", "String")}},
	}
	s := NewWalker(types).Type("User")
	addr := nestedByName(s)["address"]
	if addr.Type != "Address" || len(addr.Nested) != 0 {
		t.Errorf("address = %+v, want leaf Address (no nested at depth 1)", addr)
	}
}

func TestWalkJacksonRename(t *testing.T) {
	types := fakeTypes{"C": {Name: "C", Fields: []FieldDef{
		field("name", "String", Annotation{Name: "JsonProperty", Arg: "user_name"}),
		field("secret", "String", Annotation{Name: "JsonIgnore"}),
	}}}
	s := NewWalker(types).Type("C")
	n := nestedByName(s)
	if _, ok := n["user_name"]; !ok {
		t.Errorf("@JsonProperty rename missing: %+v", s.Nested)
	}
	if _, ok := n["secret"]; ok {
		t.Error("@JsonIgnore field should be dropped")
	}
}

func TestWalkFieldGetterUnion(t *testing.T) {
	// A declared field and its getter are one field after the union.
	types := fakeTypes{"C": {Name: "C", Fields: []FieldDef{
		{Name: "name", Type: "String", Source: SourceField},
		{Name: "name", Type: "String", Source: SourceGetter},
	}}}
	s := NewWalker(types).Type("C")
	if len(s.Nested) != 1 || s.Nested[0].Name != "name" {
		t.Errorf("field+getter should union to one: %+v", s.Nested)
	}
}

func TestWalkUnresolvedUncertain(t *testing.T) {
	s := NewWalker(fakeTypes{}).Type("MysteryDto")
	if s.Type != "MysteryDto" || s.Confidence != model.Uncertain {
		t.Errorf("unresolved = %+v, want MysteryDto/uncertain", s)
	}
}

func TestWalkOpaqueUncertain(t *testing.T) {
	if s := NewWalker(nil).Type("Object"); s.Confidence != model.Uncertain {
		t.Errorf("Object = %+v, want uncertain", s)
	}
}

func TestWalkVoid(t *testing.T) {
	if s := NewWalker(nil).Type("void"); s.Type != "void" {
		t.Errorf("void = %+v", s)
	}
	if s := NewWalker(nil).Type("ResponseEntity<Void>"); s.Type != "void" {
		t.Errorf("ResponseEntity<Void> = %+v, want void", s)
	}
}
