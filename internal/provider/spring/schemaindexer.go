package spring

import (
	"path"
	"sort"
	"strings"

	"github.com/farhadamjady/archerik-extractor/internal/model"
	"github.com/farhadamjady/archerik-extractor/internal/provider"
	"github.com/farhadamjady/archerik-extractor/internal/schema"
	"github.com/farhadamjady/archerik-extractor/internal/schema/contract"
)

// schemaSourceIndexer parses Kafka contract files (KindKafkaSchema) into
// Index.Schemas, so the Kafka schema pass can resolve a payload type files-first.
// Each schema is registered by its declared name (Avro record / Proto message /
// JSON Schema title) and by its file basename as a fallback.
type schemaSourceIndexer struct{}

func (schemaSourceIndexer) Name() string { return "spring.schema-sources" }

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
