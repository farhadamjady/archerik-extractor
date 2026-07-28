package deployrepo

import (
	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/scan"
)

// helmResolver renders every Helm chart in the repo (real chart-engine
// execution, per env values overlay) and extracts its Service/Ingress/
// VirtualService identity.
type helmResolver struct{}

func (helmResolver) Name() string { return "helm" }

func (helmResolver) Resolve(rc ResolveContext) ([]model.IdentityEntry, []RenderError) {
	return RenderHelmCharts(rc.AbsRoot, discoverCharts(rc.Tree), rc.Environments, rc.Opts)
}

// kustomizeResolver builds every Kustomize overlay root (respecting
// namePrefix/nameSuffix and base composition) and extracts identity.
type kustomizeResolver struct{}

func (kustomizeResolver) Name() string { return "kustomize" }

func (kustomizeResolver) Resolve(rc ResolveContext) ([]model.IdentityEntry, []RenderError) {
	return RenderKustomizations(rc.AbsRoot, discoverKustomizations(rc.Tree), rc.Opts)
}

// k8sRawResolver reads plain (already-rendered) k8s manifests. It owns the
// exclusion of Helm chart and Kustomize base/overlay directories from the raw
// scan — those are covered by their own resolvers, and re-reading their source
// YAML here would double-count (or, for Kustomize, emit a base's pre-prefix
// name as if it deployed standalone).
type k8sRawResolver struct{}

func (k8sRawResolver) Name() string { return "k8s-raw" }

func (k8sRawResolver) Resolve(rc ResolveContext) ([]model.IdentityEntry, []RenderError) {
	chartDirs := discoverCharts(rc.Tree)
	kustomizationDirs := allKustomizationDirs(rc.Tree)

	exclude := append([]string{}, deployRepoExclude...)
	exclude = append(exclude, rawScanExclusions(chartDirs, kustomizationDirs)...)
	exclude = append(exclude, kustomizationReferencedExclusions(rc.Tree, kustomizationDirs)...)

	rawTree := scan.NewOSFileTree(rc.AbsRoot, exclude)
	docs, errs := discoverK8sRaw(rawTree)
	return extractEntries(docs, model.SourceK8sRaw, rc.Opts), errs
}
