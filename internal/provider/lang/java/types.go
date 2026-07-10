package java

import (
	"sort"
	"strings"

	"github.com/farhadamjady/service-discovery/internal/schema"
)

// Types is the cross-file DTO index. It implements schema.TypeSource.
type Types struct {
	byName map[string]*schema.TypeDef
}

// Lookup resolves a simple or qualified type name to its definition; a qualified
// name falls back to its last segment.
func (t *Types) Lookup(name string) (*schema.TypeDef, bool) {
	if d, ok := t.byName[name]; ok {
		return d, true
	}
	if i := strings.LastIndex(name, "."); i >= 0 {
		d, ok := t.byName[name[i+1:]]
		return d, ok
	}
	return nil, false
}

// IndexTypes builds the DTO index from the given files (sorted for determinism).
func IndexTypes(files []*File) *Types {
	t := &Types{byName: map[string]*schema.TypeDef{}}
	sorted := append([]*File(nil), files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].path < sorted[j].path })

	for _, f := range sorted {
		pkg := packageName(f)
		imports := importMap(f)
		f.Root().Walk(func(n Node) bool {
			switch n.Type() {
			case "class_declaration", "record_declaration", "interface_declaration", "enum_declaration",
				"annotation_type_declaration":
				td := buildTypeDef(n, pkg, imports)
				t.byName[td.Name] = td
			}
			return true // index nested types too
		})
	}
	return t
}

func buildTypeDef(n Node, pkg string, imports map[string]string) *schema.TypeDef {
	td := &schema.TypeDef{
		Name:        n.ChildByFieldName("name").Text(),
		Package:     pkg,
		Kind:        kindOf(n.Type()),
		Imports:     imports,
		Annotations: annotationList(directChild(n, "modifiers")),
	}
	if sc := n.ChildByFieldName("superclass"); sc.Valid() {
		td.Super = simpleTypeName(sc.NamedChild(0).Text())
	}
	if n.Type() == "record_declaration" {
		td.Fields = append(td.Fields, recordComponents(n)...)
	}
	td.Fields = append(td.Fields, bodyFields(n.ChildByFieldName("body"))...)
	return td
}

func kindOf(nodeType string) schema.Kind {
	switch nodeType {
	case "record_declaration":
		return schema.KindRecord
	case "interface_declaration":
		return schema.KindInterface
	case "enum_declaration":
		return schema.KindEnum
	case "annotation_type_declaration":
		return schema.KindAnnotation
	default:
		return schema.KindClass
	}
}

// recordComponents extracts a record's header components as fields.
func recordComponents(rec Node) []schema.FieldDef {
	var out []schema.FieldDef
	params := rec.ChildByFieldName("parameters")
	for _, p := range namedChildrenOf(params) {
		if p.Type() == "formal_parameter" {
			out = append(out, paramField(p, schema.SourceRecordComponent))
		}
	}
	return out
}

// bodyFields collects declared instance fields, getters, and constructor params
// from a type body.
func bodyFields(body Node) []schema.FieldDef {
	if !body.Valid() {
		return nil
	}
	var out []schema.FieldDef
	for _, m := range namedChildrenOf(body) {
		switch m.Type() {
		case "field_declaration":
			if fs := instanceFields(m); fs != nil {
				out = append(out, fs...)
			}
		case "method_declaration":
			if f, ok := getterField(m); ok {
				out = append(out, f)
			}
		case "constructor_declaration":
			params := directChild(m, "formal_parameters")
			for _, p := range namedChildrenOf(params) {
				if p.Type() == "formal_parameter" {
					out = append(out, paramField(p, schema.SourceCtorParam))
				}
			}
		}
	}
	return out
}

// instanceFields returns a field_declaration's declarators, skipping static and
// transient fields (never part of the serialized shape).
func instanceFields(fd Node) []schema.FieldDef {
	mods := directChild(fd, "modifiers")
	if mods.Valid() {
		if txt := mods.Text(); strings.Contains(txt, "static") || strings.Contains(txt, "transient") {
			return nil
		}
	}
	typ := fd.ChildByFieldName("type").Text()
	anns := annotationList(mods)
	var out []schema.FieldDef
	for _, d := range namedChildrenOf(fd) {
		if d.Type() == "variable_declarator" {
			out = append(out, schema.FieldDef{
				Name:        d.ChildByFieldName("name").Text(),
				Type:        typ,
				Source:      schema.SourceField,
				Annotations: anns,
			})
		}
	}
	return out
}

// getterField turns a no-arg getX()/isX() method into a field candidate.
func getterField(m Node) (schema.FieldDef, bool) {
	name := m.ChildByFieldName("name").Text()
	params := directChild(m, "formal_parameters")
	if params.Valid() && params.NamedChildCount() > 0 {
		return schema.FieldDef{}, false // getters take no arguments
	}
	prop := getterProperty(name)
	if prop == "" {
		return schema.FieldDef{}, false
	}
	ret := m.ChildByFieldName("type")
	if !ret.Valid() || ret.Text() == "void" {
		return schema.FieldDef{}, false
	}
	return schema.FieldDef{
		Name:        prop,
		Type:        ret.Text(),
		Source:      schema.SourceGetter,
		Annotations: annotationList(directChild(m, "modifiers")),
	}, true
}

// getterProperty derives the property name: getName -> name, isActive -> active.
func getterProperty(method string) string {
	switch {
	case strings.HasPrefix(method, "get") && len(method) > 3:
		return lowerFirst(method[3:])
	case strings.HasPrefix(method, "is") && len(method) > 2:
		return lowerFirst(method[2:])
	}
	return ""
}

func paramField(p Node, src schema.FieldSource) schema.FieldDef {
	return schema.FieldDef{
		Name:        p.ChildByFieldName("name").Text(),
		Type:        p.ChildByFieldName("type").Text(),
		Source:      src,
		Annotations: annotationList(directChild(p, "modifiers")),
	}
}

// annotationList captures the annotations in a modifiers node.
func annotationList(mods Node) []schema.Annotation {
	if !mods.Valid() {
		return nil
	}
	var out []schema.Annotation
	for _, a := range namedChildrenOf(mods) {
		if a.Type() != "annotation" && a.Type() != "marker_annotation" {
			continue
		}
		out = append(out, schema.Annotation{
			Name:  a.ChildByFieldName("name").Text(),
			Arg:   firstStringArg(a),
			Named: namedArgs(a),
		})
	}
	return out
}

func firstStringArg(a Node) string {
	args := a.ChildByFieldName("arguments")
	for _, c := range namedChildrenOf(args) {
		if c.Type() == "string_literal" {
			return stripQuotes(c.Text())
		}
	}
	return ""
}

func namedArgs(a Node) map[string]string {
	args := a.ChildByFieldName("arguments")
	var m map[string]string
	for _, c := range namedChildrenOf(args) {
		if c.Type() == "element_value_pair" {
			if m == nil {
				m = map[string]string{}
			}
			m[c.ChildByFieldName("key").Text()] = stripQuotes(c.ChildByFieldName("value").Text())
		}
	}
	return m
}

// packageName reads the package_declaration's scoped identifier (com.acme).
func packageName(f *File) string {
	for _, n := range namedChildrenOf(f.Root()) {
		if n.Type() == "package_declaration" {
			return n.NamedChild(0).Text()
		}
	}
	return ""
}

// importMap maps each import's simple name to its fully-qualified name.
func importMap(f *File) map[string]string {
	m := map[string]string{}
	for _, n := range namedChildrenOf(f.Root()) {
		if n.Type() != "import_declaration" {
			continue
		}
		fqn := n.NamedChild(0).Text()
		if i := strings.LastIndex(fqn, "."); i >= 0 {
			m[fqn[i+1:]] = fqn
		}
	}
	return m
}

// simpleTypeName strips a package qualifier and type arguments: com.x.Base<T> -> Base.
func simpleTypeName(s string) string {
	if i := strings.IndexByte(s, '<'); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndex(s, "."); i >= 0 {
		s = s[i+1:]
	}
	return strings.TrimSpace(s)
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}
