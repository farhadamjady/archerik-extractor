// Package tsjs is the shared TypeScript/JavaScript LANGUAGE layer of the provider
// seam (Recipe B). It owns TS/JS parsing (tree-sitter) and the language-generic
// node type; Node.js framework providers (NestJS, Express) declare rules over it.
// The TypeScript grammar is a superset that also parses plain JS, so one grammar
// serves both .ts and .js backend sources (JSX/.tsx is out of scope — backend
// services don't use it). The Node API mirrors lang/java.Node so detectors read
// the same, but the tree-sitter-typescript node shapes differ (decorators are
// preceding siblings, not modifiers; strings are string>string_fragment; calls
// are call_expression>member_expression) — the helpers hide that.
package tsjs

import (
	"context"
	"fmt"
	"strings"
	"sync"

	sitter "github.com/smacker/go-tree-sitter"
	ts "github.com/smacker/go-tree-sitter/typescript/typescript"

	"github.com/farhadamjady/archerik-extractor/internal/provider"
)

// Parser parses TS/JS sources into a tree-sitter AST. Not concurrency-safe, so
// Parse builds a fresh parser per file.
type Parser struct{}

// NewParser returns the shared TS/JS parser.
func NewParser() *Parser { return &Parser{} }

func (*Parser) Parse(path string, src []byte) (provider.ParsedFile, error) {
	p := sitter.NewParser()
	p.SetLanguage(ts.GetLanguage())
	tree, err := p.ParseCtx(context.Background(), nil, src)
	if err != nil {
		return nil, err
	}
	return &File{path: path, src: src, tree: tree}, nil
}

// File is the concrete ParsedFile for TS/JS sources.
type File struct {
	path string
	src  []byte
	tree *sitter.Tree
}

func (f *File) Path() string            { return f.path }
func (f *File) Kind() provider.FileKind { return provider.KindJava } // primary-source routing bucket

func (f *File) Src() []byte { return f.src }
func (f *File) Root() Node  { return Node{inner: f.tree.RootNode(), file: f} }

func (f *File) Close() {
	if f.tree != nil {
		f.tree.Close()
	}
}

// Node is the concrete ASTNode for TS/JS: handlers type-assert provider.ASTNode
// to tsjs.Node. Same shape and API as java.Node.
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

// PrevNamedSibling returns the previous named sibling, or an invalid Node. Used
// to collect the decorators that precede a class/method (TS puts them as
// preceding siblings, not inside a modifiers node).
func (n Node) PrevNamedSibling() Node {
	if n.inner == nil {
		return Node{}
	}
	return Node{inner: n.inner.PrevNamedSibling(), file: n.file}
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

// RunQuery implements provider.QueryRunner over the TypeScript grammar.
func (f *File) RunQuery(patterns []string, onMatch func(int, map[string]provider.ASTNode)) error {
	q, err := compiledQuery(patterns)
	if err != nil {
		return err
	}
	if int(q.PatternCount()) != len(patterns) {
		return fmt.Errorf("tsjs: expected %d query patterns (one per rule), got %d — a Rule.Query must be exactly one pattern",
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
	q, err := sitter.NewQuery([]byte(strings.Join(patterns, "\n")), ts.GetLanguage())
	if err != nil {
		return nil, err
	}
	queryCache[key] = q
	return q, nil
}
