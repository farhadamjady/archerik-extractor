package aspnet

import (
	"strings"

	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/csharp"
	"github.com/farhadamjady/archerik-extractor/internal/schema"
)

// csTypeIndex is a schema.TypeSource over the repo's C# classes, records, and
// structs (DTOs / envelopes / commands), so the schema walker can resolve an
// action's request/response body structure. Types are keyed by simple name;
// nested types (Create.Model) collapse to their last segment (Model), last
// declaration wins — a known bound, acceptable for MVP schema recovery.
type csTypeIndex struct {
	defs map[string]*schema.TypeDef
}

func (t *csTypeIndex) Lookup(name string) (*schema.TypeDef, bool) {
	td, ok := t.defs[simpleName(name)]
	return td, ok
}

// simpleName drops a namespace/outer-type qualifier and a trailing nullable `?`.
func simpleName(name string) string {
	name = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(name), "?"))
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		name = name[i+1:]
	}
	return name
}

// buildTypeIndex indexes every class/record/struct across the parsed C# files.
func buildTypeIndex(files []*csharp.File) *csTypeIndex {
	idx := &csTypeIndex{defs: map[string]*schema.TypeDef{}}
	for _, f := range files {
		f.Root().Walk(func(n csharp.Node) bool {
			switch n.Type() {
			case "class_declaration", "struct_declaration":
				if td := typeDef(n, schema.KindClass); td != nil {
					idx.defs[td.Name] = td
				}
			case "record_declaration", "record_struct_declaration":
				if td := typeDef(n, schema.KindRecord); td != nil {
					idx.defs[td.Name] = td
				}
			}
			return true
		})
	}
	return idx
}

// typeDef builds a TypeDef from a class/record/struct: its property declarations,
// a record's positional parameters, and the first base type (for inheritance).
func typeDef(decl csharp.Node, kind schema.Kind) *schema.TypeDef {
	name := csharp.Name(decl)
	if name == "" {
		return nil
	}
	td := &schema.TypeDef{Name: name, Kind: kind, Super: baseType(decl)}

	// record positional parameters: `record Envelope(Comment Comment)`
	if pl := csharp.ChildByType(decl, "parameter_list"); pl.Valid() {
		for _, p := range csharp.NamedChildren(pl) {
			if p.Type() == "parameter" {
				if fd, ok := paramField(p); ok {
					td.Fields = append(td.Fields, fd)
				}
			}
		}
	}

	// property declarations in the body
	if body := csharp.ChildByType(decl, "declaration_list"); body.Valid() {
		for _, m := range csharp.NamedChildren(body) {
			if m.Type() != "property_declaration" || !serializableProperty(m) {
				continue
			}
			nm := m.ChildByFieldName("name")
			ty := m.ChildByFieldName("type")
			if nm.Valid() && ty.Valid() {
				td.Fields = append(td.Fields, schema.FieldDef{Name: nm.Text(), Type: ty.Text(), Source: schema.SourceField})
			}
		}
	}
	return td
}

// serializableProperty reports whether a C# property is part of the default JSON
// contract. System.Text.Json / Newtonsoft serialize PUBLIC INSTANCE properties
// only, so a non-public property (private/protected/internal — a helper, not wire
// state) invents no phantom field, a static property is off the instance, and
// [JsonIgnore] drops it. [JsonInclude] forces a non-public property back in — the
// C# analogue of Java's @JsonProperty override.
func serializableProperty(prop csharp.Node) bool {
	if csharp.HasAttribute(prop, "JsonIgnore") {
		return false
	}
	public, static := false, false
	for _, c := range csharp.NamedChildren(prop) {
		if c.Type() != "modifier" {
			continue
		}
		switch c.Text() {
		case "public":
			public = true
		case "static":
			static = true
		}
	}
	if static {
		return false
	}
	return public || csharp.HasAttribute(prop, "JsonInclude")
}

// paramField turns a record positional parameter into a field.
func paramField(p csharp.Node) (schema.FieldDef, bool) {
	nm := p.ChildByFieldName("name")
	ty := p.ChildByFieldName("type")
	if !nm.Valid() || !ty.Valid() {
		return schema.FieldDef{}, false
	}
	return schema.FieldDef{Name: nm.Text(), Type: ty.Text(), Source: schema.SourceRecordComponent}, true
}

// baseType returns the first entry of a base_list — the superclass for inherited
// fields. Interfaces may also appear here; resolving a non-DTO base simply finds
// nothing in the index, so a wrong guess is harmless.
func baseType(decl csharp.Node) string {
	bl := csharp.ChildByType(decl, "base_list")
	if !bl.Valid() {
		return ""
	}
	for _, c := range csharp.NamedChildren(bl) {
		t := strings.TrimSpace(c.Text())
		if i := strings.IndexByte(t, '<'); i >= 0 {
			t = t[:i]
		}
		if t != "" {
			return t
		}
	}
	return ""
}
