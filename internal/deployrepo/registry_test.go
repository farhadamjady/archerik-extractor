package deployrepo

import (
	"strings"
	"testing"
)

func resolverNames(rs []Resolver) map[string]bool {
	out := map[string]bool{}
	for _, r := range rs {
		out[r.Name()] = true
	}
	return out
}

func TestSelectDefaultAllOn(t *testing.T) {
	rs, opts, err := Select(nil, ResolverOptions{})
	if err != nil {
		t.Fatal(err)
	}
	names := resolverNames(rs)
	for _, want := range []string{"helm", "kustomize", "k8s-raw", "self-declared"} {
		if !names[want] {
			t.Errorf("default set missing %q; got %v", want, names)
		}
	}
	if !opts.Ingress || !opts.Istio {
		t.Errorf("default kinds = ingress:%v istio:%v, want both on", opts.Ingress, opts.Istio)
	}
}

func TestSelectSubset(t *testing.T) {
	rs, opts, err := Select([]string{"helm"}, ResolverOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if names := resolverNames(rs); len(names) != 1 || !names["helm"] {
		t.Fatalf("resolvers = %v, want only helm", names)
	}
	if opts.Ingress || opts.Istio {
		t.Errorf("kinds = ingress:%v istio:%v, want both off (not requested)", opts.Ingress, opts.Istio)
	}
}

func TestSelectKindToggles(t *testing.T) {
	rs, opts, err := Select([]string{"kustomize", "ingress"}, ResolverOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if names := resolverNames(rs); len(names) != 1 || !names["kustomize"] {
		t.Fatalf("resolvers = %v, want only kustomize (ingress is a kind toggle, not a source)", names)
	}
	if !opts.Ingress || opts.Istio {
		t.Errorf("kinds = ingress:%v istio:%v, want ingress on, istio off", opts.Ingress, opts.Istio)
	}
}

func TestSelectUnknownError(t *testing.T) {
	_, _, err := Select([]string{"helm", "bogus"}, ResolverOptions{})
	if err == nil {
		t.Fatal("want error for unknown resolver, got nil")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("error = %v, want it to name the bad resolver", err)
	}
}

func TestSelectPreservesNamespaceConvention(t *testing.T) {
	_, opts, err := Select([]string{"helm"}, ResolverOptions{NamespaceConvention: "service-name"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.NamespaceConvention != "service-name" {
		t.Errorf("NamespaceConvention = %q, want it carried through Select", opts.NamespaceConvention)
	}
}
