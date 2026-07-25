package tsjs

// Navigation helpers over the tree-sitter-typescript grammar. Decorators are the
// TS analogue of Java annotations, but structurally they are PRECEDING SIBLINGS
// of the class/method they apply to (children of export_statement / class_body),
// not a modifiers child — so PrecedingDecorators collects them by walking back
// over previous siblings.

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

// PrecedingDecorators returns the decorator nodes that immediately precede n
// among its previous siblings (in source order). Works for a class_declaration
// (decorators under export_statement/program) and a method_definition
// (decorators under class_body).
func PrecedingDecorators(n Node) []Node {
	var out []Node
	for s := n.PrevNamedSibling(); s.Valid() && s.Type() == "decorator"; s = s.PrevNamedSibling() {
		out = append(out, s)
	}
	// collected back-to-front; reverse to source order
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// ClassDecorators returns a class's decorators from BOTH places tree-sitter puts
// them: as CHILDREN of the class_declaration (a non-exported class) and as
// PRECEDING SIBLINGS (an `export class`, where the decorators sit in the
// enclosing export_statement). A class only uses one, so the union is safe.
func ClassDecorators(class Node) []Node {
	var out []Node
	for _, c := range NamedChildren(class) {
		if c.Type() == "decorator" {
			out = append(out, c)
		}
	}
	return append(out, PrecedingDecorators(class)...)
}

// DecoratorName is a decorator's simple name: `@Get(...)` -> "Get",
// `@Injectable` -> "Injectable". Handles both the call form
// (decorator > call_expression > identifier) and the bare form
// (decorator > identifier).
func DecoratorName(dec Node) string {
	for _, c := range NamedChildren(dec) {
		switch c.Type() {
		case "call_expression":
			return simpleName(c.ChildByFieldName("function"))
		case "identifier", "member_expression":
			return simpleName(c)
		}
	}
	return ""
}

// simpleName reduces an identifier or member_expression to its trailing name.
func simpleName(n Node) string {
	switch n.Type() {
	case "identifier", "property_identifier":
		return n.Text()
	case "member_expression":
		return n.ChildByFieldName("property").Text()
	}
	return n.Text()
}

// FindDecorator returns the first decorator named `name` in decs.
func FindDecorator(decs []Node, name string) Node {
	for _, d := range decs {
		if DecoratorName(d) == name {
			return d
		}
	}
	return Node{}
}

// HasDecorator reports whether decs contains a decorator named `name`.
func HasDecorator(decs []Node, name string) bool {
	return FindDecorator(decs, name).Valid()
}

// DecoratorStringArg returns the first string-literal argument of a decorator
// call (unquoted), and whether one was present as a literal. `@Get(':id')` ->
// (":id", true, true); `@Get()` / `@Get(SOME_CONST)` -> ("", literal?, ok?).
func DecoratorStringArg(dec Node) (value string, literal, ok bool) {
	call := ChildByType(dec, "call_expression")
	if !call.Valid() {
		return "", true, false // bare decorator, no args
	}
	args := call.ChildByFieldName("arguments")
	if !args.Valid() {
		return "", true, false
	}
	for _, a := range NamedChildren(args) {
		switch a.Type() {
		case "string":
			return StringValue(a), true, true
		case "comment":
			continue
		default:
			// First argument is a non-string expression (identifier, template).
			return a.Text(), false, true
		}
	}
	return "", true, false
}

// ObjectStringProp returns the string value of a `key:` property in a
// decorator's object argument — e.g. @Controller({ path: 'auth', version: '1' })
// with key "path" -> ("auth", true, true). Handles the object-options form NestJS
// controllers commonly use.
func ObjectStringProp(dec Node, key string) (value string, literal, ok bool) {
	call := ChildByType(dec, "call_expression")
	if !call.Valid() {
		return "", true, false
	}
	args := call.ChildByFieldName("arguments")
	if !args.Valid() {
		return "", true, false
	}
	obj := ChildByType(args, "object")
	if !obj.Valid() {
		return "", true, false
	}
	for _, p := range NamedChildren(obj) {
		if p.Type() != "pair" {
			continue
		}
		if k := ChildByType(p, "property_identifier"); k.Valid() && k.Text() == key {
			val := p.ChildByFieldName("value")
			if val.Type() == "string" {
				return StringValue(val), true, true
			}
			return val.Text(), false, true
		}
	}
	return "", true, false
}

// StringValue returns a `string` node's content without quotes: its
// string_fragment child, or "" for an empty string literal.
func StringValue(str Node) string {
	if f := ChildByType(str, "string_fragment"); f.Valid() {
		return f.Text()
	}
	return ""
}
