package springkt

import (
	"sort"

	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/kotlin"
)

// typeIndexer builds the repo DTO index (Index.Types) that the schema pass walks
// to extract request/response and topic-payload structure. Kotlin sibling of
// spring.typeIndexer.
type typeIndexer struct{}

func (typeIndexer) Name() string { return "springkt.types" }

func (typeIndexer) Index(ic *provider.IndexContext, idx *provider.Index) error {
	idx.Types = kotlin.IndexTypes(serviceKotlinFiles(ic.Parsed), serviceKotlinFiles(ic.Shared))
	return nil
}

// serviceKotlinFiles collects the parsed Kotlin files in stable path order.
func serviceKotlinFiles(parsed map[string]provider.ParsedFile) []*kotlin.File {
	var paths []string
	for p, pf := range parsed {
		if _, ok := pf.(*kotlin.File); ok {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	files := make([]*kotlin.File, 0, len(paths))
	for _, p := range paths {
		files = append(files, parsed[p].(*kotlin.File))
	}
	return files
}
