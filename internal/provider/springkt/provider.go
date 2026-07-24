// Package springkt is the Spring Boot (Kotlin) framework provider — the first
// non-Java-language provider, built on the Recipe-B lang/kotlin layer. Spring's
// annotations are identical to the Java provider's (@RestController,
// @GetMapping, ...), but the source is Kotlin, so the detectors navigate the
// tree-sitter-kotlin AST (via lang/kotlin) instead of tree-sitter-java. A Kotlin
// Spring service ships as its own provider (Language() = "Kotlin") per the
// language-scoped seam.
package springkt

import (
	"bytes"

	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/kotlin"
)

// Provider detects and extracts from Spring Boot services written in Kotlin.
type Provider struct{}

// New returns the Spring-Kotlin provider.
func New() *Provider { return &Provider{} }

func (*Provider) Name() string { return "spring-boot-kotlin" }

// Language is Kotlin: this provider parses **/*.kt only.
func (*Provider) Language() string { return "Kotlin" }

// Match scores a Kotlin Spring repo. Requires Kotlin sources (else score 0, so
// the Java Spring provider keeps pure-Java repos), and beats the Java provider's
// build-file-only score on a Kotlin repo via the @SpringBootApplication marker
// found in a .kt file.
func (*Provider) Match(root string, fs provider.FileTree) (bool, int) {
	kt := fs.Glob("**/*.kt")
	if len(kt) == 0 {
		return false, 0
	}
	score := 1 // Kotlin sources present

	for _, bf := range []string{"pom.xml", "build.gradle", "build.gradle.kts"} {
		if b, err := fs.Read(bf); err == nil {
			if bytes.Contains(b, []byte("spring-boot")) || bytes.Contains(b, []byte("springframework")) {
				score += 3
				break
			}
		}
	}
	for _, f := range kt {
		if b, err := fs.Read(f); err == nil && bytes.Contains(b, []byte("@SpringBootApplication")) {
			score += 3
			break
		}
	}
	return score > 1, score
}

// FileSpec collects Kotlin sources under the JVM-source kind (KindJava is the
// routing bucket; the parser map sends it to the Kotlin parser). Config/schema
// kinds land when those detectors do. Test/generated/build output excluded.
func (*Provider) FileSpec() provider.FileSpec {
	return provider.FileSpec{
		Groups: []provider.FileGroup{
			{Kind: provider.KindJava, Include: []string{"**/*.kt"}},
		},
		Exclude: []string{
			"**/src/test/**",
			"**/generated/**",
			"**/build/**",
			"**/target/**",
		},
	}
}

// Parsers routes the Kotlin sources to the shared Kotlin language parser.
func (*Provider) Parsers() map[provider.FileKind]provider.Parser {
	return map[provider.FileKind]provider.Parser{
		provider.KindJava: kotlin.NewParser(),
	}
}

// Indexers: none yet — round 1 is REST endpoints only. Symbol/type/config
// indexers land with the client/kafka detectors and schema extraction.
func (*Provider) Indexers() []provider.Indexer { return nil }

// Detectors: REST endpoints from @RestController classes.
func (*Provider) Detectors() []provider.Detector {
	return []provider.Detector{
		restDetector{},
	}
}

// NewResolver: no value resolver yet (endpoints need none). The Kotlin evaluator
// lands with the client/kafka detectors.
func (*Provider) NewResolver(idx *provider.Index) provider.Resolver { return nil }
