package spring

import (
	"bytes"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/farhadamjady/service-discovery/internal/deployconfig"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/scan"
)

// deployIndexer parses externalized deployment config (KindDeployConfig) into an
// env-var binding layer and attaches it to the Spring ConfigResolver, so
// ${ENV_VAR} placeholders resolve through Helm / K8s / .env when application.*
// does not carry the value (DESIGN §8.5). It runs after configIndexer, which
// creates the springConfig this attaches to.
type deployIndexer struct{}

func (deployIndexer) Name() string { return "spring.deployconfig" }

func (deployIndexer) Index(ic *provider.IndexContext, idx *provider.Index) error {
	sc, ok := idx.Config.(*springConfig)
	if !ok {
		return nil // no Spring config store to attach the deploy layer to
	}

	layer := deployconfig.NewLayer()
	values := deployconfig.NewLayer() // values-only sub-layer for template tracing
	var templates []deployconfig.NamedSource

	for _, p := range sortedDeployPaths(ic.Parsed) {
		src := ic.Parsed[p].(*rawFile).Src()
		// Helm templates are not valid YAML; collect them for the .Values trace.
		if bytes.Contains(src, []byte("{{")) {
			templates = append(templates, deployconfig.NamedSource{Path: p, Src: src})
			continue
		}
		bs, err := deployconfig.Parse(p, src)
		if err != nil {
			return err
		}
		isValues := isValuesFile(path.Base(p))
		for _, b := range bs {
			layer.Add(b)
			if isValues {
				values.Add(b)
			}
		}
	}

	// Trace templates: env-var name -> .Values path -> value (overlays propagate).
	for _, b := range deployconfig.TraceTemplates(templates, values) {
		layer.Add(b)
	}

	// Monorepos keep deploy config at the REPO root (kubernetes-manifests/...),
	// outside the scanned service folder (IMPROVEMENTS #9): walk up to the repo
	// top and read k8s documents from there too (strict — k8s kinds and
	// values*.yaml only, so unrelated yaml adds nothing).
	addRepoRootDeployConfig(ic.Root, layer)

	sc.setDeploy(layer, ic.Environment)
	return nil
}

// addRepoRootDeployConfig finds the enclosing repo top (a .git up the tree) and
// adds its k8s/values bindings to the layer. No-op when the scanned root IS the
// repo top (its files were already collected).
func addRepoRootDeployConfig(root string, layer *deployconfig.Layer) {
	top := repoTop(root)
	if top == "" || top == root {
		return
	}
	tree := scan.NewOSFileTree(top, []string{
		"**/.git/**", "**/node_modules/**", "**/target/**", "**/build/**", "**/src/test/**",
	})
	for _, pattern := range []string{"**/*.yaml", "**/*.yml"} {
		for _, rel := range tree.Glob(pattern) {
			src, err := tree.Read(rel)
			if err != nil {
				continue
			}
			var bs []deployconfig.Binding
			if isValuesFile(path.Base(rel)) {
				bs, _ = deployconfig.Parse(rel, src)
			} else {
				bs, _ = deployconfig.ParseK8s(rel, src)
			}
			for _, b := range bs {
				layer.Add(b)
			}
		}
	}
}

// repoTop walks up from dir (max 6 levels) to the first directory containing
// .git, or "".
func repoTop(dir string) string {
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
	return ""
}

func sortedDeployPaths(parsed map[string]provider.ParsedFile) []string {
	var out []string
	for p, pf := range parsed {
		if rf, ok := pf.(*rawFile); ok && rf.Kind() == provider.KindDeployConfig {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

func isValuesFile(base string) bool {
	return base == "values.yaml" || base == "values.yml" || strings.HasPrefix(base, "values-")
}
