package spring

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/farhadamjady/service-discovery/internal/deployconfig"
	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/scan"
)

// maxDeployCandidates caps overlay fan-out per key (bounding, T4.5.8).
const maxDeployCandidates = 8

// maxPlaceholderDepth bounds recursive ${a}->${b} chains (D4). Real chains are
// 1-3 deep; 10 is a safety ceiling, not a target. Deeper → unresolved.
const maxPlaceholderDepth = 10

// placeholderRe matches one ${...} placeholder. The inner text has no braces, so
// nested placeholders inside a default (${a:${b}}) are not matched — a rare case
// left unresolved rather than mis-parsed.
var placeholderRe = regexp.MustCompile(`\$\{([^{}]*)\}`)

// configIndexer parses Spring config files (KindSpringConfig) into a
// profile-merged key store and installs it as Index.Config. Merge policy (D3):
// application.* is the base; application-<p>.* for each ACTIVE profile overrides
// it; profiles not active remain a lower-precedence fallback so their values are
// still resolvable. Active profiles come from --profiles (IndexContext.Profiles)
// or, absent that, spring.profiles.active in the base config.
//
// This builds the store only; ${...} resolution + confidence rules land in PR 9.
type configIndexer struct{}

func (configIndexer) Name() string { return "spring.config" }

func (configIndexer) Index(ic *provider.IndexContext, idx *provider.Index) error {
	cfg, err := buildConfig(ic)
	if err != nil {
		return err
	}
	idx.Config = cfg
	return nil
}

// configVal is a resolved value plus provenance (which file/profile it came
// from), used for the ResolvedValue candidates the detectors read.
type configVal struct {
	value   string
	source  string // file base name, e.g. "application-prod.yml"
	profile string // "" for the base profile, else the profile name
}

// springConfig implements provider.ConfigResolver over the merged store. Resolve
// takes an EXPRESSION (the raw attribute string, e.g. "${payment.url}" or
// "http://${host}:${port}/api") and expands every ${key} / ${key:default}
// against config, recursively. Config indirection is at most `likely` (B.4).
//
// It also records every key referenced during resolution, surfaced as
// config_dependencies for transparency.
type springConfig struct {
	values   map[string]configVal // base + active profiles (authoritative)
	fallback map[string]configVal // non-active profiles (lower precedence)

	// Deploy layer (Helm/K8s/.env), attached by the deploy indexer. Consulted
	// AFTER Spring config, matched by relaxed-bound key (§8.5). env is the
	// selected overlay (--environment); "" surfaces divergent overlays as candidates.
	deploy *deployconfig.Layer
	env    string

	mu         sync.Mutex
	referenced map[string]model.ConfigDep // keys queried during resolution
}

// setDeploy attaches the deployment-config layer and selected environment.
func (c *springConfig) setDeploy(l *deployconfig.Layer, env string) {
	c.deploy = l
	c.env = env
}

func (c *springConfig) lookup(key string) (configVal, bool) {
	if v, ok := c.values[key]; ok {
		return v, true
	}
	v, ok := c.fallback[key]
	return v, ok
}

// Resolve expands the placeholders in expr. Fully resolved → (value, likely,
// source). Any placeholder left unresolved (unknown key with no default,
// cycle, or over the depth cap) → (best-effort partial, uncertain, "", false).
func (c *springConfig) Resolve(expr string) (string, model.Confidence, string, bool) {
	out, ok := c.expand(expr, 0, nil)
	if !ok {
		return out, model.Uncertain, "", false
	}
	return out, model.Likely, c.recordedSource(expr), true
}

// recordedSource returns the provenance recorded for expr's key when expr is a
// lone ${key} placeholder (the common @Value("${x}") case) — read back from the
// referenced-key bookkeeping expand() just updated. A mixed expression (several
// keys, or a key alongside literal text) resolves to one string from several
// origins, so it stays blank rather than picking one misleadingly.
func (c *springConfig) recordedSource(expr string) string {
	key, _, _, ok := singlePlaceholder(expr)
	if !ok {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.referenced[key].ResolvedVia
}

// Candidates resolves expr to one or more values. A lone ${key} whose value
// diverges across env overlays (staging vs prod, no --environment selected)
// yields one candidate per distinct value — the detector emits one edge each.
// Spring config wins and is single-valued; mixed expressions collapse to the
// single best resolution.
func (c *springConfig) Candidates(expr string) []provider.ResolvedValue {
	if key, def, hasDef, ok := singlePlaceholder(expr); ok {
		if v, okc := c.lookup(key); okc {
			ev, _ := c.expand(v.value, 0, nil)
			return []provider.ResolvedValue{{Value: ev, Conf: model.Likely, Source: v.source, Origin: v.profile}}
		}
		if cands := c.deployCandidates(key); len(cands) > 0 {
			return cands
		}
		if hasDef {
			ev, _ := c.expand(def, 0, nil)
			return []provider.ResolvedValue{{Value: ev, Conf: model.Likely, Source: "default"}}
		}
		return nil
	}
	v, conf, src, ok := c.Resolve(expr)
	if !ok {
		return nil
	}
	return []provider.ResolvedValue{{Value: v, Conf: conf, Source: src}}
}

// deployLookup returns the single best deploy-layer value for a key (relaxed
// binding) plus the file it came from. With an environment selected, the
// matching overlay wins; else the base value, else the deterministically-first
// binding.
func (c *springConfig) deployLookup(key string) (value, source string, ok bool) {
	if c.deploy == nil {
		return "", "", false
	}
	bs := c.deploy.Get(deployconfig.NormalizeKey(key))
	if len(bs) == 0 {
		return "", "", false
	}
	v, src := c.pickBinding(bs)
	return v, src, true
}

func (c *springConfig) pickBinding(bs []deployconfig.Binding) (value, source string) {
	sorted := sortBindings(bs)
	if c.env != "" {
		for _, b := range sorted {
			if b.Overlay == c.env {
				return b.Value, b.Source
			}
		}
	}
	for _, b := range sorted {
		if b.Overlay == "" {
			return b.Value, b.Source
		}
	}
	return sorted[0].Value, sorted[0].Source
}

// deployCandidates returns the deploy-layer candidates for a key. With an
// environment selected it collapses to one; otherwise distinct overlay values
// become separate candidates (capped, deterministic).
func (c *springConfig) deployCandidates(key string) []provider.ResolvedValue {
	if c.deploy == nil {
		return nil
	}
	bs := c.deploy.Get(deployconfig.NormalizeKey(key))
	if len(bs) == 0 {
		return nil
	}
	if c.env != "" {
		v, src := c.pickBinding(bs)
		c.record(key, v, true, src)
		return []provider.ResolvedValue{{Value: v, Conf: model.Likely, Source: src, Origin: c.env}}
	}
	var out []provider.ResolvedValue
	seen := map[string]bool{}
	for _, b := range sortBindings(bs) {
		ev, _ := c.expand(b.Value, 0, nil)
		if seen[ev] {
			continue
		}
		seen[ev] = true
		out = append(out, provider.ResolvedValue{Value: ev, Conf: model.Likely, Source: b.Source, Origin: b.Overlay})
		if len(out) >= maxDeployCandidates {
			break
		}
	}
	if len(out) > 0 {
		c.record(key, out[0].Value, true, out[0].Source)
	}
	return out
}

// sortBindings orders by (overlay, value) for deterministic selection/output.
func sortBindings(bs []deployconfig.Binding) []deployconfig.Binding {
	out := append([]deployconfig.Binding(nil), bs...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Overlay != out[j].Overlay {
			return out[i].Overlay < out[j].Overlay
		}
		return out[i].Value < out[j].Value
	})
	return out
}

// singlePlaceholder reports whether expr is exactly one ${key} / ${key:default}
// with no surrounding literal, returning the key and default.
func singlePlaceholder(expr string) (key, def string, hasDef, ok bool) {
	if strings.HasPrefix(expr, "${") && strings.HasSuffix(expr, "}") {
		inner := expr[2 : len(expr)-1]
		if !strings.ContainsAny(inner, "${}") {
			k, d, hd := cutDefault(inner)
			return k, d, hd, k != ""
		}
	}
	return "", "", false, false
}

// Dependencies returns the config keys touched during resolution, sorted by key,
// with whether each resolved — the raw material behind config_dependencies.
func (c *springConfig) Dependencies() []model.ConfigDep {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]model.ConfigDep, 0, len(c.referenced))
	for _, d := range c.referenced {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// expand replaces ${key} / ${key:default} in s recursively. seen is the set of
// keys on the current resolution path (cycle guard); depth bounds chain length.
// Returns (result, fullyResolved).
func (c *springConfig) expand(s string, depth int, seen map[string]bool) (string, bool) {
	if !strings.Contains(s, "${") {
		return s, true
	}
	if depth > maxPlaceholderDepth {
		return s, false
	}
	resolved := true
	out := placeholderRe.ReplaceAllStringFunc(s, func(m string) string {
		key, def, hasDef := cutDefault(m[2 : len(m)-1]) // strip ${ }
		if key == "" || seen[key] {
			resolved = false // empty or cyclic reference — leave visible
			return m
		}
		if v, ok := c.lookup(key); ok {
			ev, ok2 := c.expand(v.value, depth+1, with(seen, key))
			resolved = resolved && ok2
			c.record(key, ev, ok2, v.source)
			return ev
		}
		if dv, src, ok := c.deployLookup(key); ok {
			ev, ok2 := c.expand(dv, depth+1, with(seen, key))
			resolved = resolved && ok2
			c.record(key, ev, ok2, src)
			return ev
		}
		if hasDef {
			ev, ok2 := c.expand(def, depth+1, with(seen, key))
			resolved = resolved && ok2
			c.record(key, ev, ok2, "default")
			return ev
		}
		resolved = false
		c.record(key, "", false, "")
		return m
	})
	return out, resolved
}

// record notes a referenced key and where its value came from. Config
// indirection resolves at `likely`; an unresolved reference is `uncertain`. A
// resolved record is not overwritten by a later unresolved one for the same key.
func (c *springConfig) record(key, value string, resolved bool, source string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if prev, ok := c.referenced[key]; ok && prev.Resolved && !resolved {
		return
	}
	conf := model.Uncertain
	if resolved {
		conf = model.Likely
	}
	c.referenced[key] = model.ConfigDep{Key: key, Value: value, Resolved: resolved, Confidence: conf, ResolvedVia: source}
}

// cutDefault splits ${key:default}; the first ':' separates, so a default may
// itself contain colons (jdbc:postgresql://h).
func cutDefault(inner string) (key, def string, hasDef bool) {
	if k, d, ok := strings.Cut(inner, ":"); ok {
		return strings.TrimSpace(k), d, true
	}
	return strings.TrimSpace(inner), "", false
}

// with returns seen plus key (a copy, so sibling branches don't share a path).
func with(seen map[string]bool, key string) map[string]bool {
	m := make(map[string]bool, len(seen)+1)
	for k := range seen {
		m[k] = true
	}
	m[key] = true
	return m
}

func buildConfig(ic *provider.IndexContext) (*springConfig, error) {
	// Group flattened keys by profile, iterating files in sorted order so a
	// later file overriding an earlier one within a profile is deterministic.
	layers := map[string]map[string]configVal{} // profile ("" = base) -> keys
	for _, p := range sortedSpringConfigPaths(ic.Parsed) {
		rf := ic.Parsed[p].(*rawFile)
		flat, err := parseConfig(p, rf.Src())
		if err != nil {
			return nil, fmt.Errorf("config %s: %w", p, err)
		}
		prof := profileFromFilename(p)
		if layers[prof] == nil {
			layers[prof] = map[string]configVal{}
		}
		src := path.Base(p)
		for k, v := range flat {
			layers[prof][k] = configVal{value: v, source: src, profile: prof}
		}
	}

	active := ic.Profiles
	if len(active) == 0 {
		if base := layers[""]; base != nil {
			if v, ok := base["spring.profiles.active"]; ok {
				active = splitList(v.value)
			}
		}
	}

	// Authoritative store: base overlaid with each active profile, in order.
	values := map[string]configVal{}
	for k, v := range layers[""] {
		values[k] = v
	}
	for _, prof := range active {
		for k, v := range layers[prof] {
			values[k] = v
		}
	}

	// Fallback: non-active profiles, deterministic (sorted profile name), for
	// keys absent from the authoritative store; first sorted profile wins.
	fallback := map[string]configVal{}
	for _, prof := range sortedProfiles(layers) {
		if prof == "" || contains(active, prof) {
			continue
		}
		for k, v := range layers[prof] {
			if _, ok := values[k]; ok {
				continue
			}
			if _, ok := fallback[k]; !ok {
				fallback[k] = v
			}
		}
	}

	// External config repo (Spring Cloud Config Server checkout, IMPROVEMENTS
	// #16): its yml/properties feed the FALLBACK layer — in-repo config always
	// wins; the config repo fills what the service does not carry itself.
	if ic.ConfigRepo != "" {
		addConfigRepo(ic.ConfigRepo, values, fallback)
	}

	return &springConfig{
		values:     values,
		fallback:   fallback,
		referenced: map[string]model.ConfigDep{},
	}, nil
}

// addConfigRepo flattens every yml/properties file in the config repo into the
// fallback map (never overriding existing keys). Files are visited in sorted
// order, so the first file to define a key wins deterministically.
func addConfigRepo(dir string, values, fallback map[string]configVal) {
	tree := scan.NewOSFileTree(dir, []string{"**/.git/**"})
	var files []string
	for _, pattern := range []string{"**/*.yml", "**/*.yaml", "**/*.properties"} {
		files = append(files, tree.Glob(pattern)...)
	}
	sort.Strings(files)
	for _, rel := range files {
		src, err := tree.Read(rel)
		if err != nil {
			continue
		}
		flat, err := parseConfig(rel, src)
		if err != nil {
			continue // a broken file in the config repo never fails the scan
		}
		for k, v := range flat {
			if _, ok := values[k]; ok {
				continue
			}
			if _, ok := fallback[k]; ok {
				continue
			}
			fallback[k] = configVal{value: v, source: "config-repo/" + rel}
		}
	}
}

// sortedSpringConfigPaths returns the KindSpringConfig file paths, sorted.
func sortedSpringConfigPaths(parsed map[string]provider.ParsedFile) []string {
	var paths []string
	for p, pf := range parsed {
		if rf, ok := pf.(*rawFile); ok && rf.Kind() == provider.KindSpringConfig {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	return paths
}

func sortedProfiles(layers map[string]map[string]configVal) []string {
	profs := make([]string, 0, len(layers))
	for p := range layers {
		profs = append(profs, p)
	}
	sort.Strings(profs)
	return profs
}

// profileFromFilename extracts the profile from an application[-<profile>].<ext>
// file name: "application.yml" -> "", "application-prod.yml" -> "prod".
func profileFromFilename(p string) string {
	name := strings.TrimSuffix(path.Base(p), path.Ext(p))
	const base = "application"
	if strings.HasPrefix(name, base+"-") {
		return name[len(base)+1:]
	}
	return ""
}

// splitList splits a comma-separated profile list, trimming and dropping blanks.
func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
