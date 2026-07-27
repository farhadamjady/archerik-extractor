// Package deployrepo implements --mode=deploy-repo: scanning a deployment/
// GitOps repo (Helm charts, Kustomize overlays, plain k8s manifests) and
// emitting an identity map — service_name/hosts[] facts the backend joins
// against a service-mode scan's resolved-but-unproven host strings
// (Dependency.TargetName) to complete those edges. This is a second,
// parallel top-level flow, not a provider.Provider and not an extension of
// internal/deployconfig (which resolves placeholders FOR a service scan);
// this scans a different kind of repo with no source code and no
// provider/detector seam.
package deployrepo

import (
	"context"
	"encoding/json"
	"path/filepath"

	"github.com/farhadamjady/service-discovery/internal/auth"
	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/scan"
	"github.com/farhadamjady/service-discovery/internal/submit"
)

// Options carries everything a deploy-repo scan needs.
type Options struct {
	Root       string
	Repository string // repo identifier emitted as IdentityMap.repository
	APIKey     string
	APIURL     string
	DryRun     bool
	// Environments filters which values/overlay environments render; empty
	// renders every discovered environment (Helm: base + every values-<env>.yaml;
	// Kustomize: every overlay).
	Environments []string
	CI           submit.Meta
	// OnSubmitResponse, when set, receives the raw ingest-response body after
	// a successful submit.
	OnSubmitResponse func([]byte)
}

// deployRepoExclude keeps the directory walk off version-control and
// dependency metadata that never holds a k8s manifest.
var deployRepoExclude = []string{"**/.git/**", "**/node_modules/**"}

// Run scans opt.Root for Helm charts, Kustomize overlays, and plain k8s
// manifests, renders/parses each, extracts Service/Ingress/VirtualService
// identity facts, and returns the deterministic, deduped IdentityMap plus any
// non-fatal per-unit render failures. Stateless — no cross-run cache, matching
// the extractor's per-commit/incremental design: the backend does any
// longitudinal reconciliation, not this scan.
func Run(ctx context.Context, opt Options) (*model.IdentityMap, []RenderError, error) {
	if _, err := auth.Validate(ctx, opt.APIKey, opt.APIURL); err != nil {
		return nil, nil, err
	}

	root, err := filepath.Abs(opt.Root)
	if err != nil {
		return nil, nil, err
	}

	tree := scan.NewOSFileTree(root, deployRepoExclude)
	var allErrs []RenderError

	kustomizationDirs := allKustomizationDirs(tree)
	kustomizeRoots := discoverKustomizations(tree)
	kEntries, kErrs := RenderKustomizations(root, kustomizeRoots)
	allErrs = append(allErrs, kErrs...)

	chartDirs := discoverCharts(tree)
	hEntries, hErrs := RenderHelmCharts(root, chartDirs, opt.Environments)
	allErrs = append(allErrs, hErrs...)

	rawExclude := append([]string{}, deployRepoExclude...)
	rawExclude = append(rawExclude, rawScanExclusions(chartDirs, kustomizationDirs)...)
	rawExclude = append(rawExclude, kustomizationReferencedExclusions(tree, kustomizationDirs)...)
	rawTree := scan.NewOSFileTree(root, rawExclude)
	rawDocs, rawErrs := discoverK8sRaw(rawTree)
	allErrs = append(allErrs, rawErrs...)
	rawEntries := extractEntries(rawDocs, model.SourceK8sRaw)

	im := model.NewIdentityMap(opt.Repository)
	im.Entries = append(im.Entries, kEntries...)
	im.Entries = append(im.Entries, hEntries...)
	im.Entries = append(im.Entries, rawEntries...)
	model.SortIdentityMap(im)

	if err := submitIdentityMap(ctx, opt, im); err != nil {
		return im, allErrs, err
	}
	return im, allErrs, nil
}

// rawScanExclusions keeps the raw-manifest scanner out of any Helm chart or
// Kustomize base/overlay directory — both are already covered by their own
// renderers, and re-reading their source YAML here would double-emit (or,
// for Kustomize, wrongly emit a base's pre-prefix name as if it were
// deployed standalone).
func rawScanExclusions(chartDirs, kustomizeDirs []string) []string {
	var out []string
	for _, d := range chartDirs {
		out = append(out, d+"/**")
	}
	for _, d := range kustomizeDirs {
		out = append(out, d+"/**")
	}
	return out
}

// Marshal encodes an IdentityMap canonically: sorted/deduped + compact JSON +
// trailing newline — mirrors pipeline.Marshal for *model.Service.
func Marshal(im *model.IdentityMap) ([]byte, error) {
	model.SortIdentityMap(im)
	b, err := json.Marshal(im)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// submitIdentityMap marshals and submits im, honoring DryRun — mirrors
// pipeline's submitGraph handling for *model.Service.
func submitIdentityMap(ctx context.Context, opt Options, im *model.IdentityMap) error {
	if opt.DryRun || opt.APIURL == "" {
		return nil
	}
	body, err := Marshal(im)
	if err != nil {
		return err
	}
	resp, err := submit.SubmitIdentityMap(ctx, opt.APIURL, opt.APIKey, body, opt.CI)
	if err != nil {
		return err
	}
	if opt.OnSubmitResponse != nil {
		opt.OnSubmitResponse(resp)
	}
	return nil
}
