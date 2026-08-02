package kotlin

import (
	"testing"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/schema"
)

// indexOf parses one Kotlin source into a Types index.
func indexOf(t *testing.T, src string) *Types {
	t.Helper()
	f, err := NewParser().Parse("D.kt", []byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return IndexTypes([]*File{f.(*File)}, nil)
}

func TestIndexDataClassFields(t *testing.T) {
	src := `package com.acme.dto

import com.fasterxml.jackson.annotation.JsonProperty

data class User(
    val id: Long,
    @JsonProperty("full_name") val name: String?,
    val roles: List<Role>,
    val address: Address,
    logger: Logger,
) : Auditable {
    val nickname: String = "n/a"
}
`
	idx := indexOf(t, src)

	td, ok := idx.Lookup("User")
	if !ok {
		t.Fatal("User not indexed")
	}
	if td.Kind != schema.KindRecord {
		t.Errorf("User kind = %v, want KindRecord (data class)", td.Kind)
	}
	if td.Super != "Auditable" {
		t.Errorf("User super = %q, want Auditable", td.Super)
	}
	if td.Package != "com.acme.dto" {
		t.Errorf("User package = %q", td.Package)
	}

	// A non-property constructor arg (`logger: Logger`, no val/var) is NOT a field.
	want := map[string]struct {
		typ      string
		nullable bool
		wire     string
	}{
		"id":       {"Long", false, ""},
		"name":     {"String", true, "full_name"},
		"roles":    {"List<Role>", false, ""},
		"address":  {"Address", false, ""},
		"nickname": {"String", false, ""}, // class-body property
	}
	got := map[string]schema.FieldDef{}
	for _, f := range td.Fields {
		got[f.Name] = f
	}
	if len(got) != len(want) {
		t.Fatalf("field count = %d %v, want %d", len(got), keys(got), len(want))
	}
	for name, w := range want {
		f, ok := got[name]
		if !ok {
			t.Errorf("missing field %q", name)
			continue
		}
		if f.Type != w.typ {
			t.Errorf("%s type = %q, want %q", name, f.Type, w.typ)
		}
		if hasAnnotation(f.Annotations, "Nullable") != w.nullable {
			t.Errorf("%s nullable = %v, want %v", name, hasAnnotation(f.Annotations, "Nullable"), w.nullable)
		}
		if w.wire != "" && jsonPropertyArg(f.Annotations) != w.wire {
			t.Errorf("%s @JsonProperty = %q, want %q", name, jsonPropertyArg(f.Annotations), w.wire)
		}
	}
}

// TestWalkerResolvesKotlinNesting proves the index is walker-compatible: the
// language-neutral schema walker expands a Kotlin DTO with a nested data class,
// keeping the nullable flag and the @JsonProperty wire rename.
func TestWalkerResolvesKotlinNesting(t *testing.T) {
	src := `package x
data class Order(val id: Long, val customer: Customer, val note: String?)
data class Customer(val name: String, val vip: Boolean)
`
	idx := indexOf(t, src)
	s := schema.NewWalker(idx).Type("Order")
	if s == nil || s.Type != "Order" {
		t.Fatalf("Order schema = %+v", s)
	}

	fields := map[string]model.Schema{}
	for _, f := range s.Nested {
		fields[f.Name] = f
	}
	if len(fields) != 3 {
		t.Fatalf("Order fields = %v, want id/customer/note", keysOf(fields))
	}
	if fields["id"].Type != "integer" {
		t.Errorf("id type = %q, want integer", fields["id"].Type)
	}
	if !fields["note"].Nullable {
		t.Errorf("note should be nullable")
	}

	cust := fields["customer"]
	if cust.Type != "Customer" || len(cust.Nested) != 2 {
		t.Fatalf("customer not expanded: %+v", cust)
	}
	cf := map[string]string{}
	for _, f := range cust.Nested {
		cf[f.Name] = f.Type
	}
	if cf["name"] != "string" || cf["vip"] != "boolean" {
		t.Errorf("customer fields = %v, want name:string vip:boolean", cf)
	}
}

func hasAnnotation(anns []schema.Annotation, name string) bool {
	for _, a := range anns {
		if a.Name == name {
			return true
		}
	}
	return false
}

func jsonPropertyArg(anns []schema.Annotation) string {
	for _, a := range anns {
		if a.Name == "JsonProperty" {
			return a.Arg
		}
	}
	return ""
}

func keys(m map[string]schema.FieldDef) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func keysOf(m map[string]model.Schema) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
