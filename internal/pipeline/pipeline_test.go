package pipeline

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/farhadamjady/service-discovery/internal/exitcode"
	"github.com/farhadamjady/service-discovery/internal/model"
)

// testKey satisfies the presence-only auth gate so pipeline tests can run
// without real validation (auth is a stub until PR 23).
const testKey = "test-key"

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

	svc, err := Run(context.Background(), Options{Root: root, APIKey: testKey})
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
	svc2, err := Run(context.Background(), Options{Root: root, APIKey: testKey})
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := Marshal(svc2)
	if !bytes.Equal(b1, b2) {
		t.Errorf("output not byte-stable:\n run1: %s run2: %s", b1, b2)
	}

	want := fmt.Sprintf(`{"service_id":%[1]q,"service_name":%[1]q,"repository":"","language":"Java",`+
		`"endpoints":[],"outbound_dependencies":[],"kafka_producers":[],"kafka_consumers":[],`+
		`"databases_used":[],"config_dependencies":[]}`+"\n", wantName)
	if string(b1) != want {
		t.Errorf("contract drift:\n got: %swant: %s", b1, want)
	}
}

// TestRunFailsLoudOnNoProvider pins the fail-loud detection contract: a repo no
// provider recognizes is a hard error (exit 2), not a silent empty graph.
func TestRunFailsLoudOnNoProvider(t *testing.T) {
	root := t.TempDir()
	write(t, root, "main.go", "package main")

	_, err := Run(context.Background(), Options{Root: root, APIKey: testKey})
	if err == nil {
		t.Fatal("expected detection error for a non-Spring repo, got nil")
	}
	if code := exitcode.Of(err); code != int(exitcode.Detect) {
		t.Errorf("exit code = %d, want %d (detect)", code, exitcode.Detect)
	}
}

// TestRunRequiresKey pins the fail-closed auth gate: no key means nothing runs.
// The error must carry exit 10 AND fire before detection (a Spring repo with no
// key still fails with the auth code, not by scanning).
func TestRunRequiresKey(t *testing.T) {
	root := springRepo(t)

	_, err := Run(context.Background(), Options{Root: root}) // no APIKey
	if err == nil {
		t.Fatal("expected missing-key error, got nil")
	}
	if code := exitcode.Of(err); code != int(exitcode.AuthMissingKey) {
		t.Errorf("exit code = %d, want %d (missing key)", code, exitcode.AuthMissingKey)
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

// TestSharedModuleTypes (IMPROVEMENTS #6): the Kafka payload DTO lives in a
// SIBLING Maven module; the schema must still resolve (types only).
func TestSharedModuleTypes(t *testing.T) {
	repo := t.TempDir()
	// parent pom listing both modules
	write(t, repo, "pom.xml", "<project><modules><module>base-domain</module><module>order-service</module></modules></project>")
	// shared module with the DTO
	write(t, repo, "base-domain/pom.xml", "<project></project>")
	write(t, repo, "base-domain/src/main/java/Order.java", "public class Order { private String id; private int total; }")
	// the scanned service
	root := filepath.Join(repo, "order-service")
	write(t, repo, "order-service/pom.xml", "<project><artifactId>spring-boot-starter</artifactId></project>")
	write(t, repo, "order-service/src/main/java/App.java", "@SpringBootApplication public class App {}")
	write(t, repo, "order-service/src/main/java/Pub.java",
		"class Pub { KafkaTemplate<String, Order> kt; void m() { kt.send(\"orders\", null); } }")

	svc, err := Run(context.Background(), Options{Root: root, APIKey: testKey})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(svc.KafkaProducers) != 1 {
		t.Fatalf("producers = %+v, want 1", svc.KafkaProducers)
	}
	sc := svc.KafkaProducers[0].Schema
	if sc == nil || sc.Type != "Order" {
		t.Fatalf("schema = %+v, want Order (resolved from the sibling module)", sc)
	}
	if len(sc.Nested) != 2 {
		t.Errorf("Order fields = %+v, want 2", sc.Nested)
	}
}

// TestSharedLibraryByGAV (IMPROVEMENTS #25): no aggregator pom — the shared lib
// is a standalone sibling project the service consumes as a versioned Maven
// dependency (published artifact). Its types must still feed the schema pass; a
// sibling the service does NOT depend on must stay invisible.
func TestSharedLibraryByGAV(t *testing.T) {
	repo := t.TempDir()
	// the shared lib: own GAV, no reactor
	write(t, repo, "common-lib/pom.xml",
		"<project><groupId>io.acme</groupId><artifactId>common-lib</artifactId><version>1.0.7</version></project>")
	write(t, repo, "common-lib/src/main/java/OrderEvent.java",
		"public class OrderEvent { private String orderId; private int total; }")
	// a sibling project that is NOT a dependency — must not be indexed
	write(t, repo, "unrelated-lib/pom.xml",
		"<project><groupId>io.acme</groupId><artifactId>unrelated-lib</artifactId></project>")
	write(t, repo, "unrelated-lib/src/main/java/StockEvent.java",
		"public class StockEvent { private String sku; }")
	// the scanned service depends on common-lib by GAV
	root := filepath.Join(repo, "order-service")
	write(t, repo, "order-service/pom.xml",
		`<project><artifactId>order-service</artifactId><dependencies>
			<dependency><groupId>io.acme</groupId><artifactId>common-lib</artifactId><version>1.0.7</version></dependency>
			<dependency><groupId>org.springframework.boot</groupId><artifactId>spring-boot-starter</artifactId></dependency>
		</dependencies></project>`)
	write(t, repo, "order-service/src/main/java/App.java", "@SpringBootApplication public class App {}")
	write(t, repo, "order-service/src/main/java/Pub.java",
		"class Pub { KafkaTemplate<String, OrderEvent> kt; KafkaTemplate<String, StockEvent> kt2;"+
			" void m() { kt.send(\"orders\", null); kt2.send(\"stock\", null); } }")

	svc, err := Run(context.Background(), Options{Root: root, APIKey: testKey})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(svc.KafkaProducers) != 2 {
		t.Fatalf("producers = %+v, want 2", svc.KafkaProducers)
	}
	var orders, stock *model.KafkaEdge
	for i := range svc.KafkaProducers {
		switch svc.KafkaProducers[i].Topic {
		case "orders":
			orders = &svc.KafkaProducers[i]
		case "stock":
			stock = &svc.KafkaProducers[i]
		}
	}
	if orders == nil || orders.Schema == nil || orders.Schema.Type != "OrderEvent" {
		t.Fatalf("orders schema = %+v, want OrderEvent (resolved from the GAV sibling)", orders)
	}
	if len(orders.Schema.Nested) != 2 {
		t.Errorf("OrderEvent fields = %+v, want 2", orders.Schema.Nested)
	}
	if stock == nil || stock.Schema != nil {
		t.Errorf("stock schema = %+v, want nil (unrelated-lib is not a dependency)", stock)
	}
}

// TestOpenAPIIngestion (IMPROVEMENTS #1): when the build generates controllers
// from openapi.yml (openapi-generator in pom.xml), spec endpoints are ingested
// at likely; without the generator plugin, the spec is ignored (docs drift).
func TestOpenAPIIngestion(t *testing.T) {
	spec := "openapi: 3.0.1\npaths:\n  /owners:\n    get:\n      responses:\n        \"200\":\n          description: ok\n"

	// with the generator plugin -> ingested
	gen := t.TempDir()
	write(t, gen, "pom.xml", "<project><artifactId>x</artifactId><plugin>openapi-generator-maven-plugin</plugin><dep>spring-boot</dep></project>")
	write(t, gen, "src/main/java/App.java", "@SpringBootApplication public class App {}")
	write(t, gen, "src/main/resources/openapi.yml", spec)
	svc, err := Run(context.Background(), Options{Root: gen, APIKey: testKey})
	if err != nil {
		t.Fatal(err)
	}
	if len(svc.Endpoints) != 1 || svc.Endpoints[0].Path != "/owners" {
		t.Fatalf("generated build: endpoints = %+v, want GET /owners from spec", svc.Endpoints)
	}
	e := svc.Endpoints[0]
	if e.Detection != model.DetectOpenAPI || e.Confidence != model.Likely || e.Protocol != model.ProtoREST {
		t.Errorf("spec endpoint fields = (%s,%s,%s), want openapi/likely/rest", e.Detection, e.Confidence, e.Protocol)
	}

	// without the plugin -> spec ignored
	plain := t.TempDir()
	write(t, plain, "pom.xml", "<project><dep>spring-boot</dep></project>")
	write(t, plain, "src/main/java/App.java", "@SpringBootApplication public class App {}")
	write(t, plain, "src/main/resources/openapi.yml", spec)
	svc2, err := Run(context.Background(), Options{Root: plain, APIKey: testKey})
	if err != nil {
		t.Fatal(err)
	}
	if len(svc2.Endpoints) != 0 {
		t.Errorf("plain build: spec must be ignored, got %+v", svc2.Endpoints)
	}
}

// TestRepoRootDeployConfig (IMPROVEMENTS #9): a monorepo keeps its k8s config
// at the repo top; a service scanned from a subfolder still resolves env vars
// through it.
func TestRepoRootDeployConfig(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, repo, "kubernetes-manifests/config.yaml",
		"apiVersion: v1\nkind: ConfigMap\nmetadata: { name: cfg }\ndata:\n  BALANCES_API_ADDR: \"balancereader:8080\"\n")
	root := filepath.Join(repo, "src", "ledgerwriter")
	write(t, repo, "src/ledgerwriter/pom.xml", "<project><dep>spring-boot</dep></project>")
	write(t, repo, "src/ledgerwriter/src/main/java/App.java", "@SpringBootApplication public class App {}")
	write(t, repo, "src/ledgerwriter/src/main/java/P.java",
		"@FeignClient(name=\"balances\", url=\"http://${BALANCES_API_ADDR}\") interface P {}")

	svc, err := Run(context.Background(), Options{Root: root, APIKey: testKey})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(svc.OutboundDependencies) != 1 {
		t.Fatalf("deps = %+v, want 1", svc.OutboundDependencies)
	}
	d := svc.OutboundDependencies[0]
	if d.URL != "http://balancereader:8080" || !d.Resolved || string(d.Confidence) != "likely" {
		t.Errorf("dep = %+v, want http://balancereader:8080 resolved/likely (via repo-root ConfigMap)", d)
	}
}

// TestConfigRepo (IMPROVEMENTS #16): a value that lives only in the external
// Spring Cloud Config repo resolves when --config-repo points at its checkout.
func TestConfigRepo(t *testing.T) {
	cfgRepo := t.TempDir()
	write(t, cfgRepo, "orders-service.yml", "payment:\n  url: http://payment:8080\n")

	root := springRepo(t)
	write(t, root, "src/main/java/P.java",
		"@FeignClient(name=\"pay\", url=\"${payment.url}\") interface P {}")

	svc, err := Run(context.Background(), Options{Root: root, APIKey: testKey, ConfigRepo: cfgRepo})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(svc.OutboundDependencies) != 1 || svc.OutboundDependencies[0].URL != "http://payment:8080" {
		t.Fatalf("deps = %+v, want resolved via config repo", svc.OutboundDependencies)
	}
	// without the config repo the same placeholder stays unresolved
	svc2, _ := Run(context.Background(), Options{Root: root, APIKey: testKey})
	if len(svc2.OutboundDependencies) != 1 || svc2.OutboundDependencies[0].Resolved {
		t.Errorf("without config repo = %+v, want unresolved", svc2.OutboundDependencies)
	}
}

// TestNestedAggregatorSharedModule (IMPROVEMENTS #27): the shared lib listed in
// the reactor is ITSELF an aggregator — sources live one level down
// (common-lib/common-kafka/src/...). The walk must expand its <modules>.
func TestNestedAggregatorSharedModule(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "pom.xml", "<project><modules><module>common-lib</module><module>order-service</module></modules></project>")
	write(t, repo, "common-lib/pom.xml", "<project><artifactId>common-lib</artifactId><modules><module>common-kafka</module></modules></project>")
	write(t, repo, "common-lib/common-kafka/pom.xml", "<project><artifactId>common-kafka</artifactId></project>")
	write(t, repo, "common-lib/common-kafka/src/main/java/OrderEvent.java",
		"public class OrderEvent { private String orderId; private int total; }")
	root := filepath.Join(repo, "order-service")
	write(t, repo, "order-service/pom.xml", "<project><artifactId>spring-boot-starter</artifactId></project>")
	write(t, repo, "order-service/src/main/java/App.java", "@SpringBootApplication public class App {}")
	write(t, repo, "order-service/src/main/java/Pub.java",
		"class Pub { KafkaTemplate<String, OrderEvent> kt; void m() { kt.send(\"orders\", null); } }")

	svc, err := Run(context.Background(), Options{Root: root, APIKey: testKey})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(svc.KafkaProducers) != 1 {
		t.Fatalf("producers = %+v, want 1", svc.KafkaProducers)
	}
	sc := svc.KafkaProducers[0].Schema
	if sc == nil || sc.Type != "OrderEvent" || len(sc.Nested) != 2 {
		t.Fatalf("schema = %+v, want OrderEvent with 2 fields (from the nested submodule)", sc)
	}
}

// TestGradleSharedModule (IMPROVEMENTS #29): a Gradle repo — no pom anywhere.
// settings.gradle includes the service; its build.gradle depends on
// project(':shared-lib'), whose types must feed the schema pass.
func TestGradleSharedModule(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "settings.gradle", "include \"shared-lib\"\ninclude \"order-service\"\n")
	write(t, repo, "shared-lib/build.gradle", "apply plugin: 'java'\n")
	write(t, repo, "shared-lib/src/main/java/OrderEvent.java",
		"public class OrderEvent { private String orderId; private int total; }")
	root := filepath.Join(repo, "order-service")
	write(t, repo, "order-service/build.gradle",
		"dependencies { implementation project(':shared-lib')\n implementation 'org.springframework.boot:spring-boot-starter' }\n")
	write(t, repo, "order-service/src/main/java/App.java", "@SpringBootApplication public class App {}")
	write(t, repo, "order-service/src/main/java/Pub.java",
		"class Pub { KafkaTemplate<String, OrderEvent> kt; void m() { kt.send(\"orders\", null); } }")

	svc, err := Run(context.Background(), Options{Root: root, APIKey: testKey})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(svc.KafkaProducers) != 1 {
		t.Fatalf("producers = %+v, want 1", svc.KafkaProducers)
	}
	sc := svc.KafkaProducers[0].Schema
	if sc == nil || sc.Type != "OrderEvent" || len(sc.Nested) != 2 {
		t.Fatalf("schema = %+v, want OrderEvent with 2 fields (from the Gradle project dep)", sc)
	}
}
