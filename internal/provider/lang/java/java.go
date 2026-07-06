// Package java is the shared Java LANGUAGE layer of the provider seam. It owns
// Java parsing and the language-generic node type; framework providers (Spring
// today, Micronaut next) declare rules over it and never parse Java themselves.
// A DTO is a DTO regardless of framework, so this layer is reused as-is by every
// Java framework provider.
package java

import "github.com/farhadamjady/service-discovery/internal/provider"

// Parser parses Java sources. It will produce a tree-sitter AST
// (smacker/go-tree-sitter + Java grammar, cgo); until that lands it carries the
// raw source so the pipeline runs end to end.
type Parser struct{}

// NewParser returns the shared Java parser.
func NewParser() *Parser { return &Parser{} }

func (*Parser) Parse(path string, src []byte) (provider.ParsedFile, error) {
	return &File{path: path, Src: src}, nil
}

// File is the concrete ParsedFile for KindJava. Detector handlers and indexers
// type-assert provider.ParsedFile to *java.File. It gains the tree-sitter tree
// when the real parser lands.
type File struct {
	path string
	Src  []byte
	// Tree *sitter.Tree — added with the tree-sitter parser
}

func (f *File) Path() string            { return f.path }
func (f *File) Kind() provider.FileKind { return provider.KindJava }

// Node is the concrete ASTNode for Java: handlers type-assert
// provider.ASTNode to java.Node. It will wrap a *sitter.Node plus its owning
// File (tree-sitter nodes need the source to yield text); declared now so
// handler signatures are stable across the seam.
type Node struct {
	// inner *sitter.Node; file *File — added with the tree-sitter parser
}
