package registry

import "testing"

// TestDefault pins the registered provider set. Adding a framework must extend
// this table, not surprise it.
func TestDefault(t *testing.T) {
	want := map[string]bool{
		"spring-boot-java":   false,
		"micronaut-java":     false,
		"quarkus-java":       false,
		"spring-boot-kotlin": false,
	}
	ps := Default()
	if len(ps) != len(want) {
		t.Fatalf("got %d providers, want %d", len(ps), len(want))
	}
	for _, p := range ps {
		if _, ok := want[p.Name()]; !ok {
			t.Errorf("unexpected provider %q", p.Name())
			continue
		}
		want[p.Name()] = true
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("provider %q not registered", name)
		}
	}
}
