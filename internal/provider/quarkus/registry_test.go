package quarkus

import (
	"testing"

	"github.com/farhadamjady/archerik-extractor/internal/provider"
	"github.com/farhadamjady/archerik-extractor/internal/schema/registry"
)

// TestRegistryConfigAttached proves K4 wiring on Quarkus: the config indexer
// detects an Apicurio registry URL + subject-name strategy from
// application.properties and attaches it to the Index.
func TestRegistryConfigAttached(t *testing.T) {
	props := "mp.messaging.connector.smallrye-kafka.apicurio.registry.url=http://apicurio:8080/apis/registry/v2\n" +
		"mp.messaging.outgoing.orders.value.subject.name.strategy=io.confluent.kafka.serializers.subject.RecordNameStrategy\n"
	pf, err := (rawParser{kind: provider.KindSpringConfig}).Parse("application.properties", []byte(props))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	idx := &provider.Index{}
	ic := &provider.IndexContext{Parsed: map[string]provider.ParsedFile{"application.properties": pf}}
	if err := (configIndexer{}).Index(ic, idx); err != nil {
		t.Fatalf("index: %v", err)
	}
	if idx.Registry == nil {
		t.Fatal("Registry not attached")
	}
	if idx.Registry.URL != "http://apicurio:8080/apis/registry/v2" {
		t.Errorf("url = %q", idx.Registry.URL)
	}
	if idx.Registry.Strategy != registry.StrategyRecord {
		t.Errorf("strategy = %q, want %q", idx.Registry.Strategy, registry.StrategyRecord)
	}
}
