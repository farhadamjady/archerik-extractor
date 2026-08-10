package aspnet

import (
	"strings"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/csharp"
)

// clientDetector extracts outbound HTTP dependencies from .NET HttpClient call
// sites: `httpClient.GetAsync(url)` / `PostAsync` / `SendAsync` and the
// System.Net.Http.Json extensions (`GetFromJsonAsync` / `PostAsJsonAsync` / ...).
// A literal absolute URL resolves to its authority (host[:port]) as target_name;
// a relative path (`api/orders`, common when the client has a BaseAddress set at
// registration) or a dynamic/interpolated URL emits an honest uncertain edge —
// never dropped (the honesty rule). Receiver-type tracking (proving the
// receiver is really an HttpClient) and resolving BaseAddress through DI are
// later rounds; this covers the call-site verbs, which are HttpClient-shaped.
type clientDetector struct{}

func (clientDetector) Name() string             { return "aspnet.client" }
func (clientDetector) Protocol() model.Protocol { return model.ProtoREST }

// callQuery captures every method invocation of the form `<expr>.<name>(args)`;
// the handler filters to HttpClient verbs. refitQuery captures interface
// declarations, whose methods may carry Refit `[Get("/path")]` attributes.
const (
	callQuery = `(invocation_expression
  function: (member_access_expression
    name: (_) @method)
  arguments: (argument_list) @args) @call`

	refitQuery = `(interface_declaration) @iface`
)

func (d clientDetector) Rules() []provider.Rule {
	return []provider.Rule{
		{Query: callQuery, OnMatch: d.onCall},
		{Query: refitQuery, OnMatch: d.onRefitInterface},
	}
}

// refitVerbAttr is the set of Refit HTTP-verb attributes carrying a path template.
var refitVerbAttr = map[string]bool{
	"Get": true, "Post": true, "Put": true, "Delete": true,
	"Patch": true, "Head": true, "Options": true,
}

// onRefitInterface emits an outbound edge for each Refit-annotated method of a
// declarative client interface. Refit puts the relative path in the attribute
// (`[Get("/catalog-service/products")]`) and the host in the DI registration
// (AddRefitClient BaseAddress), which isn't visible here — so the target is
// derived best-effort from the path's leading segment (the gateway/service route)
// and marked uncertain. Edges to the same target collapse via dedup.
func (clientDetector) onRefitInterface(mc *provider.MatchContext) {
	iface, ok := mc.Captures["iface"].(csharp.Node)
	if !ok || !iface.Valid() {
		return
	}
	body := csharp.ChildByType(iface, "declaration_list")
	if !body.Valid() {
		return
	}
	for _, m := range csharp.NamedChildren(body) {
		if m.Type() != "method_declaration" {
			continue
		}
		for _, attr := range csharp.AttributesOf(m) {
			if !refitVerbAttr[csharp.AttributeName(attr)] {
				continue
			}
			path, _, hasPath := csharp.AttributeStringArg(attr)
			dep := model.Dependency{Protocol: model.ProtoREST, Detection: model.DetectRefit, Confidence: model.Uncertain}
			if hasPath {
				dep.URL = path
				if seg := leadingSegment(path); seg != "" {
					dep.TargetName = seg
				}
			}
			mc.Out.OutboundDependencies = append(mc.Out.OutboundDependencies, dep)
			break // one verb attribute per method
		}
	}
}

// leadingSegment returns the first path segment of a Refit path template
// (`/catalog-service/products?...` -> `catalog-service`), or "" when the path is
// empty or has no usable first segment. Query strings and templated segments
// (`{id}`) are excluded from the segment itself.
func leadingSegment(path string) string {
	p := strings.TrimLeft(path, "/")
	if i := strings.IndexAny(p, "/?"); i >= 0 {
		p = p[:i]
	}
	if p == "" || strings.HasPrefix(p, "{") {
		return ""
	}
	return p
}

// httpClientMethod is the set of HttpClient / HttpClientJsonExtensions methods
// whose first argument is the request URL.
var httpClientMethod = map[string]bool{
	"GetAsync": true, "PostAsync": true, "PutAsync": true, "DeleteAsync": true,
	"PatchAsync": true, "SendAsync": true,
	"GetStringAsync": true, "GetByteArrayAsync": true, "GetStreamAsync": true,
	"GetFromJsonAsync": true, "PostAsJsonAsync": true, "PutAsJsonAsync": true,
	"DeleteFromJsonAsync": true, "PatchAsJsonAsync": true,
}

func (clientDetector) onCall(mc *provider.MatchContext) {
	method, _ := mc.Captures["method"].(csharp.Node)
	args, _ := mc.Captures["args"].(csharp.Node)
	if !method.Valid() || !httpClientMethod[methodName(method)] {
		return
	}
	arg := firstArgExpr(args)
	if !arg.Valid() {
		return
	}
	dep := model.Dependency{Protocol: model.ProtoREST, Detection: model.DetectDotNetHTTPClient}
	switch {
	case arg.Type() == "string_literal":
		url := csharp.StringContent(arg)
		switch {
		case authority(url) != "":
			dep.TargetName, dep.URL, dep.Resolved, dep.Confidence = authority(url), url, true, model.Confirmed
		case strings.Contains(url, "/"):
			// relative API path — the host lives in BaseAddress (not visible here)
			dep.URL, dep.Confidence = url, model.Uncertain
		default:
			// a bare non-URL string (e.g. a cache key) — not an HTTP target
			return
		}
	default:
		// interpolated / variable / builder URL — honest anonymous uncertain edge
		dep.Confidence = model.Uncertain
	}
	mc.Out.OutboundDependencies = append(mc.Out.OutboundDependencies, dep)
}

// methodName returns the called method's simple name, unwrapping a generic_name
// (`GetFromJsonAsync<Foo>` -> `GetFromJsonAsync`) to its identifier.
func methodName(name csharp.Node) string {
	if name.Type() == "generic_name" {
		if id := csharp.ChildByType(name, "identifier"); id.Valid() {
			return id.Text()
		}
		return ""
	}
	return name.Text()
}

// firstArgExpr returns the expression of the first positional argument, unwrapping
// the `argument` node that C#'s grammar wraps each argument in.
func firstArgExpr(args csharp.Node) csharp.Node {
	for _, a := range csharp.NamedChildren(args) {
		if a.Type() != "argument" {
			continue
		}
		kids := csharp.NamedChildren(a)
		if len(kids) > 0 {
			return kids[0]
		}
	}
	return csharp.Node{}
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
