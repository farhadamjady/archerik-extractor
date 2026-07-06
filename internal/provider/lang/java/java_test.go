package java

import (
	"testing"

	"github.com/farhadamjady/service-discovery/internal/provider"
)

const sample = `package com.acme;

import org.springframework.web.bind.annotation.RestController;

@RestController
public class UserController {
    public String hello() { return "hi"; }
}
`

// parseSample parses the sample and returns the *File, failing on error.
func parseSample(t *testing.T) *File {
	t.Helper()
	pf, err := NewParser().Parse("UserController.java", []byte(sample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	f := pf.(*File)
	t.Cleanup(f.Close)
	return f
}

// first returns the first node of the given tree-sitter type, pre-order.
func first(root Node, typ string) Node {
	var found Node
	root.Walk(func(n Node) bool {
		if found.Valid() {
			return false
		}
		if n.Type() == typ {
			found = n
			return false
		}
		return true
	})
	return found
}

// TestParseKindAndRoot pins the ParsedFile contract and the root node type.
func TestParseKindAndRoot(t *testing.T) {
	f := parseSample(t)

	if f.Path() != "UserController.java" {
		t.Errorf("Path() = %q", f.Path())
	}
	if f.Kind() != provider.KindJava {
		t.Errorf("Kind() = %d, want KindJava", f.Kind())
	}
	if got := f.Root().Type(); got != "program" {
		t.Errorf("Root().Type() = %q, want %q", got, "program")
	}
}

// TestExtractClassAndMethod proves node navigation + text extraction: the class
// name and the method name come back out of the parse tree by field.
func TestExtractClassAndMethod(t *testing.T) {
	root := parseSample(t)

	class := first(root.Root(), "class_declaration")
	if !class.Valid() {
		t.Fatal("no class_declaration found")
	}
	if name := class.ChildByFieldName("name").Text(); name != "UserController" {
		t.Errorf("class name = %q, want %q", name, "UserController")
	}

	method := first(root.Root(), "method_declaration")
	if name := method.ChildByFieldName("name").Text(); name != "hello" {
		t.Errorf("method name = %q, want %q", name, "hello")
	}
}

// TestExtractAnnotation is the marker the REST detector will key on: a
// no-argument annotation is a marker_annotation whose name is the annotation
// identifier.
func TestExtractAnnotation(t *testing.T) {
	root := parseSample(t)

	ann := first(root.Root(), "marker_annotation")
	if !ann.Valid() {
		t.Fatal("no marker_annotation found")
	}
	if got := ann.Text(); got != "@RestController" {
		t.Errorf("annotation text = %q, want %q", got, "@RestController")
	}
	if name := ann.ChildByFieldName("name").Text(); name != "RestController" {
		t.Errorf("annotation name = %q, want %q", name, "RestController")
	}
}

// TestInvalidNodeIsSafe guards the zero-Node contract: accessors on a missing
// child return zero values instead of panicking.
func TestInvalidNodeIsSafe(t *testing.T) {
	root := parseSample(t)
	missing := root.Root().ChildByFieldName("does_not_exist")
	if missing.Valid() {
		t.Fatal("expected invalid node for absent field")
	}
	if missing.Type() != "" || missing.Text() != "" || missing.NamedChildCount() != 0 {
		t.Error("accessors on invalid node should return zero values")
	}
}
