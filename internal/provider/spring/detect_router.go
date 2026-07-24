package spring

import (
	"strings"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/java"
)

// routerDetector extracts endpoints from FUNCTIONAL routing (IMPROVEMENTS #20):
//
//	route(GET("/posts"), handler)                      // RequestPredicates style
//	RouterFunctions.route().GET("/posts", handler)     // builder style
//
// Gated on the file importing the functional web API (reactive or servlet), so
// unrelated GET(...)/POST(...) calls elsewhere never match. Nest()/path()
// prefixes are not composed yet — best-effort, noted.
type routerDetector struct{}

func (routerDetector) Name() string             { return "spring.router" }
func (routerDetector) Protocol() model.Protocol { return model.ProtoREST }

var routerVerbs = map[string]bool{
	"GET": true, "POST": true, "PUT": true, "DELETE": true,
	"PATCH": true, "HEAD": true, "OPTIONS": true,
}

const routerQuery = `(method_invocation
  name: (identifier) @name
  arguments: (argument_list) @args
) @call`

func (d routerDetector) Rules() []provider.Rule {
	return []provider.Rule{{Query: routerQuery, OnMatch: d.onCall}}
}

func (d routerDetector) onCall(mc *provider.MatchContext) {
	name, _ := mc.Captures["name"].(java.Node)
	if !routerVerbs[name.Text()] || !usesFunctionalWeb(mc.File) {
		return
	}
	args, _ := mc.Captures["args"].(java.Node)
	arg := args.NamedChild(0)
	if !arg.Valid() || arg.Type() != "string_literal" {
		return // dynamic route paths are rare; literal-only keeps this precise
	}
	// IMPROVEMENTS #38: compose enclosing path("/prefix", builder → …) and
	// nest(path("/prefix"), …) prefixes, like class @RequestMapping + method
	// mapping. Without this, /accounts/current and /notifications/current both
	// emit as "/current" and dedup-collapse into one wrong endpoint.
	verbCall, _ := mc.Captures["call"].(java.Node)
	path := joinRoute(append(routePrefixes(verbCall), unquote(arg.Text()))...)
	mc.Out.Endpoints = append(mc.Out.Endpoints, model.Endpoint{
		Method:     name.Text(),
		Path:       path,
		Protocol:   model.ProtoREST,
		Detection:  model.DetectRouter,
		Confidence: model.Confirmed,
	})
}

// routePrefixes walks out from a verb call and collects the path prefixes of the
// enclosing routing DSL, OUTERMOST first: path("/a", b → …) contributes "/a";
// nest(path("/a"), …) likewise (its predicate is the path). Verb calls and other
// invocations in the chain are skipped, so nested groups compose (/a/b/c).
func routePrefixes(verbCall java.Node) []string {
	var inner []string // collected innermost-first while walking up
	for p := verbCall.Parent(); p.Valid(); p = p.Parent() {
		if p.Type() != "method_invocation" {
			continue
		}
		switch p.ChildByFieldName("name").Text() {
		case "path":
			if s, ok := firstStringArg(p); ok {
				inner = append(inner, s)
			}
		case "nest":
			// nest(path("/a"), …): the prefix lives in the predicate argument.
			if a0 := methodArg(p, 0); a0.Valid() && a0.Type() == "method_invocation" &&
				a0.ChildByFieldName("name").Text() == "path" {
				if s, ok := firstStringArg(a0); ok {
					inner = append(inner, s)
				}
			}
		}
	}
	// reverse to outermost-first
	for i, j := 0, len(inner)-1; i < j; i, j = i+1, j-1 {
		inner[i], inner[j] = inner[j], inner[i]
	}
	return inner
}

// methodArg returns the i-th positional argument of a method_invocation.
func methodArg(mi java.Node, i int) java.Node {
	return mi.ChildByFieldName("arguments").NamedChild(i)
}

// firstStringArg returns the unquoted first string-literal argument of a
// method_invocation, if it has one.
func firstStringArg(mi java.Node) (string, bool) {
	a := methodArg(mi, 0)
	if !a.Valid() || a.Type() != "string_literal" {
		return "", false
	}
	return unquote(a.Text()), true
}

// joinRoute composes route segments into a single path with single slashes,
// each segment trimmed of surrounding slashes; empty segments (a "/" root) drop
// out. No segments → "/".
func joinRoute(segs ...string) string {
	var parts []string
	for _, s := range segs {
		if s = strings.Trim(s, "/"); s != "" {
			parts = append(parts, s)
		}
	}
	if len(parts) == 0 {
		return "/"
	}
	return "/" + strings.Join(parts, "/")
}

// usesFunctionalWeb reports whether the file imports the functional web API.
func usesFunctionalWeb(f provider.ParsedFile) bool {
	jf, ok := f.(*java.File)
	if !ok {
		return false
	}
	src := string(jf.Src())
	return strings.Contains(src, "org.springframework.web.reactive.function.server") ||
		strings.Contains(src, "org.springframework.web.servlet.function")
}
