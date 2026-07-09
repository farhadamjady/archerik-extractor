// Package java is the shared Java LANGUAGE layer of the provider seam. It owns
// Java parsing (tree-sitter) and the language-generic node type; framework
// providers (Spring today, Micronaut next) declare rules over it and never parse
// Java themselves. A DTO is a DTO regardless of framework, so this layer is
// reused as-is by every Java framework provider.
package java

import (
	"context"
	"fmt"
	"strings"
	"sync"

	sitter "github.com/smacker/go-tree-sitter"
	tsjava "github.com/smacker/go-tree-sitter/java"

	"github.com/farhadamjady/service-discovery/internal/provider"
)

// Parser parses Java sources into a tree-sitter AST. A parser is not safe for
// concurrent use, so Parse constructs a fresh one per file; parsing is not yet a
// hot path, and this keeps future parallel collection trivially safe.
type Parser struct{}

// NewParser returns the shared Java parser.
func NewParser() *Parser { return &Parser{} }

func (*Parser) Parse(path string, src []byte) (provider.ParsedFile, error) {
	p := sitter.NewParser()
	p.SetLanguage(tsjava.GetLanguage())
	tree, err := p.ParseCtx(context.Background(), nil, src)
	if err != nil {
		return nil, err
	}
	return &File{path: path, src: src, tree: tree}, nil
}

// File is the concrete ParsedFile for KindJava. Detector handlers and indexers
// type-assert provider.ParsedFile to *java.File, then walk from Root.
type File struct {
	path string
	src  []byte
	tree *sitter.Tree
}

func (f *File) Path() string            { return f.path }
func (f *File) Kind() provider.FileKind { return provider.KindJava }

// Src is the original source; node text is sliced out of it by byte offset.
func (f *File) Src() []byte { return f.src }

// Root is the top of the parse tree (a "program" node).
func (f *File) Root() Node { return Node{inner: f.tree.RootNode(), file: f} }

// Close releases the tree-sitter tree's C-allocated memory. Safe to call once
// detection and the schema pass no longer need the file.
func (f *File) Close() {
	if f.tree != nil {
		f.tree.Close()
	}
}

// Node is the concrete ASTNode for Java: handlers type-assert provider.ASTNode
// to java.Node. It is a value wrapping a tree-sitter node plus its owning File
// (node text is a slice of the file source). The zero Node is invalid; guard
// with Valid.
type Node struct {
	inner *sitter.Node
	file  *File
}

// Valid reports whether n refers to a real node (accessors on an invalid node
// return zero values, never panic).
func (n Node) Valid() bool { return n.inner != nil }

// Type is the tree-sitter grammar node type, e.g. "class_declaration",
// "method_declaration", "annotation", "string_literal".
func (n Node) Type() string {
	if n.inner == nil {
		return ""
	}
	return n.inner.Type()
}

// Named reports whether this is a named node (not an anonymous token like "{").
func (n Node) Named() bool { return n.inner != nil && n.inner.IsNamed() }

// Text is the source text this node spans.
func (n Node) Text() string {
	if n.inner == nil {
		return ""
	}
	return n.inner.Content(n.file.src)
}

// NamedChildCount / NamedChild iterate the named children (skipping punctuation).
func (n Node) NamedChildCount() int {
	if n.inner == nil {
		return 0
	}
	return int(n.inner.NamedChildCount())
}

func (n Node) NamedChild(i int) Node {
	if n.inner == nil {
		return Node{}
	}
	return Node{inner: n.inner.NamedChild(i), file: n.file}
}

// ChildByFieldName returns the child under a grammar field (e.g. "name",
// "body", "type"), or an invalid Node if absent.
func (n Node) ChildByFieldName(field string) Node {
	if n.inner == nil {
		return Node{}
	}
	return Node{inner: n.inner.ChildByFieldName(field), file: n.file}
}

// Parent returns the enclosing node, or an invalid Node at the root. Used to
// walk out to a declaration's scope (e.g. the class owning a referenced field).
func (n Node) Parent() Node {
	if n.inner == nil {
		return Node{}
	}
	return Node{inner: n.inner.Parent(), file: n.file}
}

// StartByte is the node's start offset in the source — a stable per-file
// identity used to memoize evaluation.
func (n Node) StartByte() uint32 {
	if n.inner == nil {
		return 0
	}
	return n.inner.StartByte()
}

// Walk calls fn for n and every descendant, pre-order. Returning false from fn
// prunes that node's subtree. Traversal order is deterministic (child index).
func (n Node) Walk(fn func(Node) bool) {
	if n.inner == nil || !fn(n) {
		return
	}
	for i := 0; i < n.NamedChildCount(); i++ {
		n.NamedChild(i).Walk(fn)
	}
}

// RunQuery implements provider.QueryRunner: it compiles patterns into one
// multi-pattern query (cached), runs it over this file in a single traversal,
// and dispatches each match to onMatch with its pattern index and named
// captures. Match order follows node position, so it is deterministic.
func (f *File) RunQuery(patterns []string, onMatch func(int, map[string]provider.ASTNode)) error {
	q, err := compiledQuery(patterns)
	if err != nil {
		return err
	}
	// Guard the 1:1 patternIndex<->rule mapping: a single rule query that
	// smuggled in two top-level patterns would silently misroute matches.
	if int(q.PatternCount()) != len(patterns) {
		return fmt.Errorf("java: expected %d query patterns (one per rule), got %d — a Rule.Query must be exactly one pattern",
			len(patterns), q.PatternCount())
	}

	qc := sitter.NewQueryCursor()
	defer qc.Close()
	qc.Exec(q, f.tree.RootNode())
	for {
		m, ok := qc.NextMatch()
		if !ok {
			break
		}
		m = qc.FilterPredicates(m, f.src) // apply #eq?/#match? predicates
		caps := make(map[string]provider.ASTNode, len(m.Captures))
		for _, c := range m.Captures {
			caps[q.CaptureNameForId(c.Index)] = Node{inner: c.Node, file: f}
		}
		onMatch(int(m.PatternIndex), caps)
	}
	return nil
}

// Compiled tree-sitter queries are immutable and reusable across files and
// cursors, so they are cached by their combined pattern text (compile once, per
// DESIGN §6). The cache is process-lifetime — a single scan uses one detector
// set, so it holds one entry.
var (
	queryCacheMu sync.Mutex
	queryCache   = map[string]*sitter.Query{}
)

func compiledQuery(patterns []string) (*sitter.Query, error) {
	key := strings.Join(patterns, "\x00")
	queryCacheMu.Lock()
	defer queryCacheMu.Unlock()
	if q, ok := queryCache[key]; ok {
		return q, nil
	}
	q, err := sitter.NewQuery([]byte(strings.Join(patterns, "\n")), tsjava.GetLanguage())
	if err != nil {
		return nil, err
	}
	queryCache[key] = q
	return q, nil
}
