package quarkus

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

// Thin wrappers over the language-neutral machinery in lang/java and
// internal/schema — the real logic is framework-free and shared with Spring and
// Micronaut. Only the wiring is Quarkus-specific.

type symbolIndexer struct{}

func (symbolIndexer) Name() string { return "quarkus.symbols" }

func (symbolIndexer) Index(ic *provider.IndexContext, idx *provider.Index) error {
	idx.Symbols = java.IndexSymbols(javaFilesOf(ic))
	return nil
}

type typeIndexer struct{}

func (typeIndexer) Name() string { return "quarkus.types" }

func (typeIndexer) Index(ic *provider.IndexContext, idx *provider.Index) error {
	idx.Types = java.IndexTypes(serviceJavaFiles(ic.Parsed), serviceJavaFiles(ic.Shared))
	return nil
}

type schemaSourceIndexer struct{}

func (schemaSourceIndexer) Name() string { return "quarkus.schema-sources" }

func (schemaSourceIndexer) Index(ic *provider.IndexContext, idx *provider.Index) error {
	sources := schema.NewSources()
	for _, p := range sortedSchemaPaths(ic.Parsed) {
		src := ic.Parsed[p].(*rawFile).Src()
		name, sch := parseContract(p, src)
		sources.Add(name, sch)
		sources.Add(fileBase(p), sch)
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
