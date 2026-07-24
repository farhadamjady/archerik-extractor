package kotlin

import "strings"

// Annotation-navigation helpers over the tree-sitter-kotlin grammar, mirroring
// lang/java's API so Kotlin detectors read the same. The Kotlin shapes differ:
//   - a declaration's annotations live under a `modifiers` (or `parameter_modifiers`)
//     child, each an `annotation` node;
//   - an annotation's NAME is a `type_identifier` inside `user_type` (no args) or
//     inside `constructor_invocation > user_type` (with args);
//   - arguments are `value_arguments > value_argument`, positional or named
//     (`name = expr`), with array values written as a `collection_literal` (`[..]`);
//   - a string literal's text is its `string_content` child (already unquoted).

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

// Modifiers returns the `modifiers` child of a declaration, or an invalid Node.
func Modifiers(decl Node) Node { return ChildByType(decl, "modifiers") }

// AnnotationsOf returns the annotation nodes under a modifiers/parameter_modifiers.
func AnnotationsOf(modifiers Node) []Node {
	var out []Node
	for _, c := range NamedChildren(modifiers) {
		if c.Type() == "annotation" {
			out = append(out, c)
		}
	}
	return out
}

// AnnotationName is the annotation's simple name (e.g. "GetMapping"): the first
// type_identifier in the annotation subtree.
func AnnotationName(ann Node) string {
	var name string
	ann.Walk(func(n Node) bool {
		if name != "" {
			return false
		}
		if n.Type() == "type_identifier" {
			name = n.Text()
			return false
		}
		return true
	})
	return name
}

// FindAnnotation returns the first annotation named `name` under modifiers.
func FindAnnotation(modifiers Node, name string) Node {
	for _, a := range AnnotationsOf(modifiers) {
		if AnnotationName(a) == name {
			return a
		}
	}
	return Node{}
}

// valueArguments returns the value_arguments node of an argumented annotation.
func valueArguments(ann Node) Node {
	if ci := ChildByType(ann, "constructor_invocation"); ci.Valid() {
		return ChildByType(ci, "value_arguments")
	}
	return ChildByType(ann, "value_arguments")
}

// AnnotationStringValues extracts the string value(s) of an annotation argument:
// the positional string, a positional string array ([...]), or the named value
// under one of keys ("value"/"path"), single or array. Returns (values, literal,
// ok) with the same contract as lang/java.AnnotationStringValues.
func AnnotationStringValues(ann Node, keys ...string) (values []string, literal, ok bool) {
	args := valueArguments(ann)
	if !args.Valid() {
		return nil, true, false // no arguments
	}
	for _, va := range NamedChildren(args) {
		if va.Type() != "value_argument" {
			continue
		}
		// Named argument: `key = value`.
		if id := ChildByType(va, "simple_identifier"); id.Valid() {
			if !containsStr(keys, id.Text()) {
				continue
			}
			return valuesOf(va)
		}
		// Positional argument (first one wins).
		return valuesOf(va)
	}
	return nil, true, false
}

// valuesOf reads the string value(s) from a value_argument: a string_literal, a
// collection_literal ([..]) of strings, or a non-literal expression.
func valuesOf(va Node) (values []string, literal, ok bool) {
	if sl := ChildByType(va, "string_literal"); sl.Valid() {
		return []string{stringContent(sl)}, true, true
	}
	if cl := ChildByType(va, "collection_literal"); cl.Valid() {
		var out []string
		for _, e := range NamedChildren(cl) {
			if e.Type() == "string_literal" {
				out = append(out, stringContent(e))
			}
		}
		if len(out) > 0 {
			return out, true, true
		}
		return nil, true, false
	}
	// Some other expression (constant, identifier) — non-literal.
	kids := NamedChildren(va)
	if len(kids) > 0 {
		return []string{kids[len(kids)-1].Text()}, false, true
	}
	return nil, true, false
}

// stringContent returns a string literal's content without quotes: the
// string_content child, or the trimmed text if the literal is empty.
func stringContent(sl Node) string {
	if sc := ChildByType(sl, "string_content"); sc.Valid() {
		return sc.Text()
	}
	return strings.Trim(sl.Text(), `"`)
}

func containsStr(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
