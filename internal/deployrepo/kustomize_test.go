package deployrepo

import (
	"testing"

	"github.com/farhadamjady/service-discovery/internal/scan"
)

func writeBaseAndStagingOverlay(t *testing.T, root string) {
	t.Helper()
	write(t, root, "base/kustomization.yaml", "resources:\n- service.yaml\n")
	write(t, root, "base/service.yaml", `apiVersion: v1
kind: Service
metadata:
  name: pym-service
  namespace: payments
`)
	write(t, root, "overlays/staging/kustomization.yaml", "namePrefix: staging-\nresources:\n- ../../base\n")
}

// TestDiscoverKustomizationsExcludesReferencedBase: base/ is only ever
// rendered as part of the overlay that includes it — rendering it AGAIN
// standalone would double-emit pym-service (once prefixed, once not).
func TestDiscoverKustomizationsExcludesReferencedBase(t *testing.T) {
	root := t.TempDir()
	writeBaseAndStagingOverlay(t, root)

	tree := scan.NewOSFileTree(root, nil)
	roots := discoverKustomizations(tree)
	if len(roots) != 1 || roots[0] != "overlays/staging" {
		t.Fatalf(`roots = %v, want ["overlays/staging"] (base is referenced, must not render standalone)`, roots)
	}

	all := allKustomizationDirs(tree)
	if len(all) != 2 {
		t.Fatalf("allKustomizationDirs = %v, want both base and overlays/staging", all)
	}
}

// TestRenderKustomizationAppliesNamePrefix proves the real kustomize API is
// doing the rendering (not a string trace): namePrefix comes from the
// library's own transformer, and Environment is inferred from the overlay
// directory's own basename per this package's documented convention.
func TestRenderKustomizationAppliesNamePrefix(t *testing.T) {
	root := t.TempDir()
	writeBaseAndStagingOverlay(t, root)

	entries, errs := RenderKustomizations(root, []string{"overlays/staging"}, testKinds())
	if len(errs) != 0 {
		t.Fatalf("errs = %v", errs)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %+v, want exactly one Service entry", entries)
	}
	e := entries[0]
	if e.ServiceName != "staging-pym-service" {
		t.Errorf("ServiceName = %q, want staging-pym-service (namePrefix applied)", e.ServiceName)
	}
	if e.Environment != "staging" {
		t.Errorf("Environment = %q, want staging", e.Environment)
	}
	if e.Namespace != "payments" {
		t.Errorf("Namespace = %q", e.Namespace)
	}
}
