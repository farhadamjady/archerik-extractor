package nethttp

import (
	"strings"

	"github.com/farhadamjady/service-discovery/internal/provider/lang/golang"
	"github.com/farhadamjady/service-discovery/internal/schema"
)

// goTypeIndex is a schema.TypeSource over the repo's Go struct types, so the
// schema walker can resolve a handler's request/response body structure. Fields
// use their json-tag wire name when present (dropping `json:"-"` and unexported
// fields, which never serialize). Types are keyed by simple name; a package
// qualifier (entities.InventoryData) collapses to its last segment.
type goTypeIndex struct {
	defs map[string]*schema.TypeDef
}

func (t *goTypeIndex) Lookup(name string) (*schema.TypeDef, bool) {
	td, ok := t.defs[goSimpleName(name)]
	return td, ok
}

// goSimpleName strips a leading `*`/`[]`, a package qualifier, and whitespace.
func goSimpleName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimPrefix(name, "*")
	name = strings.TrimPrefix(name, "[]")
	name = strings.TrimSpace(name)
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		name = name[i+1:]
	}
	return name
}

// buildTypeIndex indexes every `type X struct {...}` across the parsed Go files.
func buildTypeIndex(files []*golang.File) *goTypeIndex {
	idx := &goTypeIndex{defs: map[string]*schema.TypeDef{}}
	for _, f := range files {
		f.Root().Walk(func(n golang.Node) bool {
			if n.Type() != "type_spec" {
				return true
			}
			st := n.ChildByFieldName("type")
			nameNode := n.ChildByFieldName("name")
			if !st.Valid() || st.Type() != "struct_type" || !nameNode.Valid() {
				return true
			}
			idx.defs[nameNode.Text()] = structDef(nameNode.Text(), st)
			return true
		})
	}
	return idx
}

// structDef builds a TypeDef from a struct_type's field declarations.
func structDef(name string, st golang.Node) *schema.TypeDef {
	td := &schema.TypeDef{Name: name, Kind: schema.KindClass}
	list := golang.ChildByType(st, "field_declaration_list")
	if !list.Valid() {
		return td
	}
	for _, fd := range golang.NamedChildren(list) {
		if fd.Type() != "field_declaration" {
			continue
		}
		ty := fd.ChildByFieldName("type")
		if !ty.Valid() {
			continue // embedded field — not modeled this round
		}
		wire, skip := jsonWire(fd)
		for _, nm := range fieldNames(fd) {
			if !exported(nm) {
				continue // unexported fields never serialize
			}
			w := wire
			if w == "" {
				w = nm
			}
			if skip {
				continue
			}
			td.Fields = append(td.Fields, schema.FieldDef{Name: w, Type: normalizeGoType(ty.Text()), Source: schema.SourceField})
		}
	}
	return td
}

// fieldNames returns the identifier(s) declared by a field_declaration
// (`Name, Alias string` declares two).
func fieldNames(fd golang.Node) []string {
	var names []string
	for _, c := range golang.NamedChildren(fd) {
		if c.Type() == "field_identifier" {
			names = append(names, c.Text())
		}
	}
	return names
}

// jsonWire reads a field's `json:"..."` tag: the wire name (before the first
// comma) and whether the field is explicitly excluded (`json:"-"`).
func jsonWire(fd golang.Node) (wire string, skip bool) {
	tag := golang.ChildByType(fd, "tag")
	if !tag.Valid() {
		tag = golang.ChildByType(fd, "raw_string_literal")
	}
	if !tag.Valid() {
		return "", false
	}
	t := tag.Text()
	i := strings.Index(t, `json:"`)
	if i < 0 {
		return "", false
	}
	rest := t[i+len(`json:"`):]
	j := strings.IndexByte(rest, '"')
	if j < 0 {
		return "", false
	}
	val := rest[:j]
	if k := strings.IndexByte(val, ','); k >= 0 {
		val = val[:k]
	}
	if val == "-" {
		return "", true
	}
	return val, false
}

// exported reports whether a Go identifier is exported (capitalized).
func exported(name string) bool {
	return name != "" && name[0] >= 'A' && name[0] <= 'Z'
}

// normalizeGoType rewrites Go type syntax into the form the schema walker parses:
// a leading pointer `*T` is dropped, a slice `[]T` becomes `T[]`, and a
// `map[K]V` becomes `Map<K, V>`. Other types pass through; the package qualifier
// is left for the walker's simpleName to strip.
func normalizeGoType(t string) string {
	t = strings.TrimSpace(t)
	t = strings.TrimSpace(strings.TrimPrefix(t, "*"))
	if strings.HasPrefix(t, "[]") {
		return normalizeGoType(t[2:]) + "[]"
	}
	if strings.HasPrefix(t, "map[") {
		rest := t[4:]
		depth, end := 1, -1
		for i := 0; i < len(rest); i++ {
			switch rest[i] {
			case '[':
				depth++
			case ']':
				if depth--; depth == 0 {
					end = i
				}
			}
			if end >= 0 {
				break
			}
		}
		if end >= 0 {
			k := normalizeGoType(rest[:end])
			v := normalizeGoType(rest[end+1:])
			return "Map<" + k + ", " + v + ">"
		}
	}
	return t
}
