package schema

import "github.com/farhadamjady/service-discovery/internal/model"

// SchemaSources resolves a payload type name to a schema parsed from a contract
// file in the repo (Avro/Proto/JSON Schema) — the query surface of the Kafka
// schema-file index. Implemented by Sources.
type SchemaSources interface {
	Lookup(name string) (*model.Schema, bool)
}

// Sources is an in-memory SchemaSources, keyed by type name (and file basename).
type Sources struct {
	byName map[string]*model.Schema
}

func NewSources() *Sources { return &Sources{byName: map[string]*model.Schema{}} }

// Add registers a schema under a name (record name, message name, or filename).
// The first non-empty registration for a name wins, for determinism.
func (s *Sources) Add(name string, sch *model.Schema) {
	if name == "" || sch == nil {
		return
	}
	if _, ok := s.byName[name]; !ok {
		s.byName[name] = sch
	}
}

// Lookup resolves a type name (simple or qualified) to its contract schema.
func (s *Sources) Lookup(name string) (*model.Schema, bool) {
	sch, ok := s.byName[simpleName(name)]
	return sch, ok
}
