package laravel

import (
	"strings"

	"github.com/farhadamjady/archerik-extractor/internal/model"
	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/php"
)

// rulesFromMethod reads a rules() method, including the array_merge* forms a
// FormRequest uses to extend a base request's rules:
//
//	class NewArticleRequest extends BaseArticleRequest {
//	    public function rules() {
//	        return array_merge_recursive(parent::rules(), ['title' => ['required']]);
//	    }
//	}
//
// Reading only the literal half would report a create-article body with four
// fields when it has nine — a contract that looks complete and is not.
func rulesFromMethod(method php.Node, depth int, res *resolver) *model.Schema {
	return rulesExpr(returnedExpr(method), depth, res)
}

// rulesExpr resolves a rules expression: a literal array, a merge of several, or
// a call up to the parent's rules().
func rulesExpr(e php.Node, depth int, res *resolver) *model.Schema {
	if !e.Valid() {
		return nil
	}
	if e.Type() == "array_creation_expression" {
		return rulesSchema(e, depth)
	}
	if !php.IsCall(e) {
		return nil
	}
	switch php.CallName(e) {
	case "array_merge", "array_merge_recursive", "array_replace", "array_replace_recursive":
		out := &model.Schema{Type: "object", Required: model.ReqUnknown, Confidence: model.Confirmed}
		complete := true
		for _, a := range php.CallArgs(e) {
			part := rulesExpr(a.Value, depth, res)
			if part == nil {
				complete = false
				continue
			}
			for _, f := range part.Nested {
				upsert(out, f)
			}
		}
		if !complete || len(out.Nested) == 0 {
			// A part that could not be read means fields are missing, so the
			// object is not the whole contract even though what is there is real.
			out.Confidence = model.Uncertain
		}
		return out
	case "rules":
		if m, ok := res.parentMethod(e, "rules"); ok {
			return rulesFromMethod(m, depth, res)
		}
	}
	return nil
}

// rulesSchema reads a Laravel validation-rule array as a request contract:
//
//	return [
//	    'email'    => 'required|string|email|max:255',
//	    'username' => ['sometimes', 'string', 'max:255', Rule::unique(...)],
//	    'profile.bio' => 'nullable|string',
//	];
//
// This IS the request contract in a Laravel service, and it is unusually good
// evidence — better than most stacks get. The field names are literal keys, and
// the rules carry type, requiredness, nullability and constraints explicitly,
// where a Java DTO would leave requiredness to convention.
//
// Rules arrive in either of Laravel's two spellings — a pipe-delimited string or
// a list — and non-literal entries (`Rule::unique(...)`, a closure) are simply
// not read: they constrain a value without describing its shape.
func rulesSchema(arr php.Node, depth int) *model.Schema {
	if !arr.Valid() || arr.Type() != "array_creation_expression" {
		return nil
	}
	entries := php.ArrayEntries(arr)
	if len(entries) == 0 {
		return nil
	}
	out := &model.Schema{Type: "object", Required: model.ReqUnknown, Confidence: model.Confirmed}
	for _, e := range entries {
		if e.Key == "" {
			continue
		}
		f := fieldFromRules(e.Key, ruleTokens(e.Value))
		if f == nil {
			continue
		}
		addField(out, e.Key, *f, depth)
	}
	if len(out.Nested) == 0 {
		return nil
	}
	return out
}

// ruleTokens flattens a rule specification into individual rule strings.
func ruleTokens(v php.Node) []string {
	if s, ok := php.StringLit(v); ok {
		return strings.Split(s, "|")
	}
	var out []string
	for _, e := range php.ArrayEntries(v) {
		if s, ok := php.StringLit(e.Value); ok {
			out = append(out, strings.Split(s, "|")...)
		}
	}
	return out
}

// fieldFromRules turns a field's rules into its schema.
//
// Requiredness follows Laravel's own semantics: `required` means present,
// `sometimes`/`nullable` mean it need not be, and a field with neither is
// unknown rather than assumed — the distinction the Requiredness enum exists to
// keep.
func fieldFromRules(name string, rules []string) *model.Schema {
	if len(rules) == 0 {
		return nil
	}
	f := &model.Schema{Name: leafKey(name), Type: "string", Required: model.ReqUnknown, Confidence: model.Confirmed}
	typed := false
	// min/max are applied after the loop: what they CONSTRAIN depends on the
	// field's type, and Laravel does not require the type rule to come first.
	// `min:0` on an integer is a minimum; on a string it is a minimum length.
	var min, max string
	for _, raw := range rules {
		rule, arg, _ := strings.Cut(strings.TrimSpace(raw), ":")
		switch rule {
		case "required":
			f.Required = model.ReqRequired
		case "sometimes", "nullable", "present":
			if rule == "nullable" {
				f.Nullable = true
			}
			if f.Required != model.ReqRequired {
				f.Required = model.ReqOptional
			}
		case "string", "uuid", "ulid", "date", "ip", "json":
			f.Type, typed = "string", true
		case "integer", "int":
			f.Type, typed = "int", true
		case "numeric", "decimal":
			f.Type, typed = "float", true
		case "boolean", "bool", "accepted":
			f.Type, typed = "bool", true
		case "array":
			f.Type, typed = "array", true
		case "file", "image":
			f.Type, typed = "string", true
			setConstraint(f, "format", "binary")
		case "email":
			f.Type, typed = "string", true
			setConstraint(f, "format", "email")
		case "url", "active_url":
			f.Type, typed = "string", true
			setConstraint(f, "format", "uri")
		case "max":
			max = arg
		case "min":
			min = arg
		case "regex":
			setConstraint(f, "pattern", arg)
		case "in":
			if arg != "" {
				f.Enum = strings.Split(arg, ",")
			}
		case "confirmed", "unique", "exists":
			// Constraints on the value's relationship to other data, not its
			// shape. Nothing to record.
		}
	}
	switch f.Type {
	case "int", "float":
		setConstraint(f, "minimum", min)
		setConstraint(f, "maximum", max)
	case "array":
		setConstraint(f, "minItems", min)
		setConstraint(f, "maxItems", max)
	default:
		setConstraint(f, "minLength", min)
		setConstraint(f, "maxLength", max)
	}
	if !typed {
		// No type rule at all. The field is real and its name is known; claiming
		// `string` would be a guess, so say so.
		f.Confidence = model.Uncertain
	}
	return f
}

// addField places a field, expanding Laravel's dotted paths (`author.name`) and
// wildcard list paths (`tags.*`) into real nesting rather than emitting a field
// literally named "author.name".
func addField(out *model.Schema, key string, f model.Schema, depth int) {
	parts := strings.Split(key, ".")
	if len(parts) == 1 {
		out.Nested = append(out.Nested, f)
		return
	}
	parent := parts[0]
	rest := parts[1:]
	// `tags.*` describes the ELEMENTS of tags, which makes tags an array.
	if len(rest) == 1 && rest[0] == "*" {
		upsert(out, model.Schema{
			Name: parent, Type: "array", Items: f.Type,
			Required: f.Required, Confidence: f.Confidence,
		})
		return
	}
	container := findOrAdd(out, parent, depth)
	if container == nil {
		out.Nested = append(out.Nested, f) // out of depth: keep it flat rather than lose it
		return
	}
	addField(container, strings.Join(rest, "."), f, depth-1)
}

// findOrAdd returns the nested object for a key, creating it when absent. Beyond
// the depth budget it returns nil and the caller keeps the field flat.
func findOrAdd(out *model.Schema, name string, depth int) *model.Schema {
	if depth <= 0 {
		return nil
	}
	for i := range out.Nested {
		if out.Nested[i].Name == name {
			return &out.Nested[i]
		}
	}
	out.Nested = append(out.Nested, model.Schema{
		Name: name, Type: "object", Required: model.ReqUnknown, Confidence: model.Confirmed,
	})
	return &out.Nested[len(out.Nested)-1]
}

// upsert replaces a same-named field or appends it.
func upsert(out *model.Schema, f model.Schema) {
	for i := range out.Nested {
		if out.Nested[i].Name == f.Name {
			out.Nested[i] = f
			return
		}
	}
	out.Nested = append(out.Nested, f)
}

// leafKey returns the last segment of a dotted rule key, which is the field's
// own name once it has been nested under its parents.
func leafKey(key string) string {
	if i := strings.LastIndex(key, "."); i >= 0 {
		return key[i+1:]
	}
	return key
}

func setConstraint(f *model.Schema, k, v string) {
	if v == "" {
		return
	}
	if f.Constraints == nil {
		f.Constraints = map[string]string{}
	}
	f.Constraints[k] = v
}
