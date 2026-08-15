// Package spring is the Spring Boot framework provider — the only provider in
// the MVP (Micronaut planned next). It owns Spring's detection markers, the
// annotation query rules, and Spring config idioms, and delegates Java parsing
// to the shared language layer (provider/lang/java), per the framework-over-
// language seam.
package spring

import (
	"bytes"

	"github.com/farhadamjady/archerik-extractor/internal/provider"
	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/java"
)

// Provider detects and extracts from Spring Boot services.
type Provider struct{}

// New returns the Spring Boot provider.
func New() *Provider { return &Provider{} }

func (*Provider) Name() string { return "spring-boot-java" }

// Language is Java: this provider parses **/*.java only. A Kotlin Spring service
// would ship as its own provider returning "Kotlin".
func (*Provider) Language() string { return "Java" }

// Match scores the repo on Spring Boot markers. Build-file presence is a weak
// signal; a spring-boot dependency or a @SpringBootApplication class confirms it.
func (*Provider) Match(root string, fs provider.FileTree) (bool, int) {
	score := 0

	// A JVM build file scores, but is NOT on its own evidence of Spring: every
	// Maven/Gradle project has one. Matching on its mere existence claimed any
	// Java repo at all — a plain library with a pom.xml was "a Spring service",
	// scanned, and emitted as a service with no endpoints and no dependencies.
	// Only a declared Spring dependency or Spring in the sources decides.
	spring := false
	if fs.Exists("pom.xml") {
		score++
		if b, err := fs.Read("pom.xml"); err == nil && isSpringBuild(b) {
			spring = true
			score += 2
		}
	}
	for _, bf := range []string{"build.gradle", "build.gradle.kts"} {
		if b, err := fs.Read(bf); err == nil {
			score++
			if isSpringBuild(b) {
				spring = true
				score += 2
			}
		}
	}

	// Source signals, strongest first: the application entrypoint annotation, then
	// any Spring import (a module of a multi-module app carries controllers but no
	// @SpringBootApplication, and inherits its dependencies from a parent POM).
	for _, f := range fs.Glob("**/*.java") {
		b, err := fs.Read(f)
		if err != nil {
			continue
		}
		if bytes.Contains(b, []byte("@SpringBootApplication")) {
			spring = true
			score += 3
			break
		}
		if bytes.Contains(b, []byte("org.springframework")) {
			spring = true
			score += 2
			break
		}
	}

	if !spring {
		return false, 0
	}
	return true, score
}

// isSpringBuild reports whether a build file declares Spring — the coordinates,
// not an incidental mention.
func isSpringBuild(b []byte) bool {
	return bytes.Contains(b, []byte("spring-boot")) || bytes.Contains(b, []byte("org.springframework"))
}

// FileSpec groups what to read by kind: Java sources, Spring config (all
// profiles), and Kafka schema files. KindDeployConfig globs (Helm values*.yaml
// + templates, K8s manifests, .env) land with the DeployConfig indexer.
// Test, generated, and build output are excluded so they don't inflate the
// graph with false edges.
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
// config and schema files are carried raw — their real parsing belongs to the
// ConfigResolver indexer and the Kafka contract parsers respectively.
func (*Provider) Parsers() map[provider.FileKind]provider.Parser {
	return map[provider.FileKind]provider.Parser{
		provider.KindJava:         java.NewParser(),
		provider.KindSpringConfig: rawParser{kind: provider.KindSpringConfig},
		provider.KindKafkaSchema:  rawParser{kind: provider.KindKafkaSchema},
		provider.KindDeployConfig: rawParser{kind: provider.KindDeployConfig},
		provider.KindOpenAPI:      rawParser{kind: provider.KindOpenAPI},
	}
}

// Indexers build the shared cross-file Index. They land one at a time:
// ConfigResolver (Spring application.*) now; DeployConfig (Helm/K8s/.env),
// SymbolIndex (constants), TypeIndex (DTOs), SchemaSources (contract files) next.
func (*Provider) Indexers() []provider.Indexer {
	return []provider.Indexer{
		configIndexer{},
		deployIndexer{},
		symbolIndexer{},
		typeIndexer{},
		schemaSourceIndexer{},
		adapterIndexer{},
		kafkaTopicIndexer{},
		outboxIndexer{},
	}
}

// Detectors, one concern each, in build order: REST endpoints first, then the
// HTTP clients, then Kafka. DB detection is deferred post-MVP and OpenAPI is
// cut, so neither appears here.
func (*Provider) Detectors() []provider.Detector {
	return []provider.Detector{
		restDetector{},
		feignDetector{},
		restTemplateDetector{},
		webClientDetector{},
		kafkaDetector{},
		reactiveKafkaDetector{},
		httpExchangeDetector{},
		cloudStreamDetector{},
		adapterDetector{},
		routerDetector{},
	}
}

// NewResolver returns the shared Java value evaluator bound to the Index.
func (*Provider) NewResolver(idx *provider.Index) provider.Resolver {
	return java.NewEvaluator(idx)
}
