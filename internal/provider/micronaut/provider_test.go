package micronaut

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/farhadamjady/archerik-extractor/internal/model"
	"github.com/farhadamjady/archerik-extractor/internal/provider"
	"github.com/farhadamjady/archerik-extractor/internal/scan"
)

// Compile-time seam conformance: the Micronaut provider must satisfy the full
// Provider interface.
var _ provider.Provider = (*Provider)(nil)

// TestParsersCoverFileSpec pins the routing invariant: every kind the FileSpec
// collects must have a parser.
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

// TestDetectors pins the detector set: three concerns, unique names, each stamped
// with its protocol.
func TestDetectors(t *testing.T) {
	want := map[string]model.Protocol{
		"micronaut.rest":   model.ProtoREST,
		"micronaut.client": model.ProtoREST,
		"micronaut.kafka":  model.ProtoKafka,
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

// TestMatch exercises detection scoring: a Micronaut build file + a source
// importing io.micronaut scores highly; an unrelated repo must not match.
func TestMatch(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "build.gradle", `dependencies { implementation "io.micronaut:micronaut-http-server-netty" }`)
	writeFile(t, root, "src/main/java/PetController.java",
		"import io.micronaut.http.annotation.Controller;\n@Controller(\"/pets\") class PetController {}")

	fs := scan.NewOSFileTree(root, nil)
	matched, score := New().Match(root, fs)
	if !matched {
		t.Fatal("expected Micronaut repo to match")
	}
	// build.gradle io.micronaut (2) + import io.micronaut (3)
	if score != 5 {
		t.Errorf("score = %d, want 5", score)
	}

	empty := t.TempDir()
	writeFile(t, empty, "main.go", "package main")
	if matched, _ := New().Match(empty, scan.NewOSFileTree(empty, nil)); matched {
		t.Error("non-Micronaut repo must not match")
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
