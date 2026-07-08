package spring

import (
	"bytes"
	"path"
	"sort"
	"strings"

	"github.com/farhadamjady/service-discovery/internal/deployconfig"
	"github.com/farhadamjady/service-discovery/internal/provider"
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

	sc.setDeploy(layer, ic.Environment)
	return nil
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
