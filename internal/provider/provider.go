// Package provider defines the framework seam. Everything stack-specific
// (Spring Boot today, Micronaut next) lives behind Provider; the core never
// names a framework. The seam is per-FRAMEWORK over a shared language layer
// (provider/lang/java): the language layer owns parsing and language-generic
// indexing, the framework provider owns detection markers, query rules, and
// config idioms.
//
// Adding a framework = implement Provider in its own package and register it
// in internal/registry.Default(). Nothing in model, detect, scan, or pipeline
// changes.
package provider

import "github.com/farhadamjady/service-discovery/internal/model"

// FileKind routes a collected file to its parser. The scanner buckets files by
// kind (per FileSpec), and the pipeline feeds each bucket to Parsers()[kind].
type FileKind int

const (
	KindJava         FileKind = iota
	KindSpringConfig          // application.yml/.yaml/.properties (+ profile variants)
	KindKafkaSchema           // .avsc / .proto / JSON Schema contract files
	KindDeployConfig          // Helm values*.yaml + templates, K8s manifests, .env (DESIGN §8.5)
)

// Provider bundles everything a single framework contributes.
type Provider interface {
	// Name is the stable provider id, e.g. "spring-boot-java".
	Name() string

	// Match reports whether this provider handles the repo rooted at root, and a
	// score used to disambiguate when several providers match. Higher = more
	// certain. A score of 0 (or matched=false) means "not mine".
	Match(root string, fs FileTree) (matched bool, score int)

	// FileSpec declares which files the scanner should read, grouped by kind.
	FileSpec() FileSpec

	// Parsers maps each FileKind this provider collects to its parser.
	Parsers() map[FileKind]Parser

	// Indexers build the shared cross-file Index (config, types, symbols,
	// schemas) after parsing and before detection. They own ALL cross-file and
	// non-Java parsing; detector handlers only look values up.
	Indexers() []Indexer

	// Detectors declare the query rules for this framework, one concern each.
	Detectors() []Detector
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

// FileSpec declares what a provider needs read, grouped by FileKind so the
// scanner can route each file to the right parser. Exclusions keep src/test,
// generated code, and build output from inflating the graph.
type FileSpec struct {
	Groups  []FileGroup
	Exclude []string // globs to skip, applied to every group
}

// FileGroup is one kind's include globs.
type FileGroup struct {
	Kind    FileKind
	Include []string
}

// Parser turns a single source file into a ParsedFile. Concrete parsers live in
// the language layer (tree-sitter Java) or the provider package.
type Parser interface {
	Parse(path string, src []byte) (ParsedFile, error)
}

// ParsedFile is an opaque parsed unit. Detector handlers and indexers
// type-assert it to the concrete type of its kind; core packages never inspect it.
type ParsedFile interface {
	Path() string
	Kind() FileKind
}

// Index is the shared cross-file knowledge built by Indexers before detection
// (DESIGN §7). Non-AST facts are read from here, uniformly: detector handlers
// resolve placeholders, constants, types, and schema files through the Index
// instead of parsing anything themselves.
type Index struct {
	Config  ConfigResolver
	Types   TypeIndex
	Symbols SymbolIndex
	Schemas SchemaSources
}

// IndexContext is what Indexers read from: the file tree and the parsed buckets.
type IndexContext struct {
	Root   string
	Files  FileTree
	Parsed map[string]ParsedFile // keyed by repo-relative path
}

// Indexer populates one member of the shared Index.
type Indexer interface {
	Name() string
	Index(ic *IndexContext, idx *Index) error
}

// ConfigResolver resolves ${...} placeholders through the LAYERED config
// sources: Spring application.* (active profiles) first, then externalized
// deployment config (Helm values / K8s env / .env, DESIGN §8.5), then
// ${x:default}.
//
// Declared in its final layered shape now (PLAN T4.5.5) so the seam doesn't
// change when the deploy layer lands: Resolve returns the single best value;
// Candidates surfaces divergent env overlays (staging vs prod) so detectors
// can emit one edge per candidate.
type ConfigResolver interface {
	Resolve(placeholder string) (value string, conf model.Confidence, ok bool)
	Candidates(placeholder string) []ResolvedValue
}

// ResolvedValue is one candidate resolution of a placeholder, with provenance.
type ResolvedValue struct {
	Value  string
	Conf   model.Confidence
	Source string // e.g. "application.yml", "values-staging.yaml", "configmap/foo", ".env"
	Origin string // profile or overlay name — drives the `likely` confidence cap
}

// TypeIndex indexes repo DTOs (fields, getters, ctor params, superclass,
// annotations, imports). Its query surface lands with the TypeIndex indexer
// (schema pass); until then it is a placeholder so Index has its final shape.
type TypeIndex interface{}

// SymbolIndex resolves compile-time constants to their literal values,
// e.g. "OrderTopics.ORDERS" -> "orders".
type SymbolIndex interface {
	Constant(qualified string) (value string, ok bool)
}

// SchemaSources indexes Kafka contract files (.avsc/.proto/JSON Schema) found
// in the repo. Its query surface lands with the Kafka schema pass; placeholder
// until then, like TypeIndex.
type SchemaSources interface{}

// Detector declares the tree-sitter query rules for ONE concern (REST, Feign,
// RestTemplate, WebClient, Kafka) carrying ONE protocol. The query engine runs
// every detector's rules in a single traversal per file and dispatches matches
// to OnMatch handlers — detectors never traverse files themselves.
type Detector interface {
	Name() string
	Protocol() model.Protocol
	Rules() []Rule
}

// Rule pairs a tree-sitter query with its match handler.
type Rule struct {
	// Query is a tree-sitter S-expression over the file's parse tree. One query
	// can capture class-level and method-level annotations together (REST path
	// composition needs both in a single match).
	Query string

	// OnMatch is invoked once per match with the named captures.
	OnMatch func(mc *MatchContext)
}

// MatchContext is handed to a rule's handler for each match: the captures, the
// cross-file Index to look values up in, the value resolver for dynamic target
// expressions, and the output Service to append edges to.
type MatchContext struct {
	File     ParsedFile
	Captures map[string]ASTNode
	Index    *Index
	Resolver Resolver
	Out      *model.Service
}

// ASTNode is an opaque parse-tree node. Handlers type-assert it to the language
// layer's concrete node type (lang/java.Node), mirroring ParsedFile.
type ASTNode interface{}

// QueryRunner is a LANGUAGE capability implemented by parsed files whose grammar
// supports tree-sitter queries. It runs a set of query patterns over the file in
// a SINGLE traversal, invoking onMatch per match with the index of the pattern
// that produced it and that match's named captures. The query engine
// (internal/query) uses this so it never names a specific grammar; parsed files
// of non-queryable kinds (config, schema) simply don't implement it.
//
// Each element of patterns MUST be exactly one top-level query pattern, so
// patternIndex maps 1:1 back to the rule that supplied it — use multiple Rules
// for multiple patterns.
type QueryRunner interface {
	RunQuery(patterns []string, onMatch func(patternIndex int, captures map[string]ASTNode)) error
}

// Resolver is the shared protocol-agnostic value/target resolver (DESIGN §8):
// handlers hand it an ASTNode expression and get the possible string values
// back. Its concrete interface lands with internal/resolve; placeholder here so
// MatchContext has its final shape.
type Resolver interface{}
