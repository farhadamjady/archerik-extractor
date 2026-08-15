package quarkus

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/farhadamjady/archerik-extractor/internal/model"
	"github.com/farhadamjady/archerik-extractor/internal/provider"
	"github.com/farhadamjady/archerik-extractor/internal/scan"
)

var _ provider.Provider = (*Provider)(nil)

func TestParsersCoverFileSpec(t *testing.T) {
	p := New()
	parsers := p.Parsers()
	for _, g := range p.FileSpec().Groups {
		if _, ok := parsers[g.Kind]; !ok {
			t.Errorf("FileSpec collects kind %d but Parsers() has no parser for it", g.Kind)
		}
		if len(g.Include) == 0 {
			t.Errorf("FileSpec group for kind %d has no include globs", g.Kind)
		}
	}
}

func TestDetectors(t *testing.T) {
	want := map[string]model.Protocol{
		"quarkus.rest":         model.ProtoREST,
		"quarkus.restclient":   model.ProtoREST,
		"quarkus.jaxrs-client": model.ProtoREST,
		"quarkus.messaging":    model.ProtoKafka,
	}
	dets := New().Detectors()
	if len(dets) != len(want) {
		t.Fatalf("got %d detectors, want %d", len(dets), len(want))
	}
	seen := map[string]bool{}
	for _, d := range dets {
		if seen[d.Name()] {
			t.Errorf("duplicate detector name %q", d.Name())
		}
		seen[d.Name()] = true
		proto, ok := want[d.Name()]
		if !ok {
			t.Errorf("unexpected detector %q", d.Name())
			continue
		}
		if d.Protocol() != proto {
			t.Errorf("detector %q protocol = %q, want %q", d.Name(), d.Protocol(), proto)
		}
	}
}

func TestMatch(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "pom.xml", "<project><dependency><groupId>io.quarkus</groupId></dependency></project>")
	writeFile(t, root, "src/main/java/HeroResource.java",
		"import io.quarkus.runtime.Startup;\nimport jakarta.ws.rs.Path;\n@Path(\"/heroes\") class HeroResource {}")

	fs := scan.NewOSFileTree(root, nil)
	matched, score := New().Match(root, fs)
	if !matched {
		t.Fatal("expected Quarkus repo to match")
	}
	// pom.xml io.quarkus (2) + import io.quarkus (3)
	if score != 5 {
		t.Errorf("score = %d, want 5", score)
	}

	empty := t.TempDir()
	writeFile(t, empty, "main.go", "package main")
	if matched, _ := New().Match(empty, scan.NewOSFileTree(empty, nil)); matched {
		t.Error("non-Quarkus repo must not match")
	}
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestMatchNeedsQuarkusCoordinates locks that a bare "quarkus" mention is not a
// match: the substring hits module names, comments and doc links, and claiming
// such a repo emits an empty graph instead of failing detection honestly. The
// io.quarkus group id (or an io.quarkus import) is the real signal.
func TestMatchNeedsQuarkusCoordinates(t *testing.T) {
	mention := t.TempDir()
	writeFile(t, mention, "pom.xml",
		"<project><artifactId>quarkus-migration-notes</artifactId><!-- we may move to quarkus later --></project>")
	writeFile(t, mention, "src/main/java/App.java", "public class App {}")
	if m, score := New().Match(mention, scan.NewOSFileTree(mention, nil)); m {
		t.Errorf("a repo merely mentioning quarkus matched (score %d); only the coordinates count", score)
	}

	// The group id alone, with no io.quarkus import in sources, is still a real
	// declaration — a JAX-RS resource need never import io.quarkus.
	declared := t.TempDir()
	writeFile(t, declared, "pom.xml", "<project><dependency><groupId>io.quarkus</groupId></dependency></project>")
	writeFile(t, declared, "src/main/java/HeroResource.java",
		"import jakarta.ws.rs.Path;\n@Path(\"/heroes\") class HeroResource {}")
	if m, _ := New().Match(declared, scan.NewOSFileTree(declared, nil)); !m {
		t.Error("io.quarkus coordinates must match even without an io.quarkus source import")
	}
}
