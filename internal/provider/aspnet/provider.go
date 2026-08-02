// Package aspnet is the ASP.NET Core (C#) framework provider, on the Recipe-B
// lang/csharp layer. ASP.NET attribute routing maps cleanly to the annotation
// model: [Route("api/[controller]")] on a controller + [HttpGet("{id}")] on an
// action is the same shape as Spring's @RequestMapping + @GetMapping, over the
// tree-sitter-c-sharp AST. A .NET service ships as its own provider
// (Language() = "C#").
package aspnet

import (
	"bytes"

	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/csharp"
)

// Provider detects and extracts from ASP.NET Core services.
type Provider struct{}

// New returns the ASP.NET Core provider.
func New() *Provider { return &Provider{} }

func (*Provider) Name() string { return "aspnet-core-csharp" }

// Language is C#.
func (*Provider) Language() string { return "C#" }

// Match scores an ASP.NET Core repo. Requires C# sources (score 0 otherwise); the
// strong signals are a web-SDK / AspNetCore reference in a .csproj and a source
// using Microsoft.AspNetCore.Mvc.
func (*Provider) Match(root string, fs provider.FileTree) (bool, int) {
	cs := fs.Glob("**/*.cs")
	if len(cs) == 0 {
		return false, 0
	}
	score := 1
	for _, proj := range fs.Glob("**/*.csproj") {
		if b, err := fs.Read(proj); err == nil &&
			(bytes.Contains(b, []byte("Microsoft.NET.Sdk.Web")) || bytes.Contains(b, []byte("Microsoft.AspNetCore"))) {
			score += 3
			break
		}
	}
	for _, f := range cs {
		if b, err := fs.Read(f); err == nil && bytes.Contains(b, []byte("Microsoft.AspNetCore.Mvc")) {
			score += 3
			break
		}
	}
	return score > 1, score
}

// FileSpec collects C# sources. bin/obj build output and test projects excluded.
func (*Provider) FileSpec() provider.FileSpec {
	return provider.FileSpec{
		Groups: []provider.FileGroup{
			{Kind: provider.KindJava, Include: []string{"**/*.cs"}},
		},
		Exclude: []string{
			"**/bin/**",
			"**/obj/**",
			"**/*.Tests/**",
			"**/*.Test/**",
			"**/*Tests/**",
			"**/*.g.cs",
			"**/*.Designer.cs",
		},
	}
}

func (*Provider) Parsers() map[provider.FileKind]provider.Parser {
	return map[provider.FileKind]provider.Parser{
		provider.KindJava: csharp.NewParser(),
	}
}

// Indexers: none yet — round 1 is REST endpoints. HttpClient/typed-client
// outbound edges and DTO schemas are next rounds.
func (*Provider) Indexers() []provider.Indexer { return []provider.Indexer{typeIndexer{}} }

// Detectors: REST endpoints from attribute-routed controllers and from
// Minimal APIs (app.MapGet/MapPost/... incl. MapGroup prefixes), plus outbound
// HTTP dependencies from HttpClient call sites.
func (*Provider) Detectors() []provider.Detector {
	return []provider.Detector{
		restDetector{},
		minimalDetector{},
		clientDetector{},
	}
}

func (*Provider) NewResolver(idx *provider.Index) provider.Resolver { return nil }
