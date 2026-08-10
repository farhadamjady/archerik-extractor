// Package express is the Express (Node.js) framework provider, on the Recipe-B
// lang/tsjs layer. Unlike NestJS's decorators, Express declares routes with
// CALLS — `app.get('/users', handler)`, `router.post('/:id', ...)` — so the
// detector is call-based (like the Spring functional-router detector) rather than
// annotation-based. Express apps are .js or .ts.
package express

import (
	"bytes"

	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/tsjs"
)

// Provider detects and extracts from Express services.
type Provider struct{}

// New returns the Express provider.
func New() *Provider { return &Provider{} }

func (*Provider) Name() string { return "express-node" }

// Language is JavaScript (Express's lingua franca; a TS Express app is still
// emitted as JavaScript-ecosystem here — the backend treats it as one node).
func (*Provider) Language() string { return "JavaScript" }

// Match scores an Express repo. It DEFERS to NestJS: a repo with a @nestjs
// dependency is NestJS's (NestJS runs on Express under the hood, so the express
// dependency alone must not steal it). Otherwise it requires the express
// dependency plus real usage (an `express()`/`require('express')` in a source).
func (*Provider) Match(root string, fs provider.FileTree) (bool, int) {
	pkg, _ := fs.Read("package.json")
	if bytes.Contains(pkg, []byte("@nestjs/")) {
		return false, 0 // NestJS territory
	}
	srcs := append(fs.Glob("**/*.js"), fs.Glob("**/*.ts")...)
	if len(srcs) == 0 {
		return false, 0
	}
	score := 0
	if bytes.Contains(pkg, []byte(`"express"`)) {
		score += 2
	}
	for _, f := range srcs {
		if b, err := fs.Read(f); err == nil &&
			(bytes.Contains(b, []byte("require('express')")) ||
				bytes.Contains(b, []byte("require(\"express\")")) ||
				bytes.Contains(b, []byte("from 'express'")) ||
				bytes.Contains(b, []byte("from \"express\""))) {
			score += 3
			break
		}
	}
	return score > 0, score
}

// FileSpec collects JS and TS sources; node_modules/dist/tests excluded.
func (*Provider) FileSpec() provider.FileSpec {
	return provider.FileSpec{
		Groups: []provider.FileGroup{
			{Kind: provider.KindJava, Include: []string{"**/*.js", "**/*.mjs", "**/*.ts"}},
		},
		Exclude: []string{
			"**/node_modules/**",
			"**/dist/**",
			"**/*.spec.js", "**/*.test.js",
			"**/*.spec.ts", "**/*.test.ts",
			"**/*.d.ts",
			"**/test/**",
		},
	}
}

func (*Provider) Parsers() map[provider.FileKind]provider.Parser {
	return map[provider.FileKind]provider.Parser{
		provider.KindJava: tsjs.NewParser(),
	}
}

// Indexers: the mount indexer resolves the cross-file app.use/router.use graph
// so routes are emitted at their FULL mounted paths; the type indexer builds
// the TS DTO index for typed-handler schema inference.
func (*Provider) Indexers() []provider.Indexer {
	return []provider.Indexer{mountIndexer{}, typeIndexer{}}
}

// Detectors: route calls (app/router.get/post/...), plus outbound HTTP
// dependencies from axios/fetch/got client call sites.
func (*Provider) Detectors() []provider.Detector {
	return []provider.Detector{
		routeDetector{},
		clientDetector{},
	}
}

func (*Provider) NewResolver(idx *provider.Index) provider.Resolver { return nil }
