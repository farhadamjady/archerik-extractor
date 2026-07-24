package micronaut

import "github.com/farhadamjady/service-discovery/internal/provider"

// rawParser carries raw bytes for kinds whose real parsing lives in the
// indexers (config, Kafka schema, deploy, OpenAPI). Same shape as Spring's.
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
