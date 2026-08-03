package model

// Schema is the declared (static) structure attached to an endpoint or a topic.
// It captures the shape of the contract as written in code/spec — never runtime
// payloads. No versioning: current only, overwrite on change.
//
// Generics/collections are recorded as container + inner type. When the element
// is an object DTO its fields are expanded onto the array node (Nested), so a
// collection endpoint carries the same structure as a single-object one:
//   - List<User>          -> {Type: "array", Items: "User", Nested: [User's fields]}
//   - List<String>        -> {Type: "array", Items: "String"}
//   - Map<String, Order>  -> {Type: "map", KeyType: "String", ValueType: "Order"}
//   - Optional<String>    -> {Type: "String", Nullable: true}
//   - Page<Invoice>       -> unwrapped to the inner type
type Schema struct {
	Name string `json:"name,omitempty"`
	Type string `json:"type"` // "string","object","array","map", or a DTO name

	// Nullable: may the VALUE be null. Requiredness (Required): must the field be
	// PRESENT. Two orthogonal axes. Required is emitted ALWAYS (no omitempty) — a
	// zero value would erase the "unknown" vs "optional" distinction the backend needs.
	Nullable bool         `json:"nullable,omitempty"`
	Required Requiredness `json:"required"`

	Items     string   `json:"items,omitempty"`      // element type for arrays
	KeyType   string   `json:"key_type,omitempty"`   // for maps
	ValueType string   `json:"value_type,omitempty"` // for maps
	Nested    []Schema `json:"nested,omitempty"`     // walked to a configurable depth

	// Enum holds the declared members of an enum type, in declaration (ordinal)
	// order — the field is then a string constrained to these values
	// (Type == "string"). Order is preserved, not sorted: enum ordinals are
	// semantic, and a deterministic parse keeps it byte-stable for diffing.
	Enum []string `json:"enum,omitempty"`

	// Constraints carries declared validation metadata as a small JSON-schema-
	// flavored map (e.g. {"maxLength":"10","pattern":"[a-z]+","format":"email"}),
	// mapped from a known-annotation allowlist (Bean Validation @Size/@Min/@Max/
	// @Pattern/@Email). Unknown annotations are ignored. JSON marshals map keys
	// sorted, so output stays byte-stable for diffing.
	Constraints map[string]string `json:"constraints,omitempty"`

	// Truncated marks a field where the nested DTO walk hit its depth limit
	// (default 2). Deeper fields become {"type":"object","truncated":true}.
	Truncated bool `json:"truncated,omitempty"`

	Confidence Confidence `json:"confidence,omitempty"`
}
