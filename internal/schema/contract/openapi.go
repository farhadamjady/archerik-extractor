package contract

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/farhadamjady/archerik-extractor/internal/model"
)

// ParseOpenAPI reads an OpenAPI 3.x document (YAML or JSON — YAML parses both)
// and returns its operations as endpoints with request/response schemas
// resolved from components. The caller stamps detection/protocol/confidence.
//
// This exists for repos whose controllers are GENERATED from the spec at build
// time (openapi-generator): there the spec IS the source of the mapping
// annotations, so reading it recovers endpoints a source-only scan cannot see.
func ParseOpenAPI(src []byte) ([]model.Endpoint, error) {
	var raw map[string]any
	if err := yaml.Unmarshal(src, &raw); err != nil {
		return nil, err
	}
	// Specs often write response codes as numbers (200:); yaml.v3 then decodes
	// those maps as map[any]any. Stringify every key so lookups work.
	doc, _ := normalizeKeys(raw).(map[string]any)
	paths, _ := doc["paths"].(map[string]any)
	if paths == nil {
		return nil, nil
	}
	components := asMapAny(dig2(doc, "components", "schemas"))

	var out []model.Endpoint
	for _, path := range sortedKeysAny(paths) {
		ops, _ := paths[path].(map[string]any)
		for _, method := range sortedKeysAny(ops) {
			m := strings.ToUpper(method)
			switch m {
			case "GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS":
			default:
				continue // parameters / servers / summary keys
			}
			op, _ := ops[method].(map[string]any)
			out = append(out, model.Endpoint{
				Method:   m,
				Path:     path,
				Request:  opRequestSchema(op, components),
				Response: opResponseSchema(op, components),
			})
		}
	}
	return out, nil
}

// opRequestSchema resolves requestBody -> content -> schema.
func opRequestSchema(op, components map[string]any) *model.Schema {
	content := asMapAny(asMapAny(op["requestBody"])["content"])
	return firstContentSchema(content, components)
}

// opResponseSchema resolves the lowest 2xx response's schema.
func opResponseSchema(op, components map[string]any) *model.Schema {
	responses := asMapAny(op["responses"])
	for _, code := range sortedKeysAny(responses) {
		if len(code) != 3 || code[0] != '2' {
			continue
		}
		content := asMapAny(asMapAny(responses[code])["content"])
		if s := firstContentSchema(content, components); s != nil {
			return s
		}
	}
	return nil
}

// firstContentSchema prefers application/json, else the first media type.
func firstContentSchema(content, components map[string]any) *model.Schema {
	if content == nil {
		return nil
	}
	keys := sortedKeysAny(content)
	if _, ok := content["application/json"]; ok {
		keys = append([]string{"application/json"}, keys...)
	}
	for _, k := range keys {
		if sc := asMapAny(asMapAny(content[k])["schema"]); sc != nil {
			return openapiSchema(sc, components, 0, map[string]bool{})
		}
	}
	return nil
}

// openapiSchema converts an OpenAPI schema node to model.Schema, resolving
// #/components/schemas/X refs with a depth cap and cycle guard.
func openapiSchema(node, components map[string]any, depth int, seen map[string]bool) *model.Schema {
	if ref, ok := node["$ref"].(string); ok {
		name := refName(ref)
		if depth >= maxDepth || seen[name] {
			return &model.Schema{Type: name, Truncated: true, Confidence: model.Confirmed, Required: model.ReqUnknown}
		}
		target := asMapAny(components[name])
		if target == nil {
			return &model.Schema{Type: name, Confidence: model.Uncertain, Required: model.ReqUnknown}
		}
		seen2 := cloneSet(seen)
		seen2[name] = true
		s := openapiSchema(target, components, depth+1, seen2)
		if s.Type == "object" {
			s.Type = name
		}
		return s
	}

	typ, _ := node["type"].(string)
	s := &model.Schema{Type: typ, Confidence: model.Confirmed, Required: model.ReqUnknown}
	if nullable, _ := node["nullable"].(bool); nullable {
		s.Nullable = true
	}

	switch typ {
	case "object", "":
		s.Type = "object"
		props := asMapAny(node["properties"])
		required := requiredSet(node["required"])
		for _, name := range sortedKeysAny(props) {
			pm := asMapAny(props[name])
			if pm == nil {
				continue
			}
			fs := openapiSchema(pm, components, depth+1, seen)
			fs.Name = name
			if required[name] {
				fs.Required = model.ReqRequired
			} else {
				fs.Required = model.ReqOptional
			}
			s.Nested = append(s.Nested, *fs)
		}
	case "array":
		items := asMapAny(node["items"])
		if ref, ok := items["$ref"].(string); ok {
			s.Items = refName(ref)
		} else if it, ok := items["type"].(string); ok {
			s.Items = it
		}
	}
	return s
}

// normalizeKeys converts every map[any]any to map[string]any (keys stringified)
// so numeric YAML keys like `200:` are reachable as "200".
func normalizeKeys(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			t[k] = normalizeKeys(val)
		}
		return t
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[fmt.Sprint(k)] = normalizeKeys(val)
		}
		return out
	case []any:
		for i := range t {
			t[i] = normalizeKeys(t[i])
		}
		return t
	default:
		return v
	}
}

func refName(ref string) string {
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		return ref[i+1:]
	}
	return ref
}

func dig2(m map[string]any, k1, k2 string) any {
	return asMapAny(m[k1])[k2]
}

func asMapAny(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func sortedKeysAny(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func cloneSet(m map[string]bool) map[string]bool {
	out := make(map[string]bool, len(m)+1)
	for k := range m {
		out[k] = true
	}
	return out
}
