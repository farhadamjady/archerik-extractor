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

	// Truncated marks a node where the nested walk stopped — the DTO depth limit
	// (--schema-depth, default 2) or a cycle (A -> B -> A). The boundary node has
	// a STABLE, complete shape and is the backend/UI contract for "expand deeper
	// later":
	//
	//   {"type": "<TypeName>", "truncated": true}   // NO "nested" key
	//
	// Type carries the declared type NAME (e.g. "Address"), not "object", so the
	// UI knows which type sits at the boundary and can request a deeper re-scan.
	// A truncated node never carries a partial `nested` subtree — nesting is
	// all-or-nothing at each level. An array/map whose element hit the limit
	// carries `truncated` on the container (keeping its items / key_type+value_type)
	// with no `nested`.
	Truncated bool `json:"truncated,omitempty"`

	Confidence Confidence `json:"confidence,omitempty"`
}
