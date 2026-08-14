// Package micronaut is the Micronaut framework provider (JVM, Recipe A). It owns
// Micronaut's detection markers, annotation query rules, and config idioms, and
// delegates Java parsing to the shared language layer (provider/lang/java) — the
// framework-over-language seam. A DTO, a constant, and an annotation are
// language facts, so parsing, the value evaluator, and the symbol/type/schema
// indexers are reused as-is from the Java layer; only the annotation NAMES and
// detector rules are Micronaut-specific.
package micronaut

import (
	"bytes"

	"github.com/farhadamjady/archerik-extractor/internal/provider"
	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/java"
)

// Provider detects and extracts from Micronaut services.
type Provider struct{}

// New returns the Micronaut provider.
func New() *Provider { return &Provider{} }

func (*Provider) Name() string { return "micronaut-java" }

// Language is Java: this provider parses **/*.java only. A Kotlin Micronaut
// service would ship as its own provider returning "Kotlin".
func (*Provider) Language() string { return "Java" }

// Match scores the repo on Micronaut markers. A build file names io.micronaut;
// the strongest signal is a source importing io.micronaut (there is no single
// required app annotation like @SpringBootApplication — the runtime is started
// with Micronaut.run/@MicronautApplication or generated Application classes).
// Scoring must beat Spring's weak build-file-only score on a Micronaut repo
// (Spring gives +1 for a bare pom.xml) and stay 0 on a Spring repo.
func (*Provider) Match(root string, fs provider.FileTree) (bool, int) {
	score := 0

	for _, bf := range []string{"pom.xml", "build.gradle", "build.gradle.kts"} {
		if b, err := fs.Read(bf); err == nil {
			if bytes.Contains(b, []byte("io.micronaut")) || bytes.Contains(b, []byte("micronaut")) {
				score += 2
			}
		}
	}

	// Strongest signal: a source importing the Micronaut API.
	for _, f := range fs.Glob("**/*.java") {
		if b, err := fs.Read(f); err == nil && bytes.Contains(b, []byte("import io.micronaut")) {
			score += 3
			break
		}
	}

	return score > 0, score
}

// FileSpec groups what to read by kind. Micronaut uses the same application.*
// config files and ${} placeholder model as Spring, and the same Kafka/OpenAPI/
// deploy config formats — so the file kinds are identical; only the detectors
// differ. Test, generated, and build output are excluded.
func (*Provider) FileSpec() provider.FileSpec {
	return provider.FileSpec{
		Groups: []provider.FileGroup{
			{Kind: provider.KindJava, Include: []string{
				"**/*.java",
			}},
			{Kind: provider.KindSpringConfig, Include: []string{
				"**/application*.yml",
				"**/application*.yaml",
				"**/application*.properties",
			}},
			{Kind: provider.KindKafkaSchema, Include: []string{
				"**/*.avsc",
				"**/*.proto",
				"**/*.schema.json",
			}},
			{Kind: provider.KindOpenAPI, Include: []string{
				"**/openapi*.yml", "**/openapi*.yaml", "**/openapi*.json",
			}},
			{Kind: provider.KindDeployConfig, Include: []string{
				"**/values.yaml", "**/values.yml",
				"**/values-*.yaml", "**/values-*.yml",
				"**/templates/**/*.yaml", "**/templates/**/*.yml",
				"**/Chart.yaml",
				"**/*deployment*.yaml", "**/*deployment*.yml",
				"**/*configmap*.yaml", "**/*configmap*.yml",
				"**/.env", "**/*.env",
			}},
		},
		Exclude: []string{
			"**/src/test/**",
			"**/generated/**",
			"**/build/**",
			"**/target/**",
		},
	}
}

// Parsers routes each collected kind. Java goes to the shared language layer;
// config and schema files are carried raw (parsed by the indexers).
func (*Provider) Parsers() map[provider.FileKind]provider.Parser {
	return map[provider.FileKind]provider.Parser{
		provider.KindJava:         java.NewParser(),
		provider.KindSpringConfig: rawParser{kind: provider.KindSpringConfig},
		provider.KindKafkaSchema:  rawParser{kind: provider.KindKafkaSchema},
		provider.KindDeployConfig: rawParser{kind: provider.KindDeployConfig},
		provider.KindOpenAPI:      rawParser{kind: provider.KindOpenAPI},
	}
}

// Indexers build the shared cross-file Index. Round 1 wires the language-neutral
// indexers (constants, DTOs, Kafka contract files) reused from the Java layer;
// the config/deploy resolver lands when a benchmark repo demands placeholder
// resolution (add the expensive steps only when a benchmark shows a miss).
func (*Provider) Indexers() []provider.Indexer {
	return []provider.Indexer{
		symbolIndexer{},
		typeIndexer{},
		schemaSourceIndexer{},
		contractIndexer{},
	}
}

// Detectors, one concern each: REST endpoints (@Controller + @Get/@Post/...),
// the declarative HTTP client (@Client), and Kafka (@KafkaClient producer /
// @KafkaListener consumer, both via @Topic).
func (*Provider) Detectors() []provider.Detector {
	return []provider.Detector{
		restDetector{},
		clientDetector{},
		kafkaDetector{},
	}
}

// NewResolver returns the shared Java value evaluator bound to the Index — 100%
// framework-free, so it is reused verbatim.
func (*Provider) NewResolver(idx *provider.Index) provider.Resolver {
	return java.NewEvaluator(idx)
}
