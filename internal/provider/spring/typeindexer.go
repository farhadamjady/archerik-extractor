package spring

import (
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/java"
)

// typeIndexer builds the repo DTO index (Index.Types) that the schema pass walks
// to extract request/response and topic-payload structure.
type typeIndexer struct{}

func (typeIndexer) Name() string { return "spring.types" }

func (typeIndexer) Index(ic *provider.IndexContext, idx *provider.Index) error {
	var files []*java.File
	for _, p := range sortedJavaPaths(ic.Parsed) {
		if jf, ok := ic.Parsed[p].(*java.File); ok {
			files = append(files, jf)
		}
	}
	idx.Types = java.IndexTypes(files)
	return nil
}
