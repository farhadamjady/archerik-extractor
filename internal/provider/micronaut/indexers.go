package micronaut

import (
	"path"
	"sort"
	"strings"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/java"
	"github.com/farhadamjady/service-discovery/internal/schema"
	"github.com/farhadamjady/service-discovery/internal/schema/contract"
)

// The indexers here are thin wrappers over the language-neutral machinery in
// lang/java and internal/schema: the real logic (constant folding, DTO walking,
// Avro/Proto/JSON-Schema parsing) is framework-free and shared with Spring. Only
// the wiring lives in the provider.

// symbolIndexer builds the cross-file constant table (Index.Symbols) so the
// value evaluator can resolve references like OrderTopics.ORDERS -> "orders".
type symbolIndexer struct{}

func (symbolIndexer) Name() string { return "micronaut.symbols" }

func (symbolIndexer) Index(ic *provider.IndexContext, idx *provider.Index) error {
	idx.Symbols = java.IndexSymbols(javaFilesOf(ic))
	return nil
}

// typeIndexer builds the repo DTO index (Index.Types) that the schema pass walks
// for request/response and topic-payload structure.
type typeIndexer struct{}

func (typeIndexer) Name() string { return "micronaut.types" }

func (typeIndexer) Index(ic *provider.IndexContext, idx *provider.Index) error {
	idx.Types = java.IndexTypes(serviceJavaFiles(ic.Parsed), serviceJavaFiles(ic.Shared))
	return nil
}

// schemaSourceIndexer parses Kafka contract files (KindKafkaSchema) into
// Index.Schemas, registered by declared name and file basename.
type schemaSourceIndexer struct{}

func (schemaSourceIndexer) Name() string { return "micronaut.schema-sources" }

func (schemaSourceIndexer) Index(ic *provider.IndexContext, idx *provider.Index) error {
	sources := schema.NewSources()
	for _, p := range sortedSchemaPaths(ic.Parsed) {
		src := ic.Parsed[p].(*rawFile).Src()
		name, sch := parseContract(p, src)
		sources.Add(name, sch)
		sources.Add(fileBase(p), sch) // filename fallback
	}
	idx.Schemas = sources
	return nil
}

func parseContract(p string, src []byte) (string, *model.Schema) {
	var (
		name string
		sch  *model.Schema
	)
	switch {
	case strings.HasSuffix(p, ".avsc"):
		name, sch, _ = contract.ParseAvro(src)
	case strings.HasSuffix(p, ".proto"):
		name, sch, _ = contract.ParseProto(src)
	case strings.HasSuffix(p, ".schema.json"):
		name, sch, _ = contract.ParseJSONSchema(src)
	}
	return name, sch
}

// javaFilesOf collects the service's Java files plus shared sibling-module files
// (types/constants only — detectors never see the shared ones).
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

func sortedSchemaPaths(parsed map[string]provider.ParsedFile) []string {
	var out []string
	for p, pf := range parsed {
		if rf, ok := pf.(*rawFile); ok && rf.Kind() == provider.KindKafkaSchema {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

func fileBase(p string) string {
	b := path.Base(p)
	b = strings.TrimSuffix(b, ".schema.json")
	b = strings.TrimSuffix(b, path.Ext(b))
	return b
}
