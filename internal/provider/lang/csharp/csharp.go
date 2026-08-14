// Package csharp is the shared C# LANGUAGE layer of the provider seam (Recipe B).
// It owns C# parsing (tree-sitter) and the language-generic node type; .NET
// framework providers (ASP.NET Core) declare rules over it. The Node API mirrors
// lang/java.Node; the tree-sitter-c-sharp shapes differ (attributes live in
// attribute_list children, strings are string_literal>string_literal_content,
// method/class names are the "name" field) and the attribute helpers hide that.
package csharp

import (
	"context"
	"fmt"
	"strings"
	"sync"

	sitter "github.com/smacker/go-tree-sitter"
	cs "github.com/smacker/go-tree-sitter/csharp"

	"github.com/farhadamjady/archerik-extractor/internal/provider"
)

// Parser parses C# sources into a tree-sitter AST. Not concurrency-safe.
type Parser struct{}

// NewParser returns the shared C# parser.
func NewParser() *Parser { return &Parser{} }

func (*Parser) Parse(path string, src []byte) (provider.ParsedFile, error) {
	p := sitter.NewParser()
	p.SetLanguage(cs.GetLanguage())
	tree, err := p.ParseCtx(context.Background(), nil, src)
	if err != nil {
		return nil, err
	}
	return &File{path: path, src: src, tree: tree}, nil
}

// File is the concrete ParsedFile for C# sources.
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

// Node is the concrete ASTNode for C#. Same API as java.Node.
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

func (n Node) Walk(fn func(Node) bool) {
	if n.inner == nil || !fn(n) {
		return
	}
	for i := 0; i < n.NamedChildCount(); i++ {
		n.NamedChild(i).Walk(fn)
	}
}

// RunQuery implements provider.QueryRunner over the C# grammar.
func (f *File) RunQuery(patterns []string, onMatch func(int, map[string]provider.ASTNode)) error {
	q, err := compiledQuery(patterns)
	if err != nil {
		return err
	}
	if int(q.PatternCount()) != len(patterns) {
		return fmt.Errorf("csharp: expected %d query patterns (one per rule), got %d — a Rule.Query must be exactly one pattern",
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
	q, err := sitter.NewQuery([]byte(strings.Join(patterns, "\n")), cs.GetLanguage())
	if err != nil {
		return nil, err
	}
	queryCache[key] = q
	return q, nil
}
