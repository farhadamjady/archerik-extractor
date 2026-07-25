// Package nestjs is the NestJS (Node.js/TypeScript) framework provider, built on
// the Recipe-B lang/tsjs layer. NestJS is decorator-based and maps cleanly to the
// annotation model of the JVM providers: @Controller('cats') + @Get(':id') is the
// same shape as Spring's @RestController + @GetMapping, just over the
// tree-sitter-typescript AST (where decorators are preceding siblings). A NestJS
// service ships as its own provider (Language() = "TypeScript").
package nestjs

import (
	"bytes"

	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/tsjs"
)

// Provider detects and extracts from NestJS services.
type Provider struct{}

// New returns the NestJS provider.
func New() *Provider { return &Provider{} }

func (*Provider) Name() string { return "nestjs-typescript" }

// Language is TypeScript: NestJS controllers are TS with decorators.
func (*Provider) Language() string { return "TypeScript" }

// Match scores a NestJS repo. Requires TS sources (score 0 otherwise); the
// strongest signal is a @nestjs dependency in package.json or a source importing
// @nestjs/common. No other registered provider reads .ts, so this is unambiguous.
func (*Provider) Match(root string, fs provider.FileTree) (bool, int) {
	ts := fs.Glob("**/*.ts")
	if len(ts) == 0 {
		return false, 0
	}
	score := 1
	if b, err := fs.Read("package.json"); err == nil && bytes.Contains(b, []byte("@nestjs/")) {
		score += 3
	}
	for _, f := range ts {
		if b, err := fs.Read(f); err == nil && bytes.Contains(b, []byte("@nestjs/common")) {
			score += 3
			break
		}
	}
	return score > 1, score
}

// FileSpec collects TypeScript sources (KindJava is the primary-source routing
// bucket). node_modules, dist, and test/spec files are excluded so they don't
// inflate the graph.
func (*Provider) FileSpec() provider.FileSpec {
	return provider.FileSpec{
		Groups: []provider.FileGroup{
			{Kind: provider.KindJava, Include: []string{"**/*.ts"}},
		},
		Exclude: []string{
			"**/node_modules/**",
			"**/dist/**",
			"**/*.spec.ts",
			"**/*.test.ts",
			"**/*.d.ts",
			"**/test/**",
		},
	}
}

// Parsers routes the TS sources to the shared TS/JS language parser.
func (*Provider) Parsers() map[provider.FileKind]provider.Parser {
	return map[provider.FileKind]provider.Parser{
		provider.KindJava: tsjs.NewParser(),
	}
}

// Indexers: none yet — round 1 is REST endpoints. HttpService clients,
// @MessagePattern/@EventPattern microservice edges, and DTO schemas are next.
func (*Provider) Indexers() []provider.Indexer { return nil }

// Detectors: REST endpoints from @Controller classes.
func (*Provider) Detectors() []provider.Detector {
	return []provider.Detector{
		restDetector{},
	}
}

// NewResolver: no value resolver yet (decorator-string endpoints need none).
func (*Provider) NewResolver(idx *provider.Index) provider.Resolver { return nil }
