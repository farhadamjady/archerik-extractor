package spring

import (
	"fmt"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/java"
	"github.com/farhadamjady/service-discovery/internal/resolve"
)

// webClientDetector extracts outbound HTTP dependencies from WebClient, whose
// target is spread across a fluent chain:
//
//		WebClient.create("http://payment").get().uri("/pay/{id}")
//		WebClient.builder().baseUrl("http://payment").build() ... .uri("/pay")
//
//	  - baseUrl(x) / WebClient.create(x): the base is emitted as an edge (also
//	    catches clients stored in a field and used elsewhere).
//	  - uri(x): composed with a base found earlier in the SAME chain; a bare path
//	    with no in-chain base is skipped (it belongs to a base captured
//	    separately), a full-URL uri is emitted on its own.
//
// A same-chain create+uri therefore yields both the base and the composed edge —
// redundant but honest; the backend maps both to the one service.
type webClientDetector struct{}

func (webClientDetector) Name() string             { return "spring.webclient" }
func (webClientDetector) Protocol() model.Protocol { return model.ProtoREST }

const webClientQuery = `(method_invocation
  name: (identifier) @name
  arguments: (argument_list) @args
) @call`

func (d webClientDetector) Rules() []provider.Rule {
	return []provider.Rule{{Query: webClientQuery, OnMatch: d.onCall}}
}

func (webClientDetector) onCall(mc *provider.MatchContext) {
	name, _ := mc.Captures["name"].(java.Node)
	args, _ := mc.Captures["args"].(java.Node)
	call, _ := mc.Captures["call"].(java.Node)
	arg0 := args.NamedChild(0)

	switch name.Text() {
	case "baseUrl":
		if arg0.Valid() {
			emitTargets(mc, arg0, model.DetectWebClient, model.ProtoREST)
		}
	case "create":
		// WebClient.create(baseUrl) — the receiver must be WebClient.
		if arg0.Valid() && call.ChildByFieldName("object").Text() == "WebClient" {
			emitTargets(mc, arg0, model.DetectWebClient, model.ProtoREST)
		}
	case "uri":
		if arg0.Valid() {
			emitURI(mc, call, arg0)
		}
	}
}

// emitURI resolves a .uri() target, composing an in-chain base if present.
func emitURI(mc *provider.MatchContext, call, uriArg java.Node) {
	group := fmt.Sprintf("%s:%d:webclient", mc.File.Path(), uriArg.StartByte())
	uri := resolveNode(mc, uriArg)

	if base := chainBaseArg(call); base.Valid() {
		composed := resolve.Concat(resolveNode(mc, base), uri)
		emitValueSet(mc, composed, model.DetectWebClient, model.ProtoREST, group)
		return
	}
	// No base in this chain:
	//  - a full URL stands alone -> emit;
	//  - a KNOWN bare path (/pay) is relative to a base captured elsewhere -> skip;
	//  - an unknown PREFIX (template starting with a hole, or fully unknown) may
	//    hide the host — dropping it would lose the edge silently, which breaks
	//    the honesty rule. Emit an uncertain edge instead.
	switch {
	case looksLikeURL(uri):
		emitValueSet(mc, uri, model.DetectWebClient, model.ProtoREST, group)
	case uri.Kind == resolve.Template && len(uri.Segments) > 0 && uri.Segments[0].Hole:
		emitValueSet(mc, uri, model.DetectWebClient, model.ProtoREST, group)
	case uri.Kind == resolve.Unknown:
		emitValueSet(mc, uri, model.DetectWebClient, model.ProtoREST, group)
	}
}

// chainBaseArg walks up the receiver chain from a call to the argument of a
// baseUrl(...) or WebClient.create(...), or an invalid node if none.
func chainBaseArg(call java.Node) java.Node {
	for obj := call.ChildByFieldName("object"); obj.Valid() && obj.Type() == "method_invocation"; obj = obj.ChildByFieldName("object") {
		switch obj.ChildByFieldName("name").Text() {
		case "baseUrl":
			return firstArg(obj)
		case "create":
			if obj.ChildByFieldName("object").Text() == "WebClient" {
				return firstArg(obj)
			}
		}
	}
	return java.Node{}
}

func firstArg(invocation java.Node) java.Node {
	return invocation.ChildByFieldName("arguments").NamedChild(0)
}
