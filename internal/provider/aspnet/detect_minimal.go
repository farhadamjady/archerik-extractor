package aspnet

import (
	"strings"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/csharp"
	"github.com/farhadamjady/service-discovery/internal/schema"
)

// minimalDetector extracts REST endpoints from ASP.NET Core Minimal APIs — the
// controller-less style: `app.MapGet("/todos", handler)` on a WebApplication,
// optionally grouped: `var g = app.MapGroup("/api/items"); g.MapGet("/{id}", h)`.
// The group prefix is resolved by finding the receiver identifier's declaration
// in the same file (`X = <...>.MapGroup("prefix")`), composed recursively for
// nested groups. Route constraints strip like the attribute detector
// ({id:int} -> {id}).
type minimalDetector struct{}

func (minimalDetector) Name() string             { return "aspnet.minimal" }
func (minimalDetector) Protocol() model.Protocol { return model.ProtoREST }

// minimalQuery captures every member invocation; the handler filters to Map*.
const minimalQuery = `(invocation_expression
  function: (member_access_expression) @fn
  arguments: (argument_list) @args
) @call`

func (d minimalDetector) Rules() []provider.Rule {
	return []provider.Rule{{Query: minimalQuery, OnMatch: d.onCall}}
}

var mapVerb = map[string]string{
	"MapGet": "GET", "MapPost": "POST", "MapPut": "PUT",
	"MapDelete": "DELETE", "MapPatch": "PATCH",
}

// maxGroupDepth bounds nested MapGroup resolution.
const maxGroupDepth = 5

func (d minimalDetector) onCall(mc *provider.MatchContext) {
	fn, _ := mc.Captures["fn"].(csharp.Node)
	args, _ := mc.Captures["args"].(csharp.Node)
	if !fn.Valid() || !args.Valid() {
		return
	}
	verb, isVerb := mapVerb[memberName(fn)]
	if !isVerb {
		return
	}
	sub, ok := firstStringArg(args)
	if !ok {
		return // dynamic pattern — evaluator round later
	}
	// Compose the receiver's MapGroup prefix chain (same-file), if any.
	prefix := groupPrefix(fn.ChildByFieldName("expression"), 0)
	path := "/" + strings.Trim(cleanSeg(prefix)+"/"+cleanSeg(sub), "/")
	if path == "" {
		path = "/"
	}
	// #60(a): read request/response body types off the fluent OpenAPI metadata
	// chained onto this Map* call — `.Accepts<T>()` (request) and `.Produces<T>()`
	// (response). fn.Parent() is the Map* invocation; the chain wraps it.
	req, resp := fluentBodies(fn.Parent(), schema.NewWalkerDepth(mc.Index.Types, mc.Index.SchemaDepth))

	mc.Out.Endpoints = append(mc.Out.Endpoints, model.Endpoint{
		Method:     verb,
		Path:       path,
		Request:    req,
		Response:   resp,
		Protocol:   model.ProtoREST,
		Detection:  model.DetectRouter,
		Confidence: model.Confirmed,
	})
}

// fluentBodies walks up the fluent chain from a Map* invocation, reading the
// generic type argument of `.Accepts<T>()` (request body) and the first generic
// `.Produces<T>()` (response body). Non-generic `.Produces(StatusCodes...)` (a
// bare status, no body) and other builder calls (.WithName/.RequireAuthorization)
// are ignored. Types resolve through the C# type index + walker (#60a).
func fluentBodies(mapInvocation csharp.Node, walker *schema.Walker) (req, resp *model.Schema) {
	cur := mapInvocation
	for cur.Valid() {
		ma := cur.Parent()
		if !ma.Valid() || ma.Type() != "member_access_expression" {
			break
		}
		inv := ma.Parent()
		if !inv.Valid() || inv.Type() != "invocation_expression" {
			break
		}
		if method, arg, ok := genericCall(ma.ChildByFieldName("name")); ok {
			switch method {
			case "Accepts":
				if req == nil {
					req = bodyOrNil(walker.Type(arg))
				}
			case "Produces":
				if resp == nil {
					resp = bodyOrNil(walker.Type(arg))
				}
			}
		}
		cur = inv
	}
	return req, resp
}

// genericCall extracts a `Method<TypeArg>` from a member name node: returns the
// method identifier and the first type-argument text. ok=false for a plain
// identifier (`Produces` with no <T>, a bare status-code call).
func genericCall(name csharp.Node) (method, typeArg string, ok bool) {
	if !name.Valid() || name.Type() != "generic_name" {
		return "", "", false
	}
	var tal csharp.Node
	for _, c := range csharp.NamedChildren(name) {
		switch c.Type() {
		case "identifier":
			method = c.Text()
		case "type_argument_list":
			tal = c
		}
	}
	if method == "" || !tal.Valid() {
		return "", "", false
	}
	args := csharp.NamedChildren(tal)
	if len(args) == 0 {
		return "", "", false
	}
	return method, args[0].Text(), true
}

// memberName is the invoked member's simple name (the last identifier of a
// member_access_expression).
func memberName(fn csharp.Node) string {
	if n := fn.ChildByFieldName("name"); n.Valid() {
		return n.Text()
	}
	kids := csharp.NamedChildren(fn)
	if len(kids) > 0 {
		return kids[len(kids)-1].Text()
	}
	return ""
}

// firstStringArg returns the first positional string-literal argument.
func firstStringArg(args csharp.Node) (string, bool) {
	for _, a := range csharp.NamedChildren(args) {
		if a.Type() != "argument" {
			continue
		}
		kids := csharp.NamedChildren(a)
		if len(kids) == 1 && kids[0].Type() == "string_literal" {
			return csharp.StringContent(kids[0]), true
		}
		return "", false // first arg isn't a literal path
	}
	return "", false
}

// groupPrefix resolves the MapGroup prefix chain of a Map* receiver:
//   - a direct `<x>.MapGroup("p")` receiver contributes p (plus ITS receiver's
//     prefixes, for chained groups);
//   - an identifier receiver is looked up in the file for a declaration
//     `name = <...>.MapGroup("p")` and resolved recursively;
//   - anything else (the app itself) contributes "".
func groupPrefix(recv csharp.Node, depth int) string {
	if !recv.Valid() || depth >= maxGroupDepth {
		return ""
	}
	switch recv.Type() {
	case "invocation_expression":
		fn := recv.ChildByFieldName("function")
		if fn.Type() != "member_access_expression" {
			return ""
		}
		if memberName(fn) == "MapGroup" {
			p, _ := firstStringArg(recv.ChildByFieldName("arguments"))
			return joinGroup(groupPrefix(fn.ChildByFieldName("expression"), depth+1), p)
		}
		// A fluent builder call on the group (AddEndpointFilter,
		// RequireAuthorization, WithTags, ...) returns the group itself —
		// descend into its receiver to find the MapGroup deeper in the chain.
		return groupPrefix(fn.ChildByFieldName("expression"), depth+1)
	case "identifier":
		decl := findDeclaration(recv, recv.Text())
		if decl.Valid() {
			return groupPrefix(decl, depth+1)
		}
		return ""
	default:
		return ""
	}
}

// findDeclaration finds the initializer of `var <name> = <expr>` in the file
// containing n (last declaration before use wins; Minimal API files are small
// Program.cs top-level statements, so a file-wide scan is fine).
func findDeclaration(n csharp.Node, name string) csharp.Node {
	root := n
	for p := n.Parent(); p.Valid(); p = p.Parent() {
		root = p
	}
	before := n.StartByte()
	var out csharp.Node
	root.Walk(func(m csharp.Node) bool {
		if m.Type() != "variable_declarator" || m.StartByte() >= before {
			return true
		}
		if id := m.ChildByFieldName("name"); id.Valid() && id.Text() == name {
			kids := csharp.NamedChildren(m)
			if len(kids) > 0 {
				out = kids[len(kids)-1] // the initializer expression
			}
		}
		return true
	})
	return out
}

func joinGroup(parent, prefix string) string {
	p := strings.Trim(parent, "/")
	q := strings.Trim(prefix, "/")
	switch {
	case p == "":
		return q
	case q == "":
		return p
	default:
		return p + "/" + q
	}
}
