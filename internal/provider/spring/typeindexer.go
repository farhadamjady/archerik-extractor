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
	idx.Types = java.IndexTypes(serviceJavaFiles(ic.Parsed), serviceJavaFiles(ic.Shared))
	return nil
}

// javaFilesOf collects the service's Java files plus shared sibling-module
// files (types/constants only — detectors never see the shared ones).
func javaFilesOf(ic *provider.IndexContext) []*java.File {
	return append(serviceJavaFiles(ic.Parsed), serviceJavaFiles(ic.Shared)...)
}

func serviceJavaFiles(parsed map[string]provider.ParsedFile) []*java.File {
	var files []*java.File
	for _, p := range sortedJavaPaths(parsed) {
		if jf, ok := parsed[p].(*java.File); ok {
			files = append(files, jf)
		}
	}
	return files
}
