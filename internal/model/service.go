// Package model is THE CONTRACT: the language-agnostic output structs that every
// detector fills and the backend consumes. It holds no logic — just the shape of
// the JSON emitted, one file per service.
package model

// Service is the one-JSON-per-service output contract.
//
// NOTE: inbound_dependencies is intentionally absent — the backend derives those
// from everyone's outbound edges. Emit raw; the backend stores and maps.
type Service struct {
	ServiceID   string `json:"service_id"`
	ServiceName string `json:"service_name"`
	Repository  string `json:"repository"`
	// Language is the service's primary implementation language in canonical
	// human casing ("Java", "Kotlin", "Go", ...) — one value per service, the
	// dominant language of the module, not a per-file count or a list. Optional:
	// omitempty drops the key when undetermined, which the backend treats
	// identically to null. Set from the winning provider (Provider.Language).
	Language             string       `json:"language,omitempty"`
	Endpoints            []Endpoint   `json:"endpoints"`
	OutboundDependencies []Dependency `json:"outbound_dependencies"`
	KafkaProducers       []KafkaEdge  `json:"kafka_producers"`
	KafkaConsumers       []KafkaEdge  `json:"kafka_consumers"`
	DatabasesUsed        []Database   `json:"databases_used"`
	ConfigDependencies   []ConfigDep  `json:"config_dependencies"`
}

// NewService returns a Service with non-nil slices so the JSON emits empty
// arrays ([]) rather than null — a stable contract for the backend.
func NewService(id, name, repository string) *Service {
	return &Service{
		ServiceID:            id,
		ServiceName:          name,
		Repository:           repository,
		Endpoints:            []Endpoint{},
		OutboundDependencies: []Dependency{},
		KafkaProducers:       []KafkaEdge{},
		KafkaConsumers:       []KafkaEdge{},
		DatabasesUsed:        []Database{},
		ConfigDependencies:   []ConfigDep{},
	}
}

// Endpoint is a REST endpoint exposed by this service. Path is the composition of
// the class-level @RequestMapping and the method-level mapping, with the HTTP verb
// preserved and path variables kept intact (e.g. GET /users/{id}).
type Endpoint struct {
	Method     string          `json:"method"` // GET/POST/PUT/DELETE/PATCH
	Path       string          `json:"path"`
	Request    *Schema         `json:"request,omitempty"`
	Response   *Schema         `json:"response,omitempty"`
	Protocol   Protocol        `json:"protocol"` // rest for REST endpoints
	Detection  DetectionMethod `json:"detection"`
	Confidence Confidence      `json:"confidence"`
}

// Dependency is an outbound HTTP call target (Feign/RestTemplate/WebClient).
type Dependency struct {
	// TargetName is the raw logical name (e.g. @FeignClient(name="payment-service")).
	// It is NOT a service_id — the backend maps name -> service_id. Don't guess.
	TargetName string          `json:"target_name"`
	URL        string          `json:"url,omitempty"` // resolved when possible
	Protocol   Protocol        `json:"protocol"`      // rest for HTTP clients
	Detection  DetectionMethod `json:"detection"`
	Confidence Confidence      `json:"confidence"`
	// Resolved is false when the target could not be resolved (unknown/external).
	Resolved bool `json:"resolved"`
	// Conditional marks one of several candidate targets from a single call site
	// (e.g. a ternary URL or divergent env overlays). CandidateGroup ties the
	// alternatives together so the backend knows they are one-of-N, not all taken.
	Conditional    bool   `json:"conditional,omitempty"`
	CandidateGroup string `json:"candidate_group,omitempty"`
	// ResolvedVia is the provenance behind Confidence=likely: which config source
	// supplied the value (e.g. "application-prod.yml", "values-staging.yaml",
	// ".env", "default"). Blank when the value is a literal (nothing to explain)
	// or when it was assembled from more than one source (no single file to name
	// honestly) — never guessed.
	ResolvedVia string `json:"resolved_via,omitempty"`
}

// KafkaEdge is a produced or consumed topic. Direction is implied by which slice
// it lives in (KafkaProducers vs KafkaConsumers).
type KafkaEdge struct {
	Topic      string          `json:"topic"`
	Resolved   bool            `json:"resolved"`
	Schema     *Schema         `json:"schema,omitempty"`
	Protocol   Protocol        `json:"protocol"` // kafka
	Detection  DetectionMethod `json:"detection"`
	Confidence Confidence      `json:"confidence"`
	// Conditional/CandidateGroup: as on Dependency — one of several candidate
	// topics from a single call site (e.g. a topic resolved through divergent
	// env overlays).
	Conditional    bool   `json:"conditional,omitempty"`
	CandidateGroup string `json:"candidate_group,omitempty"`
	// ResolvedVia: as on Dependency — the config source behind a likely topic.
	ResolvedVia string `json:"resolved_via,omitempty"`
}

// Database is a datastore this service uses (JPA entities/repositories, JDBC).
type Database struct {
	Name       string          `json:"name,omitempty"`
	Kind       string          `json:"kind,omitempty"` // "jpa", "jdbc", ...
	Detection  DetectionMethod `json:"detection"`
	Confidence Confidence      `json:"confidence"`
}

// ConfigDep is a resolved (or unresolved) configuration dependency — the raw
// material behind placeholder resolution, surfaced for transparency.
type ConfigDep struct {
	Key        string     `json:"key"`
	Value      string     `json:"value,omitempty"`
	Resolved   bool       `json:"resolved"`
	Confidence Confidence `json:"confidence"`
	// ResolvedVia is the file this key's value was read from (e.g.
	// "application-prod.yml", "values-staging.yaml", ".env", "default"). Blank
	// when unresolved.
	ResolvedVia string `json:"resolved_via,omitempty"`
}
