// Package spring is the Spring Boot (Java) provider — the only provider in the
// MVP. It implements provider.Provider and provider.ConfigBuilder. Everything
// Spring-specific (annotation detectors, application.yml resolution, tree-sitter
// Java parsing) lives under this package, behind the provider seam.
package spring

import (
	"bytes"

	"github.com/farhadamjady/super-discovery/internal/provider"
)

// Provider detects and extracts from Spring Boot services.
type Provider struct{}

// New returns the Spring Boot provider.
func New() *Provider { return &Provider{} }

func (*Provider) Name() string { return "spring-boot-java" }

// Match scores the repo on Spring Boot markers. Build-file presence is a weak
// signal; a spring-boot dependency or a @SpringBootApplication class confirms it.
func (*Provider) Match(root string, fs provider.FileTree) (bool, int) {
	score := 0

	if fs.Exists("pom.xml") {
		score++
		if b, err := fs.Read("pom.xml"); err == nil && bytes.Contains(b, []byte("spring-boot")) {
			score += 2
		}
	}
	if fs.Exists("build.gradle") || fs.Exists("build.gradle.kts") {
		score++
	}

	// Strongest signal: the application entrypoint annotation.
	for _, f := range fs.Glob("**/*.java") {
		if b, err := fs.Read(f); err == nil && bytes.Contains(b, []byte("@SpringBootApplication")) {
			score += 3
			break
		}
	}

	return score > 0, score
}

// FileSpec lists what to read: Java sources, Spring config (all profiles),
// OpenAPI specs, and Kafka schema files. Test, generated, and build output are
// excluded so they don't inflate the graph with false edges.
func (*Provider) FileSpec() provider.FileSpec {
	return provider.FileSpec{
		Include: []string{
			"**/*.java",
			"**/application*.yml",
			"**/application*.yaml",
			"**/application*.properties",
			"**/openapi*.yaml", "**/openapi*.yml", "**/openapi*.json",
			"**/*.avsc", "**/*.proto",
		},
		Exclude: []string{
			"**/src/test/**",
			"**/generated/**",
			"**/build/**",
			"**/target/**",
		},
	}
}

func (*Provider) Parser() provider.Parser { return treeSitterParser{} }

// Detectors run in dependency order (build-order step 3): Feign -> RestTemplate
// -> WebClient -> Kafka -> DB, with REST endpoints first.
func (*Provider) Detectors() []provider.Detector {
	return []provider.Detector{
		restDetector{},
		feignDetector{},
		restTemplateDetector{},
		webClientDetector{},
		kafkaDetector{},
		dbDetector{},
	}
}
