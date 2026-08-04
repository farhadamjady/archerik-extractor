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
	defs    map[string]*schema.TypeDef
	aliases map[string]tsAlias           // local `type X<..> = …` declarations (#61a)
	methods map[string]map[string]string // class -> method -> return type text (#62)
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
	idx := &tsTypeIndex{defs: map[string]*schema.TypeDef{}, aliases: map[string]tsAlias{}, methods: map[string]map[string]string{}}
	for _, f := range files {
		f.Root().Walk(func(n tsjs.Node) bool {
			switch n.Type() {
			case "class_declaration":
				if td := classDef(n, schema.KindClass); td != nil {
					idx.defs[td.Name] = td
					if mr := classMethodReturns(n); len(mr) > 0 {
						idx.methods[td.Name] = mr
					}
				}
			case "interface_declaration":
				if td := interfaceDef(n); td != nil {
					idx.defs[td.Name] = td
				}
			case "type_alias_declaration":
				if name := n.ChildByFieldName("name"); name.Valid() {
					idx.aliases[name.Text()] = tsAlias{
						params: typeParamNames(n.ChildByFieldName("type_parameters")),
						body:   n.ChildByFieldName("value").Text(),
					}
				}
			}
			return true
		})
	}
	return idx
}

// typeParamNames extracts the declared type-parameter names from a
// `type_parameters` node (`<T>` -> ["T"], `<T extends X, U>` -> ["T","U"]).
func typeParamNames(tp tsjs.Node) []string {
	if !tp.Valid() {
		return nil
	}
	txt := strings.TrimSpace(tp.Text())
	txt = strings.TrimPrefix(txt, "<")
	txt = strings.TrimSuffix(txt, ">")
	var out []string
	for _, part := range splitTopLevel(txt, ',') {
		if f := strings.Fields(strings.TrimSpace(part)); len(f) > 0 {
			out = append(out, f[0]) // "T extends X" -> "T"
		}
	}
	return out
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
// understands. It drops `| null`/`| undefined` union members (a nullable type is
// still its base type here) and trims whitespace/parentheses. Unlike a naive
// split-on-`|`, it is DEPTH-AWARE, so a union nested inside a generic
// (`Promise<User | null>` -> `Promise<User>`) is simplified correctly instead of
// mangled (#61). Array (`T[]`) and generic (`Promise<T>`) forms are preserved for
// the walker to parse. This wrapper carries no alias map — see normalizeTypeAlias.
func normalizeType(t string) string {
	s, _ := normalizeCore(t, nil, 0)
	return s
}

// normalizeTypeAlias is normalizeType plus local type-alias expansion (#61a):
// `NullableType<User>` (from `type NullableType<T> = T | null`) expands to `User`
// and reports nullable=true. Used on the response/request path where aliases are
// available. Returns the normalized type and whether a null/undefined member was
// stripped anywhere (so the caller can set Schema.Nullable).
func normalizeTypeAlias(t string, aliases map[string]tsAlias) (string, bool) {
	return normalizeCore(t, aliases, 0)
}

// normalizeCore is the shared recursive normalizer: union simplification (drops
// null/undefined, depth-aware), array/generic descent, and — when aliases is
// non-nil — expansion of local `type X<..> = …` aliases. Returns the simplified
// type text and whether a nullable (null/undefined) member was removed. depth
// guards against cyclic aliases.
func normalizeCore(t string, aliases map[string]tsAlias, depth int) (string, bool) {
	t = strings.TrimSpace(t)
	for strings.HasPrefix(t, "(") && strings.HasSuffix(t, ")") {
		t = strings.TrimSpace(t[1 : len(t)-1])
	}
	if depth > 16 {
		return t, false
	}
	// Union: drop null/undefined members at THIS nesting level (depth-aware split).
	if parts := splitTopLevel(t, '|'); len(parts) > 1 {
		nullable := false
		var kept []string
		for _, part := range parts {
			p := strings.TrimSpace(part)
			if p == "" || p == "null" || p == "undefined" {
				nullable = true
				continue
			}
			np, n := normalizeCore(p, aliases, depth+1)
			nullable = nullable || n
			kept = append(kept, np)
		}
		switch len(kept) {
		case 0:
			return t, nullable
		case 1:
			return kept[0], nullable
		default:
			return strings.Join(kept, " | "), nullable // a real union — walker treats as uncertain
		}
	}
	// Array element: T[] -> normalize(T) + "[]".
	if strings.HasSuffix(t, "[]") {
		inner, n := normalizeCore(strings.TrimSuffix(t, "[]"), aliases, depth+1)
		return inner + "[]", n
	}
	// Generic Name<args>: expand a matching alias, else recurse into the args.
	if name, args, ok := splitGeneric(t); ok {
		if a, isAlias := aliases[name]; isAlias {
			return normalizeCore(a.expand(args), aliases, depth+1)
		}
		nullable := false
		var na []string
		for _, arg := range splitTopLevel(args, ',') {
			ns, n := normalizeCore(strings.TrimSpace(arg), aliases, depth+1)
			nullable = nullable || n
			na = append(na, ns)
		}
		return name + "<" + strings.Join(na, ", ") + ">", nullable
	}
	// Bare alias with no type parameters: `type Foo = Bar`.
	if a, ok := aliases[t]; ok && len(a.params) == 0 {
		return normalizeCore(a.body, aliases, depth+1)
	}
	return t, false
}

// tsAlias is a local `type Name<params...> = body` declaration.
type tsAlias struct {
	params []string // type-parameter names, e.g. ["T"]
	body   string   // right-hand side text, e.g. "T | null"
}

// expand substitutes the alias's type parameters with the supplied argument list
// (`T | null` with T=User -> `User | null`), matching whole-identifier tokens.
func (a tsAlias) expand(argsStr string) string {
	args := splitTopLevel(argsStr, ',')
	body := a.body
	for i, p := range a.params {
		if i >= len(args) {
			break
		}
		body = replaceIdent(body, p, strings.TrimSpace(args[i]))
	}
	return body
}

// splitTopLevel splits s on sep, ignoring separators nested inside <>, (), or [].
func splitTopLevel(s string, sep byte) []string {
	var parts []string
	depth, last := 0, 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '<' || c == '(' || c == '[':
			depth++
		case c == '>' || c == ')' || c == ']':
			if depth > 0 {
				depth--
			}
		case c == sep && depth == 0:
			parts = append(parts, s[last:i])
			last = i + 1
		}
	}
	return append(parts, s[last:])
}

// splitGeneric splits `Name<args>` into ("Name", "args", true); returns ok=false
// when t is not a generic application.
func splitGeneric(t string) (name, args string, ok bool) {
	i := strings.IndexByte(t, '<')
	if i <= 0 || !strings.HasSuffix(t, ">") {
		return "", "", false
	}
	return strings.TrimSpace(t[:i]), t[i+1 : len(t)-1], true
}

// replaceIdent replaces whole-identifier occurrences of param in body with arg
// (so substituting T does not touch the T inside "Truthy" or "Set").
func replaceIdent(body, param, arg string) string {
	var b strings.Builder
	for i := 0; i < len(body); {
		if identAt(body, i, param) {
			b.WriteString(arg)
			i += len(param)
		} else {
			b.WriteByte(body[i])
			i++
		}
	}
	return b.String()
}

func identAt(s string, i int, w string) bool {
	if i+len(w) > len(s) || s[i:i+len(w)] != w {
		return false
	}
	if i > 0 && isIdentByte(s[i-1]) {
		return false
	}
	if j := i + len(w); j < len(s) && isIdentByte(s[j]) {
		return false
	}
	return true
}

func isIdentByte(c byte) bool {
	return c == '_' || c == '$' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}
