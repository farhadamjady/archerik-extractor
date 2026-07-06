package spring

import "github.com/farhadamjady/service-discovery/internal/provider"

// BuildConfig parses Spring configuration (application.yml/.yaml/.properties and
// profile variants) into a placeholder resolver. Detectors resolve ${...} through
// it before emitting edges — the single biggest source of missed dependencies.
//
// TODO(config): parse YAML/properties, flatten keys, resolve nested/chained
// placeholders. Skeleton returns an empty resolver so nothing resolves yet.
func (*Provider) BuildConfig(fs provider.FileTree) (provider.Config, error) {
	return springConfig{values: map[string]string{}}, nil
}

// springConfig is a flat key -> value map with ${...} resolution.
type springConfig struct {
	values map[string]string
}

// Resolve looks up a placeholder key (the inside of ${...}) and returns its value.
func (c springConfig) Resolve(placeholder string) (string, bool) {
	v, ok := c.values[placeholder]
	return v, ok
}
