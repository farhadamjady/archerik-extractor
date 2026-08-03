package registry

import "testing"

func TestParseConfluentTopicRecord(t *testing.T) {
	c := Parse(map[string]string{
		"spring.kafka.properties.schema.registry.url":                  "http://schema-registry:8081",
		"spring.kafka.producer.properties.value.subject.name.strategy": "io.confluent.kafka.serializers.subject.TopicRecordNameStrategy",
		"spring.kafka.bootstrap-servers":                               "kafka:9092",
	})
	if !c.Configured() || c.URL != "http://schema-registry:8081" {
		t.Fatalf("url = %q, want http://schema-registry:8081", c.URL)
	}
	if c.Strategy != StrategyTopicRecord {
		t.Errorf("strategy = %q, want %q", c.Strategy, StrategyTopicRecord)
	}
}

func TestParseBareURLDefaultStrategy(t *testing.T) {
	c := Parse(map[string]string{"schema.registry.url": "http://sr:8081"})
	if !c.Configured() || c.URL != "http://sr:8081" {
		t.Fatalf("url = %q", c.URL)
	}
	if c.Strategy != "" {
		t.Errorf("strategy = %q, want empty (unset)", c.Strategy)
	}
}

func TestParseApicurioRecordStrategy(t *testing.T) {
	c := Parse(map[string]string{
		"mp.messaging.connector.smallrye-kafka.apicurio.registry.url": "http://apicurio:8080/apis/registry/v2",
		"mp.messaging.outgoing.orders.value.subject.name.strategy":    "RecordNameStrategy",
	})
	if c.URL != "http://apicurio:8080/apis/registry/v2" {
		t.Errorf("url = %q", c.URL)
	}
	if c.Strategy != StrategyRecord {
		t.Errorf("strategy = %q, want %q", c.Strategy, StrategyRecord)
	}
}

func TestParseNoRegistry(t *testing.T) {
	c := Parse(map[string]string{"spring.kafka.bootstrap-servers": "kafka:9092"})
	if c.Configured() {
		t.Errorf("configured = true, want false; %+v", c)
	}
}

func TestNormalizeStrategyVariants(t *testing.T) {
	cases := map[string]SubjectStrategy{
		"io.confluent.kafka.serializers.subject.TopicNameStrategy": StrategyTopic,
		"TopicRecordNameStrategy":                                  StrategyTopicRecord,
		"topic":                                                    StrategyTopic,
		"unknown.Thing":                                            "",
	}
	for in, want := range cases {
		if got := normalizeStrategy(in); got != want {
			t.Errorf("normalizeStrategy(%q) = %q, want %q", in, got, want)
		}
	}
}
