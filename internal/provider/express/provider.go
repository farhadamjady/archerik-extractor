// Package express is the Express (Node.js) framework provider, on the Recipe-B
// lang/tsjs layer. Unlike NestJS's decorators, Express declares routes with
// CALLS — `app.get('/users', handler)`, `router.post('/:id', ...)` — so the
// detector is call-based (like the Spring functional-router detector) rather than
// annotation-based. Express apps are .js or .ts.
package express

import (
	"bytes"

	"github.com/farhadamjady/archerik-extractor/internal/provider"
	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/tsjs"
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
// dependency alone must not steal it).
//
// Real usage — an `express` import in a source file — is REQUIRED, never just the
// package.json dependency. A dependency alone proves nothing: it arrives
// transitively (aws-serverless-express pulls express in) or sits vestigially in a
// repo that routes through something else entirely, e.g. an AWS SAM template
// declaring its routes as Lambda API events. Matching such a repo would claim it,
// find no `app.get(...)` calls, and emit an empty graph — a service that looks
// like it has no endpoints and no dependencies. Failing to match instead lets
// detection fail loudly (exit 2), which is the honest answer.
func (*Provider) Match(root string, fs provider.FileTree) (bool, int) {
	pkg, _ := fs.Read("package.json")
	if bytes.Contains(pkg, []byte("@nestjs/")) {
		return false, 0 // NestJS territory
	}
	srcs := append(fs.Glob("**/*.js"), fs.Glob("**/*.ts")...)
	if len(srcs) == 0 {
		return false, 0
	}
	used := false
	for _, f := range srcs {
		if b, err := fs.Read(f); err == nil && importsExpress(b) {
			used = true
			break
		}
	}
	if !used {
		return false, 0
	}
	score := 3
	if bytes.Contains(pkg, []byte(`"express"`)) {
		score += 2 // usage AND a declared dependency: the strongest signal
	}
	return true, score
}

// importsExpress reports whether a source pulls in express itself. Deliberately
// literal: `express-session`, `aws-serverless-express` and friends are different
// packages and must not count as express usage.
func importsExpress(src []byte) bool {
	return bytes.Contains(src, []byte("require('express')")) ||
		bytes.Contains(src, []byte("require(\"express\")")) ||
		bytes.Contains(src, []byte("from 'express'")) ||
		bytes.Contains(src, []byte("from \"express\""))
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
