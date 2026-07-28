package deployrepo

import (
	"strings"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
)

// Resolver is one way of turning a deploy repo into service-identity facts —
// rendering Helm charts, building Kustomize overlays, reading plain manifests,
// reading a self-declaration file, and (later) parsing Terraform. Companies
// resolve service hosts through different mechanisms, so resolvers are
// independently selectable (see registry.go + the --resolvers flag): a run
// enables the ones matching its infrastructure. Each resolver reads only its
// own slice of the repo and returns identity facts plus non-fatal per-unit
// errors; a failure in one resolver never aborts the others.
type Resolver interface {
	// Name is the stable identifier used in --resolvers (e.g. "helm",
	// "kustomize", "k8s-raw", "self-declared").
	Name() string
	// Resolve reads the repo through rc and returns the identity facts it
	// found. RenderErrors are non-fatal warnings (a broken chart/overlay/file),
	// surfaced to the operator but never failing the scan.
	Resolve(rc ResolveContext) ([]model.IdentityEntry, []RenderError)
}

// ResolveContext is everything a resolver needs to read the repo, threaded
// from deployrepo.Run so resolvers stay constructor-free and testable.
type ResolveContext struct {
	// AbsRoot is the absolute repo root. Helm/Kustomize need real on-disk
	// paths (their libraries read the filesystem directly); Tree is the
	// repo-relative, exclusion-aware view for discovery and raw reads.
	AbsRoot string
	Tree    provider.FileTree
	// Environments filters which values/overlay environments render; empty
	// renders every discovered environment.
	Environments []string
	// Opts carries cross-resolver knobs (e.g. the namespace convention applied
	// when a manifest declares no namespace).
	Opts ResolverOptions
}

// ResolverOptions holds tuning shared across resolvers: which non-Service
// kinds to consume (Ingress/VirtualService are cross-cutting host-resolution
// mechanisms, gated independently of the source resolvers that render them),
// and the company namespace convention.
type ResolverOptions struct {
	// Ingress/Istio gate whether Ingress and VirtualService documents are
	// consumed (their external hosts folded into the owning Service). A source
	// resolver still renders them; these decide whether their hosts are kept.
	Ingress bool
	Istio   bool
	// NamespaceConvention, when set, fills the namespace for a Service that
	// declares none, instead of defaulting to "default" — for orgs whose
	// namespace is derived from the service name. Empty leaves the default
	// behavior untouched.
	NamespaceConvention string
}

// namespaceOrDefault resolves the namespace for a service: its declared value
// wins; otherwise the company convention (if configured) derives one from the
// service name; otherwise "default". Applied uniformly to Service entries and
// to Ingress/VirtualService fold targets so a convention-namespaced Service and
// an un-namespaced Ingress pointing at it still land on the same namespace.
func namespaceOrDefault(declared, serviceName string, opts ResolverOptions) string {
	if declared != "" {
		return declared
	}
	if opts.NamespaceConvention != "" {
		return applyNamespaceConvention(serviceName, opts.NamespaceConvention)
	}
	return "default"
}

// applyNamespaceConvention derives a namespace from a service name. Supported:
//
//	"service-name"        -> namespace == the service name
//	"replace:<from>:<to>" -> service name with <from> replaced by <to>
//	                         (e.g. "replace:-service:" turns pym-service -> pym)
//
// An unrecognized convention best-efforts to the service name unchanged; the
// CLI validates the format up front.
func applyNamespaceConvention(serviceName, convention string) string {
	switch {
	case convention == "service-name":
		return serviceName
	case strings.HasPrefix(convention, "replace:"):
		if parts := strings.SplitN(convention, ":", 3); len(parts) == 3 {
			return strings.ReplaceAll(serviceName, parts[1], parts[2])
		}
	}
	return serviceName
}

// ValidNamespaceConvention reports whether s is a namespace-convention form the
// CLI accepts. Empty is valid (feature off).
func ValidNamespaceConvention(s string) bool {
	return s == "" || s == "service-name" ||
		(strings.HasPrefix(s, "replace:") && len(strings.SplitN(s, ":", 3)) == 3)
}
