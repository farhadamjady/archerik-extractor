package pipeline

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// springRepo lays out a minimal Spring Boot service the detector recognizes.
func springRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write(t, root, "pom.xml", "<project><artifactId>spring-boot-starter</artifactId></project>")
	write(t, root, "src/main/java/App.java", "@SpringBootApplication public class App {}")
	write(t, root, "src/main/resources/application.yml", "spring:\n  application:\n    name: demo\n")
	// Excluded content must be collected around, not tripped over.
	write(t, root, "src/test/java/AppTest.java", "class AppTest {}")
	return root
}

// TestRunEmptyGraph drives the whole pipeline over a real (temp) Spring repo:
// every phase runs, and the result is the empty contract graph — non-nil sorted
// slices, service name derived from the repo directory.
func TestRunEmptyGraph(t *testing.T) {
	root := springRepo(t)

	svc, err := Run(context.Background(), Options{Root: root})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	wantName := filepath.Base(root)
	if svc.ServiceName != wantName || svc.ServiceID != wantName {
		t.Errorf("service identity = (%q,%q), want both %q", svc.ServiceID, svc.ServiceName, wantName)
	}
	for what, n := range map[string]int{
		"endpoints":             len(svc.Endpoints),
		"outbound_dependencies": len(svc.OutboundDependencies),
		"kafka_producers":       len(svc.KafkaProducers),
		"kafka_consumers":       len(svc.KafkaConsumers),
	} {
		if n != 0 {
			t.Errorf("%s = %d, want 0 (no detectors have rules yet)", what, n)
		}
	}

	// The canonical encoding is the empty contract, byte-stable across runs.
	b1, err := Marshal(svc)
	if err != nil {
		t.Fatal(err)
	}
	svc2, err := Run(context.Background(), Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := Marshal(svc2)
	if !bytes.Equal(b1, b2) {
		t.Errorf("output not byte-stable:\n run1: %s run2: %s", b1, b2)
	}

	want := fmt.Sprintf(`{"service_id":%[1]q,"service_name":%[1]q,"repository":"",`+
		`"endpoints":[],"outbound_dependencies":[],"kafka_producers":[],"kafka_consumers":[],`+
		`"databases_used":[],"config_dependencies":[]}`+"\n", wantName)
	if string(b1) != want {
		t.Errorf("contract drift:\n got: %swant: %s", b1, want)
	}
}

// TestRunFailsLoudOnNoProvider pins the fail-loud detection contract: a repo no
// provider recognizes is a hard error, not a silent empty graph.
func TestRunFailsLoudOnNoProvider(t *testing.T) {
	root := t.TempDir()
	write(t, root, "main.go", "package main")

	if _, err := Run(context.Background(), Options{Root: root}); err == nil {
		t.Fatal("expected detection error for a non-Spring repo, got nil")
	}
}

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
