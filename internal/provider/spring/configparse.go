package spring

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

// parseConfig flattens one Spring config file to dotted keys. YAML nesting
// becomes dot paths (spring.application.name); .properties lines are taken
// as-is. Values are kept verbatim — ${...} placeholders are NOT resolved here
// (that is the ConfigResolver's job, PR 9). Relaxed binding (a.b.c == A_B_C) is
// applied at lookup time by the deploy layer (PR 10), not at parse.
func parseConfig(path string, src []byte) (map[string]string, error) {
	if strings.HasSuffix(path, ".properties") {
		return parseProperties(src), nil
	}
	return parseYAML(src)
}

// parseYAML flattens a YAML config file, merging MULTI-DOCUMENT files (round-9
// finding): Spring applies every `---` document; a document carrying a profile
// marker (`spring.config.activate.on-profile` / legacy `spring.profiles`) is a
// profile overlay and is skipped here — profile selection stays file-level
// (application-<profile>.yml). Later unmarked documents override earlier keys,
// matching Spring's order.
func parseYAML(src []byte) (map[string]string, error) {
	out := map[string]string{}
	dec := yaml.NewDecoder(bytes.NewReader(src))
	parsed := false
	for {
		var root map[string]any
		err := dec.Decode(&root)
		if err == io.EOF {
			break
		}
		if err != nil {
			if !parsed {
				return nil, err
			}
			break // tolerate a malformed trailing document; keep what we have
		}
		parsed = true
		doc := map[string]string{}
		flatten("", root, doc)
		if doc["spring.config.activate.on-profile"] != "" || doc["spring.profiles"] != "" {
			continue
		}
		for k, v := range doc {
			out[k] = v
		}
	}
	return out, nil
}

// flatten walks a decoded YAML value into dotted-key -> string entries. A list
// of scalars collapses to a comma-joined value (so spring.profiles.active reads
// uniformly whether written inline or as a YAML sequence); a list of objects is
// indexed key[i].
func flatten(prefix string, v any, out map[string]string) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			flatten(joinKey(prefix, k), val, out)
		}
	case []any:
		if allScalar(t) {
			parts := make([]string, len(t))
			for i, e := range t {
				parts[i] = scalarString(e)
			}
			if prefix != "" {
				out[prefix] = strings.Join(parts, ",")
			}
		} else {
			for i, e := range t {
				flatten(fmt.Sprintf("%s[%d]", prefix, i), e, out)
			}
		}
	default:
		if prefix != "" && v != nil {
			out[prefix] = scalarString(v)
		}
	}
}

func allScalar(list []any) bool {
	for _, e := range list {
		switch e.(type) {
		case map[string]any, []any:
			return false
		}
	}
	return true
}

func scalarString(v any) string { return fmt.Sprintf("%v", v) }

func joinKey(prefix, k string) string {
	if prefix == "" {
		return k
	}
	return prefix + "." + k
}

// parseProperties reads key=value / key:value lines, skipping blanks and
// # / ! comments. The first '=' or ':' (whichever comes first) is the separator,
// so URLs like a=http://h:8080 keep their colon.
func parseProperties(src []byte) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(string(src), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
			continue
		}
		if i := strings.IndexAny(line, "=:"); i >= 0 {
			key := strings.TrimSpace(line[:i])
			if key != "" {
				out[key] = strings.TrimSpace(line[i+1:])
			}
		}
	}
	return out
}
