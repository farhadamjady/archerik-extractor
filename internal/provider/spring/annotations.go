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

// annotationStringArg extracts a string value from an annotation: the positional
// string argument, or the named value under one of keys (e.g. "value", "path").
// Returns (value, literal, ok):
//   - ok=false: no such argument at all (a marker annotation, or key absent).
//   - literal=false: an argument exists but is not a plain string literal
//     (constant, concatenation, ${placeholder}) — the caller downgrades
//     confidence and, later, hands the expression to the value resolver.
func annotationStringArg(ann java.Node, keys ...string) (value string, literal, ok bool) {
	args := ann.ChildByFieldName("arguments")
	if !args.Valid() {
		return "", true, false // marker annotation — no arguments
	}
	for _, c := range namedChildren(args) {
		switch c.Type() {
		case "string_literal":
			return unquote(c.Text()), true, true
		case "array_initializer", "element_value_array_initializer":
			// {"/a","/b"} — MVP takes the first literal; multi-path arrays
			// producing several endpoints are a later refinement. (Annotation
			// array values are element_value_array_initializer, not array_initializer.)
			if first := childByType(c, "string_literal"); first.Valid() {
				return unquote(first.Text()), true, true
			}
		case "element_value_pair":
			if contains(keys, c.ChildByFieldName("key").Text()) {
				v := c.ChildByFieldName("value")
				if v.Type() == "string_literal" {
					return unquote(v.Text()), true, true
				}
				return v.Text(), false, true
			}
		default:
			// Positional non-literal (identifier, field_access, concatenation).
			return c.Text(), false, true
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
