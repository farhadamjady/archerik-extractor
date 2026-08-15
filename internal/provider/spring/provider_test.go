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

// TestMatchNeedsSpringNotJustABuildFile locks the rule that keeps an
// unrecognized JVM repo unrecognized: a pom.xml or build.gradle with no Spring
// anywhere is NOT a Spring service. Matching on the build file alone claimed
// every Maven repo in existence — a plain library was scanned as a service and
// emitted an empty graph, which is exactly what exit 2 exists to prevent.
func TestMatchNeedsSpringNotJustABuildFile(t *testing.T) {
	plain := t.TempDir()
	writeFile(t, plain, "pom.xml", "<project><artifactId>string-utils</artifactId></project>")
	writeFile(t, plain, "src/main/java/Utils.java", "public class Utils { }")
	if m, score := New().Match(plain, scan.NewOSFileTree(plain, nil)); m {
		t.Errorf("plain Maven library matched Spring (score %d); a build file alone must not match", score)
	}

	gradle := t.TempDir()
	writeFile(t, gradle, "build.gradle", "dependencies { implementation 'com.google.guava:guava:32.0' }")
	writeFile(t, gradle, "src/main/java/Utils.java", "public class Utils { }")
	if m, _ := New().Match(gradle, scan.NewOSFileTree(gradle, nil)); m {
		t.Error("plain Gradle library matched Spring; a build file alone must not match")
	}

	// A module inheriting its dependencies from a parent POM has no spring-boot
	// coordinates and no @SpringBootApplication, but its sources import Spring —
	// that is a real Spring module and must still match.
	module := t.TempDir()
	writeFile(t, module, "pom.xml", "<project><parent><artifactId>acme-parent</artifactId></parent></project>")
	writeFile(t, module, "src/main/java/OrderController.java",
		"import org.springframework.web.bind.annotation.RestController;\n@RestController public class OrderController {}")
	if m, _ := New().Match(module, scan.NewOSFileTree(module, nil)); !m {
		t.Error("a Spring module inheriting deps from a parent POM must still match")
	}
}
