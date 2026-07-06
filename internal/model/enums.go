package model

// Confidence expresses detection certainty, NOT source type.
//
//   - Confirmed: literal value found (hardcoded URL/topic, resolved placeholder).
//   - Likely:    resolved through config with one indirection.
//   - Uncertain: dynamic/computed (string concat, variable, builder) — still
//     emitted as a node, but flagged.
type Confidence string

const (
	Confirmed Confidence = "confirmed"
	Likely    Confidence = "likely"
	Uncertain Confidence = "uncertain"
)

// DetectionMethod records HOW an edge or node was found in code — its provenance.
// It is orthogonal to Protocol (which is the communication semantics): Feign,
// RestTemplate, and WebClient are three DetectionMethods of one Protocol (rest).
//
// OpenAPI and DTO are intentionally absent: OpenAPI ingestion is cut from the MVP,
// and an in-code DTO is a schema SOURCE, not an edge detection method.
type DetectionMethod string

const (
	DetectFeign        DetectionMethod = "feign"
	DetectRestTemplate DetectionMethod = "resttemplate"
	DetectWebClient    DetectionMethod = "webclient"
	DetectKafka        DetectionMethod = "kafka"
	DetectConfig       DetectionMethod = "config"
	DetectAnnotation   DetectionMethod = "annotation" // @RestController + mapping annotations
)

// Protocol is the communication protocol of an edge — a FIRST-CLASS field, kept
// orthogonal to DetectionMethod so the graph is queryable by protocol regardless
// of how each edge was detected. Interaction style (sync/async, stream/bidi) is
// reserved for later and not modeled here.
type Protocol string

const (
	ProtoREST      Protocol = "rest"
	ProtoKafka     Protocol = "kafka"
	ProtoGRPC      Protocol = "grpc"
	ProtoWebSocket Protocol = "websocket"
	ProtoUnknown   Protocol = "unknown"
)

// Requiredness is whether a schema field must be PRESENT — distinct from Nullable
// (whether its value may be null). Tri-state and always emitted (no omitempty) so
// the backend can tell an unsignaled "unknown" apart from a deliberate "optional".
type Requiredness string

const (
	ReqRequired Requiredness = "required"
	ReqOptional Requiredness = "optional"
	ReqUnknown  Requiredness = "unknown"
)
