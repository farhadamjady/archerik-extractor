// Package schema turns declared code structure into model.Schema. It is
// framework-neutral: it owns the language-neutral type model (this file) and the
// resolution walker (PR 20). The language layer (provider/lang/java) builds
// TypeDefs from source and satisfies TypeSource; the walker consumes them without
// knowing about tree-sitter or Java.
package schema

// TypeSource resolves a type name to its definition — the query surface of the
// TypeIndex. lang/java implements it over the repo's DTOs.
type TypeSource interface {
	// Lookup resolves a simple or qualified type name to its definition.
	Lookup(name string) (*TypeDef, bool)
}

// Kind is the flavor of a declared type.
type Kind int

const (
	KindClass Kind = iota
	KindRecord
	KindInterface
	KindEnum
	KindAnnotation // an @interface declaration — carries META-annotations (IMPROVEMENTS #17)
)

// TypeDef is a declared type's structure, the raw material the schema walker
// unions and resolves. It intentionally keeps overlapping field sources (a field
// AND its getter) rather than deduping — that is the walker's job (union by wire
// name), which is what makes extraction tolerant of dirty code.
type TypeDef struct {
	Name        string
	Package     string
	Kind        Kind
	Super       string            // simple name of the extended type, "" if none
	Imports     map[string]string // simple name -> fully-qualified name
	Annotations []Annotation      // type-level (Lombok @Data/@Value, @JsonNaming, ...)
	Fields      []FieldDef        // from every source; not yet deduped
}

// FieldSource records where a candidate field came from, so the walker can
// prefer authoritative sources (e.g. record components, Lombok-declared fields).
type FieldSource int

const (
	SourceField FieldSource = iota
	SourceRecordComponent
	SourceCtorParam
	SourceGetter
)

// FieldDef is one candidate field: its Java identifier, declared type text
// (e.g. "List<User>", "Optional<String>"), where it came from, and its
// annotations (@JsonProperty, @NotNull, @Nullable, @JsonIgnore, ...).
type FieldDef struct {
	Name        string
	Type        string
	Source      FieldSource
	Annotations []Annotation
	// HasDefault marks a field with a declared default value (e.g. a Kotlin
	// `val x: String = "n/a"` property/param). A defaulted field may be omitted
	// by the caller, so the walker treats it as optional regardless of type.
	HasDefault bool
}

// Annotation is a captured annotation: its simple name, the first positional
// string literal (Arg), and named arguments as text (Named), enough for the
// walker's Jackson/validation reads (@JsonProperty("x"), @JsonProperty(required=true)).
type Annotation struct {
	Name  string
	Arg   string
	Named map[string]string
}

// Has reports whether a type/field carries an annotation by simple name.
func has(anns []Annotation, name string) bool {
	for _, a := range anns {
		if a.Name == name {
			return true
		}
	}
	return false
}

// HasAnnotation reports whether the type carries the named annotation.
func (t *TypeDef) HasAnnotation(name string) bool { return has(t.Annotations, name) }

// HasAnnotation reports whether the field carries the named annotation.
func (f FieldDef) HasAnnotation(name string) bool { return has(f.Annotations, name) }

// Annotation returns the named annotation on a field, if present.
func (f FieldDef) Annotation(name string) (Annotation, bool) {
	for _, a := range f.Annotations {
		if a.Name == name {
			return a, true
		}
	}
	return Annotation{}, false
}
