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
	"fmt"
	"os"
	"path/filepath"
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
	APIKey      string
	ConfigFile  string
	Profiles    []string    // active Spring profiles (D3)
	Environment string      // deploy overlay selection, e.g. "staging" (E3)
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
	svc := model.NewService(name, name, "")

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
	idx := &provider.Index{}
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

// collectSharedModules finds SIBLING Maven modules of the scanned service
// (a multi-module repo: ../pom.xml lists the service as a <module>) and parses
// their main Java sources, so indexers can resolve shared DTOs and constants
// that live in a common module (IMPROVEMENTS #6). Only types are read from
// them — detectors never see these files. Returns nil when the service is not
// part of a multi-module build.
func collectSharedModules(root string, p provider.Provider) map[string]provider.ParsedFile {
	parentPom, err := os.ReadFile(filepath.Join(root, "..", "pom.xml"))
	if err != nil || !strings.Contains(string(parentPom), "<modules>") ||
		!strings.Contains(string(parentPom), filepath.Base(root)) {
		return nil
	}
	javaParser, ok := p.Parsers()[provider.KindJava]
	if !ok {
		return nil
	}

	parentDir := filepath.Dir(root)
	entries, err := os.ReadDir(parentDir)
	if err != nil {
		return nil
	}
	shared := map[string]provider.ParsedFile{}
	for _, e := range entries {
		if !e.IsDir() || e.Name() == filepath.Base(root) {
			continue
		}
		modDir := filepath.Join(parentDir, e.Name())
		if _, err := os.Stat(filepath.Join(modDir, "pom.xml")); err != nil {
			continue // not a module
		}
		modTree := scan.NewOSFileTree(modDir, nil)
		for _, rel := range modTree.Glob("src/main/java/**/*.java") {
			src, err := modTree.Read(rel)
			if err != nil {
				continue
			}
			pf, err := javaParser.Parse(rel, src)
			if err != nil {
				continue
			}
			shared["../"+e.Name()+"/"+rel] = pf
		}
	}
	if len(shared) == 0 {
		return nil
	}
	return shared
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
