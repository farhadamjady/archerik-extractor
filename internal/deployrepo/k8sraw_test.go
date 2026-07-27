package deployrepo

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/farhadamjady/service-discovery/internal/scan"
)

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverK8sRawFindsRecognizedKinds(t *testing.T) {
	root := t.TempDir()
	write(t, root, "manifests/service.yaml", "kind: Service\nmetadata:\n  name: pym-service\n  namespace: payments\n")
	write(t, root, "manifests/configmap.yaml", "kind: ConfigMap\nmetadata:\n  name: irrelevant\n")
	write(t, root, "README.md", "not yaml at all")

	tree := scan.NewOSFileTree(root, nil)
	docs, errs := discoverK8sRaw(tree)
	if len(errs) != 0 {
		t.Fatalf("errs = %v", errs)
	}
	if len(docs) != 1 {
		t.Fatalf("docs = %+v, want exactly the Service (ConfigMap is not a recognized kind here)", docs)
	}
	if kind(docs[0].Doc) != "Service" {
		t.Errorf("kind = %q", kind(docs[0].Doc))
	}
}

func TestDiscoverK8sRawSkipsUnrenderedHelmTemplates(t *testing.T) {
	root := t.TempDir()
	write(t, root, "chart/templates/service.yaml", "kind: Service\nmetadata:\n  name: {{ .Release.Name }}-svc\n")

	tree := scan.NewOSFileTree(root, nil)
	docs, _ := discoverK8sRaw(tree)
	if len(docs) != 0 {
		t.Fatalf("docs = %+v, want none (file contains {{ }}, not valid YAML)", docs)
	}
}

func TestInferEnvironmentFromPathSegment(t *testing.T) {
	cases := []struct {
		path, want string
	}{
		{"overlays/staging/service.yaml", "staging"},
		{"manifests/prod/service.yaml", "prod"},
		{"manifests/service.yaml", ""},
		{"Overlays/QA/service.yaml", "qa"},
	}
	for _, c := range cases {
		if got := inferEnvironment(c.path); got != c.want {
			t.Errorf("inferEnvironment(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}
