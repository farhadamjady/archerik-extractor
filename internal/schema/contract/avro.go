// Package contract parses Kafka message-schema files (Avro, Protobuf, JSON
// Schema) in the repo into model.Schema. These formats carry nullability and
// requiredness natively, so they are the authoritative source when present
// (files-first, DESIGN §12).
package contract

import (
	"encoding/json"

	"github.com/farhadamjady/service-discovery/internal/model"
)

// maxDepth bounds nested record recursion in a contract file.
const maxDepth = 4

// ParseAvro parses an Avro .avsc schema, returning the record name and schema.
func ParseAvro(src []byte) (name string, sch *model.Schema, err error) {
	var root any
	if err := json.Unmarshal(src, &root); err != nil {
		return "", nil, err
	}
	if m, ok := root.(map[string]any); ok {
		name, _ = m["name"].(string)
	}
	return name, avroSchema(root, 0), nil
}

func avroSchema(a any, depth int) *model.Schema {
	switch t := a.(type) {
	case string:
		return leaf(avroPrimitive(t))
	case []any: // union, e.g. ["null","string"]
		return avroUnion(t, depth)
	case map[string]any:
		return avroComplex(t, depth)
	}
	return &model.Schema{Type: "object", Confidence: model.Uncertain, Required: model.ReqUnknown}
}

func avroComplex(m map[string]any, depth int) *model.Schema {
	switch typ := m["type"].(type) {
	case []any:
		return avroUnion(typ, depth)
	case map[string]any:
		return avroComplex(typ, depth)
	case string:
		switch typ {
		case "record":
			return avroRecord(m, depth)
		case "array":
			return &model.Schema{Type: "array", Items: avroName(m["items"]), Confidence: model.Confirmed, Required: model.ReqUnknown}
		case "map":
			return &model.Schema{Type: "map", KeyType: "string", ValueType: avroName(m["values"]), Confidence: model.Confirmed, Required: model.ReqUnknown}
		case "enum":
			return leaf("string")
		default:
			return leaf(avroPrimitive(typ))
		}
	}
	return &model.Schema{Type: "object", Confidence: model.Uncertain, Required: model.ReqUnknown}
}

func avroRecord(rec map[string]any, depth int) *model.Schema {
	name, _ := rec["name"].(string)
	s := &model.Schema{Type: nameOr(name, "object"), Confidence: model.Confirmed, Required: model.ReqUnknown}
	if depth >= maxDepth {
		s.Truncated = true
		return s
	}
	fields, _ := rec["fields"].([]any)
	for _, f := range fields {
		fm, ok := f.(map[string]any)
		if !ok {
			continue
		}
		fs := avroSchema(fm["type"], depth+1)
		fs.Name, _ = fm["name"].(string)
		fs.Nullable = avroNullable(fm["type"])
		_, hasDefault := fm["default"]
		fs.Required = avroRequired(hasDefault, fs.Nullable)
		s.Nested = append(s.Nested, *fs)
	}
	return s
}

// avroUnion resolves ["null", T] to T with nullable set; a multi-type union
// resolves to its first non-null member.
func avroUnion(u []any, depth int) *model.Schema {
	for _, m := range u {
		if s, ok := m.(string); ok && s == "null" {
			continue
		}
		sch := avroSchema(m, depth)
		sch.Nullable = true
		return sch
	}
	return &model.Schema{Type: "null", Nullable: true, Confidence: model.Confirmed, Required: model.ReqUnknown}
}

// avroNullable reports whether a field type is a union containing "null".
func avroNullable(typ any) bool {
	if u, ok := typ.([]any); ok {
		for _, m := range u {
			if s, ok := m.(string); ok && s == "null" {
				return true
			}
		}
	}
	return false
}

// avroRequired: a field with a default is optional; a nullable one is optional;
// otherwise it must be present.
func avroRequired(hasDefault, nullable bool) model.Requiredness {
	if hasDefault || nullable {
		return model.ReqOptional
	}
	return model.ReqRequired
}

// avroName is the display name of an items/values type.
func avroName(t any) string {
	switch v := t.(type) {
	case string:
		return avroPrimitive(v)
	case map[string]any:
		if n, ok := v["name"].(string); ok {
			return n
		}
		if ts, ok := v["type"].(string); ok {
			return ts
		}
	}
	return "object"
}

func avroPrimitive(t string) string {
	switch t {
	case "string", "bytes", "fixed":
		return "string"
	case "int", "long":
		return "integer"
	case "float", "double":
		return "number"
	case "boolean":
		return "boolean"
	case "null":
		return "null"
	default:
		return t // a named record reference
	}
}

func leaf(typ string) *model.Schema {
	return &model.Schema{Type: typ, Confidence: model.Confirmed, Required: model.ReqUnknown}
}

func nameOr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
