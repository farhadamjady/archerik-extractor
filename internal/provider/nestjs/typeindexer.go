package nestjs

import (
	"sort"

	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/tsjs"
)

// typeIndexer builds Index.Types from the repo's TypeScript classes and
// interfaces so the REST detector can resolve request/response body schemas.
type typeIndexer struct{}

func (typeIndexer) Name() string { return "nestjs.types" }

func (typeIndexer) Index(ic *provider.IndexContext, idx *provider.Index) error {
	idx.Types = buildTypeIndex(tsFiles(ic.Parsed))
	return nil
}

// tsFiles returns the parsed TypeScript files in stable path order.
func tsFiles(parsed map[string]provider.ParsedFile) []*tsjs.File {
	var paths []string
	for p, pf := range parsed {
		if _, ok := pf.(*tsjs.File); ok {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	files := make([]*tsjs.File, 0, len(paths))
	for _, p := range paths {
		files = append(files, parsed[p].(*tsjs.File))
	}
	return files
}
