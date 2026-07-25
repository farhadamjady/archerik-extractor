package aspnet

import (
	"strings"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/csharp"
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
	mc.Out.Endpoints = append(mc.Out.Endpoints, model.Endpoint{
		Method:     verb,
		Path:       path,
		Protocol:   model.ProtoREST,
		Detection:  model.DetectRouter,
		Confidence: model.Confirmed,
	})
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
