package spring

import "github.com/farhadamjady/archerik-extractor/internal/provider"

// rawParser carries raw bytes for kinds whose real parsing lives elsewhere:
// SpringConfig files are parsed by the ConfigResolver indexer, KafkaSchema
// files by the contract parsers of the Kafka schema pass. Keeping them raw
// here preserves the one-parser-per-kind routing without duplicating parsing.
type rawParser struct{ kind provider.FileKind }

func (p rawParser) Parse(path string, src []byte) (provider.ParsedFile, error) {
	return &rawFile{path: path, kind: p.kind, src: src}, nil
}

// rawFile is the concrete ParsedFile for raw-carried kinds.
type rawFile struct {
	path string
	kind provider.FileKind
	src  []byte
}

func (f *rawFile) Path() string            { return f.path }
func (f *rawFile) Kind() provider.FileKind { return f.kind }

// Src exposes the raw bytes to this provider's indexers.
func (f *rawFile) Src() []byte { return f.src }
