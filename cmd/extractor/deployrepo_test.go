package main

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/farhadamjady/service-discovery/internal/backend"
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
	entries, err := readSelfDeclaredIdentity(root)
	if err != nil {
		t.Fatalf("readSelfDeclaredIdentity: %v", err)
	}
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
	entries, err := readSelfDeclaredIdentity(root)
	if err != nil {
		t.Fatalf("readSelfDeclaredIdentity: %v", err)
	}
	if len(entries) != 1 || entries[0].ServiceName != "pym-service" {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestReadSelfDeclaredIdentityAbsentOrMalformed(t *testing.T) {
	root := t.TempDir()
	entries, err := readSelfDeclaredIdentity(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("absent file: entries=%+v err=%v, want (nil, nil)", entries, err)
	}

	write(t, root, ".ekg-identity.json", `not json at all`)
	entries, err = readSelfDeclaredIdentity(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("malformed file: entries=%+v err=%v, want (nil, nil)", entries, err)
	}
}
