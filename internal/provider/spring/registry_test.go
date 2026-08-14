package spring

import (
	"testing"

	"github.com/farhadamjady/archerik-extractor/internal/provider"
	"github.com/farhadamjady/archerik-extractor/internal/schema/registry"
)

// buildIndex runs the config indexer over virtual config files and returns the
// whole Index, so tests can inspect what indexers attach (here: Registry).
func buildIndex(t *testing.T, files map[string]string) *provider.Index {
	t.Helper()
	parsed := map[string]provider.ParsedFile{}
	for p, content := range files {
		pf, err := (rawParser{kind: provider.KindSpringConfig}).Parse(p, []byte(content))
		if err != nil {
			t.Fatalf("parse %s: %v", p, err)
		}
		parsed[p] = pf
	}
	idx := &provider.Index{}
	if err := (configIndexer{}).Index(&provider.IndexContext{Parsed: parsed}, idx); err != nil {
		t.Fatalf("index: %v", err)
	}
	return idx
}

// TestRegistryConfigAttached proves K4 wiring: the Spring config indexer detects
// a Schema Registry URL + subject-name strategy and attaches it to the Index.
func TestRegistryConfigAttached(t *testing.T) {
	idx := buildIndex(t, map[string]string{
		"application.yml": "spring:\n" +
			"  kafka:\n" +
			"    properties:\n" +
			"      schema.registry.url: http://schema-registry:8081\n" +
			"    producer:\n" +
			"      properties:\n" +
			"        value.subject.name.strategy: io.confluent.kafka.serializers.subject.TopicRecordNameStrategy\n",
	})
	if idx.Registry == nil {
		t.Fatal("Registry not attached")
	}
	if idx.Registry.URL != "http://schema-registry:8081" {
		t.Errorf("url = %q", idx.Registry.URL)
	}
	if idx.Registry.Strategy != registry.StrategyTopicRecord {
		t.Errorf("strategy = %q, want %q", idx.Registry.Strategy, registry.StrategyTopicRecord)
	}
}

// TestRegistryConfigAbsent proves no registry config -> nil (not an empty struct).
func TestRegistryConfigAbsent(t *testing.T) {
	idx := buildIndex(t, map[string]string{
		"application.yml": "spring:\n  kafka:\n    bootstrap-servers: kafka:9092\n",
	})
	if idx.Registry != nil {
		t.Errorf("Registry = %+v, want nil", idx.Registry)
	}
}
