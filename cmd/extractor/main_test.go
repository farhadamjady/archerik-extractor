package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/farhadamjady/service-discovery/internal/exitcode"
)

func springRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write(t, root, "pom.xml", "<project><artifactId>spring-boot-starter</artifactId></project>")
	write(t, root, "src/main/java/App.java", "@SpringBootApplication public class App {}")
	return root
}

// TestRunEmptyJSON is the milestone: extractor scans a real Spring repo and
// prints the exact empty-contract JSON (with trailing newline), exit 0. This is
// the end-to-end demo that everything downstream enriches.
func TestRunEmptyJSON(t *testing.T) {
	root := springRepo(t)
	var stdout, stderr bytes.Buffer

	code := run([]string{"--root", root, "--api-key", "k", "--dry-run"}, &stdout, &stderr)
	if code != int(exitcode.OK) {
		t.Fatalf("exit = %d, want 0; stderr: %s", code, stderr.String())
	}

	name := filepath.Base(root)
	want := fmt.Sprintf(`{"service_id":%[1]q,"service_name":%[1]q,"repository":"",`+
		`"endpoints":[],"outbound_dependencies":[],"kafka_producers":[],"kafka_consumers":[],`+
		`"databases_used":[],"config_dependencies":[]}`+"\n", name)
	if stdout.String() != want {
		t.Errorf("stdout:\n got: %s want: %s", stdout.String(), want)
	}
}

// TestExitCodes pins the taxonomy at the CLI boundary: missing key -> 10 (before
// any scan), unrecognized repo -> 2.
func TestExitCodes(t *testing.T) {
	spring := springRepo(t)

	nonSpring := t.TempDir()
	write(t, nonSpring, "main.go", "package main")

	cases := []struct {
		name string
		args []string
		env  string // EKG_API_KEY
		want exitcode.Code
	}{
		{"missing key", []string{"--root", spring, "--dry-run"}, "", exitcode.AuthMissingKey},
		{"no provider", []string{"--root", nonSpring, "--api-key", "k", "--dry-run"}, "", exitcode.Detect},
		{"env key ok", []string{"--root", spring, "--dry-run"}, "env-key", exitcode.OK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("EKG_API_KEY", c.env)
			var stdout, stderr bytes.Buffer
			if got := run(c.args, &stdout, &stderr); got != int(c.want) {
				t.Errorf("exit = %d, want %d; stderr: %s", got, c.want, stderr.String())
			}
		})
	}
}

// TestKeyNeverLeaks guards the masking invariant (PLAN §B.3 / PR 24 audit): the
// resolved key value must appear in NEITHER stdout nor stderr on a normal run.
func TestKeyNeverLeaks(t *testing.T) {
	root := springRepo(t)
	const secret = "super-secret-key-DO-NOT-PRINT"
	var stdout, stderr bytes.Buffer

	run([]string{"--root", root, "--api-key", secret, "--dry-run"}, &stdout, &stderr)

	if strings.Contains(stdout.String(), secret) {
		t.Error("API key leaked into stdout")
	}
	if strings.Contains(stderr.String(), secret) {
		t.Error("API key leaked into stderr")
	}
}

// TestResolveKeyPrecedence pins --api-key > EKG_API_KEY > config file.
func TestResolveKeyPrecedence(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "cfg")
	write(t, dir, "cfg", "# comment\napi_key = \"from-config\"\n")

	cases := []struct {
		flag, env, cfg string
		want           string
	}{
		{"from-flag", "from-env", cfg, "from-flag"},
		{"", "from-env", cfg, "from-env"},
		{"", "", cfg, "from-config"},
		{"", "", "", ""},
	}
	for _, c := range cases {
		got, err := resolveKey(c.flag, c.env, c.cfg)
		if err != nil {
			t.Fatalf("resolveKey(%q,%q,%q): %v", c.flag, c.env, c.cfg, err)
		}
		if got != c.want {
			t.Errorf("resolveKey(%q,%q,%q) = %q, want %q", c.flag, c.env, c.cfg, got, c.want)
		}
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
