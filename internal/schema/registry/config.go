// Package registry models a Kafka Schema Registry's STATIC configuration —
// whether one is configured and which subject-name strategy is in effect —
// extracted from a service's config layer without any network access (DESIGN
// §12, no-network). It is groundwork: a later round uses the URL to fetch
// subjects (Confluent/Apicurio REST) and the strategy to compute a topic's
// subject name; this round only detects and stores the configuration.
package registry

import (
	"sort"
	"strings"
)

// SubjectStrategy is the Confluent/Apicurio subject-name strategy that maps a
// topic (and record type) to the registry subject a schema is registered under.
type SubjectStrategy string

const (
	// StrategyTopic: subject = "<topic>-key" / "<topic>-value" (the default).
	StrategyTopic SubjectStrategy = "Topic"
	// StrategyRecord: subject = the record's fully-qualified name.
	StrategyRecord SubjectStrategy = "Record"
	// StrategyTopicRecord: subject = "<topic>-<record-fqn>".
	StrategyTopicRecord SubjectStrategy = "TopicRecord"
)

// Config is the detected registry configuration. A zero Config (empty URL) means
// no registry is configured; Strategy is "" when unset (registry default =
// TopicName, but we record only what is explicitly declared).
type Config struct {
	URL      string          `json:"url,omitempty"`
	Strategy SubjectStrategy `json:"strategy,omitempty"`
}

// Configured reports whether a Schema Registry URL was found.
func (c Config) Configured() bool { return c.URL != "" }

// Parse scans a flat config key->value map for a Schema Registry URL and the
// value subject-name strategy, across the common Confluent/Apicurio and
// Spring/Quarkus key spellings. Keys are matched by suffix (so
// spring.kafka.properties.schema.registry.url and a bare schema.registry.url
// both hit), and scanned in sorted order for a deterministic pick when several
// match.
func Parse(props map[string]string) Config {
	var c Config
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		lk := strings.ToLower(k)
		switch {
		case c.URL == "" && isRegistryURLKey(lk):
			c.URL = strings.TrimSpace(props[k])
		case c.Strategy == "" && strings.HasSuffix(lk, "subject.name.strategy"):
			c.Strategy = normalizeStrategy(props[k])
		}
	}
	return c
}

// isRegistryURLKey matches the Schema Registry URL property across binders:
// Confluent's schema.registry.url (bare or namespaced) and Apicurio's
// apicurio.registry.url.
func isRegistryURLKey(lowerKey string) bool {
	return strings.HasSuffix(lowerKey, "schema.registry.url") ||
		strings.HasSuffix(lowerKey, "apicurio.registry.url")
}

// normalizeStrategy maps a strategy value — a Confluent strategy class
// (io.confluent.kafka.serializers.subject.TopicRecordNameStrategy) or a short
// mnemonic — to a SubjectStrategy, or "" when unrecognized.
func normalizeStrategy(v string) SubjectStrategy {
	name := strings.TrimSpace(v)
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		name = name[i+1:]
	}
	switch name {
	case "TopicRecordNameStrategy":
		return StrategyTopicRecord
	case "RecordNameStrategy":
		return StrategyRecord
	case "TopicNameStrategy":
		return StrategyTopic
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "topicrecord":
		return StrategyTopicRecord
	case "record":
		return StrategyRecord
	case "topic":
		return StrategyTopic
	}
	return ""
}
