package schema

import (
	"strings"

	"github.com/farhadamjady/service-discovery/internal/model"
)

// defaultDepth is how many levels of nested DTOs expand before truncation
// (config knob, §9). Two levels; deeper fields become {object, truncated}.
const defaultDepth = 2

// Walker resolves a Java type expression to a model.Schema: it unwraps
// containers, resolves the type against the TypeSource, unions a DTO's fields
// (own + inherited), and attaches nullability and tri-state requiredness. It is
// language-neutral — it works on type-name strings and TypeDefs.
//
// Data-flow recovery of opaque returns (ResponseEntity<?>, raw Object) is
// deferred (D1): those resolve to an uncertain object rather than being chased.
type Walker struct {
	types    TypeSource
	maxDepth int
}

// NewWalker builds a walker over a type source (nil is tolerated — everything
// then resolves as unresolved/uncertain).
func NewWalker(types TypeSource) *Walker { return &Walker{types: types, maxDepth: defaultDepth} }

// Type resolves a type expression (e.g. "ResponseEntity<List<User>>") to a
// schema, or nil for an empty expression. A void body yields a {type:"void"}
// schema the caller drops. Requiredness is filled in everywhere (default unknown).
func (w *Walker) Type(typeExpr string) *model.Schema {
	if strings.TrimSpace(typeExpr) == "" {
		return nil
	}
	s := w.walk(parseTypeExpr(typeExpr), w.maxDepth, map[string]bool{})
	fillRequired(s)
	return s
}

// walk resolves one type expression. seen holds the DTO names on the current
// nesting path (cycle guard); depth is the remaining nesting budget.
func (w *Walker) walk(te typeExpr, depth int, seen map[string]bool) *model.Schema {
	if te.Array {
		return w.arraySchema(typeExpr{Name: te.Name, Args: te.Args}, depth, seen)
	}
	switch {
	case te.Name == "void" || te.Name == "Void" || te.Name == "Unit" || te.Name == "Nothing":
		// Unit/Nothing are Kotlin's void/bottom types (no serialized body).
		return &model.Schema{Type: "void", Confidence: model.Confirmed}
	case singleWrapper[te.Name] && len(te.Args) == 1:
		return w.walk(te.Args[0], depth, seen) // unwrap ResponseEntity/Mono/Optional/Page/...
	case arrayWrapper[te.Name] && len(te.Args) == 1:
		return w.arraySchema(te.Args[0], depth, seen)
	case mapWrapper[te.Name] && len(te.Args) == 2:
		return &model.Schema{Type: "map", KeyType: te.Args[0].Name, ValueType: te.Args[1].Name, Confidence: model.Confirmed}
	}
	if jt, ok := scalarJSON[te.Name]; ok {
		return &model.Schema{Type: jt, Confidence: model.Confirmed}
	}
	if opaque[te.Name] || te.Name == "?" || te.Name == "" {
		return &model.Schema{Type: "object", Confidence: model.Uncertain}
	}
	if w.types != nil {
		if td, ok := w.types.Lookup(te.Name); ok {
			// An enum is a string constrained to its members, not an object — emit
			// the members and stop (no depth/cycle concern: it is a leaf).
			if td.Kind == KindEnum {
				return &model.Schema{Type: "string", Enum: td.EnumValues, Confidence: model.Confirmed}
			}
			// Truncate on depth exhaustion or a cycle (A -> B -> A).
			if depth <= 0 || seen[td.Name] {
				return &model.Schema{Type: td.Name, Truncated: true, Confidence: model.Confirmed}
			}
			return w.object(td, depth, seen)
		}
	}
	// External/unknown type: known by name, structure unresolved.
	return &model.Schema{Type: te.Name, Confidence: model.Uncertain}
}

// arraySchema builds an "array" schema for a collection whose element is `elem`
// (List<T>, Set<T>, Flux<T>, T[], IEnumerable<T>, ...). The container is
// TRANSPARENT w.r.t. depth — like Optional/ResponseEntity — so the element is
// expanded at the current depth and a bare T, Optional<T>, and List<T> all yield
// the SAME element structure. Items keeps the element type NAME (backward compat);
// when the element resolves to an object DTO its fields are hoisted onto the array
// node (and a depth-truncated element is flagged), so a collection-returning
// endpoint carries the same structure as a single-object one. Scalar, opaque, and
// unknown elements add no fields — the Items name alone describes them; an element
// that is itself an array/map is left to the Items name (nested-container elements
// aren't further unwrapped).
func (w *Walker) arraySchema(elem typeExpr, depth int, seen map[string]bool) *model.Schema {
	s := &model.Schema{Type: "array", Items: elem.Name, Confidence: model.Confirmed}
	switch es := w.walk(elem, depth, seen); {
	case es == nil, es.Type == "array", es.Type == "map":
		// nested container or nothing to expand: the Items name describes it
	default:
		s.Nested = es.Nested
		s.Truncated = es.Truncated
	}
	return s
}

func (w *Walker) object(td *TypeDef, depth int, seen map[string]bool) *model.Schema {
	s := &model.Schema{Type: td.Name, Confidence: model.Confirmed}
	next := clone(seen)
	next[td.Name] = true

	for _, mf := range unionFields(w.allFields(td, map[string]bool{}, map[string]bool{})) {
		te := parseTypeExpr(mf.typ)
		fs := w.walk(te, depth-1, next)
		fs.Name = mf.wire
		fs.Nullable = nullability(mf.annotations, te)
		fs.Required = requiredness(mf.annotations, mf.sources, te, mf.hasDefault)
		s.Nested = append(s.Nested, *fs)
	}
	return s
}

// allFields collects a type's own fields plus those inherited up the extends
// chain, stopping at Object/external/cycle. A property redeclared in a subclass
// shadows the superclass's version entirely (so a field+getter within one class
// still merge, but an inherited field of the same name is dropped). `shadowed`
// holds property names claimed by more-derived classes.
func (w *Walker) allFields(td *TypeDef, visited, shadowed map[string]bool) []FieldDef {
	if td == nil || visited[td.Name] {
		return nil
	}
	visited[td.Name] = true

	var fields []FieldDef
	for _, f := range td.Fields {
		if !shadowed[f.Name] {
			fields = append(fields, f)
		}
	}
	if td.Super != "" && w.types != nil {
		if sup, ok := w.types.Lookup(td.Super); ok {
			next := clone(shadowed)
			for _, f := range td.Fields {
				next[f.Name] = true
			}
			fields = append(fields, w.allFields(sup, visited, next)...)
		}
	}
	return fields
}

// mergedField groups a property's candidates (field, getter, ctor param, record
// component, inherited) so nullability/requiredness see ALL their signals, not
// just the authoritative source's.
type mergedField struct {
	wire        string
	typ         string
	annotations []Annotation
	sources     map[FieldSource]bool
	hasDefault  bool
}

// unionFields groups field candidates by property name (so a field and its
// getter/ctor param merge), drops @JsonIgnore, and resolves each group to one
// merged field with the authoritative type and the union of signals.
func unionFields(fields []FieldDef) []mergedField {
	var order []string
	groups := map[string][]FieldDef{}
	for _, f := range fields {
		if _, ok := groups[f.Name]; !ok {
			order = append(order, f.Name)
		}
		groups[f.Name] = append(groups[f.Name], f)
	}

	var out []mergedField
	for _, name := range order {
		cands := groups[name]
		var anns []Annotation
		srcs := map[FieldSource]bool{}
		hasDefault := false
		best := cands[0]
		for _, c := range cands {
			anns = append(anns, c.Annotations...)
			srcs[c.Source] = true
			hasDefault = hasDefault || c.HasDefault
			if srcRank(c.Source) < srcRank(best.Source) {
				best = c
			}
		}
		if hasAnn(anns, "JsonIgnore") {
			continue
		}
		out = append(out, mergedField{wire: wireNameFrom(anns, name), typ: best.Type, annotations: anns, sources: srcs, hasDefault: hasDefault})
	}
	return out
}

// wireNameFrom applies Jackson's @JsonProperty rename over merged annotations.
func wireNameFrom(anns []Annotation, javaName string) string {
	if a, ok := annOf(anns, "JsonProperty"); ok {
		if a.Arg != "" {
			return a.Arg
		}
		if v := a.Named["value"]; v != "" {
			return v
		}
	}
	return javaName
}

// nullability: may the field's value be null (§11).
func nullability(anns []Annotation, te typeExpr) bool {
	switch {
	case hasAnn(anns, "NotNull") || hasAnn(anns, "NonNull"):
		return false
	case hasAnn(anns, "Nullable"):
		return true
	case te.Name == "Optional":
		return true
	default:
		return false // primitives and unsignaled fields: not marked nullable
	}
}

// requiredness: must the field be present (§11), tri-state, default unknown.
// Explicit annotations win, then a declared default (Kotlin `= …`), then
// Optional<T>, then type/source heuristics.
func requiredness(anns []Annotation, srcs map[FieldSource]bool, te typeExpr, hasDefault bool) model.Requiredness {
	switch {
	case hasAnn(anns, "NotNull") || hasAnn(anns, "NotBlank") || hasAnn(anns, "NotEmpty"):
		return model.ReqRequired
	case jsonPropertyRequired(anns, "true"):
		return model.ReqRequired
	case hasDefault:
		return model.ReqOptional
	case hasAnn(anns, "Nullable"):
		return model.ReqOptional
	case jsonPropertyRequired(anns, "false"):
		return model.ReqOptional
	case te.Name == "Optional":
		return model.ReqOptional
	case isPrimitive(te.Name):
		return model.ReqRequired
	case srcs[SourceRecordComponent] || srcs[SourceCtorParam]:
		return model.ReqRequired
	default:
		return model.ReqUnknown
	}
}

func jsonPropertyRequired(anns []Annotation, want string) bool {
	a, ok := annOf(anns, "JsonProperty")
	return ok && a.Named["required"] == want
}

// fillRequired defaults every schema's Required to unknown (it is always emitted,
// so leaves and roots that aren't fields still carry a value).
func fillRequired(s *model.Schema) {
	if s == nil {
		return
	}
	if s.Required == "" {
		s.Required = model.ReqUnknown
	}
	for i := range s.Nested {
		fillRequired(&s.Nested[i])
	}
}

func hasAnn(anns []Annotation, name string) bool {
	for _, a := range anns {
		if a.Name == name {
			return true
		}
	}
	return false
}

func annOf(anns []Annotation, name string) (Annotation, bool) {
	for _, a := range anns {
		if a.Name == name {
			return a, true
		}
	}
	return Annotation{}, false
}

func clone(m map[string]bool) map[string]bool {
	out := make(map[string]bool, len(m)+1)
	for k := range m {
		out[k] = true
	}
	return out
}

func srcRank(s FieldSource) int {
	switch s {
	case SourceRecordComponent:
		return 0
	case SourceField:
		return 1
	case SourceCtorParam:
		return 2
	default: // getter
		return 3
	}
}

var primitives = set("int", "long", "short", "byte", "char", "boolean", "double", "float")

func isPrimitive(name string) bool { return primitives[name] }

// --- type expression parsing ---

type typeExpr struct {
	Name  string
	Args  []typeExpr
	Array bool
}

func parseTypeExpr(s string) typeExpr {
	s = strings.TrimSpace(s)
	arr := false
	for strings.HasSuffix(s, "[]") {
		arr = true
		s = strings.TrimSpace(s[:len(s)-2])
	}
	if i := strings.IndexByte(s, '<'); i >= 0 && strings.HasSuffix(s, ">") {
		return typeExpr{Name: simpleName(s[:i]), Args: splitTypeArgs(s[i+1 : len(s)-1]), Array: arr}
	}
	return typeExpr{Name: simpleName(s), Array: arr}
}

// splitTypeArgs splits generic arguments on top-level commas (bracket-aware).
func splitTypeArgs(s string) []typeExpr {
	var args []typeExpr
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '<':
			depth++
		case '>':
			depth--
		case ',':
			if depth == 0 {
				args = append(args, parseTypeExpr(s[start:i]))
				start = i + 1
			}
		}
	}
	if strings.TrimSpace(s[start:]) != "" {
		args = append(args, parseTypeExpr(s[start:]))
	}
	return args
}

// simpleName strips a package qualifier and resolves a wildcard bound.
func simpleName(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "?") {
		if i := strings.LastIndex(s, " "); i >= 0 { // "? extends User" -> User
			s = strings.TrimSpace(s[i+1:])
		} else {
			return "?"
		}
	}
	if i := strings.LastIndex(s, "."); i >= 0 {
		s = s[i+1:]
	}
	return strings.TrimSpace(s)
}

// --- type category sets ---

func set(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

var (
	// Wrapper/scalar sets are multi-language: the walker is type-name-neutral, so
	// alongside the JVM names it recognizes TypeScript (Promise/Array/Record),
	// C#/.NET (Task/ValueTask/ActionResult/IEnumerable/...), and Go container
	// spellings. Lower-case scalars (string/number/bool/...) are TS/C#/Go
	// primitives and never collide with Java's capitalized ones.
	singleWrapper = set("ResponseEntity", "HttpEntity", "Mono", "Optional", "CompletableFuture", "Callable", "Future", "Page", "Slice", "AtomicReference",
		"Promise", "Observable", // TypeScript async
		"Task", "ValueTask", "ActionResult", "JsonResult") // C#/.NET
	arrayWrapper = set("List", "Set", "Collection", "Iterable", "Flux", "Stream", "ArrayList", "LinkedList", "HashSet",
		"Array", "ReadonlyArray", // TypeScript
		"IEnumerable", "ICollection", "IList", "IReadOnlyList", "IReadOnlyCollection") // C#/.NET
	mapWrapper = set("Map", "HashMap", "LinkedHashMap", "TreeMap", "ConcurrentHashMap",
		"Record", "Dictionary", "IDictionary", "IReadOnlyDictionary") // TS Record<K,V> / C# Dictionary<K,V>
	opaque = set("Object", "JsonNode", "ObjectNode", "Serializable", "Any", // JVM (Any = Kotlin's Object)
		"any", "unknown", "object", // TypeScript
		"IActionResult", "JToken", "JObject") // C#

	scalarJSON = map[string]string{
		"String": "string", "CharSequence": "string", "char": "string", "Character": "string",
		"UUID": "string", "Instant": "string", "LocalDate": "string", "LocalDateTime": "string",
		"LocalTime": "string", "OffsetDateTime": "string", "ZonedDateTime": "string", "Date": "string",
		"int": "integer", "Integer": "integer", "long": "integer", "Long": "integer",
		"short": "integer", "Short": "integer", "byte": "integer", "Byte": "integer", "BigInteger": "integer",
		"double": "number", "Double": "number", "float": "number", "Float": "number", "BigDecimal": "number",
		"boolean": "boolean", "Boolean": "boolean",

		// Kotlin primitives beyond the shared JVM names (Long/Short/Byte/Float/
		// Double/Boolean/Char/String already map above)
		"Int": "integer", "UInt": "integer", "ULong": "integer", "UShort": "integer", "UByte": "integer",

		// TypeScript primitives
		"string": "string", "number": "number", "bigint": "integer",

		// C#/.NET scalars
		"Guid": "string", "DateTime": "string", "DateTimeOffset": "string", "DateOnly": "string",
		"TimeOnly": "string", "TimeSpan": "string", "decimal": "number", "Decimal": "number",
		"bool": "boolean",

		// Go scalars
		"int8": "integer", "int16": "integer", "int32": "integer", "int64": "integer",
		"uint": "integer", "uint8": "integer", "uint16": "integer", "uint32": "integer", "uint64": "integer",
		"rune": "integer", "float32": "number", "float64": "number", "time.Time": "string",
	}
)
