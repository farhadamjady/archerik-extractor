package spring

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/farhadamjady/archerik-extractor/internal/model"
	"github.com/farhadamjady/archerik-extractor/internal/provider"
	"github.com/farhadamjady/archerik-extractor/internal/scan"
)

// Compile-time seam conformance: the Spring provider must satisfy the full
// Provider interface. This line IS the test — it fails to build on drift.
var _ provider.Provider = (*Provider)(nil)

// TestParsersCoverFileSpec pins the routing invariant: every kind the FileSpec
// collects must have a parser, or the pipeline would read files it cannot parse.
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

// TestDetectors pins the detector set: five concerns, unique names, and every
// detector stamped with its protocol (protocol is first-class on every edge).
func TestDetectors(t *testing.T) {
	want := map[string]model.Protocol{
		"spring.rest":          model.ProtoREST,
		"spring.feign":         model.ProtoREST,
		"spring.resttemplate":  model.ProtoREST,
		"spring.webclient":     model.ProtoREST,
		"spring.kafka":         model.ProtoKafka,
		"spring.reactivekafka": model.ProtoKafka,
		"spring.httpexchange":  model.ProtoREST,
		"spring.cloudstream":   model.ProtoKafka,
		"spring.adapter":       model.ProtoREST,
		"spring.router":        model.ProtoREST,
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

// TestMatch exercises detection scoring over a real (temp) file tree:
// pom.xml with spring-boot + a @SpringBootApplication class = full score;
// an unrelated repo must not match.
func TestMatch(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "pom.xml", "<project><artifactId>spring-boot-starter</artifactId></project>")
	writeFile(t, root, "src/main/java/App.java", "@SpringBootApplication public class App {}")

	fs := scan.NewOSFileTree(root, nil)
	matched, score := New().Match(root, fs)
	if !matched {
		t.Fatal("expected Spring repo to match")
	}
	// pom.xml (1) + spring-boot dep (2) + @SpringBootApplication (3)
	if score != 6 {
		t.Errorf("score = %d, want 6", score)
	}

	empty := t.TempDir()
	writeFile(t, empty, "main.go", "package main")
	if matched, _ := New().Match(empty, scan.NewOSFileTree(empty, nil)); matched {
		t.Error("non-Spring repo must not match")
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
