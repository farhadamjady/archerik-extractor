package quarkus

import (
	"strings"

	"github.com/farhadamjady/archerik-extractor/internal/model"
	"github.com/farhadamjady/archerik-extractor/internal/provider"
	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/java"
)

// jaxrsClientDetector extracts outbound HTTP dependencies from the JAX-RS
// programmatic client (RESTEasy Reactive / MicroProfile), where the target is
// built fluently rather than declared:
//
//	ClientBuilder.newClient().register(...).target(baseUrl).path("api/villains/")
//
// The `.target(...)` argument is the base URL and anchors ONE edge per client.
// The receiver chain must root at `ClientBuilder.newClient()` (or `.newBuilder()`)
// so an unrelated `.target(...)` on some other object never inflates the graph.
// A literal absolute URL resolves to its authority (confirmed); a config-driven
// or otherwise non-literal base (e.g. fightConfig.villain().clientBaseUrl()) can't
// be resolved statically this round, so the enclosing bean's simple name is
// emitted as the raw logical target (uncertain) — honest and named, never an
// anonymous empty edge (the name-resolution and honesty rules). A base held in a field and target()'d in
// a separate statement (no in-chain newClient()) needs dataflow and is a later
// round.
type jaxrsClientDetector struct{}

func (jaxrsClientDetector) Name() string             { return "quarkus.jaxrs-client" }
func (jaxrsClientDetector) Protocol() model.Protocol { return model.ProtoREST }

const jaxrsTargetQuery = `(method_invocation
  name: (identifier) @name
  (#eq? @name "target")
  arguments: (argument_list) @args
) @call`

func (d jaxrsClientDetector) Rules() []provider.Rule {
	return []provider.Rule{{Query: jaxrsTargetQuery, OnMatch: d.onTarget}}
}

func (jaxrsClientDetector) onTarget(mc *provider.MatchContext) {
	call, _ := mc.Captures["call"].(java.Node)
	args, _ := mc.Captures["args"].(java.Node)
	if !call.Valid() || !rootsAtClientBuilder(call) {
		return
	}
	dep := model.Dependency{Protocol: model.ProtoREST, Detection: model.DetectJaxrsClient}

	switch arg0 := args.NamedChild(0); {
	case arg0.Valid() && arg0.Type() == "string_literal":
		url := java.Unquote(arg0.Text())
		if host := authority(url); host != "" {
			dep.TargetName, dep.URL, dep.Resolved, dep.Confidence = host, url, true, model.Confirmed
		} else {
			dep.URL, dep.Confidence = url, model.Uncertain
		}
	default:
		// Non-literal base (config accessor / variable / builder): the URL can't
		// be resolved statically. Fall back to the enclosing bean's name as the
		// raw logical target so the edge is named, not anonymous.
		if bean := enclosingTypeName(call); bean != "" {
			dep.TargetName = bean
		}
		dep.Confidence = model.Uncertain
	}
	mc.Out.OutboundDependencies = append(mc.Out.OutboundDependencies, dep)
}

// rootsAtClientBuilder reports whether a `.target(...)` call's receiver chain
// roots at `ClientBuilder.newClient()` / `ClientBuilder.newBuilder()` — the JAX-RS
// client entry point — so a `.target(...)` on any other object is ignored.
func rootsAtClientBuilder(call java.Node) bool {
	for obj := call.ChildByFieldName("object"); obj.Valid() && obj.Type() == "method_invocation"; obj = obj.ChildByFieldName("object") {
		switch obj.ChildByFieldName("name").Text() {
		case "newClient", "newBuilder":
			recv := obj.ChildByFieldName("object").Text()
			if recv == "ClientBuilder" || strings.HasSuffix(recv, ".ClientBuilder") {
				return true
			}
		}
	}
	return false
}
