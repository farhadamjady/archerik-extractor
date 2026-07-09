package java

import (
	"sort"
	"testing"

	"github.com/farhadamjady/service-discovery/internal/schema"
)

func indexOne(t *testing.T, srcs ...string) *Types {
	t.Helper()
	var files []*File
	for i, s := range srcs {
		pf, err := NewParser().Parse(string(rune('A'+i))+".java", []byte(s))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		files = append(files, pf.(*File))
	}
	return IndexTypes(files)
}

// fieldNames returns the field candidate names (with duplicates from multiple
// sources), sorted.
func fieldNames(td *schema.TypeDef) []string {
	var out []string
	for _, f := range td.Fields {
		out = append(out, f.Name)
	}
	sort.Strings(out)
	return out
}

func TestIndexRecord(t *testing.T) {
	types := indexOne(t, `package com.acme; record User(String name, int age) {}`)
	td, ok := types.Lookup("User")
	if !ok {
		t.Fatal("User not indexed")
	}
	if td.Kind != schema.KindRecord || td.Package != "com.acme" {
		t.Errorf("kind/pkg = %v/%q", td.Kind, td.Package)
	}
	if got := fieldNames(td); len(got) != 2 || got[0] != "age" || got[1] != "name" {
		t.Errorf("record fields = %v, want [age name]", got)
	}
	for _, f := range td.Fields {
		if f.Source != schema.SourceRecordComponent {
			t.Errorf("field %q source = %v, want record component", f.Name, f.Source)
		}
		if f.Name == "name" && f.Type != "String" {
			t.Errorf("name type = %q, want String", f.Type)
		}
	}
}

func TestIndexPOJOWithGetters(t *testing.T) {
	types := indexOne(t, `class User {
		private String name;
		private static final String CONST = "x";
		public String getName() { return name; }
		public int getAge() { return 0; }
		public void setName(String n) {}
	}`)
	td, _ := types.Lookup("User")

	// declared field "name"; getters name + age; static CONST and setName excluded.
	got := fieldNames(td)
	want := []string{"age", "name", "name"} // field name + getter name + getter age
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("fields = %v, want %v", got, want)
	}
	// getAge is a getter-sourced field with return type int
	for _, f := range td.Fields {
		if f.Name == "age" && (f.Source != schema.SourceGetter || f.Type != "int") {
			t.Errorf("age = %+v, want getter/int", f)
		}
	}
}

func TestIndexLombokData(t *testing.T) {
	types := indexOne(t, `@Data class User { private String name; private int age; }`)
	td, _ := types.Lookup("User")
	if !td.HasAnnotation("Data") {
		t.Error("class @Data annotation not recorded")
	}
	if got := fieldNames(td); len(got) != 2 || got[0] != "age" || got[1] != "name" {
		t.Errorf("lombok fields = %v, want [age name]", got)
	}
}

func TestIndexInheritance(t *testing.T) {
	types := indexOne(t, `class Base { private String id; }`,
		`class Admin extends Base { private String role; }`)
	admin, _ := types.Lookup("Admin")
	if admin.Super != "Base" {
		t.Errorf("super = %q, want Base", admin.Super)
	}
	if _, ok := types.Lookup("Base"); !ok {
		t.Error("Base not indexed")
	}
}

func TestIndexJacksonAnnotation(t *testing.T) {
	types := indexOne(t, `class C {
		@JsonProperty("user_name") private String name;
		@JsonIgnore private String secret;
		@JsonProperty(value = "n", required = true) private int n;
	}`)
	td, _ := types.Lookup("C")

	byName := map[string]schema.FieldDef{}
	for _, f := range td.Fields {
		byName[f.Name] = f
	}
	if a, ok := byName["name"].Annotation("JsonProperty"); !ok || a.Arg != "user_name" {
		t.Errorf("JsonProperty positional = %+v", a)
	}
	if !byName["secret"].HasAnnotation("JsonIgnore") {
		t.Error("JsonIgnore not recorded")
	}
	if a, _ := byName["n"].Annotation("JsonProperty"); a.Named["value"] != "n" || a.Named["required"] != "true" {
		t.Errorf("JsonProperty named args = %+v", a.Named)
	}
}

func TestIndexImports(t *testing.T) {
	types := indexOne(t, `package com.acme;
		import com.acme.dto.Order;
		class C { private Order order; }`)
	td, _ := types.Lookup("C")
	if td.Imports["Order"] != "com.acme.dto.Order" {
		t.Errorf("imports = %v, want Order -> com.acme.dto.Order", td.Imports)
	}
}

func TestLookupQualifiedFallback(t *testing.T) {
	types := indexOne(t, `class User { private String name; }`)
	if _, ok := types.Lookup("com.acme.User"); !ok {
		t.Error("qualified lookup should fall back to simple name User")
	}
}
