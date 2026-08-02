package nestjs

import (
	"strings"

	"github.com/farhadamjady/service-discovery/internal/provider/lang/tsjs"
	"github.com/farhadamjady/service-discovery/internal/schema"
)

// tsTypeIndex is a schema.TypeSource over the repo's TypeScript classes and
// interfaces (DTOs / response objects), so the schema walker can resolve an
// endpoint's request/response body structure. It indexes declared shape only —
// property name + declared type — mirroring the Java type index but over the
// tree-sitter-typescript AST.
type tsTypeIndex struct {
	defs map[string]*schema.TypeDef
}

func (t *tsTypeIndex) Lookup(name string) (*schema.TypeDef, bool) {
	td, ok := t.defs[simpleName(name)]
	return td, ok
}

// simpleName drops a namespace/module qualifier (rarely present in DTO names).
func simpleName(name string) string {
	name = strings.TrimSpace(name)
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		name = name[i+1:]
	}
	return name
}

// buildTypeIndex indexes every class/interface across the parsed TS files.
func buildTypeIndex(files []*tsjs.File) *tsTypeIndex {
	idx := &tsTypeIndex{defs: map[string]*schema.TypeDef{}}
	for _, f := range files {
		f.Root().Walk(func(n tsjs.Node) bool {
			switch n.Type() {
			case "class_declaration":
				if td := classDef(n, schema.KindClass); td != nil {
					idx.defs[td.Name] = td
				}
			case "interface_declaration":
				if td := interfaceDef(n); td != nil {
					idx.defs[td.Name] = td
				}
			}
			return true
		})
	}
	return idx
}

// classDef builds a TypeDef from a class_declaration: its public field
// definitions and the first `extends` supertype (for inherited fields).
func classDef(class tsjs.Node, kind schema.Kind) *schema.TypeDef {
	name := typeName(class)
	if name == "" {
		return nil
	}
	td := &schema.TypeDef{Name: name, Kind: kind, Super: heritage(class, "extends_clause")}
	body := tsjs.ChildByType(class, "class_body")
	if !body.Valid() {
		return td
	}
	for _, m := range tsjs.NamedChildren(body) {
		if m.Type() != "public_field_definition" && m.Type() != "field_definition" {
			continue
		}
		if fd, ok := fieldDef(m); ok {
			td.Fields = append(td.Fields, fd)
		}
	}
	return td
}

// interfaceDef builds a TypeDef from an interface_declaration: its property
// signatures and the first `extends` supertype.
func interfaceDef(iface tsjs.Node) *schema.TypeDef {
	name := typeName(iface)
	if name == "" {
		return nil
	}
	td := &schema.TypeDef{Name: name, Kind: schema.KindInterface, Super: heritage(iface, "extends_type_clause")}
	body := tsjs.ChildByType(iface, "interface_body")
	if !body.Valid() {
		body = tsjs.ChildByType(iface, "object_type")
	}
	if !body.Valid() {
		return td
	}
	for _, m := range tsjs.NamedChildren(body) {
		if m.Type() != "property_signature" {
			continue
		}
		if fd, ok := fieldDef(m); ok {
			td.Fields = append(td.Fields, fd)
		}
	}
	return td
}

// fieldDef extracts a property's name and declared type from a field/property
// node. Returns ok=false when the member has no usable name (index signatures,
// call signatures) — those aren't DTO fields.
func fieldDef(member tsjs.Node) (schema.FieldDef, bool) {
	nameNode := member.ChildByFieldName("name")
	if !nameNode.Valid() || nameNode.Type() != "property_identifier" {
		return schema.FieldDef{}, false
	}
	fd := schema.FieldDef{Name: nameNode.Text(), Source: schema.SourceField, Type: "any"}
	if ta := member.ChildByFieldName("type"); ta.Valid() {
		fd.Type = normalizeType(typeText(ta))
	}
	return fd, true
}

// typeName returns a class/interface declaration's name.
func typeName(decl tsjs.Node) string {
	if n := decl.ChildByFieldName("name"); n.Valid() {
		return n.Text()
	}
	return ""
}

// heritage returns the first extended type's simple name from a class/interface
// heritage clause, or "" when the type extends nothing.
func heritage(decl tsjs.Node, clauseType string) string {
	var super string
	decl.Walk(func(n tsjs.Node) bool {
		if super != "" {
			return false
		}
		if n.Type() == clauseType {
			for _, c := range tsjs.NamedChildren(n) {
				if t := simpleName(baseTypeName(c)); t != "" {
					super = t
					return false
				}
			}
		}
		return true
	})
	return super
}

// baseTypeName strips generic arguments from a heritage type reference.
func baseTypeName(n tsjs.Node) string {
	t := n.Text()
	if i := strings.IndexByte(t, '<'); i >= 0 {
		t = t[:i]
	}
	return strings.TrimSpace(t)
}

// typeText returns the declared type of a `type_annotation` node (dropping the
// leading colon), or the node's own text when it is already the type.
func typeText(ta tsjs.Node) string {
	if ta.Type() == "type_annotation" {
		if kids := tsjs.NamedChildren(ta); len(kids) > 0 {
			return kids[0].Text()
		}
		return strings.TrimPrefix(strings.TrimSpace(ta.Text()), ":")
	}
	return ta.Text()
}

// normalizeType reduces a TS type expression to the form the schema walker
// understands: it drops `| null`/`| undefined` union members (a nullable type is
// still its base type here) and trims whitespace/parentheses. Array (`T[]`) and
// generic (`Promise<T>`) forms pass through unchanged for the walker to parse.
func normalizeType(t string) string {
	t = strings.TrimSpace(t)
	t = strings.TrimPrefix(t, "(")
	t = strings.TrimSuffix(t, ")")
	if !strings.Contains(t, "|") {
		return strings.TrimSpace(t)
	}
	var kept []string
	for _, part := range strings.Split(t, "|") {
		p := strings.TrimSpace(part)
		if p == "null" || p == "undefined" || p == "" {
			continue
		}
		kept = append(kept, p)
	}
	if len(kept) == 1 {
		return kept[0]
	}
	return strings.Join(kept, " | ") // a real union — walker treats as uncertain
}
