package spring

import (
	"strings"

	"github.com/farhadamjady/service-discovery/internal/provider/lang/java"
)

// Annotation-navigation helpers over java.Node, shared by the Spring detectors
// (REST here; Feign, RestTemplate, WebClient, Kafka reuse them). They deal in
// tree-sitter-java node shapes: a declaration's annotations live under a
// "modifiers" child; an annotation is either a marker_annotation (no args, e.g.
// @RestController) or an annotation (with an annotation_argument_list).

// childByType returns the first named child of n with the given tree-sitter
// type, or an invalid Node.
func childByType(n java.Node, typ string) java.Node {
	for i := 0; i < n.NamedChildCount(); i++ {
		if c := n.NamedChild(i); c.Type() == typ {
			return c
		}
	}
	return java.Node{}
}

// namedChildren materializes n's named children in order.
func namedChildren(n java.Node) []java.Node {
	out := make([]java.Node, 0, n.NamedChildCount())
	for i := 0; i < n.NamedChildCount(); i++ {
		out = append(out, n.NamedChild(i))
	}
	return out
}

// annotationsOf returns the annotation nodes directly under a modifiers node.
func annotationsOf(modifiers java.Node) []java.Node {
	var out []java.Node
	for _, c := range namedChildren(modifiers) {
		if t := c.Type(); t == "marker_annotation" || t == "annotation" {
			out = append(out, c)
		}
	}
	return out
}

// annotationName is the simple annotation identifier, e.g. "GetMapping".
func annotationName(ann java.Node) string {
	return ann.ChildByFieldName("name").Text()
}

// findAnnotation returns the first annotation named `name` under modifiers.
func findAnnotation(modifiers java.Node, name string) java.Node {
	for _, a := range annotationsOf(modifiers) {
		if annotationName(a) == name {
			return a
		}
	}
	return java.Node{}
}

// annotationStringValues extracts the string value(s) of an annotation argument:
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
func annotationStringValues(ann java.Node, keys ...string) (values []string, literal, ok bool) {
	args := ann.ChildByFieldName("arguments")
	if !args.Valid() {
		return nil, true, false // marker annotation — no arguments
	}
	for _, c := range namedChildren(args) {
		switch c.Type() {
		case "string_literal":
			return []string{unquote(c.Text())}, true, true
		case "array_initializer", "element_value_array_initializer":
			// Annotation array values are element_value_array_initializer, not
			// array_initializer.
			if vs := stringLiterals(c); len(vs) > 0 {
				return vs, true, true
			}
		case "element_value_pair":
			if contains(keys, c.ChildByFieldName("key").Text()) {
				return elementValues(c.ChildByFieldName("value"))
			}
		default:
			// Positional non-literal (identifier, field_access, concatenation).
			return []string{c.Text()}, false, true
		}
	}
	return nil, true, false
}

// elementValues resolves the value node of a named annotation argument.
func elementValues(v java.Node) (values []string, literal, ok bool) {
	switch v.Type() {
	case "string_literal":
		return []string{unquote(v.Text())}, true, true
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
func stringLiterals(arr java.Node) []string {
	var out []string
	for _, e := range namedChildren(arr) {
		if e.Type() == "string_literal" {
			out = append(out, unquote(e.Text()))
		}
	}
	return out
}

// annotationNamedValue returns the string value of a NAMED annotation argument
// (key=value), ignoring any positional argument. Unlike annotationStringValues,
// it never returns a positional value — for attributes that are only ever named
// (e.g. Feign's url, which must not be confused with a positional service name).
func annotationNamedValue(ann java.Node, key string) (value string, literal, ok bool) {
	args := ann.ChildByFieldName("arguments")
	if !args.Valid() {
		return "", true, false
	}
	for _, c := range namedChildren(args) {
		if c.Type() == "element_value_pair" && c.ChildByFieldName("key").Text() == key {
			vals, lit, o := elementValues(c.ChildByFieldName("value"))
			if !o || len(vals) == 0 {
				return "", lit, false
			}
			return vals[0], lit, true
		}
	}
	return "", true, false
}

// unquote strips surrounding double quotes from a Java string literal's text.
func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
