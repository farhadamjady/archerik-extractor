package csharp

import "strings"

// Attribute-navigation helpers over the tree-sitter-c-sharp grammar, the C#
// analogue of the Java annotation helpers. C# attributes live in `attribute_list`
// children of the declaration (a class or method may have several lists), each
// holding one or more `attribute` nodes: `attribute > identifier [+
// attribute_argument_list > attribute_argument > string_literal >
// string_literal_content]`.

// ChildByType returns the first named child of n with the given type.
func ChildByType(n Node, typ string) Node {
	for i := 0; i < n.NamedChildCount(); i++ {
		if c := n.NamedChild(i); c.Type() == typ {
			return c
		}
	}
	return Node{}
}

// NamedChildren materializes n's named children in order.
func NamedChildren(n Node) []Node {
	out := make([]Node, 0, n.NamedChildCount())
	for i := 0; i < n.NamedChildCount(); i++ {
		out = append(out, n.NamedChild(i))
	}
	return out
}

// AttributesOf returns every attribute across a declaration's attribute_list
// children.
func AttributesOf(decl Node) []Node {
	var out []Node
	for _, c := range NamedChildren(decl) {
		if c.Type() == "attribute_list" {
			for _, a := range NamedChildren(c) {
				if a.Type() == "attribute" {
					out = append(out, a)
				}
			}
		}
	}
	return out
}

// AttributeName is an attribute's simple name with the optional C# "Attribute"
// suffix stripped and any namespace qualifier removed: [HttpGet] / [HttpGetAttribute]
// / [Microsoft.AspNetCore.Mvc.HttpGet] all -> "HttpGet".
func AttributeName(attr Node) string {
	name := ""
	if n := attr.ChildByFieldName("name"); n.Valid() {
		name = n.Text()
	} else if id := ChildByType(attr, "identifier"); id.Valid() {
		name = id.Text()
	} else if q := ChildByType(attr, "qualified_name"); q.Valid() {
		name = q.Text()
	}
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	return strings.TrimSuffix(name, "Attribute")
}

// FindAttribute returns the first attribute named `name` on a declaration.
func FindAttribute(decl Node, name string) Node {
	for _, a := range AttributesOf(decl) {
		if AttributeName(a) == name {
			return a
		}
	}
	return Node{}
}

// HasAttribute reports whether a declaration carries the named attribute.
func HasAttribute(decl Node, name string) bool { return FindAttribute(decl, name).Valid() }

// AttributeStringArg returns the first positional string argument of an attribute
// (unquoted), and whether one was present as a literal. `[HttpGet("{id}")]` ->
// ("{id}", true, true); `[HttpGet]` -> ("", true, false); a non-literal arg
// (nameof/const) -> (text, false, true).
func AttributeStringArg(attr Node) (value string, literal, ok bool) {
	args := ChildByType(attr, "attribute_argument_list")
	if !args.Valid() {
		return "", true, false
	}
	for _, a := range NamedChildren(args) {
		if a.Type() != "attribute_argument" {
			continue
		}
		kids := NamedChildren(a)
		if len(kids) == 0 {
			continue
		}
		child := kids[0]
		// A named argument is `Name = "x"` -> an assignment_expression; it is not
		// the route path, so skip it (this is what makes [HttpGet(Name="...")] have
		// no path). A positional route path is a direct string_literal.
		switch child.Type() {
		case "assignment_expression":
			continue
		case "string_literal":
			return StringContent(child), true, true
		default:
			return child.Text(), false, true // positional non-literal (nameof/const)
		}
	}
	return "", true, false
}

// StringContent returns a string_literal's content without quotes: its
// string_literal_content child, or the trimmed text.
func StringContent(sl Node) string {
	if c := ChildByType(sl, "string_literal_content"); c.Valid() {
		return c.Text()
	}
	return strings.Trim(sl.Text(), `"`)
}

// Name returns the simple name of a class/method declaration (the "name" field).
func Name(decl Node) string {
	if n := decl.ChildByFieldName("name"); n.Valid() {
		return n.Text()
	}
	return ""
}
