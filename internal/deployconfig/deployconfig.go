package deployconfig

import (
	"bytes"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Binding is one value from deployment config, keyed by its normalized form so
// it can be matched to a Spring property. Raw keeps the original key; Overlay is
// the environment the value came from (e.g. "staging" for values-staging.yaml),
// "" for the base.
type Binding struct {
	Key     string // NormalizeKey(Raw)
	Raw     string // original key / env-var name / values path
	Value   string
	Source  string // file base name
	Overlay string
}

// Layer aggregates bindings by normalized key. Several bindings under one key
// are divergent overlays (staging vs prod), which the resolver turns into one
// candidate edge per value rather than silently picking a winner.
type Layer struct {
	byKey map[string][]Binding
}

func NewLayer() *Layer { return &Layer{byKey: map[string][]Binding{}} }

// Add records b, ignoring an exact duplicate (same value and overlay).
func (l *Layer) Add(b Binding) {
	for _, e := range l.byKey[b.Key] {
		if e.Value == b.Value && e.Overlay == b.Overlay {
			return
		}
	}
	l.byKey[b.Key] = append(l.byKey[b.Key], b)
}

// Get returns the bindings for a normalized key (nil if none).
func (l *Layer) Get(key string) []Binding { return l.byKey[key] }

// Keys returns the normalized keys, sorted (deterministic iteration).
func (l *Layer) Keys() []string {
	ks := make([]string, 0, len(l.byKey))
	for k := range l.byKey {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// Parse reads one deployment-config file into bindings. It recognizes .env
// files, Helm values, and rendered K8s ConfigMap/workload manifests. Helm
// TEMPLATE files (containing {{ ... }}) are not valid YAML and are skipped
// here — TraceTemplates handles them. Unknown shapes yield no bindings rather
// than an error.
func Parse(p string, src []byte) ([]Binding, error) {
	base := path.Base(p)
	overlay := OverlayFromName(base)
	switch {
	case base == ".env" || strings.HasSuffix(base, ".env"):
		return parseDotenv(base, overlay, src), nil
	case strings.HasSuffix(base, ".yaml") || strings.HasSuffix(base, ".yml"):
		return parseYAML(base, overlay, src)
	}
	return nil, nil
}

// ParseK8s reads a yaml file but keeps ONLY Kubernetes documents (ConfigMap /
// workload env) — no Helm-values fallback. Used for repo-root discovery in
// monorepos, where globbing **/*.yaml would otherwise turn unrelated files
// (skaffold.yaml, CI configs) into garbage bindings.
func ParseK8s(p string, src []byte) ([]Binding, error) {
	if bytes.Contains(src, []byte("{{")) {
		return nil, nil // Helm template — not valid YAML
	}
	base := path.Base(p)
	var out []Binding
	dec := yaml.NewDecoder(bytes.NewReader(src))
	for {
		var doc map[string]any
		err := dec.Decode(&doc)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch kind, _ := doc["kind"].(string); kind {
		case "ConfigMap":
			out = append(out, configMapBindings(base, "", doc)...)
		case "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Pod", "Job", "CronJob":
			out = append(out, workloadEnvBindings(base, "", doc)...)
		}
	}
	return out, nil
}

// OverlayFromName extracts the env from values-<env>.yaml, else "".
func OverlayFromName(base string) string {
	name := strings.TrimSuffix(strings.TrimSuffix(base, ".yaml"), ".yml")
	if strings.HasPrefix(name, "values-") {
		return name[len("values-"):]
	}
	return ""
}

func parseDotenv(source, overlay string, src []byte) []Binding {
	var out []Binding
	for _, line := range strings.Split(string(src), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if k = strings.TrimSpace(k); k != "" {
			v = strings.Trim(strings.TrimSpace(v), `"'`)
			out = append(out, bind(source, overlay, k, v))
		}
	}
	return out
}

func parseYAML(source, overlay string, src []byte) ([]Binding, error) {
	// Helm templates are not valid YAML; TraceTemplates walks .Values through them.
	if bytes.Contains(src, []byte("{{")) {
		return nil, nil
	}
	if source == "Chart.yaml" {
		return nil, nil // chart metadata, not values
	}
	var out []Binding
	dec := yaml.NewDecoder(bytes.NewReader(src))
	for {
		var doc map[string]any
		err := dec.Decode(&doc)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if doc != nil {
			out = append(out, bindingsFromDoc(source, overlay, doc)...)
		}
	}
	return out, nil
}

// bindingsFromDoc dispatches one YAML document by its Kubernetes kind; anything
// without a recognized kind is treated as Helm values.
func bindingsFromDoc(source, overlay string, doc map[string]any) []Binding {
	switch kind, _ := doc["kind"].(string); kind {
	case "ConfigMap":
		return configMapBindings(source, overlay, doc)
	case "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Pod", "Job", "CronJob":
		return workloadEnvBindings(source, overlay, doc)
	default:
		return valuesBindings(source, overlay, doc)
	}
}

// configMapBindings takes each scalar data entry. Multi-line values are embedded
// files (e.g. an application.yml blob) and are skipped here.
func configMapBindings(source, overlay string, doc map[string]any) []Binding {
	data, _ := doc["data"].(map[string]any)
	var out []Binding
	for k, v := range data {
		if s := scalarString(v); s != "" && !strings.Contains(s, "\n") {
			out = append(out, bind(source, overlay, k, s))
		}
	}
	return out
}

// workloadEnvBindings pulls container env: entries with a literal value from a
// workload's pod template. valueFrom (configMapKeyRef/secretKeyRef) has no
// literal here and is skipped.
func workloadEnvBindings(source, overlay string, doc map[string]any) []Binding {
	var out []Binding
	podSpec := dig(doc, "spec", "template", "spec")
	for _, field := range []string{"containers", "initContainers"} {
		for _, c := range asList(dig(asMap(podSpec), field)) {
			for _, e := range asList(dig(asMap(c), "env")) {
				em := asMap(e)
				name, _ := em["name"].(string)
				val, hasVal := em["value"]
				if name != "" && hasVal {
					out = append(out, bind(source, overlay, name, scalarString(val)))
				}
			}
		}
	}
	return out
}

// valuesBindings flattens a Helm values doc's scalars to dotted paths. The env
// var name a value maps to comes from the chart template (TraceTemplates);
// here the key is
// the values path itself, which relaxed binding still bridges when the values
// structure mirrors the Spring property (payment.serviceUrl ~ payment.service.url).
func valuesBindings(source, overlay string, doc map[string]any) []Binding {
	flat := map[string]string{}
	flattenScalars("", doc, flat)
	out := make([]Binding, 0, len(flat))
	for k, v := range flat {
		out = append(out, bind(source, overlay, k, v))
	}
	return out
}

func flattenScalars(prefix string, v any, out map[string]string) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			key := k
			if prefix != "" {
				key = prefix + "." + k
			}
			flattenScalars(key, val, out)
		}
	case []any:
		// deployment values are scalars; lists (e.g. args) are not env sources.
	default:
		if prefix != "" && v != nil {
			out[prefix] = scalarString(v)
		}
	}
}

func bind(source, overlay, rawKey, value string) Binding {
	return Binding{Key: NormalizeKey(rawKey), Raw: rawKey, Value: value, Source: source, Overlay: overlay}
}

func dig(m map[string]any, keys ...string) any {
	var cur any = m
	for _, k := range keys {
		cm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = cm[k]
	}
	return cur
}

func asMap(v any) map[string]any { m, _ := v.(map[string]any); return m }
func asList(v any) []any         { l, _ := v.([]any); return l }

func scalarString(v any) string {
	switch v.(type) {
	case map[string]any, []any, nil:
		return ""
	default:
		return fmt.Sprintf("%v", v)
	}
}
