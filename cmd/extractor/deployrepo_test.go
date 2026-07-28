package main

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/farhadamjady/service-discovery/internal/backend"
	"github.com/farhadamjady/service-discovery/internal/deployrepo"
	"github.com/farhadamjady/service-discovery/internal/exitcode"
	"github.com/farhadamjady/service-discovery/internal/model"
)

func TestRunDeployRepoMode(t *testing.T) {
	root := t.TempDir()
	write(t, root, "manifests/service.yaml", `apiVersion: v1
kind: Service
metadata:
  name: pym-service
  namespace: payments
`)

	var stdout, stderr bytes.Buffer
	code := run([]string{"--mode", "deploy-repo", "--root", root, "--api-key", "k", "--dry-run"}, &stdout, &stderr)
	if code != int(exitcode.OK) {
		t.Fatalf("exit = %d, want 0; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"service_name":"pym-service"`) {
		t.Errorf("stdout = %s, want it to contain the pym-service entry", stdout.String())
	}
	if !strings.Contains(stdout.String(), `"source":"k8s-raw"`) {
		t.Errorf("stdout = %s, want source=k8s-raw", stdout.String())
	}
}

func TestRunUnknownMode(t *testing.T) {
	root := springRepo(t)
	var stdout, stderr bytes.Buffer
	code := run([]string{"--mode", "bogus", "--root", root, "--api-key", "k", "--dry-run"}, &stdout, &stderr)
	if code != int(exitcode.Runtime) {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, int(exitcode.Runtime), stderr.String())
	}
	if !strings.Contains(stderr.String(), "unknown --mode") {
		t.Errorf("stderr = %q, want it to mention the bad --mode value", stderr.String())
	}
}

// TestRunDeployRepoResolverSubset: a repo with both a Helm chart and a raw
// manifest, scanned with --resolvers=k8s-raw, emits only the raw entry.
func TestRunDeployRepoResolverSubset(t *testing.T) {
	root := t.TempDir()
	write(t, root, "manifests/service.yaml", "apiVersion: v1\nkind: Service\nmetadata:\n  name: raw-svc\n  namespace: payments\n")
	write(t, root, "chart/Chart.yaml", "apiVersion: v2\nname: web\nversion: 0.1.0\n")
	write(t, root, "chart/values.yaml", "x: 1\n")
	write(t, root, "chart/templates/service.yaml", "apiVersion: v1\nkind: Service\nmetadata:\n  name: {{ .Release.Name }}-svc\n  namespace: payments\n")

	var stdout, stderr bytes.Buffer
	code := run([]string{"--mode", "deploy-repo", "--root", root, "--api-key", "k", "--dry-run", "--resolvers", "k8s-raw"}, &stdout, &stderr)
	if code != int(exitcode.OK) {
		t.Fatalf("exit = %d, want 0; stderr: %s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, `"service_name":"raw-svc"`) {
		t.Errorf("want raw-svc entry, got: %s", out)
	}
	if strings.Contains(out, `"source":"helm"`) {
		t.Errorf("helm resolver ran despite --resolvers=k8s-raw: %s", out)
	}
}

// TestRunTerraformResolver: a TF repo with a module block, scanned with
// --resolvers=terraform, emits the literal service name; and the default run
// (no --resolvers) emits nothing for it, proving terraform is opt-in.
func TestRunTerraformResolver(t *testing.T) {
	root := t.TempDir()
	write(t, root, "service-a.tf", `module "service_a" {
  source = "./modules/service"
  name   = "service-a"
}`)

	var optIn bytes.Buffer
	if code := run([]string{"--mode", "deploy-repo", "--root", root, "--api-key", "k", "--dry-run", "--resolvers", "terraform"}, &optIn, &optIn); code != int(exitcode.OK) {
		t.Fatalf("exit = %d; out: %s", code, optIn.String())
	}
	if !strings.Contains(optIn.String(), `"service_name":"service-a"`) || !strings.Contains(optIn.String(), `"source":"terraform"`) {
		t.Errorf("--resolvers=terraform did not emit the module name: %s", optIn.String())
	}

	var deflt bytes.Buffer
	run([]string{"--mode", "deploy-repo", "--root", root, "--api-key", "k", "--dry-run"}, &deflt, &deflt)
	if strings.Contains(deflt.String(), "service-a") {
		t.Errorf("terraform ran by default (should be opt-in): %s", deflt.String())
	}
}

func TestRunUnknownResolver(t *testing.T) {
	root := t.TempDir()
	write(t, root, "manifests/service.yaml", "kind: Service\nmetadata:\n  name: s\n")
	var stdout, stderr bytes.Buffer
	code := run([]string{"--mode", "deploy-repo", "--root", root, "--api-key", "k", "--dry-run", "--resolvers", "bogus"}, &stdout, &stderr)
	if code == int(exitcode.OK) {
		t.Fatalf("exit = 0, want non-zero for unknown resolver; stderr: %s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "bogus") {
		t.Errorf("stderr = %q, want it to name the bad resolver", stderr.String())
	}
}

func TestRunInvalidNamespaceConvention(t *testing.T) {
	root := t.TempDir()
	write(t, root, "manifests/service.yaml", "kind: Service\nmetadata:\n  name: s\n")
	var stdout, stderr bytes.Buffer
	code := run([]string{"--mode", "deploy-repo", "--root", root, "--api-key", "k", "--dry-run", "--namespace-convention", "nonsense"}, &stdout, &stderr)
	if code != int(exitcode.Runtime) {
		t.Fatalf("exit = %d, want %d for invalid convention; stderr: %s", code, int(exitcode.Runtime), stderr.String())
	}
	if !strings.Contains(stderr.String(), "namespace-convention") {
		t.Errorf("stderr = %q, want it to mention the bad convention", stderr.String())
	}
}

// TestRunDeployRepoNamespaceConvention: a chart whose Service declares no
// namespace, scanned with --namespace-convention=service-name, yields
// namespace == the rendered service name instead of "default".
func TestRunDeployRepoNamespaceConvention(t *testing.T) {
	root := t.TempDir()
	write(t, root, "chart/Chart.yaml", "apiVersion: v2\nname: web\nversion: 0.1.0\n")
	write(t, root, "chart/values.yaml", "x: 1\n")
	write(t, root, "chart/templates/service.yaml", "apiVersion: v1\nkind: Service\nmetadata:\n  name: {{ .Release.Name }}\n") // no namespace

	var stdout, stderr bytes.Buffer
	code := run([]string{"--mode", "deploy-repo", "--root", root, "--api-key", "k", "--dry-run", "--namespace-convention", "service-name"}, &stdout, &stderr)
	if code != int(exitcode.OK) {
		t.Fatalf("exit = %d, want 0; stderr: %s", code, stderr.String())
	}
	// Release name = chart dir "chart", so service_name == "chart" and ns == "chart".
	if !strings.Contains(stdout.String(), `"namespace":"chart"`) {
		t.Errorf("want namespace derived from service name, got: %s", stdout.String())
	}
}

// TestSelfDeclaredIdentityAbsentNoop: no .ekg-identity.json at all — the
// primary scan is unaffected, no warning printed.
func TestSelfDeclaredIdentityAbsentNoop(t *testing.T) {
	root := springRepo(t)
	var stdout, stderr bytes.Buffer
	code := run([]string{"--root", root, "--api-key", "k", "--dry-run"}, &stdout, &stderr)
	if code != int(exitcode.OK) {
		t.Fatalf("exit = %d, want 0; stderr: %s", code, stderr.String())
	}
	if stderr.String() != "" {
		t.Errorf("stderr = %q, want empty (no .ekg-identity.json present)", stderr.String())
	}
}

// TestSelfDeclaredIdentityMalformedNeverFailsScan: a broken
// .ekg-identity.json must never fail the primary scan's exit code.
func TestSelfDeclaredIdentityMalformedNeverFailsScan(t *testing.T) {
	root := springRepo(t)
	write(t, root, ".ekg-identity.json", `{ not valid json `)

	var stdout, stderr bytes.Buffer
	code := run([]string{"--root", root, "--api-key", "k", "--dry-run"}, &stdout, &stderr)
	if code != int(exitcode.OK) {
		t.Fatalf("exit = %d, want 0 (malformed .ekg-identity.json must be non-fatal); stderr: %s", code, stderr.String())
	}
}

// TestSelfDeclaredIdentitySubmitFailureNonFatal: a valid .ekg-identity.json
// against a real backend that has no /v1/ingest/identity-map route (out of
// this repo's scope, per the deploy-repo-mode plan) yields a 404 on that
// side-submission — the primary scan must still succeed.
func TestSelfDeclaredIdentitySubmitFailureNonFatal(t *testing.T) {
	store, err := backend.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(backend.New([]string{"k"}, store).Handler())
	defer srv.Close()

	root := springRepo(t)
	write(t, root, ".ekg-identity.json", `{"service_name":"pym-service","hosts":["pym-service"],"namespace":"payments"}`)

	var stdout, stderr bytes.Buffer
	code := run([]string{"--root", root, "--api-key", "k", "--api-url", srv.URL}, &stdout, &stderr)
	if code != int(exitcode.OK) {
		t.Fatalf("exit = %d, want 0 (identity-map submit failure must be non-fatal); stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "non-fatal") {
		t.Errorf("stderr = %q, want a non-fatal warning about the identity submit", stderr.String())
	}
}

func TestReadSelfDeclaredIdentityArray(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".ekg-identity.json", `[
		{"service_name":"pym-service","hosts":["pym-service"],"namespace":"payments","environment":"prod"},
		{"service_name":"pym-service","hosts":["pym-service"],"namespace":"payments","environment":"staging"}
	]`)
	entries := deployrepo.ReadSelfDeclared(root)
	if len(entries) != 2 {
		t.Fatalf("entries = %+v, want 2", entries)
	}
	for _, e := range entries {
		if e.Source != model.SourceSelfDeclared || e.Confidence != model.IdentityLikely {
			t.Errorf("entry = %+v, want Source=self-declared Confidence=likely", e)
		}
	}
}

func TestReadSelfDeclaredIdentityBareObject(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".ekg-identity.json", `{"service_name":"pym-service","hosts":["pym-service"],"namespace":"payments"}`)
	entries := deployrepo.ReadSelfDeclared(root)
	if len(entries) != 1 || entries[0].ServiceName != "pym-service" {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestReadSelfDeclaredIdentityAbsentOrMalformed(t *testing.T) {
	root := t.TempDir()
	if entries := deployrepo.ReadSelfDeclared(root); len(entries) != 0 {
		t.Fatalf("absent file: entries=%+v, want none", entries)
	}

	write(t, root, ".ekg-identity.json", `not json at all`)
	if entries := deployrepo.ReadSelfDeclared(root); len(entries) != 0 {
		t.Fatalf("malformed file: entries=%+v, want none", entries)
	}
}
