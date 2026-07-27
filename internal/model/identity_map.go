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
	Hosts       []string           `json:"hosts"`
	Namespace   string             `json:"namespace"`
	Environment string             `json:"environment"` // overlay/values file this was rendered from, "" if not applicable
	Source      IdentitySource     `json:"source"`
	Confidence  IdentityConfidence `json:"confidence"`
}

// SortIdentityMap orders Entries deterministically and drops exact
// duplicates (the same service/namespace/environment/source/confidence/hosts
// seen twice — e.g. a Service rendered identically from a base kustomization
// and every overlay that doesn't override it). Idempotent; call before
// marshaling, mirroring Sort(svc) for the Service contract.
func SortIdentityMap(im *IdentityMap) {
	sort.SliceStable(im.Entries, func(i, j int) bool {
		return identityEntrySortKey(im.Entries[i]) < identityEntrySortKey(im.Entries[j])
	})
	im.Entries = dedup(im.Entries, identityEntrySortKey)
}

// identityEntrySortKey covers every scalar field plus a sorted copy of Hosts
// (original Hosts order carries no meaning). Environment is part of the key,
// so two entries sharing (ServiceName, Namespace) but differing by
// Environment are distinct and both survive — a service can have different
// hosts per environment overlay.
func identityEntrySortKey(e IdentityEntry) string {
	hosts := append([]string(nil), e.Hosts...)
	sort.Strings(hosts)
	return join(e.ServiceName, e.Namespace, e.Environment, string(e.Source), string(e.Confidence), join(hosts...))
}
