package model

import "testing"

// TestSortIdentityMapDedup: two entries differing only in Hosts order are the
// same fact and must collapse to one; the same (ServiceName, Namespace) with
// a different Environment is a distinct fact (a service can have different
// hosts per environment) and both must survive.
func TestSortIdentityMapDedup(t *testing.T) {
	im := &IdentityMap{
		Entries: []IdentityEntry{
			{
				ServiceName: "pym-service", Namespace: "payments", Environment: "prod",
				Hosts: []string{"pym-service", "pym-service.payments"}, Source: SourceHelm, Confidence: IdentityConfirmed,
			},
			{
				// same fact as above, Hosts just in a different order
				ServiceName: "pym-service", Namespace: "payments", Environment: "prod",
				Hosts: []string{"pym-service.payments", "pym-service"}, Source: SourceHelm, Confidence: IdentityConfirmed,
			},
			{
				// same (ServiceName, Namespace), different Environment: must survive
				ServiceName: "pym-service", Namespace: "payments", Environment: "staging",
				Hosts: []string{"pym-service", "pym-service.payments"}, Source: SourceHelm, Confidence: IdentityConfirmed,
			},
		},
	}
	SortIdentityMap(im)

	if len(im.Entries) != 2 {
		t.Fatalf("len(Entries) = %d, want 2 (host-order duplicate collapsed, env-distinct entry survives): %+v", len(im.Entries), im.Entries)
	}
	envs := map[string]bool{}
	for _, e := range im.Entries {
		envs[e.Environment] = true
	}
	if !envs["prod"] || !envs["staging"] {
		t.Errorf("envs = %v, want both prod and staging", envs)
	}
}

func TestNewIdentityMapNonNilEntries(t *testing.T) {
	im := NewIdentityMap("github.com/acme/payments-deploy")
	if im.Entries == nil {
		t.Fatal("Entries is nil, want empty slice (stable JSON contract: [] not null)")
	}
	if im.Repository != "github.com/acme/payments-deploy" {
		t.Errorf("Repository = %q", im.Repository)
	}
}
