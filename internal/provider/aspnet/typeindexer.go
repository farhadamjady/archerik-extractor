package aspnet

import (
	"sort"

	"github.com/farhadamjady/archerik-extractor/internal/provider"
	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/csharp"
)

// typeIndexer builds Index.Types from the repo's C# classes/records so the REST
// detector can resolve request/response body schemas.
type typeIndexer struct{}

func (typeIndexer) Name() string { return "aspnet.types" }

func (typeIndexer) Index(ic *provider.IndexContext, idx *provider.Index) error {
	idx.Types = buildTypeIndex(csFiles(ic.Parsed))
	return nil
}

// csFiles returns the parsed C# files in stable path order.
func csFiles(parsed map[string]provider.ParsedFile) []*csharp.File {
	var paths []string
	for p, pf := range parsed {
		if _, ok := pf.(*csharp.File); ok {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	files := make([]*csharp.File, 0, len(paths))
	for _, p := range paths {
		files = append(files, parsed[p].(*csharp.File))
	}
	return files
}
