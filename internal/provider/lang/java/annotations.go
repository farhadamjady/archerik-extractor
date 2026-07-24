package java

import "strings"

// Annotation-navigation helpers over Node, shared by every Java framework
// provider (Spring, Micronaut, Quarkus, ...). A DTO is a DTO and an annotation
// is an annotation regardless of framework, so these live in the language layer
// and providers only supply the annotation NAMES they care about.
//
// They deal in tree-sitter-java node shapes: a declaration's annotations live
// under a "modifiers" child; an annotation is either a marker_annotation (no
// args, e.g. @RestController) or an annotation (with an annotation_argument_list).

// ChildByType returns the first named child of n with the given tree-sitter
// type, or an invalid Node.
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

// AnnotationsOf returns the annotation nodes directly under a modifiers node.
func AnnotationsOf(modifiers Node) []Node {
	var out []Node
	for _, c := range NamedChildren(modifiers) {
		if t := c.Type(); t == "marker_annotation" || t == "annotation" {
			out = append(out, c)
		}
	}
	return out
}

// AnnotationName is the simple annotation identifier, e.g. "GetMapping". For a
// fully-qualified use (@io.micronaut.http.annotation.Get) tree-sitter models the
// name as a scoped_identifier; the trailing simple name is returned.
func AnnotationName(ann Node) string {
	name := ann.ChildByFieldName("name").Text()
	if i := strings.LastIndex(name, "."); i >= 0 {
		return name[i+1:]
	}
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

// AnnotationStringValues extracts the string value(s) of an annotation argument:
// the positional string, a positional string array ({"/a","/b"}), or the named
// value under one of keys (e.g. "value", "path"), single or array. Returns
// (values, literal, ok):
//   - ok=false: no such argument at all (a marker annotation, or key absent).
//   - literal=false: an argument exists but is not a string literal (constant,
//     concatenation, ${placeholder}) — returned as a single raw element; the
//     caller downgrades confidence and later hands it to the value resolver.
//
// Array support is what lets one @GetMapping({"/a","/b"}) expand into two
// endpoints rather than silently dropping the second path.
func AnnotationStringValues(ann Node, keys ...string) (values []string, literal, ok bool) {
	args := ann.ChildByFieldName("arguments")
	if !args.Valid() {
		return nil, true, false // marker annotation — no arguments
	}
	for _, c := range NamedChildren(args) {
		switch c.Type() {
		case "string_literal":
			return []string{Unquote(c.Text())}, true, true
		case "array_initializer", "element_value_array_initializer":
			// Annotation array values are element_value_array_initializer, not
			// array_initializer.
			if vs := stringLiterals(c); len(vs) > 0 {
				return vs, true, true
			}
		case "element_value_pair":
			if containsStr(keys, c.ChildByFieldName("key").Text()) {
				return ElementValues(c.ChildByFieldName("value"))
			}
		default:
			// Positional non-literal (identifier, field_access, concatenation).
			return []string{c.Text()}, false, true
		}
	}
	return nil, true, false
}

// ElementValues resolves the value node of a named annotation argument.
func ElementValues(v Node) (values []string, literal, ok bool) {
	switch v.Type() {
	case "string_literal":
		return []string{Unquote(v.Text())}, true, true
	case "array_initializer", "element_value_array_initializer":
		if vs := stringLiterals(v); len(vs) > 0 {
			return vs, true, true
		}
		return nil, true, false
	default:
		return []string{v.Text()}, false, true
	}
}

// stringLiterals collects the unquoted string literals directly under an array.
func stringLiterals(arr Node) []string {
	var out []string
	for _, e := range NamedChildren(arr) {
		if e.Type() == "string_literal" {
			out = append(out, Unquote(e.Text()))
		}
	}
	return out
}

// AnnotationNamedValue returns the string value of a NAMED annotation argument
// (key=value), ignoring any positional argument. Unlike AnnotationStringValues,
// it never returns a positional value — for attributes that are only ever named
// (e.g. Feign's url, which must not be confused with a positional service name).
func AnnotationNamedValue(ann Node, key string) (value string, literal, ok bool) {
	args := ann.ChildByFieldName("arguments")
	if !args.Valid() {
		return "", true, false
	}
	for _, c := range NamedChildren(args) {
		if c.Type() == "element_value_pair" && c.ChildByFieldName("key").Text() == key {
			vals, lit, o := ElementValues(c.ChildByFieldName("value"))
			if !o || len(vals) == 0 {
				return "", lit, false
			}
			return vals[0], lit, true
		}
	}
	return "", true, false
}

// AnnotationValueNodes returns the value node(s) of a named annotation argument,
// unwrapping an array ({"a","b"}) into its elements. Used where the value may be
// a constant/expression (resolved via the evaluator), not just a string literal —
// e.g. @KafkaListener(topics = ...).
func AnnotationValueNodes(ann Node, keys ...string) []Node {
	args := ann.ChildByFieldName("arguments")
	if !args.Valid() {
		return nil
	}
	for _, c := range NamedChildren(args) {
		if c.Type() == "element_value_pair" && containsStr(keys, c.ChildByFieldName("key").Text()) {
			return unwrapArrayNodes(c.ChildByFieldName("value"))
		}
	}
	return nil
}

func unwrapArrayNodes(v Node) []Node {
	if v.Type() == "array_initializer" || v.Type() == "element_value_array_initializer" {
		return NamedChildren(v)
	}
	if v.Valid() {
		return []Node{v}
	}
	return nil
}

// Unquote strips surrounding double quotes from a Java string literal's text.
func Unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

func containsStr(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
