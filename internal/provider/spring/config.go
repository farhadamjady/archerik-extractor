package spring

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
)

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

	mu         sync.Mutex
	referenced map[string]model.ConfigDep // keys queried during resolution
}

func (c *springConfig) lookup(key string) (configVal, bool) {
	if v, ok := c.values[key]; ok {
		return v, true
	}
	v, ok := c.fallback[key]
	return v, ok
}

// Resolve expands the placeholders in expr. Fully resolved → (value, likely).
// Any placeholder left unresolved (unknown key with no default, cycle, or over
// the depth cap) → (best-effort partial, uncertain, false).
func (c *springConfig) Resolve(expr string) (string, model.Confidence, bool) {
	out, ok := c.expand(expr, 0, nil)
	if !ok {
		return out, model.Uncertain, false
	}
	return out, model.Likely, true
}

// Candidates returns the single resolved value with provenance (the source file
// and profile when expr is a lone ${key}). Divergent candidates from env
// overlays arrive with the deploy layer in PR 11.
func (c *springConfig) Candidates(expr string) []provider.ResolvedValue {
	v, conf, ok := c.Resolve(expr)
	if !ok {
		return nil
	}
	src, origin := c.provenance(expr)
	return []provider.ResolvedValue{{Value: v, Conf: conf, Source: src, Origin: origin}}
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
			c.record(key, ev, ok2)
			return ev
		}
		if hasDef {
			ev, ok2 := c.expand(def, depth+1, with(seen, key))
			resolved = resolved && ok2
			c.record(key, ev, ok2)
			return ev
		}
		resolved = false
		c.record(key, "", false)
		return m
	})
	return out, resolved
}

// provenance returns the source file + profile when expr is a lone ${key}.
func (c *springConfig) provenance(expr string) (source, profile string) {
	if strings.HasPrefix(expr, "${") && strings.HasSuffix(expr, "}") {
		inner := expr[2 : len(expr)-1]
		if !strings.Contains(inner, "${") {
			key, _, _ := cutDefault(inner)
			if v, ok := c.lookup(key); ok {
				return v.source, v.profile
			}
		}
	}
	return "", ""
}

// record notes a referenced key. Config indirection resolves at `likely`; an
// unresolved reference is `uncertain`. A resolved record is not overwritten by a
// later unresolved one for the same key.
func (c *springConfig) record(key, value string, resolved bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if prev, ok := c.referenced[key]; ok && prev.Resolved && !resolved {
		return
	}
	conf := model.Uncertain
	if resolved {
		conf = model.Likely
	}
	c.referenced[key] = model.ConfigDep{Key: key, Value: value, Resolved: resolved, Confidence: conf}
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

	return &springConfig{
		values:     values,
		fallback:   fallback,
		referenced: map[string]model.ConfigDep{},
	}, nil
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
