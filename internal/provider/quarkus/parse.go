package quarkus

import "github.com/farhadamjady/archerik-extractor/internal/provider"

// rawParser carries raw bytes for kinds whose real parsing lives in the indexers.
type rawParser struct{ kind provider.FileKind }

func (p rawParser) Parse(path string, src []byte) (provider.ParsedFile, error) {
	return &rawFile{path: path, kind: p.kind, src: src}, nil
}

type rawFile struct {
	path string
	kind provider.FileKind
	src  []byte
}

func (f *rawFile) Path() string            { return f.path }
func (f *rawFile) Kind() provider.FileKind { return f.kind }
func (f *rawFile) Src() []byte             { return f.src }
