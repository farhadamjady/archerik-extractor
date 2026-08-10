package main

import (
	"bytes"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/farhadamjady/service-discovery/internal/backend"
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
	// Hermetic: pin repository to empty so the golden body is stable regardless of
	// the CI runner's GITHUB_REPOSITORY (the temp root is not a git checkout).
	t.Setenv("GITHUB_REPOSITORY", "")
	root := springRepo(t)
	var stdout, stderr bytes.Buffer

	code := run([]string{"--root", root, "--dry-run"}, &stdout, &stderr)
	if code != int(exitcode.OK) {
		t.Fatalf("exit = %d, want 0; stderr: %s", code, stderr.String())
	}

	name := filepath.Base(root)
	want := fmt.Sprintf(`{"service_id":%[1]q,"service_name":%[1]q,"repository":"","language":"Java",`+
		`"endpoints":[],"outbound_dependencies":[],"kafka_producers":[],"kafka_consumers":[],`+
		`"databases_used":[],"config_dependencies":[]}`+"\n", name)
	if stdout.String() != want {
		t.Errorf("stdout:\n got: %s want: %s", stdout.String(), want)
	}
}

// TestExitCodes pins the taxonomy at the CLI boundary: a local scan needs no
// key, a backend-targeting scan without one exits 10 before any scan, and an
// unrecognized repo exits 2.
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
		{"local run needs no key", []string{"--root", spring, "--dry-run"}, "", exitcode.OK},
		{"missing key for backend", []string{"--root", spring, "--api-url", "https://api.example.com"}, "", exitcode.AuthMissingKey},
		{"no provider", []string{"--root", nonSpring, "--dry-run"}, "", exitcode.Detect},
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

// TestKeyNeverLeaks guards the masking invariant: the resolved key value must
// appear in NEITHER stdout nor stderr on a normal run.
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

// TestCIMetaFromGitHubEnv: PR event env -> branch/sha/pr/default-branch.
func TestCIMetaFromGitHubEnv(t *testing.T) {
	t.Setenv("GITHUB_SHA", "abc123")
	t.Setenv("GITHUB_HEAD_REF", "feature/add-payment")
	t.Setenv("GITHUB_REF", "refs/pull/42/merge")
	t.Setenv("GITHUB_REF_NAME", "42/merge")
	t.Setenv("GITHUB_BASE_REF", "main")

	m := ciMeta("", "", "")
	if m.Sha != "abc123" || m.Branch != "feature/add-payment" || m.PR != "42" || m.DefaultBranch != "main" {
		t.Errorf("meta = %+v", m)
	}
	// explicit flags win
	m = ciMeta("hotfix", "def456", "7")
	if m.Branch != "hotfix" || m.Sha != "def456" || m.PR != "7" {
		t.Errorf("flag override = %+v", m)
	}
}

// TestCommentFileWritten (B4 e2e): extractor -> real backend handler -> the
// PR-comment markdown lands in --comment-out.
func TestCommentFileWritten(t *testing.T) {
	store, err := backend.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(backend.New([]string{"k"}, store).Handler())
	defer srv.Close()

	root := springRepo(t)
	write(t, root, "src/main/java/C.java",
		"@RestController class C { @GetMapping(\"/ping\") String p() { return null; } }")
	comment := filepath.Join(t.TempDir(), "comment.md")

	var stdout, stderr bytes.Buffer
	code := run([]string{"--root", root, "--api-key", "k", "--api-url", srv.URL,
		"--branch", "main", "--comment-out", comment, "--out", filepath.Join(t.TempDir(), "out.json")},
		&stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, stderr.String())
	}
	md, err := os.ReadFile(comment)
	if err != nil {
		t.Fatalf("comment file not written: %v", err)
	}
	if !strings.Contains(string(md), "GET /ping") {
		t.Errorf("comment markdown:\n%s", md)
	}
	if !strings.Contains(stderr.String(), "architecture impact") {
		t.Errorf("stderr summary missing: %s", stderr.String())
	}
}
