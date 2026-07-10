package spring

import (
	"sort"

	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/java"
)

// symbolIndexer builds the cross-file constant table (Index.Symbols) so the
// value evaluator can resolve references like OrderTopics.ORDERS -> "orders".
// It is independent of the config/deploy indexers.
type symbolIndexer struct{}

func (symbolIndexer) Name() string { return "spring.symbols" }

func (symbolIndexer) Index(ic *provider.IndexContext, idx *provider.Index) error {
	// Includes shared sibling-module files: constants (e.g. a Topics class in a
	// shared contracts module) resolve across modules too.
	idx.Symbols = java.IndexSymbols(javaFilesOf(ic))
	return nil
}

func sortedJavaPaths(parsed map[string]provider.ParsedFile) []string {
	var out []string
	for p, pf := range parsed {
		if _, ok := pf.(*java.File); ok {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}
