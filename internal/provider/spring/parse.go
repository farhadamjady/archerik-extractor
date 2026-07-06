package spring

import "github.com/farhadamjady/service-discovery/internal/provider"

// treeSitterParser will parse Java into a tree-sitter AST that the detectors
// query for annotations, class/method nesting, and DTO types.
//
// TODO(parse): wire github.com/smacker/go-tree-sitter + the Java grammar (cgo).
// Until then this is a stub that carries raw source so the pipeline compiles and
// runs end to end.
type treeSitterParser struct{}

func (treeSitterParser) Parse(path string, src []byte) (provider.ParsedFile, error) {
	return &JavaFile{Path: path, Source: src}, nil
}

// JavaFile is the provider's concrete ParsedFile. Detectors type-assert
// provider.ParsedFile to *JavaFile. It will gain the tree-sitter root node.
type JavaFile struct {
	Path   string
	Source []byte
	// Tree *sitter.Tree // TODO(parse)
}
