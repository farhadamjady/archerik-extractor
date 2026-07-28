package deployrepo

import (
	"path/filepath"
	"testing"

	"github.com/farhadamjady/service-discovery/internal/model"
)

// fullnameHelperTpl is the standard `helm create`-scaffolded fullname helper
// — real charts in the wild almost always carry something equivalent. Using
// it for real (via engine.Render) is what proves this renders the chart's
// own authored templates rather than reimplementing Helm's naming
// convention.
const fullnameHelperTpl = `
{{- define "chart.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}
`

func writeMinimalChart(t *testing.T, chartDir, chartName string) {
	t.Helper()
	write(t, chartDir, "Chart.yaml", "apiVersion: v2\nname: "+chartName+"\nversion: 0.1.0\n")
	write(t, chartDir, "values.yaml", "replicaCount: 1\n")
	write(t, chartDir, "values-staging.yaml", "replicaCount: 3\n")
	write(t, chartDir, "templates/_helpers.tpl", fullnameHelperTpl)
	write(t, chartDir, "templates/service.yaml", `apiVersion: v1
kind: Service
metadata:
  name: {{ include "chart.fullname" . }}
  namespace: payments
spec:
  ports:
  - port: 80
`)
}

// TestRenderChartFullnameAndOverlay proves real chart-engine execution (not
// a regex tracer): the chart directory is "payments" (becomes Release.Name)
// and Chart.yaml's name is "web" — since "web" isn't contained in
// "payments", the standard fullname helper concatenates them to
// "payments-web", which only resolves correctly by actually running the
// chart's own _helpers.tpl through engine.Render.
func TestRenderChartFullnameAndOverlay(t *testing.T) {
	root := t.TempDir()
	chartDir := filepath.Join(root, "payments")
	writeMinimalChart(t, chartDir, "web")

	entries, errs := renderChart(root, "payments", nil, testKinds())
	if len(errs) != 0 {
		t.Fatalf("errs = %v", errs)
	}

	byEnv := map[string]model.IdentityEntry{}
	for _, e := range entries {
		byEnv[e.Environment] = e
	}
	if len(byEnv) != 2 {
		t.Fatalf("entries = %+v, want one for base (\"\") and one for staging", entries)
	}
	if _, ok := byEnv["staging"]; !ok {
		t.Fatalf("no entry for the values-staging.yaml overlay: %+v", entries)
	}

	for env, e := range byEnv {
		if e.ServiceName != "payments-web" {
			t.Errorf("env %q: ServiceName = %q, want payments-web", env, e.ServiceName)
		}
		if e.Namespace != "payments" {
			t.Errorf("env %q: Namespace = %q", env, e.Namespace)
		}
		wantFQDN := "payments-web.payments.svc.cluster.local"
		found := false
		for _, h := range e.Hosts {
			if h.Value == wantFQDN {
				found = true
			}
		}
		if !found {
			t.Errorf("env %q: hosts = %v, want to include %q", env, model.HostValues(e.Hosts), wantFQDN)
		}
	}
}

// TestRenderChartBrokenTemplateIsNonFatal: a template referencing a values
// path that doesn't exist must surface as a RenderError, not abort the scan
// or the caller's process.
func TestRenderChartBrokenTemplateIsNonFatal(t *testing.T) {
	root := t.TempDir()
	chartDir := filepath.Join(root, "broken")
	write(t, chartDir, "Chart.yaml", "apiVersion: v2\nname: broken\nversion: 0.1.0\n")
	write(t, chartDir, "values.yaml", "foo: 1\n")
	write(t, chartDir, "templates/service.yaml", `kind: Service
metadata:
  name: {{ .Values.nonexistent.deep }}
`)

	entries, errs := renderChart(root, "broken", nil, testKinds())
	if len(errs) == 0 {
		t.Fatalf("want a RenderError from the broken template, got none; entries = %+v", entries)
	}
}

// TestRenderChartEnvFilter: when Environments is set, only the requested
// overlay(s) render — the base is skipped unless explicitly requested.
func TestRenderChartEnvFilter(t *testing.T) {
	root := t.TempDir()
	chartDir := filepath.Join(root, "payments")
	writeMinimalChart(t, chartDir, "web")

	entries, errs := renderChart(root, "payments", []string{"staging"}, testKinds())
	if len(errs) != 0 {
		t.Fatalf("errs = %v", errs)
	}
	if len(entries) != 1 || entries[0].Environment != "staging" {
		t.Fatalf("entries = %+v, want exactly one staging entry", entries)
	}
}
