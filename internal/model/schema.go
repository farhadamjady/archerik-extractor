package model

// Schema is the declared (static) structure attached to an endpoint or a topic.
// It captures the shape of the contract as written in code/spec — never runtime
// payloads. No versioning: current only, overwrite on change.
//
// Generics/collections are recorded as container + inner type:
//   - List<User>          -> {Type: "array", Items: "User"}
//   - Map<String, Order>  -> {Type: "map", KeyType: "String", ValueType: "Order"}
//   - Optional<String>    -> {Type: "String", Nullable: true}
//   - Page<Invoice>       -> unwrapped to the inner type
type Schema struct {
	Name      string   `json:"name,omitempty"`
	Type      string   `json:"type"` // "string","object","array","map", or a DTO name
	Nullable  bool     `json:"nullable,omitempty"`
	Items     string   `json:"items,omitempty"`      // element type for arrays
	KeyType   string   `json:"key_type,omitempty"`   // for maps
	ValueType string   `json:"value_type,omitempty"` // for maps
	Nested    []Schema `json:"nested,omitempty"`     // walked to a configurable depth

	// Truncated marks a field where the nested DTO walk hit its depth limit
	// (default 2). Deeper fields become {"type":"object","truncated":true}.
	Truncated bool `json:"truncated,omitempty"`

	Confidence Confidence `json:"confidence,omitempty"`
}
