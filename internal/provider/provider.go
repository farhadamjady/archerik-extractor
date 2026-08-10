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

import (
	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/resolve"
	"github.com/farhadamjady/service-discovery/internal/schema"
	"github.com/farhadamjady/service-discovery/internal/schema/registry"
)

// FileKind routes a collected file to its parser. The scanner buckets files by
// kind (per FileSpec), and the pipeline feeds each bucket to Parsers()[kind].
type FileKind int

const (
	KindJava         FileKind = iota
	KindSpringConfig          // application.yml/.yaml/.properties (+ profile variants)
	KindKafkaSchema           // .avsc / .proto / JSON Schema contract files
	KindDeployConfig          // Helm values*.yaml + templates, K8s manifests, .env
	KindOpenAPI               // openapi.yml/json — read when controllers are generated from it
)

// Provider bundles everything a single framework contributes.
type Provider interface {
	// Name is the stable provider id, e.g. "spring-boot-java".
	Name() string

	// Language is the canonical, human-cased primary implementation language of
	// services this provider handles ("Java", "Kotlin", "Go", ...). One value per
	// provider — the framework seam is language-scoped (a Kotlin Spring service
	// would be a separate provider). Emitted verbatim as Service.language; the
	// backend stores it without normalization, so keep the casing consistent.
	Language() string

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

	// NewResolver returns this framework's value/target resolver bound to the
	// built Index. Called once after indexing; the pipeline passes it to every
	// detector via MatchContext. May return nil (no in-code resolution).
	NewResolver(idx *Index) Resolver
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

// Index is the shared cross-file knowledge built by Indexers before
// detection. Non-AST facts are read from here, uniformly: detector handlers resolve
// placeholders, constants, types, and schema files through the Index instead
// of parsing anything themselves.
type Index struct {
	Config  ConfigResolver
	Types   schema.TypeSource
	Symbols SymbolIndex
	Schemas schema.SchemaSources

	// Registry is the detected Kafka Schema Registry configuration (URL +
	// subject-name strategy) read statically from the config layer (no 	// network), or nil when no registry is configured. Groundwork: a later round
	// uses it to resolve topic subjects; nothing consumes it yet.
	Registry *registry.Config

	// Adapters are company-specific wrapper declarations from .ekg-adapters.json
	// in the repo: internal SDK calls like platformClient.call("payment-service",
	// ...) that no generic detector can recognize. Each entry names a method and
	// which argument is the target.
	Adapters []AdapterSpec

	// TopicBeans are Kafka `@Bean NewTopic` methods built via
	// TopicBuilder.name(x) . Big-company producers rarely pass a literal topic to
	// KafkaTemplate.send: they inject the NewTopic bean and set the destination
	// through a Message header (KafkaHeaders.TOPIC, topic.name()). Indexing each
	// bean's name-argument lets the producer detector resolve that topic through
	// the existing config layer instead of emitting an anonymous uncertain edge.
	TopicBeans []TopicBean

	// OutboxRoutes are Debezium outbox EventRouter topic patterns found in
	// Kafka-Connect connector JSONs near the repo root, e.g.
	// "${routedByValue}.events". In the outbox pattern the service "produces" by
	// inserting a row — the topic only materializes in the connector config, so
	// the producer detector joins these patterns with the aggregate-type values
	// found in the service's builder calls.
	OutboxRoutes []string

	// MountPrefixes maps a source file (repo-relative path) to the URL prefix(es)
	// its routes are mounted under. Express composes routes across files —
	// app.use('/v1', routes) in one file mounts a router module defined in
	// another (often NESTED: '/v1' -> '/users' -> the route file) — so a mount
	// indexer resolves the require/import graph and stores the composed
	// prefixes here; the route detector prepends them. A file absent
	// from the map (or an empty list) is unmounted: its routes are emitted at
	// their declared paths.
	MountPrefixes map[string][]string

	// HTTPContracts map an API interface / abstract-type simple name to the
	// declarative HTTP handler-method nodes it declares. Under the API-interface
	// pattern (dominant in Micronaut, common in Quarkus/JAX-RS and Spring), a
	// @Controller implements an interface whose methods carry the mapping
	// annotations (@Get/@Post/@Path/...) — and that interface usually lives in a
	// sibling *-api module the detector never scans. Indexers see sibling modules
	// (IndexContext.Shared), so a contract indexer stores the mapped method nodes
	// here; the REST detector composes them with the implementing controller's
	// base path. Values are lang/java.Node kept opaque (like TopicBeans.NameArg).
	HTTPContracts map[string][]ASTNode

	// SchemaDepth is the nested-DTO walk depth (--schema-depth), threaded to
	// every REST schema walker. 0 means unset — the walker falls back to its
	// default (2).
	SchemaDepth int

	// GoFuncBodies maps a Go function/method simple name to its declaration's
	// body node, indexed across ALL scanned files so the net/http route detector
	// can follow a handler referenced by name (`getUsers` / `h.GetUsers`) into
	// another file and resolve its request/response schema — Go handlers carry no
	// declared types, so the body is the only source. Method names can collide
	// across receivers (the selector gives the method name, not the type); the
	// first declaration in path-sorted order wins, so resolution stays
	// deterministic and best-effort. Values are lang/golang.Node kept opaque here
	// (like HTTPContracts).
	GoFuncBodies map[string]ASTNode

	// MethodReturns maps a type's SIMPLE name to each of its methods' declared
	// return-type text: MethodReturns["OrderService"]["findAll"] = "Page<OrderDTO>".
	// Built across parsed+shared files so a detector can resolve the payload of a
	// method-call expression (`orderService.findAll(page)`) when the declaring
	// signature erases it — e.g. a JAX-RS handler returning the typeless
	// `Response` whose body is `Response.ok().entity(orderService.findAll(...))`
	// (#64). Best-effort, overloads collapse to the first declaration seen.
	MethodReturns map[string]map[string]string
}

// TopicBean is one Kafka `@Bean NewTopic` declaration: the bean/method name and
// the TopicBuilder.name(<arg>) argument expression, resolved lazily by the
// producer detector (NameArg is a lang/java.Node, kept opaque here so this
// package does not depend on the language layer).
type TopicBean struct {
	Name    string  // @Bean method name (= Spring bean name)
	NameArg ASTNode // the TopicBuilder.name(<arg>) argument expression
}

// AdapterSpec declares one wrapper-method pattern.
type AdapterSpec struct {
	Method    string         `json:"method"`     // invocation name to match, e.g. "callService"
	TargetArg int            `json:"target_arg"` // index of the argument holding the target
	Protocol  model.Protocol `json:"protocol"`   // rest | kafka | ... (default rest)
}

// IndexContext is what Indexers read from: the file tree, the parsed buckets,
// and the run's scan options that indexers need (active Spring profiles and the
// deploy overlay), threaded from the CLI so indexers stay constructor-free.
type IndexContext struct {
	Root        string
	Files       FileTree
	Parsed      map[string]ParsedFile // keyed by repo-relative path
	Profiles    []string              // active Spring profiles; overrides spring.profiles.active
	Environment string                // deploy overlay selection
	ConfigRepo  string                // local checkout of an external config repo

	// Shared holds parsed files from SIBLING Maven modules of the same repo (a
	// shared contracts/domain module). Indexers may read types and constants from
	// them, but detectors never run on them — edges belong to the scanned service
	// only.
	Shared map[string]ParsedFile
}

// Indexer populates one member of the shared Index.
type Indexer interface {
	Name() string
	Index(ic *IndexContext, idx *Index) error
}

// ConfigResolver resolves ${...} placeholders through the LAYERED config
// sources: Spring application.* (active profiles) first, then externalized
// deployment config (Helm values / K8s env / .env), then
// ${x:default}.
//
// Declared in its final layered shape now so the seam doesn't
// change when the deploy layer lands: Resolve returns the single best value;
// Candidates surfaces divergent env overlays (staging vs prod) so detectors
// can emit one edge per candidate.
type ConfigResolver interface {
	// Resolve returns the single best value plus its provenance: source names
	// the config file the value came from (e.g. "application-prod.yml",
	// "values-staging.yaml", ".env", "default"), blank when placeholder isn't a
	// lone ${key} (a mixed expression has no single honest source to name).
	Resolve(placeholder string) (value string, conf model.Confidence, source string, ok bool)
	Candidates(placeholder string) []ResolvedValue
}

// ResolvedValue is one candidate resolution of a placeholder, with provenance.
type ResolvedValue struct {
	Value  string
	Conf   model.Confidence
	Source string // e.g. "application.yml", "values-staging.yaml", "configmap/foo", ".env"
	Origin string // profile or overlay name — drives the `likely` confidence cap
}

// SymbolIndex resolves compile-time constants to their literal values,
// e.g. "OrderTopics.ORDERS" -> "orders".
type SymbolIndex interface {
	Constant(qualified string) (value string, ok bool)
}

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

// SpecIngester is an OPTIONAL provider capability: after detection, ingest
// endpoints declared in spec files (OpenAPI) that the source scan cannot see —
// e.g. controllers generated from openapi.yml at build time. The provider
// decides when ingestion applies (e.g. only when the build uses
// openapi-generator) and must dedup against endpoints already found in code.
type SpecIngester interface {
	IngestSpecs(ic *IndexContext, svc *model.Service) error
}

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

// Resolver is the shared protocol-agnostic value/target resolver: handlers
// hand it an ASTNode expression and get back the possible string values as a
// lattice ValueSet. The Java implementation lands in provider/lang/java (PR
// 14-15); MatchContext.Resolver is nil until then.
type Resolver interface {
	Resolve(node ASTNode) resolve.ValueSet
}
