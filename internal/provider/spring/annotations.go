package spring

import (
	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/java"
)

// Annotation-navigation helpers used by the Spring detectors. The logic is
// Java-generic and now lives in the language layer (java.*) so every JVM
// framework provider (Micronaut, Quarkus) reuses it; these thin wrappers keep
// the existing Spring call sites unchanged.

func childByType(n java.Node, typ string) java.Node { return java.ChildByType(n, typ) }

func namedChildren(n java.Node) []java.Node { return java.NamedChildren(n) }

func annotationsOf(modifiers java.Node) []java.Node { return java.AnnotationsOf(modifiers) }

func annotationName(ann java.Node) string { return java.AnnotationName(ann) }

func findAnnotation(modifiers java.Node, name string) java.Node {
	return java.FindAnnotation(modifiers, name)
}

func annotationStringValues(ann java.Node, keys ...string) (values []string, literal, ok bool) {
	return java.AnnotationStringValues(ann, keys...)
}

func annotationNamedValue(ann java.Node, key string) (value string, literal, ok bool) {
	return java.AnnotationNamedValue(ann, key)
}

func annotationValueNodes(ann java.Node, keys ...string) []java.Node {
	return java.AnnotationValueNodes(ann, keys...)
}

func unquote(s string) string { return java.Unquote(s) }

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
