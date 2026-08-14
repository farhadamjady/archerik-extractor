package contract

import (
	"encoding/json"

	"github.com/farhadamjady/archerik-extractor/internal/model"
)

// ParseJSONSchema parses a JSON Schema document. The name comes from "title".
func ParseJSONSchema(src []byte) (name string, sch *model.Schema, err error) {
	var root map[string]any
	if err := json.Unmarshal(src, &root); err != nil {
		return "", nil, err
	}
	name, _ = root["title"].(string)
	return name, jsonSchema(root, 0), nil
}

func jsonSchema(m map[string]any, depth int) *model.Schema {
	s := &model.Schema{Type: jsonType(m["type"]), Confidence: model.Confirmed, Required: model.ReqUnknown}
	s.Nullable = jsonNullable(m["type"])

	switch s.Type {
	case "object":
		if title, ok := m["title"].(string); ok {
			s.Type = title
		}
		if depth >= maxDepth {
			s.Truncated = true
			return s
		}
		props, _ := m["properties"].(map[string]any)
		required := requiredSet(m["required"])
		for name, p := range props {
			pm, ok := p.(map[string]any)
			if !ok {
				continue
			}
			fs := jsonSchema(pm, depth+1)
			fs.Name = name
			if required[name] {
				fs.Required = model.ReqRequired
			} else {
				fs.Required = model.ReqOptional
			}
			s.Nested = append(s.Nested, *fs)
		}
	case "array":
		if items, ok := m["items"].(map[string]any); ok {
			if it := jsonType(items["type"]); it != "" {
				s.Items = it
			}
			if title, ok := items["title"].(string); ok {
				s.Items = title
			}
		}
	}
	return s
}

func jsonType(t any) string {
	switch v := t.(type) {
	case string:
		return v
	case []any: // ["string","null"]
		for _, e := range v {
			if s, ok := e.(string); ok && s != "null" {
				return s
			}
		}
	}
	return "object"
}

func jsonNullable(t any) bool {
	if arr, ok := t.([]any); ok {
		for _, e := range arr {
			if s, ok := e.(string); ok && s == "null" {
				return true
			}
		}
	}
	return false
}

func requiredSet(r any) map[string]bool {
	out := map[string]bool{}
	if arr, ok := r.([]any); ok {
		for _, e := range arr {
			if s, ok := e.(string); ok {
				out[s] = true
			}
		}
	}
	return out
}
