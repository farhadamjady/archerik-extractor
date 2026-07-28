// Command extractor is the service-discovery CLI: it scans a single service repo
// and emits its architecture graph as JSON (see docs/DESIGN.md). The tool is
// paid — a run requires a valid API key (resolved here, validated fail-closed
// before any scan).
//
// Two modes share this binary (--mode): "scan-repo" (default) scans a single
// service repo; "deploy-repo" scans a deployment/GitOps repo (Helm/Kustomize/
// plain k8s manifests) and emits an identity map instead of a service graph.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"encoding/json"

	"github.com/farhadamjady/service-discovery/internal/deployrepo"
	"github.com/farhadamjady/service-discovery/internal/exitcode"
	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/pipeline"
	"github.com/farhadamjady/service-discovery/internal/submit"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

// run is the testable entry point: args in, streams in, exit code out (no
// os.Exit, no globals), so tests can drive the whole CLI and assert output.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("extractor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		mode         = fs.String("mode", "scan-repo", "scan mode: scan-repo (default, single service) or deploy-repo (Helm/Kustomize/k8s deploy repo)")
		root         = fs.String("root", ".", "repository root to scan")
		repository   = fs.String("repository", "", "repository identifier emitted as service.repository (e.g. github.com/owner/repo); auto-detected from CI env / git remote")
		apiKey       = fs.String("api-key", "", "API key (overrides EKG_API_KEY and config file)")
		configFile   = fs.String("config", "", "path to config file")
		profiles     = fs.String("profiles", "", "comma-separated active Spring profiles (scan-repo mode only)")
		environment  = fs.String("environment", "", "deploy overlay to resolve (e.g. staging) (scan-repo mode only)")
		environments = fs.String("environments", "", "comma-separated deploy environments to render, empty renders every discovered environment (deploy-repo mode only)")
		resolvers    = fs.String("resolvers", "", "comma-separated host resolvers to run: helm,kustomize,k8s-raw,ingress,istio,self-declared; empty runs all (deploy-repo mode only)")
		nsConvention = fs.String("namespace-convention", "", "derive namespace from service name when a manifest declares none: service-name | replace:<from>:<to> (deploy-repo mode only)")
		configRepo   = fs.String("config-repo", "", "local checkout of the Spring Cloud Config repo (its yml/properties feed resolution) (scan-repo mode only)")
		out          = fs.String("out", "-", "output path for the JSON, or - for stdout")
		apiURL       = fs.String("api-url", "", "backend base URL for key validation + submit; empty runs local/dev")
		dryRun       = fs.Bool("dry-run", false, "produce JSON but do not submit")
		branch       = fs.String("branch", "", "branch being scanned (auto-detected from CI env)")
		sha          = fs.String("sha", "", "commit sha (auto-detected from CI env)")
		pr           = fs.String("pr", "", "pull-request number (auto-detected from CI env)")
		commentOut   = fs.String("comment-out", "", "write the returned PR-comment markdown to this file, empty discards it (scan-repo mode only)")
	)
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return int(exitcode.OK)
		}
		return int(exitcode.Runtime) // flag already printed the usage error
	}

	// Key precedence: --api-key > EKG_API_KEY > config file. Never logged. An
	// empty result is not an error here — the auth gate reports the missing key
	// (exit 10) so the message lives in one place.
	key, err := resolveKey(*apiKey, os.Getenv("EKG_API_KEY"), *configFile)
	if err != nil {
		fmt.Fprintln(stderr, "extractor:", err)
		return exitcode.Of(err)
	}

	switch *mode {
	case "scan-repo", "":
		return runScanRepo(scanRepoArgs{
			root: *root, repository: *repository, configFile: *configFile,
			profiles: *profiles, environment: *environment, configRepo: *configRepo,
			out: *out, apiURL: *apiURL, dryRun: *dryRun,
			branch: *branch, sha: *sha, pr: *pr, commentOut: *commentOut,
		}, key, stdout, stderr)
	case "deploy-repo":
		if !deployrepo.ValidNamespaceConvention(*nsConvention) {
			fmt.Fprintf(stderr, "extractor: invalid --namespace-convention %q (want service-name or replace:<from>:<to>)\n", *nsConvention)
			return int(exitcode.Runtime)
		}
		return runDeployRepo(deployRepoArgs{
			root: *root, repository: *repository, apiURL: *apiURL, out: *out,
			environments: *environments, resolvers: *resolvers, nsConvention: *nsConvention,
			dryRun: *dryRun, branch: *branch, sha: *sha, pr: *pr,
		}, key, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "extractor: unknown --mode %q (want scan-repo or deploy-repo)\n", *mode)
		return int(exitcode.Runtime)
	}
}

// scanRepoArgs is the resolved flag set for the default, single-service scan.
type scanRepoArgs struct {
	root, repository, configFile, profiles, environment, configRepo, out, apiURL string
	dryRun                                                                       bool
	branch, sha, pr, commentOut                                                  string
}

// runScanRepo is the original, unchanged single-service scan path — extracted
// verbatim out of run() so --mode can dispatch to it. Every existing test
// omits --mode, so it hits this exact path via the "scan-repo"/"" default.
func runScanRepo(a scanRepoArgs, key string, stdout, stderr io.Writer) int {
	svc, err := pipeline.Run(context.Background(), pipeline.Options{
		Root:        a.root,
		Repository:  resolveRepository(a.repository, a.root),
		APIKey:      key,
		ConfigFile:  a.configFile,
		Profiles:    splitCSV(a.profiles),
		Environment: a.environment,
		ConfigRepo:  a.configRepo,
		APIURL:      a.apiURL,
		DryRun:      a.dryRun,
		CI:          ciMeta(a.branch, a.sha, a.pr),
		OnSubmitResponse: func(body []byte) {
			handleIngestResponse(body, a.commentOut, stderr)
		},
	})
	if err != nil {
		fmt.Fprintln(stderr, "extractor:", err)
		return exitcode.Of(err)
	}

	data, err := pipeline.Marshal(svc)
	if err != nil {
		fmt.Fprintln(stderr, "extractor:", err)
		return int(exitcode.Runtime)
	}
	if err := writeOutput(a.out, data, stdout); err != nil {
		fmt.Fprintln(stderr, "extractor:", err)
		return int(exitcode.Runtime)
	}

	submitSelfDeclaredIdentity(context.Background(), a, key, stderr)

	return int(exitcode.OK)
}

// deployRepoArgs is the resolved flag set for a deploy-repo scan.
type deployRepoArgs struct {
	root, repository, apiURL, out, environments, resolvers, nsConvention string
	dryRun                                                               bool
	branch, sha, pr                                                      string
}

// runDeployRepo scans a deployment/GitOps repo and emits an identity map
// instead of a service graph. Render failures on individual charts/overlays/
// manifests are non-fatal — printed as warnings, never aborting the scan.
func runDeployRepo(a deployRepoArgs, key string, stdout, stderr io.Writer) int {
	im, renderErrs, err := deployrepo.Run(context.Background(), deployrepo.Options{
		Root:                a.root,
		Repository:          resolveRepository(a.repository, a.root),
		APIKey:              key,
		APIURL:              a.apiURL,
		DryRun:              a.dryRun,
		Environments:        splitCSV(a.environments),
		Resolvers:           splitCSV(a.resolvers),
		NamespaceConvention: a.nsConvention,
		CI:                  ciMeta(a.branch, a.sha, a.pr),
	})
	for _, re := range renderErrs {
		fmt.Fprintln(stderr, "extractor: render warning:", re)
	}
	if err != nil {
		fmt.Fprintln(stderr, "extractor:", err)
		return exitcode.Of(err)
	}

	data, err := deployrepo.Marshal(im)
	if err != nil {
		fmt.Fprintln(stderr, "extractor:", err)
		return int(exitcode.Runtime)
	}
	if err := writeOutput(a.out, data, stdout); err != nil {
		fmt.Fprintln(stderr, "extractor:", err)
		return int(exitcode.Runtime)
	}
	return int(exitcode.OK)
}

// submitSelfDeclaredIdentity submits .ekg-identity.json's declared hosts (if
// the file exists) as a small IdentityMap alongside the primary scan's
// Service submission. Best-effort and side-channel: any failure here is
// logged and never affects the primary scan's exit code, and it is never
// merged with a deploy-repo-mode scan locally — the extractor stays
// stateless, the backend reconciles sources via IdentityEntry.Source. The
// file's parsing lives in the deployrepo self-declared resolver, shared with
// deploy-repo mode.
func submitSelfDeclaredIdentity(ctx context.Context, a scanRepoArgs, key string, stderr io.Writer) {
	entries := deployrepo.ReadSelfDeclared(a.root)
	if len(entries) == 0 {
		return
	}
	im := model.NewIdentityMap(resolveRepository(a.repository, a.root))
	im.Entries = entries
	data, err := deployrepo.Marshal(im)
	if err != nil {
		fmt.Fprintln(stderr, "extractor: .ekg-identity.json marshal failed (non-fatal):", err)
		return
	}
	if a.dryRun || a.apiURL == "" {
		return
	}
	if _, err := submit.SubmitIdentityMap(ctx, a.apiURL, key, data, ciMeta(a.branch, a.sha, a.pr)); err != nil {
		fmt.Fprintln(stderr, "extractor: .ekg-identity.json submit failed (non-fatal):", err)
	}
}

// resolveKey applies key precedence. The config-file lookup is intentionally
// tiny (a single `api_key = <value>` / `api_key: <value>` line) — the full
// config file is parsed by the config indexer in PR 8; this only needs the key.
func resolveKey(flagKey, envKey, configFile string) (string, error) {
	if flagKey != "" {
		return flagKey, nil
	}
	if envKey != "" {
		return envKey, nil
	}
	if configFile != "" {
		b, err := os.ReadFile(configFile)
		if err != nil {
			return "", exitcode.Wrap(exitcode.Runtime, "read config file", err)
		}
		if k := scanConfigKey(b); k != "" {
			return k, nil
		}
	}
	return "", nil
}

// scanConfigKey extracts an api_key value from a config file, tolerating both
// `api_key = v` and `api_key: v`, ignoring surrounding quotes and comments.
func scanConfigKey(b []byte) string {
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		for _, sep := range []string{"=", ":"} {
			if k, v, ok := strings.Cut(line, sep); ok && strings.TrimSpace(k) == "api_key" {
				return strings.Trim(strings.TrimSpace(v), `"'`)
			}
		}
	}
	return ""
}

// resolveRepository determines the repo identifier emitted as service.repository
// — the backend's read-API key, so it must be non-empty and stable per repo.
// Precedence: explicit flag > GitHub Actions env > git remote. All paths
// normalize to host/owner/repo (e.g. github.com/acme/orders) so a CI run and a
// local run of the same repo produce the identical key.
func resolveRepository(flagRepo, root string) string {
	if flagRepo != "" {
		return flagRepo
	}
	// GitHub Actions: GITHUB_REPOSITORY is owner/repo; prepend the server host.
	if r := os.Getenv("GITHUB_REPOSITORY"); r != "" {
		host := strings.TrimPrefix(strings.TrimPrefix(os.Getenv("GITHUB_SERVER_URL"), "https://"), "http://")
		if host == "" {
			host = "github.com"
		}
		return host + "/" + r
	}
	return gitRemoteSlug(root) // best-effort local fallback; "" when not a git checkout
}

// gitRemoteSlug reads origin's URL and normalizes it to host/owner/repo. Returns
// "" on any error (not a checkout, no origin) — never fails the run.
func gitRemoteSlug(root string) string {
	out, err := exec.Command("git", "-C", root, "config", "--get", "remote.origin.url").Output()
	if err != nil {
		return ""
	}
	return normalizeRemote(string(out))
}

// normalizeRemote maps both SSH (git@host:owner/repo.git) and HTTPS
// (https://host/owner/repo.git) remotes to host/owner/repo.
func normalizeRemote(url string) string {
	url = strings.TrimSuffix(strings.TrimSpace(url), ".git")
	if i := strings.Index(url, "://"); i >= 0 {
		url = url[i+3:] // strip scheme
	}
	url = strings.TrimPrefix(url, "git@")
	if at := strings.Index(url, "@"); at >= 0 {
		url = url[at+1:] // strip any remaining userinfo
	}
	return strings.Replace(url, ":", "/", 1) // scp-form host:owner/repo -> host/owner/repo
}

// ciMeta builds the commit metadata for the submission: explicit flags win,
// else GitHub Actions env vars. On PR events GITHUB_HEAD_REF is the source
// branch and GITHUB_BASE_REF the target (used as the baseline branch).
func ciMeta(branch, sha, pr string) submit.Meta {
	m := submit.Meta{Sha: sha, Branch: branch, PR: pr}
	if m.Sha == "" {
		m.Sha = os.Getenv("GITHUB_SHA")
	}
	if m.Branch == "" {
		if m.Branch = os.Getenv("GITHUB_HEAD_REF"); m.Branch == "" {
			m.Branch = os.Getenv("GITHUB_REF_NAME")
		}
	}
	if m.PR == "" {
		// refs/pull/42/merge -> 42
		if ref := os.Getenv("GITHUB_REF"); strings.HasPrefix(ref, "refs/pull/") {
			m.PR = strings.SplitN(strings.TrimPrefix(ref, "refs/pull/"), "/", 2)[0]
		}
	}
	m.DefaultBranch = os.Getenv("GITHUB_BASE_REF") // PR target; backend defaults to main
	return m
}

// handleIngestResponse surfaces the backend's diff: a one-line summary on
// stderr and, when requested, the PR-comment markdown written to a file
// (CI posts it: `gh pr comment --body-file`).
func handleIngestResponse(body []byte, commentPath string, stderr io.Writer) {
	var resp struct {
		Unchanged bool   `json:"unchanged"`
		FirstScan bool   `json:"first_scan"`
		Markdown  string `json:"markdown"`
		Diff      *struct {
			Summary struct{ Added, Removed, Changed int } `json:"summary"`
		} `json:"diff"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return // response not understood — never fail the scan over reporting
	}
	switch {
	case resp.Unchanged:
		fmt.Fprintln(stderr, "extractor: architecture unchanged")
	case resp.Diff != nil:
		fmt.Fprintf(stderr, "extractor: architecture impact — %d added · %d removed · %d changed\n",
			resp.Diff.Summary.Added, resp.Diff.Summary.Removed, resp.Diff.Summary.Changed)
	}
	if commentPath != "" && resp.Markdown != "" {
		if err := os.WriteFile(commentPath, []byte(resp.Markdown), 0o644); err != nil {
			fmt.Fprintln(stderr, "extractor: write comment file:", err)
		}
	}
}

func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func writeOutput(path string, data []byte, stdout io.Writer) error {
	if path == "-" || path == "" {
		_, err := stdout.Write(data)
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
