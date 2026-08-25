package laravel

import (
	"regexp"
	"strings"

	"github.com/farhadamjady/archerik-extractor/internal/provider"
	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/php"
)

// modelIndexer types the attributes of Eloquent models, which is what turns a
// Laravel response schema from a list of field names into a contract.
//
// An API Resource states its payload as `'slug' => $this->resource->slug`. The
// keys are literal, but the types are not there — and they are not on the model
// either, because Eloquent materializes attributes from the database at runtime,
// so a model class usually declares no properties at all. They ARE statically
// knowable, in three other places, merged here strongest first:
//
//  1. `$casts` — the model saying how the attribute is converted, which is
//     exactly what json_encode then sees. Most authoritative.
//  2. the `@property` docblock — usually generated from the real schema, and
//     the only source that carries nullability.
//  3. the MIGRATION that created the column (`$table->string('slug')`). The most
//     complete source, since every column has one, and plain static PHP.
//
// Migrations are keyed by table, and a model is joined to its table by Laravel's
// convention (`Article` -> `articles`) or its explicit `$table` property.
type modelIndexer struct{}

func (modelIndexer) Name() string { return "laravel.model" }

func (modelIndexer) Index(ic *provider.IndexContext, idx *provider.Index) error {
	tables := map[string]map[string]string{} // table -> column -> type
	var models []php.Node

	for _, p := range sortedPHPPaths(ic.Parsed) {
		f, ok := ic.Parsed[p].(*php.File)
		if !ok {
			continue
		}
		if strings.HasPrefix(p, "database/migrations/") || strings.Contains(p, "/database/migrations/") {
			readMigration(f, tables)
			continue
		}
		f.Root().Walk(func(n php.Node) bool {
			if n.Type() == "class_declaration" {
				models = append(models, n)
			}
			return true
		})
	}

	attrs := map[string]map[string]string{}
	for _, n := range models {
		cls := phpClass{node: n, idx: idx}
		if !isEloquentModel(cls) {
			continue
		}
		name := cls.name()
		if name == "" {
			continue
		}
		m := map[string]string{}
		// Weakest first, so a stronger source overwrites.
		if cols, ok := tables[modelTable(cls, name)]; ok {
			for k, v := range cols {
				m[k] = v
			}
		}
		for k, v := range docblockProperties(n) {
			m[k] = v
		}
		for k, v := range castTypes(cls) {
			m[k] = v
		}
		if len(m) > 0 {
			attrs[name] = m
		}
	}
	if len(attrs) > 0 {
		idx.PHPModelAttrs = attrs
	}
	return nil
}

// isEloquentModel reports whether a class is a model. `Model` and
// `Authenticatable` are the two bases Laravel apps extend; a repo that subclasses
// its own base still reaches one of them through the extends chain.
func isEloquentModel(cls phpClass) bool {
	return cls.extends("Model") || cls.extends("Authenticatable") || cls.extends("Pivot")
}

// modelTable returns the table a model maps to: its explicit `$table` property,
// else Laravel's convention — the snake_case plural of the class name.
func modelTable(cls phpClass, name string) string {
	if t, ok := cls.staticString("table"); ok && t != "" {
		return t
	}
	return pluralize(snakeCase(name))
}

// readMigration records the columns of every `Schema::create('t', function ($table) {...})`
// in a migration file. `Schema::table(...)` (an alteration) is read the same way,
// since a column added later is just as real.
func readMigration(f *php.File, tables map[string]map[string]string) {
	f.Root().Walk(func(n php.Node) bool {
		if !php.IsCall(n) || php.CallReceiver(n).Text() != "Schema" {
			return true
		}
		switch php.CallName(n) {
		case "create", "table":
		default:
			return true
		}
		table, ok := php.StringLit(php.PositionalArg(n, 0))
		if !ok {
			return true
		}
		cols := tables[table]
		if cols == nil {
			cols = map[string]string{}
			tables[table] = cols
		}
		readBlueprint(php.PositionalArg(n, 1), cols)
		return true
	})
}

// readBlueprint walks a migration closure collecting `$table->TYPE('name')` calls.
func readBlueprint(closure php.Node, cols map[string]string) {
	if !closure.Valid() {
		return
	}
	closure.Walk(func(n php.Node) bool {
		if !php.IsCall(n) {
			return true
		}
		method := php.CallName(n)
		// The zero-argument shorthands, which name their own columns.
		switch method {
		case "id", "bigIncrements", "increments":
			cols["id"] = "int"
			return true
		case "timestamps", "nullableTimestamps":
			cols["created_at"], cols["updated_at"] = "string", "string"
			return true
		case "softDeletes":
			cols["deleted_at"] = "string"
			return true
		case "rememberToken":
			cols["remember_token"] = "string"
			return true
		}
		typ, ok := columnType(method)
		if !ok {
			return true
		}
		name, ok := php.StringLit(php.PositionalArg(n, 0))
		if !ok || name == "" {
			return true
		}
		cols[name] = typ
		return true
	})
}

// columnType maps a Blueprint method to a wire type.
func columnType(method string) (string, bool) {
	switch method {
	case "string", "char", "text", "mediumText", "longText", "tinyText",
		"uuid", "ulid", "ipAddress", "macAddress", "enum", "date", "dateTime",
		"dateTimeTz", "time", "timeTz", "timestamp", "timestampTz", "year",
		"binary", "rememberTokenColumn":
		return "string", true
	case "integer", "bigInteger", "mediumInteger", "smallInteger", "tinyInteger",
		"unsignedBigInteger", "unsignedInteger", "unsignedMediumInteger",
		"unsignedSmallInteger", "unsignedTinyInteger", "foreignId", "foreignIdFor":
		return "int", true
	case "boolean":
		return "bool", true
	case "decimal", "unsignedDecimal", "float", "double":
		return "float", true
	case "json", "jsonb":
		return "object", true
	}
	return "", false
}

// castTypes reads the model's `$casts` array.
func castTypes(cls phpClass) map[string]string {
	init, ok := cls.propertyInit("casts")
	if !ok {
		return nil
	}
	out := map[string]string{}
	for _, v := range php.NamedChildren(init) {
		for _, e := range php.ArrayEntries(v) {
			cast, ok := php.StringLit(e.Value)
			if !ok || e.Key == "" {
				continue
			}
			if t, ok := castType(cast); ok {
				out[e.Key] = t
			}
		}
	}
	return out
}

// castType maps a Laravel cast to a wire type. A parameterised cast keeps only
// its head (`decimal:2` -> decimal).
func castType(cast string) (string, bool) {
	head, _, _ := strings.Cut(cast, ":")
	switch head {
	case "int", "integer", "timestamp":
		return "int", true
	case "real", "float", "double", "decimal":
		return "float", true
	case "string", "encrypted", "hashed":
		return "string", true
	case "bool", "boolean":
		return "bool", true
	case "array", "collection", "encrypted:array":
		return "array", true
	case "object", "json":
		return "object", true
	case "date", "datetime", "immutable_date", "immutable_datetime", "custom_datetime":
		// Serialized as an ISO-8601 string, not a structure.
		return "string", true
	}
	return "", false
}

// docblockProperty matches an `@property` line: `@property string|null $bio`.
// `@property-read` counts too — a computed accessor still appears in the payload.
var docblockProperty = regexp.MustCompile(`@property(?:-read|-write)?\s+([^\s$]+)\s+\$(\w+)`)

// docblockProperties reads the `@property` annotations above a class. They are
// usually generated from the live schema, and they are the only source here that
// records nullability.
func docblockProperties(class php.Node) map[string]string {
	src := class.File()
	if src == nil {
		return nil
	}
	// The docblock sits immediately before the class, so scan from the file start
	// up to the class — cheap, and it captures the block wherever it is written.
	text := string(src.Src())
	if end := int(class.StartByte()); end > 0 && end <= len(text) {
		text = text[:end]
	}
	out := map[string]string{}
	for _, m := range docblockProperty.FindAllStringSubmatch(text, -1) {
		if t, ok := docblockType(m[1]); ok {
			out[m[2]] = t
		}
	}
	return out
}

// docblockType maps a PHPDoc type to a wire type, taking the first member of a
// union (`\Carbon|null` -> Carbon) since nullability is not modeled per-field here.
func docblockType(t string) (string, bool) {
	t = strings.TrimPrefix(strings.TrimSpace(t), "?")
	if i := strings.IndexByte(t, '|'); i >= 0 {
		t = t[:i]
	}
	if strings.HasSuffix(t, "[]") {
		return "array", true
	}
	switch simpleClassName(t) {
	case "int", "integer":
		return "int", true
	case "float", "double":
		return "float", true
	case "string":
		return "string", true
	case "bool", "boolean":
		return "bool", true
	case "array", "iterable":
		return "array", true
	case "Carbon", "CarbonImmutable", "DateTime", "DateTimeInterface":
		return "string", true
	}
	return "", false
}

// snakeCase converts a StudlyCase class name to snake_case.
func snakeCase(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(r - 'A' + 'a')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// pluralize is the inverse of the singular() used for route parameters, covering
// the same short list of irregulars for the same reason: the table name is how a
// model finds its columns, and a wrong one silently yields no types.
func pluralize(s string) string {
	for plural, sing := range irregularSingular {
		if sing == s {
			return plural
		}
	}
	switch {
	case strings.HasSuffix(s, "y") && len(s) > 1 && !isVowel(s[len(s)-2]):
		return s[:len(s)-1] + "ies"
	case strings.HasSuffix(s, "s"), strings.HasSuffix(s, "x"),
		strings.HasSuffix(s, "z"), strings.HasSuffix(s, "ch"), strings.HasSuffix(s, "sh"):
		return s + "es"
	default:
		return s + "s"
	}
}

func isVowel(c byte) bool {
	switch c {
	case 'a', 'e', 'i', 'o', 'u':
		return true
	}
	return false
}
