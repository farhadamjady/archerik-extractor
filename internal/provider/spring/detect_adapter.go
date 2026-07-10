package spring

import (
	"encoding/json"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/java"
)

// adapterIndexer loads company wrapper declarations from .ekg-adapters.json at
// the service root (IMPROVEMENTS #15). Absent file = no adapters, no error.
type adapterIndexer struct{}

func (adapterIndexer) Name() string { return "spring.adapters" }

func (adapterIndexer) Index(ic *provider.IndexContext, idx *provider.Index) error {
	b, err := ic.Files.Read(".ekg-adapters.json")
	if err != nil {
		return nil // optional file
	}
	var specs []provider.AdapterSpec
	if err := json.Unmarshal(b, &specs); err != nil {
		return nil // a broken adapters file never fails the scan
	}
	for i := range specs {
		if specs[i].Protocol == "" {
			specs[i].Protocol = model.ProtoREST
		}
	}
	idx.Adapters = specs
	return nil
}

// adapterDetector emits an outbound edge for every declared wrapper call:
// the target argument goes through the value resolver, so literals, constants,
// config values, and conditionals all work.
type adapterDetector struct{}

func (adapterDetector) Name() string             { return "spring.adapter" }
func (adapterDetector) Protocol() model.Protocol { return model.ProtoREST }

const adapterQuery = `(method_invocation
  name: (identifier) @name
  arguments: (argument_list) @args
)`

func (d adapterDetector) Rules() []provider.Rule {
	return []provider.Rule{{Query: adapterQuery, OnMatch: d.onCall}}
}

func (adapterDetector) onCall(mc *provider.MatchContext) {
	if len(mc.Index.Adapters) == 0 {
		return
	}
	name, _ := mc.Captures["name"].(java.Node)
	args, _ := mc.Captures["args"].(java.Node)
	for _, spec := range mc.Index.Adapters {
		if spec.Method != name.Text() {
			continue
		}
		if arg := args.NamedChild(spec.TargetArg); arg.Valid() {
			emitTargets(mc, arg, model.DetectAdapter, spec.Protocol)
		}
		return
	}
}
