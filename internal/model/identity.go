package model

import "sort"

// Deterministic identity + ordering. The backend diffs each commit's full graph
// against the stored one, so "the same edge across commits" must be stable, and
// output must be byte-stable across runs. This file defines both:
//
//   - Identity keys (EndpointKey/DependencyKey/KafkaKey) — the canonical node
//     identity the backend dedupes/diffs on. NOT emitted as IDs.
//   - Sort(svc) — total, deterministic ordering of every slice (and nested schema
//     fields), so marshaling is byte-stable regardless of detection order.
//
// Identity keys are intentionally coarse (per DESIGN §14); the sort comparators
// below add every remaining field as a tiebreak so ordering is TOTAL even when
// two edges share an identity key (e.g. conditional candidates from one call site).

// EndpointKey = HTTP verb + composed path.
func EndpointKey(e Endpoint) string { return e.Method + " " + e.Path }

// DependencyKey = target name + detection method.
func DependencyKey(d Dependency) string { return d.TargetName + "|" + string(d.Detection) }

// KafkaKey = topic + direction. Direction ("producer"/"consumer") is implied by
// the slice the edge lives in; callers pass it so a produce and a consume of the
// same topic get distinct identities.
func KafkaKey(k KafkaEdge, direction string) string { return k.Topic + "|" + direction }

// Sort orders every slice in svc deterministically and recursively sorts nested
// schema fields. Idempotent. Call it in the marshal phase before encoding.
func Sort(svc *Service) {
	sort.SliceStable(svc.Endpoints, func(i, j int) bool {
		return endpointSortKey(svc.Endpoints[i]) < endpointSortKey(svc.Endpoints[j])
	})
	sort.SliceStable(svc.OutboundDependencies, func(i, j int) bool {
		return dependencySortKey(svc.OutboundDependencies[i]) < dependencySortKey(svc.OutboundDependencies[j])
	})
	sort.SliceStable(svc.KafkaProducers, func(i, j int) bool {
		return kafkaSortKey(svc.KafkaProducers[i]) < kafkaSortKey(svc.KafkaProducers[j])
	})
	sort.SliceStable(svc.KafkaConsumers, func(i, j int) bool {
		return kafkaSortKey(svc.KafkaConsumers[i]) < kafkaSortKey(svc.KafkaConsumers[j])
	})
	sort.SliceStable(svc.DatabasesUsed, func(i, j int) bool {
		return databaseSortKey(svc.DatabasesUsed[i]) < databaseSortKey(svc.DatabasesUsed[j])
	})
	sort.SliceStable(svc.ConfigDependencies, func(i, j int) bool {
		return configSortKey(svc.ConfigDependencies[i]) < configSortKey(svc.ConfigDependencies[j])
	})
	for i := range svc.Endpoints {
		sortSchema(svc.Endpoints[i].Request)
		sortSchema(svc.Endpoints[i].Response)
	}
	for i := range svc.KafkaProducers {
		sortSchema(svc.KafkaProducers[i].Schema)
	}
	for i := range svc.KafkaConsumers {
		sortSchema(svc.KafkaConsumers[i].Schema)
	}
}

// sortSchema recursively orders nested fields by wire name. Fields are deduped by
// wire name upstream, so Name is the primary key with Type as a tiebreak.
func sortSchema(s *Schema) {
	if s == nil {
		return
	}
	for i := range s.Nested {
		sortSchema(&s.Nested[i])
	}
	sort.SliceStable(s.Nested, func(i, j int) bool {
		return schemaSortKey(s.Nested[i]) < schemaSortKey(s.Nested[j])
	})
}

// Sort comparators use NUL separators so field boundaries never blur (e.g. so
// {"a","b"} and {"ab",""} can't collide), and include every field for a total order.

func endpointSortKey(e Endpoint) string {
	return join(e.Method, e.Path, string(e.Protocol), string(e.Detection), string(e.Confidence))
}

func dependencySortKey(d Dependency) string {
	return join(d.TargetName, d.URL, string(d.Protocol), string(d.Detection),
		string(d.Confidence), boolStr(d.Resolved), boolStr(d.Conditional), d.CandidateGroup)
}

func kafkaSortKey(k KafkaEdge) string {
	return join(k.Topic, string(k.Protocol), string(k.Detection), string(k.Confidence),
		boolStr(k.Resolved), boolStr(k.Conditional), k.CandidateGroup)
}

func databaseSortKey(d Database) string {
	return join(d.Name, d.Kind, string(d.Detection), string(d.Confidence))
}

func configSortKey(c ConfigDep) string {
	return join(c.Key, c.Value, boolStr(c.Resolved), string(c.Confidence))
}

func schemaSortKey(s Schema) string { return join(s.Name, s.Type) }

func join(parts ...string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += "\x00"
		}
		out += p
	}
	return out
}

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
