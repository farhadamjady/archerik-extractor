package model

import "sort"

// IdentityMap is the deploy-repo-mode (and .ekg-identity.json fallback)
// output contract: a flat list of facts joining a service's real identity
// (name, namespace, environment) to every hostname a caller might resolve for
// it. A service-mode scan resolves an outbound call to a host string (e.g.
// Dependency.TargetName = "pym-service") but cannot prove which service owns
// that host — repo directory name, logical service name, and DNS host
// frequently diverge. The mapping lives in the deployment/GitOps repo, not in
// any service repo; this type carries it so the backend can join on the host
// string and complete those edges.
type IdentityMap struct {
	Repository string          `json:"repository"`
	Entries    []IdentityEntry `json:"entries"`
}

// NewIdentityMap returns an IdentityMap with a non-nil Entries slice (empty
// array, not null) — the same stable-contract convention as NewService.
func NewIdentityMap(repository string) *IdentityMap {
	return &IdentityMap{Repository: repository, Entries: []IdentityEntry{}}
}

// IdentityEntry is one service's resolvable identity within one namespace of
// one environment. The backend keys on (ServiceName, Namespace); Hosts always
// includes the fully-qualified in-cluster DNS name
// (name.namespace.svc.cluster.local) precisely so namespace collisions
// disambiguate on data, never by dropping entries.
type IdentityEntry struct {
	// ServiceName is the k8s Service's metadata.name (post Helm/Kustomize name
	// templating) — what DNS actually resolves, NOT the source repo's
	// directory name and not necessarily the app's logical/business name.
	ServiceName string `json:"service_name"`
	// Hosts is every hostname this entry answers to: the bare Service name,
	// its namespace-qualified and FQDN forms, and — for entries folded from an
	// Ingress/VirtualService — externally routed hostnames. Always non-empty.
	Hosts       []Host             `json:"hosts"`
	Namespace   string             `json:"namespace"`
	Environment string             `json:"environment"` // overlay/values file this was rendered from, "" if not applicable
	Source      IdentitySource     `json:"source"`
	Confidence  IdentityConfidence `json:"confidence"`
}

// Host is one address a service answers to, tagged with how the backend should
// match it and which resolver produced it. Kind drives the join rule: an
// in-cluster host is matched after normalizing a caller's k8s/mesh FQDN down to
// its bare service-name form (the suffix is mechanical); an external host is
// matched exactly (an opaque domain from an Ingress/gateway/DNS record has no
// algorithmic tie to the service name). Resolver is provenance for honesty and
// debugging when hosts from several resolvers merge into one entry.
type Host struct {
	Value    string         `json:"value"`
	Kind     string         `json:"kind,omitempty"`
	Resolver IdentitySource `json:"resolver,omitempty"`
}

// Host kinds. In-cluster hosts join after k8s/mesh-suffix normalization;
// external hosts join by exact string match.
const (
	HostInCluster = "in-cluster"
	HostExternal  = "external"
)

// HostValues extracts the bare host strings, order-preserving — for callers
// (and tests) that only need the values.
func HostValues(hosts []Host) []string {
	out := make([]string, len(hosts))
	for i, h := range hosts {
		out[i] = h.Value
	}
	return out
}

// SortIdentityMap orders Entries deterministically and drops exact
// duplicates (the same service/namespace/environment/source/confidence/hosts
// seen twice — e.g. a Service rendered identically from a base kustomization
// and every overlay that doesn't override it). Idempotent; call before
// marshaling, mirroring Sort(svc) for the Service contract.
func SortIdentityMap(im *IdentityMap) {
	for i := range im.Entries {
		sortHosts(&im.Entries[i])
	}
	sort.SliceStable(im.Entries, func(i, j int) bool {
		return identityEntrySortKey(im.Entries[i]) < identityEntrySortKey(im.Entries[j])
	})
	im.Entries = dedup(im.Entries, identityEntrySortKey)
}

// sortHosts orders an entry's Hosts and drops exact duplicates (same
// value/kind/resolver, e.g. one external host folded in from two Ingresses),
// so the emitted list is deterministic and clean.
func sortHosts(e *IdentityEntry) {
	sort.SliceStable(e.Hosts, func(i, j int) bool {
		return hostKey(e.Hosts[i]) < hostKey(e.Hosts[j])
	})
	e.Hosts = dedup(e.Hosts, hostKey)
}

func hostKey(h Host) string { return join(h.Value, h.Kind, string(h.Resolver)) }

// identityEntrySortKey covers every scalar field plus the entry's hosts by
// their (already order-independent) keys. Environment is part of the key, so
// two entries sharing (ServiceName, Namespace) but differing by Environment
// are distinct and both survive — a service can have different hosts per
// environment overlay.
func identityEntrySortKey(e IdentityEntry) string {
	keys := make([]string, len(e.Hosts))
	for i, h := range e.Hosts {
		keys[i] = hostKey(h)
	}
	sort.Strings(keys)
	return join(e.ServiceName, e.Namespace, e.Environment, string(e.Source), string(e.Confidence), join(keys...))
}
