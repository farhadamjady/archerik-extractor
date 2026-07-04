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

// DetectionMethod records how an edge or node was found. It travels with every
// edge so the backend knows the provenance of each relationship.
type DetectionMethod string

const (
	DetectFeign        DetectionMethod = "feign"
	DetectRestTemplate DetectionMethod = "resttemplate"
	DetectWebClient    DetectionMethod = "webclient"
	DetectKafka        DetectionMethod = "kafka"
	DetectConfig       DetectionMethod = "config"
	DetectOpenAPI      DetectionMethod = "openapi"
	DetectDTO          DetectionMethod = "dto"
	DetectAnnotation   DetectionMethod = "annotation" // @RestController + mapping annotations
)
