package spring

import (
	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/java"
)

// restTemplateDetector extracts outbound HTTP dependencies from RestTemplate call
// sites. The URL is always the first argument; it is handed to the value
// evaluator, so literals, concatenations, constants, @Value fields, and
// conditionals all resolve (one edge per candidate).
//
// Matched by method name (in the handler): the distinctive RestTemplate methods.
// Bare put/delete/execute are omitted for now — they collide with Map/collection
// methods and need receiver-type info (the TypeIndex) to disambiguate.
type restTemplateDetector struct{}

func (restTemplateDetector) Name() string             { return "spring.resttemplate" }
func (restTemplateDetector) Protocol() model.Protocol { return model.ProtoREST }

var restTemplateMethods = map[string]bool{
	"getForObject":    true,
	"getForEntity":    true,
	"postForObject":   true,
	"postForEntity":   true,
	"patchForObject":  true,
	"headForHeaders":  true,
	"optionsForAllow": true,
	"exchange":        true,
}

// restCallQuery matches any invocation with an argument list; the handler filters
// by method name and reads the first argument as the URL.
const restCallQuery = `(method_invocation
  name: (identifier) @name
  arguments: (argument_list) @args
)`

func (d restTemplateDetector) Rules() []provider.Rule {
	return []provider.Rule{{Query: restCallQuery, OnMatch: d.onCall}}
}

func (restTemplateDetector) onCall(mc *provider.MatchContext) {
	name, _ := mc.Captures["name"].(java.Node)
	if !restTemplateMethods[name.Text()] {
		return
	}
	args, ok := mc.Captures["args"].(java.Node)
	if !ok {
		return
	}
	url := args.NamedChild(0)
	if !url.Valid() {
		return
	}
	emitTargets(mc, url, model.DetectRestTemplate, model.ProtoREST)
}
