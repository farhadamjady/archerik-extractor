package registry

import "testing"

// TestDefault pins the registered provider set: Spring only in the MVP.
// Adding Micronaut later must extend this table, not surprise it.
func TestDefault(t *testing.T) {
	ps := Default()
	if len(ps) != 1 {
		t.Fatalf("got %d providers, want 1", len(ps))
	}
	if got := ps[0].Name(); got != "spring-boot-java" {
		t.Errorf("provider = %q, want %q", got, "spring-boot-java")
	}
}
