package quarkus

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/farhadamjady/archerik-extractor/internal/model"
	"github.com/farhadamjady/archerik-extractor/internal/provider"
	"github.com/farhadamjady/archerik-extractor/internal/schema/registry"
)

// configIndexer builds a FLAT key→value view of the Quarkus config files
// (application.properties / application.yaml) and installs it as Index.Config.
// It is intentionally lightweight — a single-layer store with ${x[:default]}
// expansion — not the full Spring layered/deploy resolver: Quarkus messaging
// (channel→topic) and rest-client URLs read a handful of literal keys, which
// this covers. Profile-specific keys (`%dev.`, `%test.`, `%prod.`) are dropped so
// the base (production) values win; a real profile-select flag can refine later.
type configIndexer struct{}

func (configIndexer) Name() string { return "quarkus.config" }

func (configIndexer) Index(ic *provider.IndexContext, idx *provider.Index) error {
	cfg := &flatConfig{values: map[string]string{}}
	for _, p := range sortedConfigPaths(ic.Parsed) {
		src := ic.Parsed[p].(*rawFile).Src()
		if strings.HasSuffix(p, ".properties") {
			parseProperties(src, cfg.values)
		} else {
			parseYAML(src, cfg.values)
		}
	}
	if len(cfg.values) > 0 {
		idx.Config = cfg
	}
	if rc := registry.Parse(cfg.values); rc.Configured() {
		idx.Registry = &rc
	}
	return nil
}

// flatConfig is a single-layer key store implementing provider.ConfigResolver.
type flatConfig struct{ values map[string]string }

// Resolve accepts either a bare key ("mp.messaging.outgoing.x.topic") or a
// ${...} placeholder, and expands nested ${...} references one level deep.
// Source is "application" for a key found in the merged config, "default" for
// a ${x:default} fallback, "" when unresolved.
func (c *flatConfig) Resolve(expr string) (string, model.Confidence, string, bool) {
	key := expr
	if strings.HasPrefix(expr, "${") && strings.HasSuffix(expr, "}") {
		key = expr[2 : len(expr)-1]
	}
	key, def, hasDef := cutDefault(key)
	if v, ok := c.values[key]; ok {
		return c.expand(v), model.Likely, "application", true
	}
	if hasDef {
		return c.expand(def), model.Likely, "default", true
	}
	return "", model.Uncertain, "", false
}

func (c *flatConfig) Candidates(expr string) []provider.ResolvedValue {
	if v, conf, src, ok := c.Resolve(expr); ok {
		return []provider.ResolvedValue{{Value: v, Conf: conf, Source: src}}
	}
	return nil
}

// Lookup is the direct key accessor the messaging detector uses.
func (c *flatConfig) Lookup(key string) (string, bool) {
	v, ok := c.values[key]
	return v, ok
}

// expand resolves ${x[:default]} references inside a value, one level deep.
func (c *flatConfig) expand(v string) string {
	for {
		i := strings.Index(v, "${")
		if i < 0 {
			return v
		}
		j := strings.Index(v[i:], "}")
		if j < 0 {
			return v
		}
		inner := v[i+2 : i+j]
		key, def, _ := cutDefault(inner)
		rep := def
		if val, ok := c.values[key]; ok {
			rep = val
		}
		v = v[:i] + rep + v[i+j+1:]
	}
}

func cutDefault(inner string) (key, def string, hasDef bool) {
	if k, d, ok := strings.Cut(inner, ":"); ok {
		return k, d, true
	}
	return inner, "", false
}

// parseProperties reads `key=value` / `key:value` lines, skipping comments and
// Quarkus profile-scoped keys (`%profile.key=...`).
func parseProperties(src []byte, out map[string]string) {
	for _, line := range strings.Split(string(src), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
			continue
		}
		if strings.HasPrefix(line, "%") {
			continue // profile-scoped override (%dev./%test./%prod.)
		}
		k, v, ok := cutKV(line)
		if !ok {
			continue
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
}

// cutKV splits a properties line at the first '=' or ':'.
func cutKV(line string) (k, v string, ok bool) {
	eq := strings.IndexByte(line, '=')
	co := strings.IndexByte(line, ':')
	switch {
	case eq < 0 && co < 0:
		return "", "", false
	case eq < 0:
		return line[:co], line[co+1:], true
	case co < 0:
		return line[:eq], line[eq+1:], true
	case eq < co:
		return line[:eq], line[eq+1:], true
	default:
		return line[:co], line[co+1:], true
	}
}

// parseYAML flattens a YAML document into dotted keys, dropping profile-scoped
// top-level keys (`"%dev"`) so base values win.
func parseYAML(src []byte, out map[string]string) {
	var root map[string]any
	if err := yaml.Unmarshal(src, &root); err != nil {
		return
	}
	flatten("", root, out)
}

func flatten(prefix string, node any, out map[string]string) {
	switch n := node.(type) {
	case map[string]any:
		for k, v := range n {
			if strings.HasPrefix(k, "%") {
				continue // profile-scoped subtree
			}
			flatten(join(prefix, k), v, out)
		}
	case map[any]any:
		for k, v := range n {
			ks := toStr(k)
			if strings.HasPrefix(ks, "%") {
				continue
			}
			flatten(join(prefix, ks), v, out)
		}
	default:
		if prefix != "" && node != nil {
			out[prefix] = toStr(node)
		}
	}
}

func join(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

func toStr(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	default:
		return strings.TrimSpace(fmt.Sprint(t))
	}
}

func sortedConfigPaths(parsed map[string]provider.ParsedFile) []string {
	var out []string
	for p, pf := range parsed {
		if rf, ok := pf.(*rawFile); ok && rf.Kind() == provider.KindSpringConfig {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}
