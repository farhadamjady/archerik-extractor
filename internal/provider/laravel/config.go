package laravel

import (
	"sort"
	"strings"

	"github.com/farhadamjady/archerik-extractor/internal/model"
	"github.com/farhadamjady/archerik-extractor/internal/provider"
	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/php"
)

// configIndexer builds the key→value view of a Laravel service's configuration
// and installs it as Index.Config, so `config('services.payment.url')` in a
// detector resolves to a value rather than staying an opaque call.
//
// Laravel's config is CODE, not a properties file: every `config/<name>.php`
// returns a nested array, and the dotted key is the file's basename followed by
// the path through that array — `config/services.php` returning
// `['payment' => ['url' => ...]]` answers `services.payment.url`. Nothing needs
// to run for this: the file is already parsed as PHP, and the array literal is
// walked.
//
// The values are usually not literals either. The convention the framework
// pushes is `env('PAYMENT_URL', 'http://localhost')`, which is why the two
// layers are built together here:
//
//	config('services.payment.url')  ->  config/services.php
//	                               ->  env('PAYMENT_URL', 'http://localhost')
//	                               ->  .env, else the in-code default
//
// A value that came from .env or from an in-code default is `likely`, not
// `confirmed`: it is one indirection away, and a deployed service may well be
// given a different one. Only the deploy layer knows for sure, and it is not a
// source this round reads.
type configIndexer struct{}

func (configIndexer) Name() string { return "laravel.config" }

func (configIndexer) Index(ic *provider.IndexContext, idx *provider.Index) error {
	cfg := &laravelConfig{
		values: map[string]string{},
		env:    map[string]string{},
	}
	for _, p := range envFilesByPrecedence(ic.Parsed) {
		rf := ic.Parsed[p].(*rawFile)
		parseDotenv(rf.Src(), cfg.env)
	}
	for _, p := range sortedPHPPaths(ic.Parsed) {
		name, ok := configFileKey(p)
		if !ok {
			continue
		}
		f := ic.Parsed[p].(*php.File)
		if ret := returnedArray(f); ret.Valid() {
			cfg.flatten(name, ret)
		}
	}
	if len(cfg.values) > 0 || len(cfg.env) > 0 {
		idx.Config = cfg
	}
	return nil
}

// configFileKey reports whether a path is a Laravel config file, and the dotted
// prefix its contents answer under. Only the `config/` directory counts —
// `config/services.php` is `services.*`, and a `config/` nested deeper in the
// repo is not the application's config.
func configFileKey(p string) (string, bool) {
	if !strings.HasPrefix(p, "config/") || !strings.HasSuffix(p, ".php") {
		return "", false
	}
	rest := strings.TrimSuffix(strings.TrimPrefix(p, "config/"), ".php")
	if rest == "" || strings.Contains(rest, "/") {
		return "", false
	}
	return rest, true
}

// returnedArray finds the array a config file returns. The file is a single
// top-level `return [...];`.
func returnedArray(f *php.File) php.Node {
	for _, stmt := range php.NamedChildren(f.Root()) {
		if stmt.Type() != "return_statement" {
			continue
		}
		for _, k := range php.NamedChildren(stmt) {
			if k.Type() == "array_creation_expression" {
				return k
			}
		}
	}
	return php.Node{}
}

// laravelConfig is the merged config view, implementing provider.ConfigResolver.
type laravelConfig struct {
	values map[string]string // dotted config key -> resolved value
	env    map[string]string // .env variable -> value
}

// flatten walks a config array into dotted keys under prefix. Nested arrays
// recurse; a leaf is resolved through the env layer.
func (c *laravelConfig) flatten(prefix string, arr php.Node) {
	for _, e := range php.ArrayEntries(arr) {
		if e.Key == "" {
			continue // a positional entry has no key to address it by
		}
		key := prefix + "." + e.Key
		if e.Value.Type() == "array_creation_expression" {
			c.flatten(key, e.Value)
			continue
		}
		if v, ok := php.Eval(e.Value, envScope{c}); ok {
			c.values[key] = v[0]
		}
	}
}

// envScope evaluates a config file's leaf expressions. It resolves `env(...)`
// and nothing else: a config value computed from a variable or a helper call is
// left unresolved rather than guessed at.
type envScope struct{ c *laravelConfig }

func (envScope) Var(string) ([]string, bool) { return nil, false }

func (s envScope) Call(call php.Node) ([]string, bool) {
	if php.CallName(call) != "env" {
		return nil, false
	}
	name, ok := php.StringLit(php.PositionalArg(call, 0))
	if !ok {
		return nil, false
	}
	if v, ok := s.c.env[name]; ok {
		return []string{v}, true
	}
	if def, ok := php.StringLit(php.PositionalArg(call, 1)); ok {
		return []string{def}, true
	}
	return nil, false
}

// Resolve answers a dotted config key (`services.payment.url`) or a bare .env
// variable name (`PAYMENT_URL`) — Laravel code reaches for both.
func (c *laravelConfig) Resolve(key string) (string, model.Confidence, string, bool) {
	if v, ok := c.values[key]; ok {
		return v, model.Likely, "config/" + configFileOf(key) + ".php", true
	}
	if v, ok := c.env[key]; ok {
		return v, model.Likely, ".env", true
	}
	return "", model.Uncertain, "", false
}

// Candidates returns the single resolution, or none. Laravel has one config tree
// per run; divergent per-environment overlays live in the deploy layer, which
// this round does not read — when it lands, this is where those candidates
// surface.
func (c *laravelConfig) Candidates(key string) []provider.ResolvedValue {
	v, conf, src, ok := c.Resolve(key)
	if !ok {
		return nil
	}
	return []provider.ResolvedValue{{Value: v, Conf: conf, Source: src}}
}

func configFileOf(key string) string {
	if i := strings.Index(key, "."); i >= 0 {
		return key[:i]
	}
	return key
}

// parseDotenv reads KEY=value lines. Quotes are stripped, `export` prefixes and
// comments ignored. A key already present is NOT overwritten, so the first file
// in sorted order wins deterministically.
func parseDotenv(src []byte, out map[string]string) {
	for _, line := range strings.Split(string(src), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
		if k == "" {
			continue
		}
		if _, taken := out[k]; !taken {
			out[k] = v
		}
	}
}

// envFilesByPrecedence returns the parsed env files strongest first, which is
// the order parseDotenv's first-wins rule needs.
//
// A real `.env` beats `.env.example`, and the distinction is not pedantic: a
// committed `.env.example` holds placeholders (`APP_URL=http://localhost`,
// blank secrets) that describe how to fill the file in, not what the service
// runs with. It is read at all because `.env` is normally gitignored, so the
// example is often the only environment the repo carries — but when both are
// present the real one is the answer. Ties below that break on path, so
// indexing stays deterministic.
func envFilesByPrecedence(parsed map[string]provider.ParsedFile) []string {
	var paths []string
	for p, pf := range parsed {
		if _, ok := pf.(*rawFile); ok {
			paths = append(paths, p)
		}
	}
	rank := func(p string) int {
		if strings.HasSuffix(p, ".example") || strings.HasSuffix(p, ".sample") {
			return 1
		}
		return 0
	}
	sort.SliceStable(paths, func(i, j int) bool {
		if ri, rj := rank(paths[i]), rank(paths[j]); ri != rj {
			return ri < rj
		}
		return paths[i] < paths[j]
	})
	return paths
}
