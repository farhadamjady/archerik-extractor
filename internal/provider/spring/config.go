package spring

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
)

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

// springConfig implements provider.ConfigResolver over the merged store. In
// PR 8 Resolve is a direct lookup returning the raw (possibly ${...}-bearing)
// value; recursive resolution, defaults, and cycle handling arrive in PR 9.
type springConfig struct {
	values   map[string]configVal // base + active profiles (authoritative)
	fallback map[string]configVal // non-active profiles (lower precedence)
}

func (c *springConfig) lookup(key string) (configVal, bool) {
	if v, ok := c.values[key]; ok {
		return v, true
	}
	v, ok := c.fallback[key]
	return v, ok
}

// Resolve returns the value bound to a placeholder key. Config indirection is at
// most `likely` confidence (B.4). Not found → ok=false.
func (c *springConfig) Resolve(key string) (string, model.Confidence, bool) {
	if v, ok := c.lookup(key); ok {
		return v.value, model.Likely, true
	}
	return "", "", false
}

// Candidates returns the single resolved value with provenance. Divergent
// candidates (env overlays) come from the deploy layer in PR 11.
func (c *springConfig) Candidates(key string) []provider.ResolvedValue {
	if v, ok := c.lookup(key); ok {
		return []provider.ResolvedValue{{
			Value:  v.value,
			Conf:   model.Likely,
			Source: v.source,
			Origin: v.profile,
		}}
	}
	return nil
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

	return &springConfig{values: values, fallback: fallback}, nil
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
