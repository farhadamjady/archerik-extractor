// Package quarkus is the Quarkus framework provider (JVM, Recipe A). Quarkus
// REST is JAX-RS (Jakarta/`jakarta.ws.rs`): the HTTP verb and the path are
// SEPARATE annotations (@GET + @Path), unlike Spring/Micronaut where one
// annotation carries both — so this provider owns its own REST rules while still
// reusing the shared Java language layer (parser, evaluator, symbol/type/schema
// indexers) verbatim, plus the neutral HTTPContracts machinery for the
// API-interface pattern (which JAX-RS uses heavily).
package quarkus

import (
	"bytes"

	"github.com/farhadamjady/archerik-extractor/internal/provider"
	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/java"
)

// Provider detects and extracts from Quarkus services.
type Provider struct{}

// New returns the Quarkus provider.
func New() *Provider { return &Provider{} }

func (*Provider) Name() string { return "quarkus-java" }

// Language is Java. A Kotlin Quarkus service would ship as its own provider.
func (*Provider) Language() string { return "Java" }

// Match scores the repo on Quarkus markers: `quarkus` in a build file, and the
// strongest signal, a source importing io.quarkus or the Quarkus JAX-RS stack.
// JAX-RS annotations alone are NOT enough (other frameworks use them), so the
// io.quarkus marker is required to distinguish Quarkus from a plain JAX-RS app.
func (*Provider) Match(root string, fs provider.FileTree) (bool, int) {
	score := 0
	for _, bf := range []string{"pom.xml", "build.gradle", "build.gradle.kts"} {
		if b, err := fs.Read(bf); err == nil {
			if bytes.Contains(b, []byte("io.quarkus")) || bytes.Contains(b, []byte("quarkus")) {
				score += 2
			}
		}
	}
	for _, f := range fs.Glob("**/*.java") {
		if b, err := fs.Read(f); err == nil && bytes.Contains(b, []byte("import io.quarkus")) {
			score += 3
			break
		}
	}
	return score > 0, score
}

// FileSpec: same file kinds as the other JVM providers (Quarkus uses
// application.properties/yaml + Kafka/OpenAPI/deploy config).
func (*Provider) FileSpec() provider.FileSpec {
	return provider.FileSpec{
		Groups: []provider.FileGroup{
			{Kind: provider.KindJava, Include: []string{"**/*.java"}},
			{Kind: provider.KindSpringConfig, Include: []string{
				"**/application*.properties",
				"**/application*.yml",
				"**/application*.yaml",
			}},
			{Kind: provider.KindKafkaSchema, Include: []string{
				"**/*.avsc", "**/*.proto", "**/*.schema.json",
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

func (*Provider) Parsers() map[provider.FileKind]provider.Parser {
	return map[provider.FileKind]provider.Parser{
		provider.KindJava:         java.NewParser(),
		provider.KindSpringConfig: rawParser{kind: provider.KindSpringConfig},
		provider.KindKafkaSchema:  rawParser{kind: provider.KindKafkaSchema},
		provider.KindDeployConfig: rawParser{kind: provider.KindDeployConfig},
		provider.KindOpenAPI:      rawParser{kind: provider.KindOpenAPI},
	}
}

// Indexers: the language-neutral set reused from lang/java + the contract
// indexer for the API-interface pattern (JAX-RS resources very commonly put
// the @Path/@GET on an interface the resource implements). Config/messaging
// resolution lands in a later round.
func (*Provider) Indexers() []provider.Indexer {
	return []provider.Indexer{
		symbolIndexer{},
		typeIndexer{},
		methodIndexer{},
		schemaSourceIndexer{},
		contractIndexer{},
		configIndexer{},
	}
}

// Detectors: REST endpoints (@Path + @GET/@POST/...), the MicroProfile REST
// client (@RegisterRestClient), the JAX-RS programmatic client
// (ClientBuilder.newClient()...target(...)), and MicroProfile Reactive Messaging
// (@Incoming/@Outgoing/@Channel), whose channel→topic mapping and Kafka-vs-in-
// memory connector distinction are read from the parsed application config.
func (*Provider) Detectors() []provider.Detector {
	return []provider.Detector{
		restDetector{},
		restClientDetector{},
		jaxrsClientDetector{},
		messagingDetector{},
	}
}

func (*Provider) NewResolver(idx *provider.Index) provider.Resolver {
	return java.NewEvaluator(idx)
}
