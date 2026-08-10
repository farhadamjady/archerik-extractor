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
// DTO is intentionally absent: an in-code DTO is a schema SOURCE, not an edge
// detection method. OpenAPI is back for builds that GENERATE
// controllers from a spec — there the spec is the real source of the mappings.
type DetectionMethod string

const (
	DetectFeign             DetectionMethod = "feign"
	DetectRestTemplate      DetectionMethod = "resttemplate"
	DetectWebClient         DetectionMethod = "webclient"
	DetectKafka             DetectionMethod = "kafka"
	DetectConfig            DetectionMethod = "config"
	DetectAnnotation        DetectionMethod = "annotation"         // @RestController + mapping annotations
	DetectOpenAPI           DetectionMethod = "openapi"            // read from an OpenAPI spec the build generates code from
	DetectHTTPExchange      DetectionMethod = "httpexchange"       // Spring 6 @HttpExchange declarative client
	DetectCloudStream       DetectionMethod = "cloudstream"        // Spring Cloud Stream functional binding
	DetectAdapter           DetectionMethod = "adapter"            // company wrapper declared in .ekg-adapters.json
	DetectRouter            DetectionMethod = "router"             // WebFlux/WebMvc.fn functional routing (RouterFunction)
	DetectMicronautClient   DetectionMethod = "micronaut-client"   // Micronaut @Client declarative HTTP client
	DetectMPRestClient      DetectionMethod = "mp-rest-client"     // MicroProfile @RegisterRestClient (Quarkus)
	DetectJaxrsClient       DetectionMethod = "jaxrs-client"       // JAX-RS programmatic client (ClientBuilder/WebTarget)
	DetectReactiveMessaging DetectionMethod = "reactive-messaging" // MicroProfile Reactive Messaging @Incoming/@Outgoing/@Channel
	DetectHTTPClient        DetectionMethod = "http-client"        // std-lib HTTP client call (Go http.Get/NewRequest)
	DetectAxios             DetectionMethod = "axios"              // axios / @nestjs/axios HttpService call (Node)
	DetectFetch             DetectionMethod = "fetch"              // WHATWG fetch() call (Node/browser)
	DetectDotNetHTTPClient  DetectionMethod = "dotnet-httpclient"  // .NET HttpClient call (GetAsync/PostAsJsonAsync/SendAsync)
	DetectRefit             DetectionMethod = "refit"              // Refit declarative HTTP client interface (.NET)
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
