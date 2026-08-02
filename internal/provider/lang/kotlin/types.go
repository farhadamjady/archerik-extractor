package kotlin

import (
	"sort"
	"strings"

	"github.com/farhadamjady/service-discovery/internal/schema"
)

// Types is the cross-file Kotlin DTO index. It implements schema.TypeSource, so
// the language-neutral schema walker resolves Kotlin request/response and
// topic-payload structure through it exactly as it does for Java. It is the
// Kotlin sibling of lang/java.Types, but types-only: Kotlin has no value-flow
// evaluator yet (client/kafka detectors land later), so there are no creation/
// call-site maps here.
type Types struct {
	byName map[string]*schema.TypeDef
}

// Lookup resolves a simple or qualified type name to its definition; a qualified
// name falls back to its last segment (mirrors lang/java.Types.Lookup).
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

// IndexTypes builds the DTO index. `files` are the scanned service's own Kotlin
// sources; `shared` are sibling-module sources that contribute TYPE DEFINITIONS
// only. Shared types are indexed first so a service-owned type wins a name
// collision.
func IndexTypes(files, shared []*File) *Types {
	t := &Types{byName: map[string]*schema.TypeDef{}}
	for _, f := range sortedByPath(shared) {
		indexFile(t, f)
	}
	for _, f := range sortedByPath(files) {
		indexFile(t, f)
	}
	return t
}

func sortedByPath(files []*File) []*File {
	sorted := append([]*File(nil), files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].path < sorted[j].path })
	return sorted
}

// indexFile walks one file, registering every declared class/interface/enum/
// object (including nested ones) into the index by simple name.
func indexFile(t *Types, f *File) {
	pkg := packageName(f)
	imports := importMap(f)
	f.Root().Walk(func(n Node) bool {
		switch n.Type() {
		case "class_declaration", "object_declaration":
			if td := buildTypeDef(n, pkg, imports); td != nil {
				t.byName[td.Name] = td
			}
		}
		return true // descend into nested types too
	})
}

// buildTypeDef turns a class/interface/enum/object declaration into a TypeDef:
// its name, kind, single supertype, and serialized properties (primary-
// constructor `val`/`var` params + class-body property declarations).
func buildTypeDef(n Node, pkg string, imports map[string]string) *schema.TypeDef {
	nameNode := ChildByType(n, "type_identifier")
	if !nameNode.Valid() {
		return nil
	}
	td := &schema.TypeDef{
		Name:        nameNode.Text(),
		Package:     pkg,
		Kind:        kindOfDecl(n, nameNode),
		Imports:     imports,
		Annotations: schemaAnnotations(Modifiers(n)),
		Super:       superType(n),
	}
	td.Fields = append(td.Fields, primaryCtorFields(n)...)
	td.Fields = append(td.Fields, bodyFields(n)...)
	return td
}

// kindOfDecl classifies the declaration. An `enum_class_body` marks an enum; the
// keyword region before the type name distinguishes `interface`/`data class`
// from a plain class. Kind is not consulted by the walker today, but is set
// faithfully for future readers (enum constants, record-style requiredness).
func kindOfDecl(n, nameNode Node) schema.Kind {
	if ChildByType(n, "enum_class_body").Valid() {
		return schema.KindEnum
	}
	rel := int(nameNode.StartByte() - n.StartByte())
	head := n.Text()
	if rel > 0 && rel <= len(head) {
		head = head[:rel]
	}
	switch {
	case strings.Contains(head, "interface"):
		return schema.KindInterface
	case strings.Contains(head, "data "):
		return schema.KindRecord
	default:
		return schema.KindClass
	}
}

// superType returns the first delegation specifier's type name (the superclass
// or first implemented interface), "" when the type extends nothing. Kotlin
// allows one class + several interfaces; like lang/java we keep a single Super
// and walk it up the chain — extra interfaces don't add serialized fields.
func superType(n Node) string {
	for _, c := range NamedChildren(n) {
		if c.Type() != "delegation_specifier" {
			continue
		}
		var name string
		c.Walk(func(x Node) bool {
			if name != "" {
				return false
			}
			if x.Type() == "type_identifier" {
				name = x.Text()
				return false
			}
			return true
		})
		if name != "" {
			return name
		}
	}
	return ""
}

// primaryCtorFields extracts the `val`/`var` properties declared in the primary
// constructor. A parameter WITHOUT a `binding_pattern_kind` (val/var) is an
// ordinary constructor argument, not a property, so it is skipped — it is not
// part of the serialized shape.
func primaryCtorFields(n Node) []schema.FieldDef {
	pc := ChildByType(n, "primary_constructor")
	if !pc.Valid() {
		return nil
	}
	var out []schema.FieldDef
	for _, p := range NamedChildren(pc) {
		if p.Type() != "class_parameter" || !ChildByType(p, "binding_pattern_kind").Valid() {
			continue
		}
		name := ChildByType(p, "simple_identifier")
		if !name.Valid() {
			continue
		}
		typ, nullable := DeclaredType(p)
		out = append(out, field(name.Text(), typ, nullable, schema.SourceCtorParam, Modifiers(p)))
	}
	return out
}

// bodyFields extracts the class-body `val`/`var` property declarations. Plain
// stored and computed properties alike are part of the serialized shape (a
// Kotlin computed `val` is serialized via its getter); @JsonIgnore drops are
// handled downstream in the walker's field union.
func bodyFields(n Node) []schema.FieldDef {
	body := ChildByType(n, "class_body")
	if !body.Valid() {
		return nil
	}
	var out []schema.FieldDef
	for _, m := range NamedChildren(body) {
		if m.Type() != "property_declaration" {
			continue
		}
		vd := ChildByType(m, "variable_declaration")
		name := ChildByType(vd, "simple_identifier")
		if !name.Valid() {
			continue
		}
		typ, nullable := DeclaredType(vd)
		out = append(out, field(name.Text(), typ, nullable, schema.SourceField, Modifiers(m)))
	}
	return out
}

// field assembles a FieldDef. A Kotlin nullable type (`T?`) is encoded as a
// synthetic @Nullable annotation so the walker's existing nullability +
// requiredness rules treat it as a nullable, optional field — no walker change
// needed. Explicit annotations are preserved alongside it.
func field(name, typ string, nullable bool, src schema.FieldSource, mods Node) schema.FieldDef {
	anns := schemaAnnotations(mods)
	if nullable {
		anns = append(anns, schema.Annotation{Name: "Nullable"})
	}
	return schema.FieldDef{Name: name, Type: typ, Source: src, Annotations: anns}
}

// DeclaredType reads the declared type node under a class_parameter,
// variable_declaration, or function parameter and returns its type text with the
// nullability marker stripped, plus whether the type was nullable (`T?`). A
// `List<Role>?` yields ("List<Role>", true). Missing (inferred) types yield
// ("", false) — the walker resolves those as an uncertain object.
func DeclaredType(parent Node) (typ string, nullable bool) {
	for _, c := range NamedChildren(parent) {
		switch c.Type() {
		case "nullable_type":
			if inner := ChildByType(c, "user_type"); inner.Valid() {
				return inner.Text(), true
			}
			return strings.TrimSuffix(strings.TrimSpace(c.Text()), "?"), true
		case "user_type":
			return c.Text(), false
		}
	}
	return "", false
}

// schemaAnnotations converts the annotations under a modifiers node into
// schema.Annotations (name, first positional string arg, named string args) —
// enough for the walker's Jackson/validation reads. A use-site target
// (@field:/@get:) is already stripped by AnnotationName.
func schemaAnnotations(mods Node) []schema.Annotation {
	if !mods.Valid() {
		return nil
	}
	var out []schema.Annotation
	for _, a := range AnnotationsOf(mods) {
		out = append(out, schema.Annotation{
			Name:  AnnotationName(a),
			Arg:   firstPositionalArg(a),
			Named: namedAnnotationArgs(a),
		})
	}
	return out
}

// firstPositionalArg returns the first positional argument's value (a bare
// string like @JsonProperty("full_name")), "" when the first argument is named
// or absent.
func firstPositionalArg(a Node) string {
	for _, va := range NamedChildren(valueArguments(a)) {
		if va.Type() != "value_argument" {
			continue
		}
		if ChildByType(va, "simple_identifier").Valid() {
			return "" // first argument is named, not positional
		}
		return argValue(va)
	}
	return ""
}

// namedAnnotationArgs collects the `key = value` arguments (e.g.
// @JsonProperty(required = true)) as a string map.
func namedAnnotationArgs(a Node) map[string]string {
	var m map[string]string
	for _, va := range NamedChildren(valueArguments(a)) {
		if va.Type() != "value_argument" {
			continue
		}
		key := ChildByType(va, "simple_identifier")
		if !key.Valid() {
			continue
		}
		if m == nil {
			m = map[string]string{}
		}
		m[key.Text()] = argValue(va)
	}
	return m
}

// argValue reads a value_argument's value: the unquoted content of a string
// literal, or the raw text of any other expression (a boolean/constant).
func argValue(va Node) string {
	if sl := ChildByType(va, "string_literal"); sl.Valid() {
		return stringContent(sl)
	}
	kids := NamedChildren(va)
	if len(kids) > 0 {
		return kids[len(kids)-1].Text()
	}
	return ""
}

// packageName reads the file's package_header identifier (com.acme.dto), "" when
// absent (Kotlin allows a package-less file).
func packageName(f *File) string {
	if ph := ChildByType(f.Root(), "package_header"); ph.Valid() {
		if id := ChildByType(ph, "identifier"); id.Valid() {
			return id.Text()
		}
	}
	return ""
}

// importMap maps each import's simple name to its fully-qualified name.
func importMap(f *File) map[string]string {
	m := map[string]string{}
	list := ChildByType(f.Root(), "import_list")
	if !list.Valid() {
		return m
	}
	for _, ih := range NamedChildren(list) {
		if ih.Type() != "import_header" {
			continue
		}
		if id := ChildByType(ih, "identifier"); id.Valid() {
			fqn := id.Text()
			if i := strings.LastIndex(fqn, "."); i >= 0 {
				m[fqn[i+1:]] = fqn
			}
		}
	}
	return m
}
