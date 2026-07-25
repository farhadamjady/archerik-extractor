// Package nethttp is the Go standard-library net/http provider (no framework), on
// the Recipe-B lang/golang layer. Go routing is call-based:
// `mux.HandleFunc("GET /items/{id}", handler)` / `http.Handle("/path", h)`. There
// is no framework marker and no base-path composition — each registration carries
// the full pattern, and (Go 1.22+) the HTTP method and `{wildcards}` are embedded
// in the pattern string. Third-party routers (gin/echo/chi/gorilla) are
// frameworks and out of scope; this provider targets the standard library.
package nethttp

import (
	"bytes"

	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/golang"
)

// Provider detects and extracts from Go net/http services.
type Provider struct{}

// New returns the net/http provider.
func New() *Provider { return &Provider{} }

func (*Provider) Name() string { return "go-nethttp" }

// Language is Go.
func (*Provider) Language() string { return "Go" }

// Match scores a Go net/http repo. Requires Go sources; the strong signals are a
// net/http import and an actual routing call (HandleFunc/Handle/ListenAndServe).
func (*Provider) Match(root string, fs provider.FileTree) (bool, int) {
	gofiles := fs.Glob("**/*.go")
	if len(gofiles) == 0 {
		return false, 0
	}
	score := 1
	importsNetHTTP, routes := false, false
	for _, f := range gofiles {
		b, err := fs.Read(f)
		if err != nil {
			continue
		}
		if !importsNetHTTP && bytes.Contains(b, []byte(`"net/http"`)) {
			importsNetHTTP = true
			score += 3
		}
		if !routes && (bytes.Contains(b, []byte(".HandleFunc(")) ||
			bytes.Contains(b, []byte(".Handle(")) ||
			bytes.Contains(b, []byte("http.ListenAndServe"))) {
			routes = true
			score += 2
		}
		if importsNetHTTP && routes {
			break
		}
	}
	if !importsNetHTTP {
		return false, 0
	}
	return true, score
}

// FileSpec collects Go sources; vendor, tests, and testdata excluded.
func (*Provider) FileSpec() provider.FileSpec {
	return provider.FileSpec{
		Groups: []provider.FileGroup{
			{Kind: provider.KindJava, Include: []string{"**/*.go"}},
		},
		Exclude: []string{
			"**/vendor/**",
			"**/*_test.go",
			"**/testdata/**",
		},
	}
}

func (*Provider) Parsers() map[provider.FileKind]provider.Parser {
	return map[provider.FileKind]provider.Parser{
		provider.KindJava: golang.NewParser(),
	}
}

// Indexers: none yet — round 1 is server routes. Outbound http.Get/NewRequest
// edges and request/response schemas are next rounds.
func (*Provider) Indexers() []provider.Indexer { return nil }

// Detectors: server routes from HandleFunc/Handle registrations.
func (*Provider) Detectors() []provider.Detector {
	return []provider.Detector{
		routeDetector{},
	}
}

func (*Provider) NewResolver(idx *provider.Index) provider.Resolver { return nil }
