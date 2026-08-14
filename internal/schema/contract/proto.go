package contract

import (
	"regexp"
	"strings"

	"github.com/farhadamjady/archerik-extractor/internal/model"
)

var (
	protoMessageRe = regexp.MustCompile(`^\s*message\s+(\w+)\s*\{`)
	// [repeated|optional] Type name = N;  (Type may be qualified or map<K,V>)
	protoFieldRe = regexp.MustCompile(`^\s*(repeated\s+|optional\s+)?([\w.]+(?:<[\w., ]+>)?)\s+(\w+)\s*=\s*\d+`)
	protoMapRe   = regexp.MustCompile(`^map<\s*([\w.]+)\s*,\s*([\w.]+)\s*>$`)
)

// ParseProto extracts the first top-level message from a .proto file (proto3).
// Nested messages and imports are not followed (best-effort).
func ParseProto(src []byte) (name string, sch *model.Schema, err error) {
	lines := strings.Split(string(src), "\n")
	for i, line := range lines {
		m := protoMessageRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		name = m[1]
		s := &model.Schema{Type: name, Confidence: model.Confirmed, Required: model.ReqUnknown}
		for _, fl := range lines[i+1:] {
			if strings.Contains(fl, "}") {
				break
			}
			if f := protoField(fl); f != nil {
				s.Nested = append(s.Nested, *f)
			}
		}
		return name, s, nil
	}
	return "", nil, nil
}

func protoField(line string) *model.Schema {
	m := protoFieldRe.FindStringSubmatch(line)
	if m == nil {
		return nil
	}
	label, typ, fname := strings.TrimSpace(m[1]), m[2], m[3]

	fs := &model.Schema{Name: fname, Required: model.ReqUnknown, Confidence: model.Confirmed}
	switch {
	case label == "repeated":
		fs.Type, fs.Items = "array", protoScalar(typ)
	case protoMapRe.MatchString(typ):
		mm := protoMapRe.FindStringSubmatch(typ)
		fs.Type, fs.KeyType, fs.ValueType = "map", protoScalar(mm[1]), protoScalar(mm[2])
	default:
		fs.Type = protoScalar(typ)
	}
	if label == "optional" {
		fs.Required = model.ReqOptional
	}
	return fs
}

func protoScalar(t string) string {
	switch t {
	case "string", "bytes":
		return "string"
	case "int32", "int64", "uint32", "uint64", "sint32", "sint64", "fixed32", "fixed64":
		return "integer"
	case "float", "double":
		return "number"
	case "bool":
		return "boolean"
	default:
		if i := strings.LastIndex(t, "."); i >= 0 {
			return t[i+1:]
		}
		return t
	}
}
