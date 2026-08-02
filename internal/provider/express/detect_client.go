package express

import (
	"strings"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/tsjs"
)

// clientDetector extracts outbound HTTP dependencies from Express/Node call
// sites: `axios.get/post/...(url, ...)`, `axios(url)`, `fetch(url, ...)`, and
// `got(url, ...)` / `got.get(url, ...)`. A literal absolute URL — directly or via
// a local variable bound to a literal (one hop of const propagation, see
// tsjs.StringArgValue) — resolves to its authority (host[:port]) as target_name;
// a literal relative path or a dynamic expression (template / builder /
// unresolved variable) emits an honest anonymous uncertain edge — never dropped
// (CLAUDE.md honesty rule). A value evaluator that resolves a base-URL variable
// through config is a later round.
type clientDetector struct{}

func (clientDetector) Name() string             { return "express.client" }
func (clientDetector) Protocol() model.Protocol { return model.ProtoREST }

const (
	memberCallQuery = `(call_expression
  function: (member_expression
    object: (_) @obj
    property: (property_identifier) @method)
  arguments: (arguments) @args) @call`

	identCallQuery = `(call_expression
  function: (identifier) @fn
  arguments: (arguments) @args) @call`
)

func (d clientDetector) Rules() []provider.Rule {
	return []provider.Rule{
		{Query: memberCallQuery, OnMatch: d.onMemberCall},
		{Query: identCallQuery, OnMatch: d.onIdentCall},
	}
}

// httpVerbMethod is the set of client methods that carry a URL first argument.
var httpVerbMethod = map[string]bool{
	"get": true, "post": true, "put": true, "delete": true,
	"patch": true, "head": true, "request": true,
}

// clientIdent is the set of bare identifiers whose member calls (`axios.get`,
// `got.post`) are HTTP clients. Kept narrow so unrelated `.get(...)` calls
// (maps, query builders) don't inflate the graph.
var clientIdent = map[string]bool{"axios": true, "got": true}

// onMemberCall handles `<client>.<verb>(url, ...)` where client is a recognized
// HTTP client identifier and verb carries a URL first argument.
func (clientDetector) onMemberCall(mc *provider.MatchContext) {
	obj, _ := mc.Captures["obj"].(tsjs.Node)
	method, _ := mc.Captures["method"].(tsjs.Node)
	args, _ := mc.Captures["args"].(tsjs.Node)
	if !obj.Valid() || !method.Valid() || !httpVerbMethod[method.Text()] {
		return
	}
	if obj.Type() != "identifier" || !clientIdent[obj.Text()] {
		return
	}
	emitURLArg(mc.Out, args, 0, model.DetectAxios)
}

// onIdentCall handles bare calls: `fetch(url, ...)`, `axios(url)`, `got(url)`.
func (clientDetector) onIdentCall(mc *provider.MatchContext) {
	fn, _ := mc.Captures["fn"].(tsjs.Node)
	args, _ := mc.Captures["args"].(tsjs.Node)
	if !fn.Valid() {
		return
	}
	switch fn.Text() {
	case "fetch":
		emitURLArg(mc.Out, args, 0, model.DetectFetch)
	case "axios", "got":
		emitURLArg(mc.Out, args, 0, model.DetectAxios)
	}
}

// emitURLArg appends an outbound edge from the URL at args[idx]: a literal
// absolute URL becomes a confirmed host node; a literal relative path or a
// dynamic expression becomes an uncertain edge (honest, never dropped).
func emitURLArg(out *model.Service, args tsjs.Node, idx int, detection model.DetectionMethod) {
	dep := model.Dependency{Protocol: model.ProtoREST, Detection: detection}
	kids := tsjs.NamedChildren(args)
	if len(kids) <= idx {
		dep.Confidence = model.Uncertain
		out.OutboundDependencies = append(out.OutboundDependencies, dep)
		return
	}
	if url, ok := tsjs.StringArgValue(kids[idx]); ok {
		if host := authority(url); host != "" {
			dep.TargetName, dep.URL, dep.Resolved, dep.Confidence = host, url, true, model.Confirmed
		} else {
			// bare path / relative URL — names no service
			dep.URL, dep.Confidence = url, model.Uncertain
		}
	} else {
		// dynamic URL (unresolved variable, template literal, builder)
		dep.Confidence = model.Uncertain
	}
	out.OutboundDependencies = append(out.OutboundDependencies, dep)
}

// authority returns host[:port] of an absolute URL (scheme://host/...), or ""
// when the string is not an absolute URL.
func authority(url string) string {
	i := strings.Index(url, "://")
	if i < 0 {
		return ""
	}
	a := url[i+3:]
	if j := strings.IndexByte(a, '/'); j >= 0 {
		a = a[:j]
	}
	return a
}
