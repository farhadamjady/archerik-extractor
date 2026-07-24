// Package kotlin is the shared Kotlin LANGUAGE layer of the provider seam — the
// Recipe-B sibling of lang/java. It owns Kotlin parsing (tree-sitter) and the
// language-generic node type; Kotlin framework providers (Spring-Kotlin) declare
// rules over it and never parse Kotlin themselves. The node API mirrors
// lang/java.Node so detector code reads the same, but the tree-sitter-kotlin
// grammar's node types differ (function_declaration vs method_declaration,
// simple_identifier vs identifier, string_literal>string_content, annotations as
// user_type / constructor_invocation) — the annotation helpers hide those.
package kotlin

import (
	"context"
	"fmt"
	"strings"
	"sync"

	sitter "github.com/smacker/go-tree-sitter"
	tskotlin "github.com/smacker/go-tree-sitter/kotlin"

	"github.com/farhadamjady/service-discovery/internal/provider"
)

// Parser parses Kotlin sources into a tree-sitter AST. Not concurrency-safe, so
// Parse builds a fresh parser per file (mirrors lang/java).
type Parser struct{}

// NewParser returns the shared Kotlin parser.
func NewParser() *Parser { return &Parser{} }

func (*Parser) Parse(path string, src []byte) (provider.ParsedFile, error) {
	p := sitter.NewParser()
	p.SetLanguage(tskotlin.GetLanguage())
	tree, err := p.ParseCtx(context.Background(), nil, src)
	if err != nil {
		return nil, err
	}
	return &File{path: path, src: src, tree: tree}, nil
}

// File is the concrete ParsedFile for Kotlin sources.
type File struct {
	path string
	src  []byte
	tree *sitter.Tree
}

func (f *File) Path() string            { return f.path }
func (f *File) Kind() provider.FileKind { return provider.KindJava } // JVM source kind; routed by the provider's parser map

// Src is the original source; node text is sliced out of it by byte offset.
func (f *File) Src() []byte { return f.src }

// Root is the top of the parse tree (a "source_file" node).
func (f *File) Root() Node { return Node{inner: f.tree.RootNode(), file: f} }

// Close releases the tree-sitter tree's C-allocated memory.
func (f *File) Close() {
	if f.tree != nil {
		f.tree.Close()
	}
}

// Node is the concrete ASTNode for Kotlin: handlers type-assert provider.ASTNode
// to kotlin.Node. Same shape and API as java.Node.
type Node struct {
	inner *sitter.Node
	file  *File
}

func (n Node) Valid() bool { return n.inner != nil }

func (n Node) Type() string {
	if n.inner == nil {
		return ""
	}
	return n.inner.Type()
}

func (n Node) Named() bool { return n.inner != nil && n.inner.IsNamed() }

func (n Node) Text() string {
	if n.inner == nil {
		return ""
	}
	return n.inner.Content(n.file.src)
}

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

func (n Node) ChildByFieldName(field string) Node {
	if n.inner == nil {
		return Node{}
	}
	return Node{inner: n.inner.ChildByFieldName(field), file: n.file}
}

func (n Node) Parent() Node {
	if n.inner == nil {
		return Node{}
	}
	return Node{inner: n.inner.Parent(), file: n.file}
}

func (n Node) StartByte() uint32 {
	if n.inner == nil {
		return 0
	}
	return n.inner.StartByte()
}

// Walk calls fn for n and every descendant, pre-order; returning false prunes.
func (n Node) Walk(fn func(Node) bool) {
	if n.inner == nil || !fn(n) {
		return
	}
	for i := 0; i < n.NamedChildCount(); i++ {
		n.NamedChild(i).Walk(fn)
	}
}

// RunQuery implements provider.QueryRunner over the Kotlin grammar. Identical
// structure to lang/java: compile the patterns into one multi-pattern query,
// dispatch each match by pattern index with named captures.
func (f *File) RunQuery(patterns []string, onMatch func(int, map[string]provider.ASTNode)) error {
	q, err := compiledQuery(patterns)
	if err != nil {
		return err
	}
	if int(q.PatternCount()) != len(patterns) {
		return fmt.Errorf("kotlin: expected %d query patterns (one per rule), got %d — a Rule.Query must be exactly one pattern",
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
		m = qc.FilterPredicates(m, f.src)
		caps := make(map[string]provider.ASTNode, len(m.Captures))
		for _, c := range m.Captures {
			caps[q.CaptureNameForId(c.Index)] = Node{inner: c.Node, file: f}
		}
		onMatch(int(m.PatternIndex), caps)
	}
	return nil
}

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
	q, err := sitter.NewQuery([]byte(strings.Join(patterns, "\n")), tskotlin.GetLanguage())
	if err != nil {
		return nil, err
	}
	queryCache[key] = q
	return q, nil
}
