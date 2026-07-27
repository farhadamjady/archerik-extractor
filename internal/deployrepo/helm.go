package deployrepo

import (
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"

	"github.com/farhadamjady/service-discovery/internal/deployconfig"
	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
)

// discoverCharts finds every Helm chart directory (one containing
// Chart.yaml) under tree, as repo-relative directory paths.
func discoverCharts(tree provider.FileTree) []string {
	seen := map[string]bool{}
	var dirs []string
	for _, rel := range tree.Glob("**/Chart.yaml") {
		dir := path.Dir(rel)
		if !seen[dir] {
			seen[dir] = true
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

// RenderHelmCharts renders every chart directory in chartDirs (repo-relative,
// resolved against absRoot) and returns the identity entries found across all
// of them. Rendering is real Helm chart-engine execution — loader.LoadDir +
// chartutil.ToRenderValues + engine.Render — so a chart's own _helpers.tpl
// fullname convention (nameOverride/fullnameOverride,
// {{ .Release.Name }}-{{ .Chart.Name }}) resolves correctly, because it's the
// chart's own authored helper templates running, not a reimplementation.
// Fully offline: only values files committed in the repo are read, no
// cluster access and no live chart-repo pulls. A failure loading or
// rendering one chart, or one of its env overlays, is collected as a
// RenderError and never aborts the rest of the scan.
func RenderHelmCharts(absRoot string, chartDirs []string, envFilter []string) ([]model.IdentityEntry, []RenderError) {
	var entries []model.IdentityEntry
	var errs []RenderError
	for _, dir := range chartDirs {
		e, chartErrs := renderChart(absRoot, dir, envFilter)
		entries = append(entries, e...)
		errs = append(errs, chartErrs...)
	}
	return entries, errs
}

// valuesOverlay is one values context to render a chart with: "" is the
// chart's own base values.yaml (already loaded by loader.LoadDir), any other
// name is a values-<env>.yaml overlay coalesced on top of the base.
type valuesOverlay struct {
	env    string
	values map[string]any
}

// renderChart loads one chart directory and renders it once per applicable
// values overlay. A panic from the Helm SDK — a known risk on malformed or
// unusual templates — is recovered here so one broken chart can't take down
// the rest of the scan.
func renderChart(absRoot, chartRelDir string, envFilter []string) (entries []model.IdentityEntry, errs []RenderError) {
	defer func() {
		if r := recover(); r != nil {
			errs = append(errs, RenderError{Unit: chartRelDir, Err: fmt.Errorf("panic loading/rendering chart: %v", r)})
		}
	}()

	chartDir := filepath.Join(absRoot, filepath.FromSlash(chartRelDir))
	chrt, err := loader.LoadDir(chartDir)
	if err != nil {
		return nil, []RenderError{{Unit: chartRelDir, Err: err}}
	}

	overlays, overlayErr := discoverValuesOverlays(chartDir, chrt.Values, envFilter)
	if overlayErr != nil {
		errs = append(errs, RenderError{Unit: chartRelDir, Err: overlayErr})
	}

	for _, ov := range overlays {
		out, rerr := renderChartOverlay(chrt, chartRelDir, ov)
		if rerr != nil {
			errs = append(errs, RenderError{Unit: chartRelDir, Env: ov.env, Err: rerr})
			continue
		}
		entries = append(entries, out...)
	}
	return entries, errs
}

// discoverValuesOverlays finds values-<env>.yaml files alongside chartDir's
// Chart.yaml (the standard Helm convention — deployconfig.OverlayFromName
// reused so this doesn't reimplement it) and merges each on top of the
// chart's own base values. When envFilter is non-empty only the requested
// environments render (the base render is skipped unless explicitly
// requested); empty envFilter renders the base plus every discovered env.
func discoverValuesOverlays(chartDir string, baseValues map[string]any, envFilter []string) ([]valuesOverlay, error) {
	include := map[string]bool{}
	for _, e := range envFilter {
		include[e] = true
	}
	wantEnv := func(env string) bool {
		if len(include) == 0 {
			return true
		}
		return include[env]
	}

	var out []valuesOverlay
	if wantEnv("") {
		out = append(out, valuesOverlay{env: "", values: baseValues})
	}

	dirEntries, err := os.ReadDir(chartDir)
	if err != nil {
		return out, err
	}
	var firstErr error
	for _, de := range dirEntries {
		if de.IsDir() {
			continue
		}
		name := de.Name()
		if !strings.HasPrefix(name, "values-") || !(strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml")) {
			continue
		}
		env := deployconfig.OverlayFromName(name)
		if env == "" || !wantEnv(env) {
			continue
		}
		src, readErr := os.ReadFile(filepath.Join(chartDir, name))
		if readErr != nil {
			if firstErr == nil {
				firstErr = readErr
			}
			continue
		}
		var envValues map[string]any
		if err := yaml.Unmarshal(src, &envValues); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		merged := chartutil.CoalesceTables(deepCloneValues(envValues), deepCloneValues(baseValues))
		out = append(out, valuesOverlay{env: env, values: merged})
	}
	return out, firstErr
}

// deepCloneValues round-trips m through YAML so repeated CoalesceTables
// calls (which mutate their inputs) never leak changes between overlays
// sharing the same base values map.
func deepCloneValues(m map[string]any) map[string]any {
	b, err := yaml.Marshal(m)
	if err != nil {
		return m
	}
	var out map[string]any
	if err := yaml.Unmarshal(b, &out); err != nil {
		return m
	}
	if out == nil {
		out = map[string]any{}
	}
	return out
}

// releaseNamespaceDefault is used when a chart's rendering needs a
// Release.Namespace and none is otherwise known — no live Helm release ever
// existed for a committed-repo scan, so this is a documented best-effort
// stand-in, not a discovered fact.
const releaseNamespaceDefault = "default"

// renderChartOverlay renders chrt with one values overlay and extracts
// Service/Ingress/VirtualService identity entries from the result. The
// release name is approximated as the chart directory's own name — the
// overwhelmingly common convention (`helm install <chart-dir-name> ./chart`)
// — since no live release exists in a committed repo to read the real name
// from; a CD pipeline naming releases differently from the chart directory
// will mis-derive ServiceName here, a known, accepted limitation.
func renderChartOverlay(chrt *chart.Chart, chartRelDir string, ov valuesOverlay) (entries []model.IdentityEntry, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()

	releaseName := path.Base(chartRelDir)
	renderVals, err := chartutil.ToRenderValues(chrt, ov.values, chartutil.ReleaseOptions{
		Name:      releaseName,
		Namespace: releaseNamespaceDefault,
		Revision:  1,
		IsInstall: true,
	}, chartutil.DefaultCapabilities)
	if err != nil {
		return nil, err
	}

	rendered, err := engine.Render(chrt, renderVals)
	if err != nil {
		return nil, err
	}

	var docs []k8sDoc
	for tplPath, text := range rendered {
		if strings.TrimSpace(text) == "" || strings.HasSuffix(tplPath, "NOTES.txt") {
			continue
		}
		dec := yaml.NewDecoder(strings.NewReader(text))
		for {
			var doc map[string]any
			derr := dec.Decode(&doc)
			if derr == io.EOF {
				break
			}
			if derr != nil {
				break // a partially-rendered helper template; skip the rest of this file, not the whole chart
			}
			if doc == nil {
				continue
			}
			switch kind(doc) {
			case "Service", "Ingress", "VirtualService":
				docs = append(docs, k8sDoc{Path: tplPath, Environment: ov.env, Doc: doc})
			}
		}
	}
	return extractEntries(docs, model.SourceHelm), nil
}
