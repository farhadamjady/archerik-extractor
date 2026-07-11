// Package graphdiff computes the SEMANTIC diff between two extractions of the
// same service — the engine behind the per-commit "PR impact comment". It is a
// pure function over model.Service values: no storage, no network, no
// rendering. Identity follows the model's identity keys (endpoint = verb +
// path, dependency = target + detection, kafka edge = topic + direction), so
// the diff and the graph agree on what "the same edge" means.
//
// Renames are deliberately NOT detected: a changed path is a removal for every
// consumer, so remove+add is the honest semantics. A renderer may pair a
// removed+added with an identical schema as a cosmetic hint; the diff object
// stays pure.
package graphdiff

import (
	"sort"

	"github.com/farhadamjady/service-discovery/internal/model"
)

// Diff is the renderer-agnostic result. Every entry keeps the full edge (with
// detection + confidence — the same honesty rules as the graph).
type Diff struct {
	ServiceID string `json:"service_id"`

	Endpoints      CategoryDiff[model.Endpoint]   `json:"endpoints"`
	Outbound       CategoryDiff[model.Dependency] `json:"outbound_dependencies"`
	KafkaProducers CategoryDiff[model.KafkaEdge]  `json:"kafka_producers"`
	KafkaConsumers CategoryDiff[model.KafkaEdge]  `json:"kafka_consumers"`

	Summary Summary `json:"summary"`

	// TargetResolutions maps an outbound dependency's identity key to the KNOWN
	// service id it points at, or "external" — filled by the backend, which
	// knows the fleet (every service with a stored baseline). Both outcomes are
	// reportable per the feature spec.
	TargetResolutions map[string]string `json:"target_resolutions,omitempty"`
}

// CategoryDiff holds one category's changes, all sorted by identity key.
type CategoryDiff[T any] struct {
	Added   []T         `json:"added,omitempty"`
	Removed []T         `json:"removed,omitempty"`
	Changed []Change[T] `json:"changed,omitempty"`
}

// Change is one edge whose identity is unchanged but whose content differs.
// Changes names what differs ("confidence", "url", "resolved", "detection",
// "request_schema", "response_schema", "schema"); schema differences also get
// field-level detail in SchemaDiff.
type Change[T any] struct {
	Key        string      `json:"key"`
	Changes    []string    `json:"changes"`
	SchemaDiff []FieldDiff `json:"schema_diff,omitempty"`
	Before     T           `json:"before"`
	After      T           `json:"after"`
}

// FieldDiff is one schema-field change, with a dotted path for nested fields.
type FieldDiff struct {
	Field  string `json:"field"`
	Change string `json:"change"` // added | removed | type_changed | requiredness_changed | nullability_changed
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
}

// Summary is the roll-up across all categories.
type Summary struct {
	Added   int `json:"added"`
	Removed int `json:"removed"`
	Changed int `json:"changed"`
}

// Empty reports whether nothing changed.
func (d *Diff) Empty() bool {
	return d.Summary.Added == 0 && d.Summary.Removed == 0 && d.Summary.Changed == 0
}

// Compute diffs head against base (base = stored default-branch state,
// head = the PR scan). Inputs are expected in canonical order (model.Sort);
// outputs are sorted by identity key either way.
func Compute(base, head *model.Service) *Diff {
	d := &Diff{ServiceID: head.ServiceID}

	d.Endpoints = diffCategory(base.Endpoints, head.Endpoints, model.EndpointKey, endpointChanges)
	d.Outbound = diffCategory(base.OutboundDependencies, head.OutboundDependencies, model.DependencyKey, dependencyChanges)
	d.KafkaProducers = diffCategory(base.KafkaProducers, head.KafkaProducers,
		func(k model.KafkaEdge) string { return model.KafkaKey(k, "producer") }, kafkaChanges)
	d.KafkaConsumers = diffCategory(base.KafkaConsumers, head.KafkaConsumers,
		func(k model.KafkaEdge) string { return model.KafkaKey(k, "consumer") }, kafkaChanges)

	for _, c := range []Summary{
		summarize(d.Endpoints), summarize(d.Outbound),
		summarize(d.KafkaProducers), summarize(d.KafkaConsumers),
	} {
		d.Summary.Added += c.Added
		d.Summary.Removed += c.Removed
		d.Summary.Changed += c.Changed
	}
	return d
}

func summarize[T any](c CategoryDiff[T]) Summary {
	return Summary{Added: len(c.Added), Removed: len(c.Removed), Changed: len(c.Changed)}
}

// diffCategory matches base and head by identity key: head-only = added,
// base-only = removed, both-but-different = changed (per the compare func).
func diffCategory[T any](base, head []T, key func(T) string, compare func(before, after T) ([]string, []FieldDiff)) CategoryDiff[T] {
	baseBy := map[string]T{}
	for _, b := range base {
		baseBy[key(b)] = b
	}
	headBy := map[string]T{}
	for _, h := range head {
		headBy[key(h)] = h
	}

	var out CategoryDiff[T]
	for k, h := range headBy {
		b, ok := baseBy[k]
		if !ok {
			out.Added = append(out.Added, h)
			continue
		}
		if changes, schemaDiff := compare(b, h); len(changes) > 0 {
			out.Changed = append(out.Changed, Change[T]{
				Key: k, Changes: changes, SchemaDiff: schemaDiff, Before: b, After: h,
			})
		}
	}
	for k, b := range baseBy {
		if _, ok := headBy[k]; !ok {
			out.Removed = append(out.Removed, b)
		}
	}

	sort.Slice(out.Added, func(i, j int) bool { return key(out.Added[i]) < key(out.Added[j]) })
	sort.Slice(out.Removed, func(i, j int) bool { return key(out.Removed[i]) < key(out.Removed[j]) })
	sort.Slice(out.Changed, func(i, j int) bool { return out.Changed[i].Key < out.Changed[j].Key })
	return out
}

// --- per-type comparisons (identity is equal; what content differs?) ---

func endpointChanges(b, a model.Endpoint) ([]string, []FieldDiff) {
	var changes []string
	var fields []FieldDiff
	if b.Confidence != a.Confidence {
		changes = append(changes, "confidence")
	}
	if b.Detection != a.Detection {
		changes = append(changes, "detection")
	}
	if req := diffSchema(b.Request, a.Request, ""); len(req) > 0 {
		changes = append(changes, "request_schema")
		fields = append(fields, prefix("request", req)...)
	}
	if resp := diffSchema(b.Response, a.Response, ""); len(resp) > 0 {
		changes = append(changes, "response_schema")
		fields = append(fields, prefix("response", resp)...)
	}
	return changes, fields
}

func dependencyChanges(b, a model.Dependency) ([]string, []FieldDiff) {
	var changes []string
	if b.URL != a.URL {
		changes = append(changes, "url")
	}
	if b.Resolved != a.Resolved {
		changes = append(changes, "resolved")
	}
	if b.Confidence != a.Confidence {
		changes = append(changes, "confidence")
	}
	return changes, nil
}

func kafkaChanges(b, a model.KafkaEdge) ([]string, []FieldDiff) {
	var changes []string
	if b.Confidence != a.Confidence {
		changes = append(changes, "confidence")
	}
	if b.Resolved != a.Resolved {
		changes = append(changes, "resolved")
	}
	if b.Detection != a.Detection {
		changes = append(changes, "detection")
	}
	var fields []FieldDiff
	if sd := diffSchema(b.Schema, a.Schema, ""); len(sd) > 0 {
		changes = append(changes, "schema")
		fields = sd
	}
	return changes, fields
}

// --- schema field diff ---

// diffSchema compares two schemas recursively, matching nested fields by wire
// name. path is the dotted prefix ("" at the root).
func diffSchema(b, a *model.Schema, path string) []FieldDiff {
	switch {
	case b == nil && a == nil:
		return nil
	case b == nil:
		return []FieldDiff{{Field: rootOr(path), Change: "added", After: a.Type}}
	case a == nil:
		return []FieldDiff{{Field: rootOr(path), Change: "removed", Before: b.Type}}
	}

	var out []FieldDiff
	if b.Type != a.Type || b.Items != a.Items || b.ValueType != a.ValueType {
		out = append(out, FieldDiff{Field: rootOr(path), Change: "type_changed",
			Before: schemaTypeString(b), After: schemaTypeString(a)})
	}
	if b.Required != a.Required {
		out = append(out, FieldDiff{Field: rootOr(path), Change: "requiredness_changed",
			Before: string(b.Required), After: string(a.Required)})
	}
	if b.Nullable != a.Nullable {
		out = append(out, FieldDiff{Field: rootOr(path), Change: "nullability_changed",
			Before: boolStr(b.Nullable), After: boolStr(a.Nullable)})
	}

	bFields := fieldsByName(b.Nested)
	aFields := fieldsByName(a.Nested)
	for _, name := range sortedNames(bFields, aFields) {
		bf, inB := bFields[name]
		af, inA := aFields[name]
		child := joinPath(path, name)
		switch {
		case !inB:
			out = append(out, FieldDiff{Field: child, Change: "added", After: af.Type})
		case !inA:
			out = append(out, FieldDiff{Field: child, Change: "removed", Before: bf.Type})
		default:
			out = append(out, diffSchema(&bf, &af, child)...)
		}
	}
	return out
}

// schemaTypeString renders a type for humans: array/items and maps included.
func schemaTypeString(s *model.Schema) string {
	switch s.Type {
	case "array":
		return "array<" + s.Items + ">"
	case "map":
		return "map<" + s.KeyType + "," + s.ValueType + ">"
	default:
		return s.Type
	}
}

func fieldsByName(nested []model.Schema) map[string]model.Schema {
	m := make(map[string]model.Schema, len(nested))
	for _, f := range nested {
		m[f.Name] = f
	}
	return m
}

func sortedNames(a, b map[string]model.Schema) []string {
	seen := map[string]bool{}
	var names []string
	for n := range a {
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	for n := range b {
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

func prefix(p string, fds []FieldDiff) []FieldDiff {
	out := make([]FieldDiff, len(fds))
	for i, fd := range fds {
		fd.Field = joinPath(p, fd.Field)
		out[i] = fd
	}
	return out
}

func joinPath(p, name string) string {
	switch {
	case name == "" || name == "(root)":
		return rootOr(p)
	case p == "":
		return name
	default:
		return p + "." + name
	}
}

func rootOr(path string) string {
	if path == "" {
		return "(root)"
	}
	return path
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
