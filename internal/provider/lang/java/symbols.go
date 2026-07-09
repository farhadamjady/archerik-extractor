package java

import (
	"sort"
	"strings"
)

// Symbols resolves compile-time constants to their literal values, so the value
// evaluator can turn a reference like OrderTopics.ORDERS into "orders". It
// implements provider.SymbolIndex.
//
// Indexed: `static final` fields with a literal initializer, and enum constants
// whose first constructor argument is a string literal (enum Topic { ORDERS("orders") }).
type Symbols struct {
	qualified map[string]string // "Class.FIELD" -> value
	bare      map[string]string // "FIELD" -> value (dropped when ambiguous)
}

// Constant looks up a reference. A qualified "Class.FIELD" is tried directly;
// otherwise the bare field name is tried (a statically-imported or same-class
// constant), which resolves only when unambiguous across the repo.
func (s *Symbols) Constant(qualified string) (string, bool) {
	if v, ok := s.qualified[qualified]; ok {
		return v, true
	}
	bare := qualified
	if i := strings.LastIndex(qualified, "."); i >= 0 {
		bare = qualified[i+1:]
	}
	v, ok := s.bare[bare]
	return v, ok
}

// IndexSymbols scans the given Java files for constants. Files are processed in
// sorted path order so ambiguity resolution is deterministic.
func IndexSymbols(files []*File) *Symbols {
	s := &Symbols{qualified: map[string]string{}, bare: map[string]string{}}
	conflict := map[string]bool{}

	sorted := append([]*File(nil), files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].path < sorted[j].path })

	for _, f := range sorted {
		f.Root().Walk(func(n Node) bool {
			switch n.Type() {
			case "class_declaration", "interface_declaration", "enum_declaration":
				indexDecl(n, s, conflict)
			}
			return true // descend into nested types too
		})
	}
	for k := range conflict {
		delete(s.bare, k)
	}
	return s
}

func indexDecl(decl Node, s *Symbols, conflict map[string]bool) {
	cls := decl.ChildByFieldName("name").Text()
	body := decl.ChildByFieldName("body")
	for i := 0; i < body.NamedChildCount(); i++ {
		child := body.NamedChild(i)
		switch child.Type() {
		case "field_declaration":
			if name, val, ok := constantField(child); ok {
				addConstant(s, conflict, cls, name, val)
			}
		case "enum_constant":
			if name, val, ok := enumConstant(child); ok {
				addConstant(s, conflict, cls, name, val)
			}
		}
	}
}

func addConstant(s *Symbols, conflict map[string]bool, cls, name, val string) {
	s.qualified[cls+"."+name] = val
	if prev, ok := s.bare[name]; ok && prev != val {
		conflict[name] = true
		return
	}
	s.bare[name] = val
}

// constantField returns a static field's name and literal value.
func constantField(fd Node) (name, value string, ok bool) {
	mods := directChild(fd, "modifiers")
	if !mods.Valid() || !strings.Contains(mods.Text(), "static") {
		return "", "", false
	}
	decl := directChild(fd, "variable_declarator")
	if !decl.Valid() {
		return "", "", false
	}
	name = decl.ChildByFieldName("name").Text()
	if v, ok := literalValue(decl.ChildByFieldName("value")); ok && name != "" {
		return name, v, true
	}
	return "", "", false
}

// enumConstant returns an enum constant's name and its first string-literal arg.
func enumConstant(ec Node) (name, value string, ok bool) {
	name = ec.ChildByFieldName("name").Text()
	args := ec.ChildByFieldName("arguments")
	if !args.Valid() || name == "" {
		return "", "", false
	}
	for i := 0; i < args.NamedChildCount(); i++ {
		if v, ok := literalValue(args.NamedChild(i)); ok {
			return name, v, true
		}
	}
	return "", "", false
}

// literalValue extracts a string or numeric literal's value.
func literalValue(n Node) (string, bool) {
	switch n.Type() {
	case "string_literal":
		return stripQuotes(n.Text()), true
	case "decimal_integer_literal", "decimal_floating_point_literal",
		"hex_integer_literal", "octal_integer_literal":
		return n.Text(), true
	default:
		return "", false
	}
}

func directChild(n Node, typ string) Node {
	for i := 0; i < n.NamedChildCount(); i++ {
		if c := n.NamedChild(i); c.Type() == typ {
			return c
		}
	}
	return Node{}
}

func stripQuotes(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}
