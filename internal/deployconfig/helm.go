package deployconfig

import (
	"bytes"
	"regexp"
	"strings"
)

// NamedSource is a file's path and bytes, used to feed Helm templates to the
// tracer (templates are not valid YAML, so they aren't parsed as documents).
type NamedSource struct {
	Path string
	Src  []byte
}

var (
	// A container env entry's name line: `- name: FOO` / `name: FOO`.
	envNameRe = regexp.MustCompile(`^\s*-?\s*name:\s*"?([A-Za-z_][A-Za-z0-9_]*)"?\s*$`)
	// A value line and, within it, a {{ .Values.a.b.c }} reference.
	valueLineRe = regexp.MustCompile(`^\s*value:\s*(.+?)\s*$`)
	valuesRefRe = regexp.MustCompile(`\.Values\.([A-Za-z0-9_.]+)`)
)

// TraceTemplates statically maps env-var names to values through Helm chart
// templates: it pairs each `name: FOO` with the following
// `value: {{ .Values.a.b.c }}`, resolves .Values.a.b.c against the values layer,
// and returns FOO -> value bindings. Overlays on the values propagate to the env
// binding (so staging vs prod divergence carries through).
//
// This does NOT render the chart. Named templates (_helpers.tpl), include/tpl,
// and any value expression that isn't a plain .Values reference are left
// unresolved — best-effort, honest Determinism: input files are
// processed in the order given (the indexer sorts them), line order within each.
func TraceTemplates(templates []NamedSource, values *Layer) []Binding {
	var out []Binding
	for _, tpl := range templates {
		out = append(out, traceOne(tpl, values)...)
	}
	return out
}

func traceOne(tpl NamedSource, values *Layer) []Binding {
	var out []Binding
	pendingName := ""
	for _, line := range strings.Split(string(tpl.Src), "\n") {
		if m := envNameRe.FindStringSubmatch(line); m != nil {
			// Only env-var-shaped names (UPPER_SNAKE); skips metadata/container
			// `name:` (e.g. "cart", "app") to avoid mispairing.
			if isEnvVarName(m[1]) {
				pendingName = m[1]
			}
			continue
		}
		if m := valueLineRe.FindStringSubmatch(line); m != nil && pendingName != "" {
			if ref := valuesRefRe.FindStringSubmatch(m[1]); ref != nil {
				key := NormalizeKey(ref[1])
				for _, b := range values.Get(key) {
					out = append(out, Binding{
						Key:     NormalizeKey(pendingName),
						Raw:     pendingName,
						Value:   b.Value,
						Source:  tpl.Path,
						Overlay: b.Overlay,
					})
				}
			}
			pendingName = ""
		}
	}
	return out
}

// TraceConfigMapData reads `data:` entries out of TEMPLATED ConfigMap
// manifests : real charts put env config in a ConfigMap template and wire it
// to the container with envFrom + configMapRef, so the keys ARE env vars.
// Values may be literals or {{ .Values.x }} refs (resolved against the values
// layer); other expressions ({{ include }}) are skipped. The envFrom link
// itself is not verified — within one chart this over-approximates safely.
func TraceConfigMapData(templates []NamedSource, values *Layer) []Binding {
	var out []Binding
	for _, tpl := range templates {
		if !bytes.Contains(tpl.Src, []byte("kind: ConfigMap")) {
			continue
		}
		out = append(out, traceConfigMap(tpl, values)...)
	}
	return out
}

var cmEntryRe = regexp.MustCompile(`^(\s+)([A-Za-z0-9_.-]+):\s*(.*)$`)

func traceConfigMap(tpl NamedSource, values *Layer) []Binding {
	var out []Binding
	inData := false
	dataIndent := -1
	for _, line := range strings.Split(string(tpl.Src), "\n") {
		trimmed := strings.TrimRight(line, " \t")
		if strings.TrimSpace(trimmed) == "data:" {
			inData = true
			dataIndent = len(trimmed) - len(strings.TrimLeft(trimmed, " "))
			continue
		}
		if !inData {
			continue
		}
		m := cmEntryRe.FindStringSubmatch(trimmed)
		if m == nil || len(m[1]) <= dataIndent {
			if strings.TrimSpace(trimmed) != "" {
				inData = false // dedent or non-entry ends the data block
			}
			continue
		}
		key, raw := m[2], strings.Trim(strings.TrimSpace(m[3]), `"'`)
		switch {
		case raw == "" || raw == "|" || raw == ">":
			continue // multi-line blob (embedded file) — skip
		case strings.Contains(raw, "{{"):
			if ref := valuesRefRe.FindStringSubmatch(raw); ref != nil {
				for _, b := range values.Get(NormalizeKey(ref[1])) {
					out = append(out, Binding{
						Key: NormalizeKey(key), Raw: key, Value: b.Value,
						Source: tpl.Path, Overlay: b.Overlay,
					})
				}
			}
			// include/tpl/other expressions: unresolved, skip (honest)
		default:
			out = append(out, Binding{Key: NormalizeKey(key), Raw: key, Value: raw, Source: tpl.Path})
		}
	}
	return out
}

// isEnvVarName reports whether s looks like an environment variable: it must
// contain an underscore or an uppercase letter (so lowercase words like "app"
// and "cart" — container/metadata names — are rejected).
func isEnvVarName(s string) bool {
	for _, r := range s {
		if r == '_' || (r >= 'A' && r <= 'Z') {
			return true
		}
	}
	return false
}
