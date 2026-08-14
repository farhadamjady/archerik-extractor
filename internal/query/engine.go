// Package query is the detection dispatch engine. It runs every detector's
// tree-sitter rules over a parsed file in a single traversal and hands each
// match to the rule's handler. It is language-agnostic: the tree-sitter
// mechanics live behind provider.QueryRunner in the language layer, so the
// engine never names a grammar.
package query

import (
	"github.com/farhadamjady/archerik-extractor/internal/model"
	"github.com/farhadamjady/archerik-extractor/internal/provider"
)

// Engine dispatches detector rules over parsed files. It is stateless; the
// compiled-query cache lives in the language layer (queries are grammar-bound).
type Engine struct{}

// New returns a dispatch engine.
func New() *Engine { return &Engine{} }

// Run collects every rule from dets, runs them over f in one traversal, and
// invokes each matching rule's OnMatch with a MatchContext. Files whose kind is
// not queryable (config, schema) are skipped. Detectors with no rules are a
// no-op, so this is safe to call before any detector declares rules.
func (e *Engine) Run(f provider.ParsedFile, dets []provider.Detector, idx *provider.Index, res provider.Resolver, out *model.Service) error {
	runner, ok := f.(provider.QueryRunner)
	if !ok {
		return nil
	}

	// Flatten rules to a pattern list; pattern index i maps back to rules[i]
	// (each Rule.Query is one pattern — enforced by the runner).
	var patterns []string
	var rules []provider.Rule
	for _, d := range dets {
		for _, r := range d.Rules() {
			patterns = append(patterns, r.Query)
			rules = append(rules, r)
		}
	}
	if len(patterns) == 0 {
		return nil
	}

	return runner.RunQuery(patterns, func(pi int, caps map[string]provider.ASTNode) {
		rules[pi].OnMatch(&provider.MatchContext{
			File:     f,
			Captures: caps,
			Index:    idx,
			Resolver: res,
			Out:      out,
		})
	})
}
