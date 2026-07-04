// Package provider defines the language/framework seam. Everything specific to a
// stack (Spring Boot today; Go, Node, Python later) lives behind Provider. The
// orchestrator never names a language — it detects one, selects a Provider, and
// runs that provider's detectors, which all fill the shared model.
//
// Adding a language = implement Provider in its own package and register it in
// internal/registry.Default(). Nothing in model, detect, scan, or pipeline changes.
package provider

import "github.com/farhadamjady/super-discovery/internal/model"

// Provider bundles everything a single language/framework contributes.
type Provider interface {
	// Name is the stable provider id, e.g. "spring-boot-java".
	Name() string

	// Match reports whether this provider handles the repo rooted at root, and a
	// score used to disambiguate when several providers match. Higher = more
	// certain. A score of 0 (or matched=false) means "not mine".
	Match(root string, fs FileTree) (matched bool, score int)

	// FileSpec declares which files the scanner should read for this provider.
	FileSpec() FileSpec

	// Parser turns source files into the AST the provider's detectors consume.
	Parser() Parser

	// Detectors are run in order, each appending to the shared model.Service.
	Detectors() []Detector
}

// ConfigBuilder is an OPTIONAL provider capability. Config resolution is a
// per-stack idiom (Spring's application.yml/.properties + ${...} placeholders),
// so it is not part of the core Provider interface. The pipeline calls it when a
// provider implements it, and hands the result to detectors via ScanContext.
type ConfigBuilder interface {
	BuildConfig(fs FileTree) (Config, error)
}

// FileTree is a read-only, repo-relative view of files, used during both
// detection and scanning. Paths use forward slashes.
type FileTree interface {
	Exists(rel string) bool
	Read(rel string) ([]byte, error)
	// Glob returns repo-relative paths matching a glob pattern. Supports "**" for
	// any number of path segments and "*"/"?" within a segment.
	Glob(pattern string) []string
}

// FileSpec declares what a provider needs read, and what to skip. Exclusions keep
// src/test, generated code, and build output from inflating the graph.
type FileSpec struct {
	Include []string // globs to read
	Exclude []string // globs to skip
}

// Parser turns a single source file into an opaque ParsedFile. The concrete
// implementation (tree-sitter Java for Spring) lives inside its provider package.
type Parser interface {
	Parse(path string, src []byte) (ParsedFile, error)
}

// ParsedFile is an opaque parsed unit. Detectors within a provider type-assert it
// to their provider's concrete type; the core packages never inspect it.
type ParsedFile interface{}

// Detector fills the shared model from parsed sources. One concern per detector
// (REST, Feign, RestTemplate, WebClient, Kafka, DB), so each is independently
// testable and added one at a time.
type Detector interface {
	Name() string
	Detect(ctx *ScanContext, svc *model.Service) error
}

// ScanContext is handed to every detector: the parsed files plus resolved config.
type ScanContext struct {
	Root   string
	Files  FileTree
	Parsed map[string]ParsedFile
	Config Config
}

// Config resolves ${...} placeholders to concrete values. Detectors MUST resolve
// through it before emitting an edge; unresolved targets are emitted as
// unknown/external with Uncertain confidence.
type Config interface {
	Resolve(placeholder string) (value string, ok bool)
}
