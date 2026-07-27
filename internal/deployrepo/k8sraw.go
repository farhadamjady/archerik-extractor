package deployrepo

import (
	"bytes"
	"io"
	"path"
	"strings"

	"github.com/farhadamjady/service-discovery/internal/provider"
	"gopkg.in/yaml.v3"
)

// environmentAllowlist is the set of directory-name segments treated as an
// environment signal for plain k8s manifests, which have no first-class
// environment field the way Helm's values-<env>.yaml filenames do. Best
// effort only: a repo laid out differently just gets Environment == "".
var environmentAllowlist = map[string]bool{
	"dev": true, "staging": true, "prod": true, "production": true, "qa": true,
}

// inferEnvironment reads path segments looking for a recognized environment
// name (e.g. "overlays/staging/service.yaml" -> "staging"). Never fails the
// scan over an unrecognized layout — returns "" instead.
func inferEnvironment(relPath string) string {
	for _, seg := range strings.Split(path.Dir(relPath), "/") {
		if environmentAllowlist[strings.ToLower(seg)] {
			return strings.ToLower(seg)
		}
	}
	return ""
}

// discoverK8sRaw walks tree for plain k8s manifests and parses every
// Service/Ingress/VirtualService document found. Callers are expected to
// have already excluded Helm chart and Kustomize base/overlay directories
// from tree's exclude list (see deployrepo.go) — those go through their own
// renderers, and double-counting them here would emit spurious entries (a
// Kustomize base's pre-prefix Service alongside its rendered, prefixed
// form). Files containing "{{" are unrendered Helm templates (not valid
// YAML) and are skipped, the same rule deployconfig.ParseK8s applies.
func discoverK8sRaw(tree provider.FileTree) ([]k8sDoc, []RenderError) {
	seen := map[string]bool{}
	var paths []string
	for _, pattern := range []string{"**/*.yaml", "**/*.yml"} {
		for _, rel := range tree.Glob(pattern) {
			if !seen[rel] {
				seen[rel] = true
				paths = append(paths, rel)
			}
		}
	}

	var docs []k8sDoc
	var errs []RenderError
	for _, rel := range paths {
		src, err := tree.Read(rel)
		if err != nil {
			errs = append(errs, RenderError{Unit: rel, Err: err})
			continue
		}
		if bytes.Contains(src, []byte("{{")) {
			continue
		}
		env := inferEnvironment(rel)
		dec := yaml.NewDecoder(bytes.NewReader(src))
		for {
			var doc map[string]any
			decErr := dec.Decode(&doc)
			if decErr == io.EOF {
				break
			}
			if decErr != nil {
				errs = append(errs, RenderError{Unit: rel, Err: decErr})
				break
			}
			if doc == nil {
				continue
			}
			switch kind(doc) {
			case "Service", "Ingress", "VirtualService":
				docs = append(docs, k8sDoc{Path: rel, Environment: env, Doc: doc})
			}
		}
	}
	return docs, errs
}
