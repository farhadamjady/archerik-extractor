package deployrepo

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunEndToEnd mixes one Helm chart, one Kustomize base+overlay, and one
// standalone raw manifest under a single deploy-repo root, proving Run
// discovers and renders all three sources into one deduped, sorted
// IdentityMap without cross-contaminating each other's output.
func TestRunEndToEnd(t *testing.T) {
	root := t.TempDir()

	writeMinimalChart(t, filepath.Join(root, "charts/payments"), "web")
	writeBaseAndStagingOverlay(t, filepath.Join(root, "deploy"))
	write(t, root, "manifests/legacy-service.yaml", `apiVersion: v1
kind: Service
metadata:
  name: legacy-service
  namespace: legacy
`)

	im, errs, err := Run(context.Background(), Options{
		Root:   root,
		APIKey: "test-key",
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(errs) != 0 {
		t.Fatalf("render errs = %v", errs)
	}

	names := map[string]bool{}
	for _, e := range im.Entries {
		names[e.ServiceName] = true
	}
	for _, want := range []string{"payments-web", "staging-pym-service", "legacy-service"} {
		if !names[want] {
			t.Errorf("missing entry %q; got entries = %+v", want, im.Entries)
		}
	}
	// The Kustomize base's pre-prefix name must never appear standalone —
	// that would mean the raw-manifest exclusion (or the base/overlay
	// reference filter) failed to keep it out.
	if names["pym-service"] {
		t.Errorf("base's un-prefixed pym-service leaked in as a standalone entry: %+v", im.Entries)
	}

	data, err := Marshal(im)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Errorf("Marshal output missing trailing newline")
	}
}

// TestRunLooseFileRefBaseNoDoubleCount is the regression for the loose-base
// edge case: an overlay references an individual manifest FILE that lives
// outside its own directory (../../base/service.yaml), and that base dir has
// no kustomization.yaml of its own. Two things must hold: (1) the overlay
// renders — LoadRestrictionsNone permits the out-of-tree file reference — so
// the name-prefixed Service appears via Kustomize; (2) the base file is NOT
// also picked up by the raw scanner, which would emit a spurious un-prefixed
// entry for a manifest that is never deployed standalone.
func TestRunLooseFileRefBaseNoDoubleCount(t *testing.T) {
	root := t.TempDir()
	write(t, root, "base/service.yaml", `apiVersion: v1
kind: Service
metadata:
  name: orders-service
  namespace: orders
`)
	write(t, root, "overlays/staging/kustomization.yaml", `namePrefix: staging-
resources:
- ../../base/service.yaml
`)

	im, errs, err := Run(context.Background(), Options{Root: root, APIKey: "k", DryRun: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(errs) != 0 {
		t.Fatalf("render errs = %v (overlay should render under LoadRestrictionsNone)", errs)
	}

	bySource := map[string]string{} // service_name -> source
	for _, e := range im.Entries {
		bySource[e.ServiceName] = string(e.Source)
	}
	if bySource["staging-orders-service"] != "kustomize" {
		t.Errorf("want staging-orders-service from kustomize, got entries = %+v", im.Entries)
	}
	if _, leaked := bySource["orders-service"]; leaked {
		t.Errorf("un-prefixed base leaked into raw scan: %+v", im.Entries)
	}
	if len(im.Entries) != 1 {
		t.Errorf("want exactly 1 entry, got %d: %+v", len(im.Entries), im.Entries)
	}
}
