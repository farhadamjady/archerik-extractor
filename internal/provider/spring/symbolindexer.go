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
	var files []*java.File
	for _, p := range sortedJavaPaths(ic.Parsed) {
		if jf, ok := ic.Parsed[p].(*java.File); ok {
			files = append(files, jf)
		}
	}
	idx.Symbols = java.IndexSymbols(files)
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
