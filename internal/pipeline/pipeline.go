// Package pipeline orchestrates a scan end to end, in the phase order of
// DESIGN §3:
//
//	auth-gate → detect → collect → parse → index → detect(query engine)
//	          → schema pass → marshal(deterministic) → submit
//
// Every phase is wired here even though several are still structured no-ops
// (auth, query engine, schema, submit land in their own PRs) — the shape of the
// run never changes again, only phase bodies fill in.
package pipeline

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/farhadamjady/service-discovery/internal/auth"
	"github.com/farhadamjady/service-discovery/internal/detect"
	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/query"
	"github.com/farhadamjady/service-discovery/internal/registry"
	"github.com/farhadamjady/service-discovery/internal/scan"
	"github.com/farhadamjady/service-discovery/internal/submit"
)

// Options carries everything a run needs. APIKey/APIURL/DryRun are consumed by
// the auth and submit phases; ConfigFile/Profiles/Environment by the config and
// deploy-config indexers.
type Options struct {
	Root        string
	Repository  string // repo identifier emitted as service.repository — the backend's read-API key
	APIKey      string
	ConfigFile  string
	Profiles    []string    // active Spring profiles (D3)
	Environment string      // deploy overlay selection, e.g. "staging" (E3)
	SchemaDepth int         // nested-DTO walk depth (--schema-depth, N2); 0 = default (2)
	ConfigRepo  string      // local checkout of the Spring Cloud Config repo (IMPROVEMENTS #16)
	APIURL      string      // backend base URL (auth validate + submit); empty = local/dev
	DryRun      bool        // skip submit
	CI          submit.Meta // commit metadata sent with the submission (headers)

	// OnSubmitResponse, when set, receives the raw ingest-response body (the
	// architecture diff + rendered PR comment) after a successful submit.
	OnSubmitResponse func(body []byte)

	// Providers overrides the registry — for tests. Nil means registry.Default().
	Providers []provider.Provider
}

// Run executes the full pipeline over the single service at opt.Root and
// returns the complete, deterministically-ordered Service graph.
func Run(ctx context.Context, opt Options) (*model.Service, error) {
	if err := authGate(ctx, opt); err != nil {
		return nil, err
	}

	providers := opt.Providers
	if providers == nil {
		providers = registry.Default()
	}

	// detect — pick the single provider; fails loud on none/ambiguous.
	// Detection sees the whole tree (no excludes yet: excludes are provider-specific).
	root, err := filepath.Abs(opt.Root)
	if err != nil {
		return nil, fmt.Errorf("pipeline: resolve root: %w", err)
	}
	p, _, err := detect.Detect(root, scan.NewOSFileTree(root, nil), providers)
	if err != nil {
		return nil, err
	}

	// ServiceID: the extractor emits the raw service name; the backend maps
	// names to canonical ids (CLAUDE.md coverage rule 4). Until submit carries
	// a configured identity, both default to the repo directory name.
	name := filepath.Base(root)
	svc := model.NewService(name, name, opt.Repository)
	svc.Language = p.Language() // the winning provider declares the module's language

	spec := p.FileSpec()
	tree := scan.NewOSFileTree(root, spec.Exclude)

	buckets := collect(tree, spec)

	parsed, err := parse(p, tree, spec, buckets)
	if err != nil {
		return nil, err
	}

	idx, ic, err := index(root, tree, p, parsed, opt)
	if err != nil {
		return nil, err
	}

	if err := detectEdges(p, parsed, idx, svc); err != nil {
		return nil, err
	}

	// Spec ingestion (optional capability): endpoints generated from an OpenAPI
	// spec at build time — the source scan cannot see them (IMPROVEMENTS #1).
	if ing, ok := p.(provider.SpecIngester); ok {
		if err := ing.IngestSpecs(ic, svc); err != nil {
			return nil, err
		}
	}

	collectConfigDeps(idx, svc)

	if err := schemaPass(idx, svc); err != nil {
		return nil, err
	}

	// marshal-phase ordering: deterministic identity + byte-stable output.
	model.Sort(svc)

	if err := submitGraph(ctx, opt, svc); err != nil {
		return nil, err
	}
	return svc, nil
}

// Marshal encodes svc canonically: Sort + compact JSON + trailing newline.
// This is THE byte shape the backend diffs; nothing else may encode a Service.
func Marshal(svc *model.Service) ([]byte, error) {
	model.Sort(svc)
	b, err := json.Marshal(svc)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// authGate validates the API key before anything is scanned (fail-closed): no
// valid key means nothing runs. auth.Validate is a presence-only stub for now;
// the phone-home validation lands in PR 23.
func authGate(ctx context.Context, opt Options) error {
	_, err := auth.Validate(ctx, opt.APIKey, opt.APIURL)
	return err
}

// collect walks the repo per FileSpec and buckets files by kind. Globs within a
// group may overlap, so paths are deduped; each bucket is sorted so every later
// phase iterates files in a stable order.
func collect(tree provider.FileTree, spec provider.FileSpec) map[provider.FileKind][]string {
	buckets := make(map[provider.FileKind][]string, len(spec.Groups))
	for _, g := range spec.Groups {
		seen := map[string]bool{}
		var paths []string
		for _, pattern := range g.Include {
			for _, rel := range tree.Glob(pattern) {
				if !seen[rel] {
					seen[rel] = true
					paths = append(paths, rel)
				}
			}
		}
		sort.Strings(paths)
		buckets[g.Kind] = paths
	}
	return buckets
}

// parse routes each bucket to its kind's parser. Iterates in FileSpec group
// order (never map order) so any parse error surfaces deterministically.
func parse(p provider.Provider, tree provider.FileTree, spec provider.FileSpec, buckets map[provider.FileKind][]string) (map[string]provider.ParsedFile, error) {
	parsers := p.Parsers()
	parsed := make(map[string]provider.ParsedFile)
	for _, g := range spec.Groups {
		parser, ok := parsers[g.Kind]
		if !ok {
			return nil, fmt.Errorf("pipeline: provider %q collects kind %d but has no parser for it", p.Name(), g.Kind)
		}
		for _, rel := range buckets[g.Kind] {
			src, err := tree.Read(rel)
			if err != nil {
				return nil, fmt.Errorf("pipeline: read %s: %w", rel, err)
			}
			pf, err := parser.Parse(rel, src)
			if err != nil {
				return nil, fmt.Errorf("pipeline: parse %s: %w", rel, err)
			}
			parsed[rel] = pf
		}
	}
	return parsed, nil
}

// index builds the shared cross-file Index by running the provider's indexers
// in order. Indexers own all cross-file/non-Java parsing (DESIGN §7).
func index(root string, tree provider.FileTree, p provider.Provider, parsed map[string]provider.ParsedFile, opt Options) (*provider.Index, *provider.IndexContext, error) {
	idx := &provider.Index{SchemaDepth: opt.SchemaDepth}
	ic := &provider.IndexContext{
		Root:        root,
		Files:       tree,
		Parsed:      parsed,
		Profiles:    opt.Profiles,
		Environment: opt.Environment,
		ConfigRepo:  opt.ConfigRepo,
		Shared:      collectSharedModules(root, p),
	}
	for _, ix := range p.Indexers() {
		if err := ix.Index(ic, idx); err != nil {
			return nil, nil, fmt.Errorf("pipeline: indexer %q: %w", ix.Name(), err)
		}
	}
	return idx, ic, nil
}

// detectEdges dispatches every detector's rules over the parsed files via the
// query engine — one traversal per file, files visited in sorted order for
// deterministic dispatch. The provider's value resolver (Java evaluator) is
// built from the Index and threaded to every handler via MatchContext.
func detectEdges(p provider.Provider, parsed map[string]provider.ParsedFile, idx *provider.Index, svc *model.Service) error {
	eng := query.New()
	dets := p.Detectors()
	res := p.NewResolver(idx)
	for _, path := range sortedKeys(parsed) {
		if err := eng.Run(parsed[path], dets, idx, res, svc); err != nil {
			return fmt.Errorf("pipeline: detect %s: %w", path, err)
		}
	}
	return nil
}

func sortedKeys(m map[string]provider.ParsedFile) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// collectSharedModules finds SIBLING modules of the scanned service that hold
// shared types, and parses their main Java sources so indexers can resolve
// shared DTOs and constants. Only types are read from them — detectors never see
// these files. Three repo layouts qualify a sibling:
//
//   - Maven reactor module (IMPROVEMENTS #6): ../pom.xml lists the service under
//     <modules> — every sibling module is shared;
//   - Maven GAV-matched library (IMPROVEMENTS #25): no aggregator pom — the
//     sibling is a standalone project whose groupId:artifactId the service's own
//     pom declares as a <dependency>;
//   - Gradle project dependency (IMPROVEMENTS #29): ../settings.gradle includes
//     the service, and the service's build.gradle depends on `project(':x')`.
//
// A qualifying Maven sibling that is ITSELF an aggregator (a nested module tree
// like common-lib/common-kafka, IMPROVEMENTS #27) is expanded one level into its
// listed <modules>. Returns nil when no layout applies.
func collectSharedModules(root string, p provider.Provider) map[string]provider.ParsedFile {
	javaParser, ok := p.Parsers()[provider.KindJava]
	if !ok {
		return nil
	}
	shared := map[string]provider.ParsedFile{}
	for _, dir := range append(mavenSharedDirs(root), gradleSharedDirs(root)...) {
		modTree := scan.NewOSFileTree(dir, nil)
		rootRel, _ := filepath.Rel(filepath.Dir(root), dir)
		for _, rel := range modTree.Glob("src/main/java/**/*.java") {
			src, err := modTree.Read(rel)
			if err != nil {
				continue
			}
			pf, err := javaParser.Parse(rel, src)
			if err != nil {
				continue
			}
			shared["../"+filepath.ToSlash(rootRel)+"/"+rel] = pf
		}
	}
	if len(shared) == 0 {
		return nil
	}
	return shared
}

// mavenSharedDirs lists the source directories of qualifying Maven siblings
// (reactor + GAV layouts), expanding nested aggregators one level (#27).
func mavenSharedDirs(root string) []string {
	parentDir := filepath.Dir(root)
	entries, err := os.ReadDir(parentDir)
	if err != nil {
		return nil
	}
	reactor := isReactorModule(root)
	deps := pomDependencies(filepath.Join(root, "pom.xml"))
	if !reactor && len(deps) == 0 {
		return nil
	}
	var dirs []string
	for _, e := range entries {
		if !e.IsDir() || e.Name() == filepath.Base(root) {
			continue
		}
		modDir := filepath.Join(parentDir, e.Name())
		pomPath := filepath.Join(modDir, "pom.xml")
		if _, err := os.Stat(pomPath); err != nil {
			continue // not a Maven project
		}
		dirs = append(dirs, sharedSourceDirs(reactor, deps, modDir, pomPath)...)
	}
	return dirs
}

// sharedSourceDirs qualifies one Maven sibling and returns its source dirs. A
// leaf sibling yields itself; an aggregator sibling (pom with <modules>) yields
// its listed submodule dirs — all of them under a reactor, only the GAV-matched
// ones otherwise (the service may depend on common-lib/common-kafka, not on the
// aggregator).
func sharedSourceDirs(reactor bool, deps []mavenGAV, modDir, pomPath string) []string {
	subs := pomModules(pomPath)
	if reactor {
		if len(subs) == 0 {
			return []string{modDir}
		}
		dirs := make([]string, 0, len(subs))
		for _, m := range subs {
			dirs = append(dirs, filepath.Join(modDir, filepath.FromSlash(m)))
		}
		return dirs
	}
	if len(subs) == 0 {
		if dependsOn(deps, pomPath) {
			return []string{modDir}
		}
		return nil
	}
	aggMatch := dependsOn(deps, pomPath)
	var dirs []string
	for _, m := range subs {
		subDir := filepath.Join(modDir, filepath.FromSlash(m))
		if aggMatch || dependsOn(deps, filepath.Join(subDir, "pom.xml")) {
			dirs = append(dirs, subDir)
		}
	}
	return dirs
}

// gradleProjectDepRe matches `project(':shared-lib')` / `project(":a:b")`
// dependency references in a build.gradle(.kts).
var gradleProjectDepRe = regexp.MustCompile(`project\(\s*['"]:?([^'")]+)['"]\s*\)`)

// gradleSharedDirs lists sibling module dirs a Gradle service depends on
// (IMPROVEMENTS #29): ../settings.gradle(.kts) must include the service, and
// each `project(':x')` reference in the service's build.gradle(.kts) names a
// shared module (`:a:b` maps to the a/b directory). Text-parse only — Gradle is
// never executed.
func gradleSharedDirs(root string) []string {
	parentDir := filepath.Dir(root)
	settings := readFirst(parentDir, "settings.gradle", "settings.gradle.kts")
	if settings == nil || !strings.Contains(string(settings), filepath.Base(root)) {
		return nil
	}
	build := readFirst(root, "build.gradle", "build.gradle.kts")
	if build == nil {
		return nil
	}
	var dirs []string
	seen := map[string]bool{}
	for _, m := range gradleProjectDepRe.FindAllStringSubmatch(string(build), -1) {
		mod := strings.ReplaceAll(m[1], ":", "/")
		if mod == "" || seen[mod] {
			continue
		}
		seen[mod] = true
		dir := filepath.Join(parentDir, filepath.FromSlash(mod))
		if dir == root {
			continue
		}
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

// readFirst returns the contents of the first existing file among names in dir.
func readFirst(dir string, names ...string) []byte {
	for _, n := range names {
		if b, err := os.ReadFile(filepath.Join(dir, n)); err == nil {
			return b
		}
	}
	return nil
}

// isReactorModule reports whether the service is listed as a <module> of an
// aggregator pom one directory up (the IMPROVEMENTS #6 layout).
func isReactorModule(root string) bool {
	parentPom, err := os.ReadFile(filepath.Join(root, "..", "pom.xml"))
	return err == nil && strings.Contains(string(parentPom), "<modules>") &&
		strings.Contains(string(parentPom), filepath.Base(root))
}

// pomModel is the minimal slice of a pom.xml we read: the project's own
// coordinates (groupId may be inherited from <parent>) and its direct
// dependencies. <dependencyManagement> and plugin dependencies nest deeper and
// are deliberately not matched.
type pomModel struct {
	GroupID    string `xml:"groupId"`
	ArtifactID string `xml:"artifactId"`
	Parent     struct {
		GroupID string `xml:"groupId"`
	} `xml:"parent"`
	Dependencies struct {
		Dependency []mavenGAV `xml:"dependency"`
	} `xml:"dependencies"`
	Modules struct {
		Module []string `xml:"module"`
	} `xml:"modules"`
}

type mavenGAV struct {
	GroupID    string `xml:"groupId"`
	ArtifactID string `xml:"artifactId"`
}

// pomDependencies returns the <dependencies> declared in a pom.xml, nil on any
// read/parse problem (shared-module discovery is best-effort, never fatal).
func pomDependencies(path string) []mavenGAV {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var pom pomModel
	if xml.Unmarshal(b, &pom) != nil {
		return nil
	}
	return pom.Dependencies.Dependency
}

// pomModules returns the <modules> listed in a pom.xml (empty for a leaf).
func pomModules(path string) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var pom pomModel
	if xml.Unmarshal(b, &pom) != nil {
		return nil
	}
	return pom.Modules.Module
}

// dependsOn reports whether a sibling project's own coordinates match one of the
// service's declared dependencies. artifactId must match exactly; groupId must
// match too unless either side is unresolvable statically (empty or a ${...}
// property), in which case the artifactId match decides.
func dependsOn(deps []mavenGAV, siblingPom string) bool {
	b, err := os.ReadFile(siblingPom)
	if err != nil {
		return false
	}
	var pom pomModel
	if xml.Unmarshal(b, &pom) != nil || pom.ArtifactID == "" {
		return false
	}
	group := pom.GroupID
	if group == "" {
		group = pom.Parent.GroupID
	}
	for _, d := range deps {
		if d.ArtifactID != pom.ArtifactID {
			continue
		}
		if d.GroupID == group || d.GroupID == "" || group == "" ||
			strings.Contains(d.GroupID, "${") || strings.Contains(group, "${") {
			return true
		}
	}
	return false
}

// collectConfigDeps surfaces the config keys touched during detection as
// config_dependencies (transparency). The resolver reports them via an optional
// interface; empty results leave the initialized [] untouched.
func collectConfigDeps(idx *provider.Index, svc *model.Service) {
	if idx.Config == nil {
		return
	}
	if r, ok := idx.Config.(interface {
		Dependencies() []model.ConfigDep
	}); ok {
		if deps := r.Dependencies(); len(deps) > 0 {
			svc.ConfigDependencies = deps
		}
	}
}

// schemaPass attaches request/response and topic schemas after endpoints and
// edges exist. Lands with the schema ladder.
func schemaPass(idx *provider.Index, svc *model.Service) error { return nil }

// submitGraph POSTs the full graph to the ingest API with the key, where the
// backend re-validates it (the robust gate). Skipped when --dry-run is set or no
// submit URL is configured. submit.Submit is a stub until PR 24.
func submitGraph(ctx context.Context, opt Options, svc *model.Service) error {
	if opt.DryRun || opt.APIURL == "" {
		return nil
	}
	body, err := Marshal(svc)
	if err != nil {
		return err
	}
	resp, err := submit.Submit(ctx, opt.APIURL, opt.APIKey, body, opt.CI)
	if err != nil {
		return err
	}
	if opt.OnSubmitResponse != nil {
		opt.OnSubmitResponse(resp)
	}
	return nil
}
