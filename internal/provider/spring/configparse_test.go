package spring

import "testing"

// TestParseYAMLMultiDocument (round-9 finding): Spring applies every unmarked
// `---` document of a config file; profile-marked documents are overlays and
// stay out of the base map.
func TestParseYAMLMultiDocument(t *testing.T) {
	src := []byte("spring:\n  kafka:\n    consumer:\n      group-id: search\n---\nproduct:\n  topic:\n    name: dbproduct.public.product\n---\nspring:\n  config:\n    activate:\n      on-profile: prod\nproduct:\n  topic:\n    name: prod-only\n")
	m, err := parseConfig("application.yml", src)
	if err != nil {
		t.Fatal(err)
	}
	if m["product.topic.name"] != "dbproduct.public.product" {
		t.Errorf("later unmarked doc key = %q, want dbproduct.public.product", m["product.topic.name"])
	}
	if m["spring.kafka.consumer.group-id"] != "search" {
		t.Errorf("first doc key lost: %q", m["spring.kafka.consumer.group-id"])
	}
}
