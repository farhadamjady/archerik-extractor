package nestjs

import (
	"strings"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/tsjs"
)

// clientDetector extracts outbound HTTP dependencies from NestJS/Node call sites:
//
//   - `fetch(url, ...)`                         — WHATWG fetch (DetectFetch)
//   - `axios.get/post/put/delete/patch/head/request(url, ...)` and `axios(url)`
//   - `this.httpService.get/post/...(url, ...)` — @nestjs/axios HttpService
//
// A literal absolute URL resolves to its authority (host[:port]) as target_name,
// so path variants of one host share a node (like the JVM/Go providers). A
// dynamic URL (variable / `new URL(...).toString()` / template) can't name a
// service, so it emits an honest anonymous uncertain edge — never dropped
// (CLAUDE.md honesty rule). Placeholder/config resolution for the base URL is a
// later step; today the value evaluator isn't wired for TS.
type clientDetector struct{}

func (clientDetector) Name() string             { return "nestjs.client" }
func (clientDetector) Protocol() model.Protocol { return model.ProtoREST }

// Two patterns: a member call (axios.get / this.httpService.post) and a bare
// identifier call (fetch / axios). Each Rule carries exactly one top-level
// pattern, so they map 1:1 back to their handler.
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

// onMemberCall handles `<obj>.<method>(url, ...)` where obj is an HTTP client
// (`axios` or an injected `httpService`) and method is an HTTP verb.
func (clientDetector) onMemberCall(mc *provider.MatchContext) {
	obj, _ := mc.Captures["obj"].(tsjs.Node)
	method, _ := mc.Captures["method"].(tsjs.Node)
	args, _ := mc.Captures["args"].(tsjs.Node)
	if !obj.Valid() || !method.Valid() || !httpVerbMethod[method.Text()] {
		return
	}
	if !objectIsHTTPClient(obj) {
		return
	}
	emitURLArg(mc.Out, args, 0, model.DetectAxios)
}

// onIdentCall handles bare calls: `fetch(url, ...)` and `axios(url)`.
func (clientDetector) onIdentCall(mc *provider.MatchContext) {
	fn, _ := mc.Captures["fn"].(tsjs.Node)
	args, _ := mc.Captures["args"].(tsjs.Node)
	if !fn.Valid() {
		return
	}
	switch fn.Text() {
	case "fetch":
		emitURLArg(mc.Out, args, 0, model.DetectFetch)
	case "axios":
		emitURLArg(mc.Out, args, 0, model.DetectAxios)
	}
}

// objectIsHTTPClient reports whether the call receiver is a recognized HTTP
// client: the bare `axios` identifier, or a member/identifier whose trailing
// name is `httpService` (the @nestjs/axios injection convention). Kept narrow so
// unrelated `.get(...)` calls (maps, repositories) don't inflate the graph.
func objectIsHTTPClient(obj tsjs.Node) bool {
	switch obj.Type() {
	case "identifier":
		return obj.Text() == "axios"
	case "member_expression":
		if prop := obj.ChildByFieldName("property"); prop.Valid() {
			return prop.Text() == "httpService"
		}
	}
	return false
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
		// dynamic URL (unresolved variable, `new URL(...).toString()`, template)
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
