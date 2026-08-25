package php

import "strings"

// NamedChildren materializes n's named children in order.
func NamedChildren(n Node) []Node {
	out := make([]Node, 0, n.NamedChildCount())
	for i := 0; i < n.NamedChildCount(); i++ {
		out = append(out, n.NamedChild(i))
	}
	return out
}

// ChildByType returns the first named child of the given node type, or an
// invalid Node when none is present.
func ChildByType(n Node, typ string) Node {
	for i := 0; i < n.NamedChildCount(); i++ {
		if c := n.NamedChild(i); c.Type() == typ {
			return c
		}
	}
	return Node{}
}

// IsCall reports whether n is one of PHP's call forms: `f(...)`, `X::m(...)`,
// `$o->m(...)`, `$o?->m(...)`.
func IsCall(n Node) bool {
	switch n.Type() {
	case "function_call_expression", "scoped_call_expression",
		"member_call_expression", "nullsafe_member_call_expression":
		return true
	}
	return false
}

// CallName returns the invoked method/function name of a call node: the `name`
// of `X::m()` / `$o->m()`, or the callee of `f()`. Empty for anything else.
func CallName(call Node) string {
	switch call.Type() {
	case "scoped_call_expression", "member_call_expression", "nullsafe_member_call_expression":
		return call.ChildByFieldName("name").Text()
	case "function_call_expression":
		return call.ChildByFieldName("function").Text()
	}
	return ""
}

// CallReceiver returns a call's receiver: the `object` of `$o->m()` or the
// `scope` of `X::m()`. Invalid for a plain function call.
func CallReceiver(call Node) Node {
	switch call.Type() {
	case "member_call_expression", "nullsafe_member_call_expression":
		return call.ChildByFieldName("object")
	case "scoped_call_expression":
		return call.ChildByFieldName("scope")
	}
	return Node{}
}

// Arg is one call argument: Name is the PHP 8 named-argument label ("" when
// positional), Value the argument expression.
type Arg struct {
	Name  string
	Value Node
}

// CallArgs returns a call's arguments in source order. The grammar wraps each
// one in an `argument` node (holding an optional `name` field for PHP 8 named
// arguments), so callers never see that wrapper.
func CallArgs(call Node) []Arg {
	args := call.ChildByFieldName("arguments")
	if !args.Valid() {
		// object_creation_expression carries its arguments as a plain named
		// child rather than a labelled field, so `new C([...])` reaches the
		// same accessors as a call.
		args = ChildByType(call, "arguments")
	}
	if !args.Valid() {
		return nil
	}
	var out []Arg
	for _, a := range NamedChildren(args) {
		if a.Type() != "argument" {
			continue
		}
		kids := NamedChildren(a)
		if len(kids) == 0 {
			continue
		}
		var arg Arg
		if n := a.ChildByFieldName("name"); n.Valid() {
			arg.Name = n.Text()
		}
		arg.Value = kids[len(kids)-1] // `name: value` -> value is last
		out = append(out, arg)
	}
	return out
}

// PositionalArg returns the i-th POSITIONAL argument of a call (named arguments
// are skipped, not counted), or an invalid Node when there is no such argument.
func PositionalArg(call Node, i int) Node {
	n := 0
	for _, a := range CallArgs(call) {
		if a.Name != "" {
			continue
		}
		if n == i {
			return a.Value
		}
		n++
	}
	return Node{}
}

// NamedArg returns the value of a PHP 8 named argument (`apiPrefix: 'api'`), or
// an invalid Node when the call does not pass it.
func NamedArg(call Node, name string) Node {
	for _, a := range CallArgs(call) {
		if a.Name == name {
			return a.Value
		}
	}
	return Node{}
}

// StringLit returns the value of a PHP string literal without its quotes, and
// whether the node is a literal string at all. A single-quoted `string` always
// is; a double-quoted `encapsed_string` only when it holds no interpolation
// (`"/users/{$id}"` is a dynamic expression, not a literal). Heredocs are not
// treated as literals.
func StringLit(n Node) (string, bool) {
	switch n.Type() {
	case "string":
		return stringParts(n), true
	case "encapsed_string":
		for _, c := range NamedChildren(n) {
			switch c.Type() {
			case "string_content", "escape_sequence":
			default:
				return "", false // interpolated variable/expression
			}
		}
		return stringParts(n), true
	}
	return "", false
}

// stringParts concatenates a string node's literal segments.
func stringParts(n Node) string {
	var b strings.Builder
	for _, c := range NamedChildren(n) {
		switch c.Type() {
		case "string_content", "escape_sequence":
			b.WriteString(c.Text())
		}
	}
	return b.String()
}

// ArrayEntry is one element of an array literal. Key is the literal string key
// of a `'k' => v` element ("" for a positional element or a non-string key);
// Value is the element expression.
type ArrayEntry struct {
	Key   string
	Value Node
}

// ArrayEntries returns the elements of an `array_creation_expression` (both the
// `[...]` and the legacy `array(...)` syntax). A non-array node yields nil.
func ArrayEntries(n Node) []ArrayEntry {
	if n.Type() != "array_creation_expression" {
		return nil
	}
	var out []ArrayEntry
	for _, el := range NamedChildren(n) {
		if el.Type() != "array_element_initializer" {
			continue
		}
		kids := NamedChildren(el)
		switch len(kids) {
		case 0:
			continue
		case 1:
			out = append(out, ArrayEntry{Value: kids[0]})
		default:
			key, _ := StringLit(kids[0])
			out = append(out, ArrayEntry{Key: key, Value: kids[len(kids)-1]})
		}
	}
	return out
}

// ArrayStrings returns the literal string elements of an array literal,
// skipping any element that is not a literal string. A literal string node
// (rather than an array) yields that one value, so callers can accept Laravel's
// `'auth'` / `['auth', 'admin']` duality without branching.
func ArrayStrings(n Node) []string {
	if s, ok := StringLit(n); ok {
		return []string{s}
	}
	var out []string
	for _, e := range ArrayEntries(n) {
		if s, ok := StringLit(e.Value); ok {
			out = append(out, s)
		}
	}
	return out
}
