package deployrepo

import (
	"fmt"
	"sort"
	"strings"
)

// sourceResolvers are the DEFAULT resolvers — run when --resolvers is empty.
// They all read the same GitOps repo. Order is fixed for deterministic
// execution (output is sorted regardless, but a stable order keeps logs
// predictable).
func sourceResolvers() []Resolver {
	return []Resolver{
		helmResolver{},
		kustomizeResolver{},
		k8sRawResolver{},
		selfDeclaredResolver{},
	}
}

// optionalResolvers are known but OFF by default — they must be named
// explicitly in --resolvers. terraform targets a separate infra repo, and
// default-on would pull vendored third-party module names into a normal scan.
func optionalResolvers() []Resolver {
	return []Resolver{
		terraformResolver{},
	}
}

// allResolvers is every selectable source/optional resolver (not the kind
// toggles), used for name validation and explicit selection.
func allResolvers() []Resolver {
	return append(sourceResolvers(), optionalResolvers()...)
}

// kindResolvers are cross-cutting host-resolution mechanisms — not independent
// readers, but toggles for whether a source resolver consumes Ingress /
// VirtualService documents it renders. They are selectable by the same
// --resolvers list, translated into ResolverOptions.
var kindResolvers = []string{"ingress", "istio"}

// Select turns a --resolvers name list into the source resolvers to run plus
// the completed ResolverOptions (Ingress/Istio kind toggles). Empty names ==
// everything on. The passed-in base carries options set elsewhere (e.g.
// NamespaceConvention); Select fills its kind toggles. Unknown names are an
// error listing what's valid.
func Select(names []string, base ResolverOptions) ([]Resolver, ResolverOptions, error) {
	if len(names) == 0 {
		base.Ingress, base.Istio = true, true
		return sourceResolvers(), base, nil
	}

	known := map[string]bool{}
	for _, r := range allResolvers() {
		known[r.Name()] = true
	}
	for _, k := range kindResolvers {
		known[k] = true
	}

	want := map[string]bool{}
	for _, n := range names {
		if !known[n] {
			return nil, base, fmt.Errorf("unknown resolver %q (valid: %s)", n, strings.Join(sortedKeys(known), ", "))
		}
		want[n] = true
	}

	var selected []Resolver
	for _, r := range allResolvers() {
		if want[r.Name()] {
			selected = append(selected, r)
		}
	}
	base.Ingress = want["ingress"]
	base.Istio = want["istio"]
	return selected, base, nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
