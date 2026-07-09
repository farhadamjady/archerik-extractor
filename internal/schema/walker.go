package schema

import (
	"sort"
	"strings"

	"github.com/farhadamjady/service-discovery/internal/model"
)

// defaultDepth is how deep nested DTOs expand. PR 20 goes one level (a top-level
// object expands to its fields as leaves); PR 21 raises it to 2 with truncation.
const defaultDepth = 1

// Walker resolves a Java type expression to a model.Schema: it unwraps
// containers, resolves the type against the TypeSource, and unions the fields of
// a DTO. It is language-neutral — it works on type-name strings and TypeDefs.
//
// Data-flow recovery of opaque returns (ResponseEntity<?>, raw Object) is
// deferred (D1): those resolve to an uncertain object rather than being chased.
type Walker struct {
	types TypeSource
}

// NewWalker builds a walker over a type source (nil is tolerated — everything
// then resolves as unresolved/uncertain).
func NewWalker(types TypeSource) *Walker { return &Walker{types: types} }

// Type resolves a type expression (e.g. "ResponseEntity<List<User>>") to a
// schema, or nil for an empty expression. A void body yields a {type:"void"}
// schema the caller drops.
func (w *Walker) Type(typeExpr string) *model.Schema {
	if strings.TrimSpace(typeExpr) == "" {
		return nil
	}
	return w.walk(parseTypeExpr(typeExpr), defaultDepth)
}

func (w *Walker) walk(te typeExpr, depth int) *model.Schema {
	if te.Array {
		inner := te
		inner.Array = false
		return &model.Schema{Type: "array", Items: te.itemName(), Confidence: model.Confirmed}
	}
	switch {
	case te.Name == "void" || te.Name == "Void":
		return &model.Schema{Type: "void", Confidence: model.Confirmed}
	case singleWrapper[te.Name] && len(te.Args) == 1:
		return w.walk(te.Args[0], depth) // unwrap ResponseEntity/Mono/Optional/Page/...
	case arrayWrapper[te.Name] && len(te.Args) == 1:
		return &model.Schema{Type: "array", Items: te.Args[0].Name, Confidence: model.Confirmed}
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
			if depth <= 0 {
				return &model.Schema{Type: td.Name, Confidence: model.Confirmed} // leaf; expanded deeper in PR 21
			}
			return w.object(td, depth)
		}
	}
	// External/unknown type: known by name, structure unresolved.
	return &model.Schema{Type: te.Name, Confidence: model.Uncertain}
}

func (w *Walker) object(td *TypeDef, depth int) *model.Schema {
	s := &model.Schema{Type: td.Name, Confidence: model.Confirmed}
	for _, fw := range unionFields(td) {
		fs := w.walk(parseTypeExpr(fw.field.Type), depth-1)
		fs.Name = fw.wire
		s.Nested = append(s.Nested, *fs)
	}
	return s
}

// fieldWithWire pairs a field with its resolved wire name.
type fieldWithWire struct {
	field FieldDef
	wire  string
}

// unionFields dedupes a type's field candidates by wire name, preferring more
// authoritative sources (record components, declared fields) over getters, and
// dropping @JsonIgnore fields. This union is the ladder's dirty-tolerance step.
func unionFields(td *TypeDef) []fieldWithWire {
	ordered := append([]FieldDef(nil), td.Fields...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Name != ordered[j].Name {
			return ordered[i].Name < ordered[j].Name
		}
		return srcRank(ordered[i].Source) < srcRank(ordered[j].Source)
	})

	seen := map[string]bool{}
	var out []fieldWithWire
	for _, f := range ordered {
		if f.HasAnnotation("JsonIgnore") {
			continue
		}
		wire := wireName(f)
		if seen[wire] {
			continue
		}
		seen[wire] = true
		out = append(out, fieldWithWire{f, wire})
	}
	return out
}

// wireName applies Jackson's @JsonProperty rename. @JsonNaming (snake_case) is
// added in PR 21.
func wireName(f FieldDef) string {
	if a, ok := f.Annotation("JsonProperty"); ok {
		if a.Arg != "" {
			return a.Arg
		}
		if v := a.Named["value"]; v != "" {
			return v
		}
	}
	return f.Name
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

// --- type expression parsing ---

type typeExpr struct {
	Name  string
	Args  []typeExpr
	Array bool
}

func (t typeExpr) itemName() string { return t.Name }

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
	singleWrapper = set("ResponseEntity", "HttpEntity", "Mono", "Optional", "CompletableFuture", "Callable", "Future", "Page", "Slice", "AtomicReference")
	arrayWrapper  = set("List", "Set", "Collection", "Iterable", "Flux", "Stream", "ArrayList", "LinkedList", "HashSet")
	mapWrapper    = set("Map", "HashMap", "LinkedHashMap", "TreeMap", "ConcurrentHashMap")
	opaque        = set("Object", "JsonNode", "ObjectNode", "Serializable")

	scalarJSON = map[string]string{
		"String": "string", "CharSequence": "string", "char": "string", "Character": "string",
		"UUID": "string", "Instant": "string", "LocalDate": "string", "LocalDateTime": "string",
		"LocalTime": "string", "OffsetDateTime": "string", "ZonedDateTime": "string", "Date": "string",
		"int": "integer", "Integer": "integer", "long": "integer", "Long": "integer",
		"short": "integer", "Short": "integer", "byte": "integer", "Byte": "integer", "BigInteger": "integer",
		"double": "number", "Double": "number", "float": "number", "Float": "number", "BigDecimal": "number",
		"boolean": "boolean", "Boolean": "boolean",
	}
)
