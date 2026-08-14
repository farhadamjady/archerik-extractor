package aspnet

import (
	"strings"

	"github.com/farhadamjady/archerik-extractor/internal/model"
	"github.com/farhadamjady/archerik-extractor/internal/provider"
	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/csharp"
	"github.com/farhadamjady/archerik-extractor/internal/schema"
)

// restDetector extracts REST endpoints from ASP.NET Core attribute-routed
// controllers. A controller is a class named *Controller or marked
// [ApiController]/[Controller]; its class-level [Route] gives the base template,
// each action's [HttpGet]/[HttpPost]/... (with an optional path) gives the verb
// and method template. The [controller]/[action] tokens are substituted, route
// constraints ({id:int}) are stripped to {id}, and paths compose like the JVM
// providers.
type restDetector struct{}

func (restDetector) Name() string             { return "aspnet.rest" }
func (restDetector) Protocol() model.Protocol { return model.ProtoREST }

const controllerQuery = `(class_declaration) @class`

func (d restDetector) Rules() []provider.Rule {
	return []provider.Rule{{Query: controllerQuery, OnMatch: d.onController}}
}

// httpVerbAttr maps the ASP.NET verb attributes to their HTTP method.
var httpVerbAttr = map[string]string{
	"HttpGet": "GET", "HttpPost": "POST", "HttpPut": "PUT", "HttpDelete": "DELETE",
	"HttpPatch": "PATCH", "HttpHead": "HEAD", "HttpOptions": "OPTIONS",
}

func (restDetector) onController(mc *provider.MatchContext) {
	class, ok := mc.Captures["class"].(csharp.Node)
	if !ok || !class.Valid() {
		return
	}
	name := csharp.Name(class)
	if !isController(class, name) {
		return
	}
	token := strings.TrimSuffix(name, "Controller") // [controller] substitution
	bases := controllerBases(class, token)

	body := csharp.ChildByType(class, "declaration_list")
	if !body.Valid() {
		return
	}
	walker := schema.NewWalkerDepth(mc.Index.Types, mc.Index.SchemaDepth)
	for _, m := range csharp.NamedChildren(body) {
		if m.Type() != "method_declaration" {
			continue
		}
		appendMethodEndpoints(mc.Out, m, bases, token, walker)
	}
}

// isController reports whether a class is an MVC/API controller: named *Controller,
// or marked [ApiController]/[Controller], or deriving from Controller/ControllerBase.
func isController(class csharp.Node, name string) bool {
	if strings.HasSuffix(name, "Controller") {
		return true
	}
	if csharp.HasAttribute(class, "ApiController") || csharp.HasAttribute(class, "Controller") {
		return true
	}
	if bl := csharp.ChildByType(class, "base_list"); bl.Valid() {
		t := bl.Text()
		return strings.Contains(t, "ControllerBase") || strings.Contains(t, "Controller")
	}
	return false
}

// controllerBases is the class-level route template(s) with [controller]
// substituted, or [""] when there is no class [Route].
func controllerBases(class csharp.Node, token string) []string {
	route := csharp.FindAttribute(class, "Route")
	if !route.Valid() {
		return []string{""}
	}
	if v, _, ok := csharp.AttributeStringArg(route); ok {
		return []string{substituteTokens(v, token, "")}
	}
	return []string{""}
}

// appendMethodEndpoints emits the endpoint(s) for one action method: its first
// HTTP-verb attribute gives the verb (and an optional path); a method-level
// [Route] can also supply a path. Paths compose onto the controller base.
func appendMethodEndpoints(out *model.Service, method csharp.Node, bases []string, token string, walker *schema.Walker) {
	verb := ""
	var subs []string
	for _, a := range csharp.AttributesOf(method) {
		an := csharp.AttributeName(a)
		if v, isVerb := httpVerbAttr[an]; isVerb {
			verb = v
			if s, _, ok := csharp.AttributeStringArg(a); ok {
				subs = append(subs, s)
			}
			break
		}
	}
	if verb == "" {
		return // not an action with an HTTP verb
	}
	if len(subs) == 0 {
		// No path on the verb attribute — a method-level [Route] may carry it.
		if r := csharp.FindAttribute(method, "Route"); r.Valid() {
			if s, _, ok := csharp.AttributeStringArg(r); ok {
				subs = append(subs, s)
			}
		}
	}
	if len(subs) == 0 {
		subs = []string{""}
	}
	mname := csharp.Name(method)
	req := actionRequest(method, walker)
	resp := actionResponse(method, walker)
	for _, base := range bases {
		for _, sub := range subs {
			sub = substituteTokens(sub, token, mname)
			out.Endpoints = append(out.Endpoints, model.Endpoint{
				Method:     verb,
				Path:       composePath(base, sub),
				Request:    req,
				Response:   resp,
				Protocol:   model.ProtoREST,
				Detection:  model.DetectAnnotation,
				Confidence: model.Confirmed,
			})
		}
	}
}

// actionResponse resolves the response body schema from an action's declared
// return type (`Task<ActionResult<ProductDto>>` -> ProductDto), dropping
// void/IActionResult (opaque) results.
func actionResponse(method csharp.Node, walker *schema.Walker) *model.Schema {
	rt := method.ChildByFieldName("returns") // C# grammar names the return type "returns"
	if !rt.Valid() {
		return nil
	}
	// A bare async wrapper with no type argument (`Task`/`ValueTask`) is an async
	// no-result — treat it as void rather than an unknown "Task" type.
	switch strings.TrimSpace(rt.Text()) {
	case "Task", "ValueTask", "void", "Task<IActionResult>", "Task<ActionResult>":
		return nil
	}
	return bodyOrNil(walker.Type(rt.Text()))
}

// actionRequest resolves the request body schema. It prefers a [FromBody]
// parameter; failing that, the first non-routing complex parameter whose type is
// a known DTO (ASP.NET binds complex types from the body by default under
// [ApiController]). Route/query/header/service/form params and CancellationToken
// are never the body.
func actionRequest(method csharp.Node, walker *schema.Walker) *model.Schema {
	pl := method.ChildByFieldName("parameters")
	if !pl.Valid() {
		return nil
	}
	params := csharp.NamedChildren(pl)
	// 1) explicit [FromBody]
	for _, p := range params {
		if p.Type() == "parameter" && csharp.HasAttribute(p, "FromBody") {
			if ty := p.ChildByFieldName("type"); ty.Valid() {
				return bodyOrNil(walker.Type(ty.Text()))
			}
		}
	}
	// 2) implicit body: first known-DTO param without a non-body binding source
	for _, p := range params {
		if p.Type() != "parameter" || hasNonBodyBinding(p) {
			continue
		}
		ty := p.ChildByFieldName("type")
		if !ty.Valid() || ty.Text() == "CancellationToken" {
			continue
		}
		if s := walker.Type(ty.Text()); s != nil && s.Confidence == model.Confirmed && len(s.Nested) > 0 {
			return s
		}
	}
	return nil
}

// hasNonBodyBinding reports whether a parameter is explicitly bound from a
// non-body source (route/query/header/service/form), so it can't be the body.
func hasNonBodyBinding(p csharp.Node) bool {
	for _, src := range []string{"FromRoute", "FromQuery", "FromHeader", "FromServices", "FromForm"} {
		if csharp.HasAttribute(p, src) {
			return true
		}
	}
	return false
}

// bodyOrNil drops a void/empty schema (no request/response body).
func bodyOrNil(s *model.Schema) *model.Schema {
	if s == nil || s.Type == "void" {
		return nil
	}
	return s
}

// substituteTokens replaces the ASP.NET [controller] and [action] route tokens.
func substituteTokens(tmpl, controller, action string) string {
	tmpl = strings.ReplaceAll(tmpl, "[controller]", controller)
	if action != "" {
		tmpl = strings.ReplaceAll(tmpl, "[action]", action)
	}
	return tmpl
}

// composePath joins a base template and a method template. An absolute method
// template ("/abs" or "~/abs") replaces the base (ASP.NET override semantics).
func composePath(base, sub string) string {
	if strings.HasPrefix(sub, "~/") {
		sub = strings.TrimPrefix(sub, "~")
		base = ""
	} else if strings.HasPrefix(sub, "/") {
		base = ""
	}
	b := cleanSeg(base)
	s := cleanSeg(sub)
	switch {
	case b == "" && s == "":
		return "/"
	case b == "":
		return "/" + s
	case s == "":
		return "/" + b
	default:
		return "/" + b + "/" + s
	}
}

// cleanSeg trims slashes and strips route constraints/modifiers from each
// {param} segment: {id:int} -> {id}, {*slug} -> {slug}, {id?} -> {id}.
func cleanSeg(p string) string {
	p = strings.Trim(p, "/")
	if !strings.Contains(p, "{") {
		return p
	}
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
			inner := s[1 : len(s)-1]
			inner = strings.TrimPrefix(strings.TrimPrefix(inner, "*"), "*") // catch-all {*x}
			if j := strings.IndexAny(inner, ":?="); j >= 0 {
				inner = inner[:j] // strip :constraint / ? optional / =default
			}
			segs[i] = "{" + inner + "}"
		}
	}
	return strings.Join(segs, "/")
}
