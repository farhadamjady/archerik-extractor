package deployrepo

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/api/types"
	"sigs.k8s.io/kustomize/kyaml/filesys"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
)

// allKustomizationDirs finds every directory under tree containing a
// kustomization.yaml/.yml — bases and overlay roots alike. Exposed
// separately from discoverKustomizations so the orchestrator can exclude the
// FULL set (not just the roots) from raw-manifest scanning: a base dir's own
// pre-prefix YAML must never be read as a standalone raw manifest, or it
// emits a spurious entry alongside its properly rendered, prefixed form.
func allKustomizationDirs(tree provider.FileTree) []string {
	seen := map[string]bool{}
	var dirs []string
	for _, pattern := range []string{"**/kustomization.yaml", "**/kustomization.yml"} {
		for _, rel := range tree.Glob(pattern) {
			dir := path.Dir(rel)
			if !seen[dir] {
				seen[dir] = true
				dirs = append(dirs, dir)
			}
		}
	}
	return dirs
}

// discoverKustomizations finds every kustomization.yaml/.yml directory under
// tree and returns only the ones never referenced as a base or resource by a
// sibling kustomization — rendering a base standalone AND through every
// overlay that includes it would double-emit its Services.
func discoverKustomizations(tree provider.FileTree) []string {
	dirs := allKustomizationDirs(tree)

	referenced := map[string]bool{}
	for _, dir := range dirs {
		for _, ref := range kustomizationLocalRefs(tree, dir) {
			if isYAMLRef(ref) {
				continue // an individual manifest file, not a base directory
			}
			referenced[path.Clean(path.Join(dir, ref))] = true
		}
	}

	var roots []string
	for _, dir := range dirs {
		if !referenced[dir] {
			roots = append(roots, dir)
		}
	}
	return roots
}

// kustomizationReferencedExclusions returns raw-scan exclusion globs for every
// file or directory any kustomization pulls in via resources:/bases:, resolved
// to repo-relative paths. A loose file-ref base living OUTSIDE its overlay's
// own directory (so allKustomizationDirs' dir-level exclusion misses it) would
// otherwise be emitted twice: once rendered (name-prefixed) by Kustomize and
// once raw (un-prefixed) by the manifest scanner. Directory references become
// a "<dir>/**" subtree glob; individual file references become the exact path.
func kustomizationReferencedExclusions(tree provider.FileTree, kustomizationDirs []string) []string {
	var out []string
	for _, dir := range kustomizationDirs {
		for _, ref := range kustomizationLocalRefs(tree, dir) {
			resolved := path.Clean(path.Join(dir, ref))
			if isYAMLRef(ref) {
				out = append(out, resolved)
			} else {
				out = append(out, resolved+"/**")
			}
		}
	}
	return out
}

// kustomizationLocalRefs returns the local (non-URL) resources:/bases: entries
// of the kustomization at dir — both directory and individual-file references.
func kustomizationLocalRefs(tree provider.FileTree, dir string) []string {
	var refs []string
	for _, name := range []string{"kustomization.yaml", "kustomization.yml"} {
		rel := path.Join(dir, name)
		if !tree.Exists(rel) {
			continue
		}
		src, err := tree.Read(rel)
		if err != nil {
			continue
		}
		var doc struct {
			Resources []string `yaml:"resources"`
			Bases     []string `yaml:"bases"`
		}
		if err := yaml.Unmarshal(src, &doc); err != nil {
			continue
		}
		for _, r := range append(doc.Resources, doc.Bases...) {
			if strings.Contains(r, "://") {
				continue // remote URL — nothing on the local filesystem to exclude
			}
			refs = append(refs, r)
		}
	}
	return refs
}

func isYAMLRef(ref string) bool {
	return strings.HasSuffix(ref, ".yaml") || strings.HasSuffix(ref, ".yml")
}

// RenderKustomizations renders every kustomization root directory in
// overlayDirs (repo-relative, resolved against absRoot) and returns the
// identity entries found across all of them. Kustomize's own bases:/
// resources: cross-directory traversal (including "../../base"-style
// relative paths) is read directly off disk via filesys.MakeFsOnDisk rather
// than through the FileTree abstraction — that traversal semantics isn't
// worth re-implementing when the real kustomize API already reads the real
// filesystem faithfully.
func RenderKustomizations(absRoot string, overlayDirs []string) ([]model.IdentityEntry, []RenderError) {
	var entries []model.IdentityEntry
	var errs []RenderError
	fSys := filesys.MakeFsOnDisk()
	// LoadRestrictionsNone (vs. the RootOnly default) lets an overlay reference
	// files outside its own directory (e.g. resources: ["../../base/svc.yaml"]).
	// This matches how Argo CD itself runs Kustomize by default; we scan a
	// trusted, read-only local checkout, so the relaxed file access is safe.
	// The raw-scan exclusion (kustomizationReferencedExclusions) keeps such
	// out-of-tree files from also being counted as standalone manifests.
	opts := krusty.MakeDefaultOptions()
	opts.LoadRestrictions = types.LoadRestrictionsNone
	kustomizer := krusty.MakeKustomizer(opts)
	for _, dir := range overlayDirs {
		e, err := renderKustomization(kustomizer, fSys, absRoot, dir)
		if err != nil {
			errs = append(errs, RenderError{Unit: dir, Err: err})
			continue
		}
		entries = append(entries, e...)
	}
	return entries, errs
}

// renderKustomization runs one overlay and extracts identity entries from
// the rendered resources. Environment is the overlay directory's own
// basename (e.g. overlays/staging -> "staging") — a documented convention,
// since Kustomize has no values-file equivalent to infer it from. A panic
// from the kustomize API is recovered so one broken overlay can't take down
// the rest of the scan. resmap resources are re-marshaled to YAML and
// re-decoded rather than read via their typed accessors, so the same
// extractEntries kind-dispatch used for raw manifests and Helm output
// handles them too — GetName()/GetNamespace() on the resource already
// reflect namePrefix/nameSuffix post-transform, and that shows up in the
// YAML the same way.
func renderKustomization(kustomizer *krusty.Kustomizer, fSys filesys.FileSystem, absRoot, relDir string) (entries []model.IdentityEntry, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic rendering kustomization: %v", r)
		}
	}()

	dir := filepath.Join(absRoot, filepath.FromSlash(relDir))
	rm, rerr := kustomizer.Run(fSys, dir)
	if rerr != nil {
		return nil, rerr
	}

	env := path.Base(relDir)

	var docs []k8sDoc
	for _, res := range rm.Resources() {
		y, yerr := res.AsYAML()
		if yerr != nil {
			continue
		}
		var doc map[string]any
		if uerr := yaml.Unmarshal(y, &doc); uerr != nil || doc == nil {
			continue
		}
		switch kind(doc) {
		case "Service", "Ingress", "VirtualService":
			docs = append(docs, k8sDoc{Path: relDir, Environment: env, Doc: doc})
		}
	}
	return extractEntries(docs, model.SourceKustomize), nil
}
