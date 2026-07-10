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
)`

func (d routerDetector) Rules() []provider.Rule {
	return []provider.Rule{{Query: routerQuery, OnMatch: d.onCall}}
}

func (routerDetector) onCall(mc *provider.MatchContext) {
	name, _ := mc.Captures["name"].(java.Node)
	if !routerVerbs[name.Text()] || !usesFunctionalWeb(mc.File) {
		return
	}
	args, _ := mc.Captures["args"].(java.Node)
	arg := args.NamedChild(0)
	if !arg.Valid() || arg.Type() != "string_literal" {
		return // dynamic route paths are rare; literal-only keeps this precise
	}
	path := unquote(arg.Text())
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	mc.Out.Endpoints = append(mc.Out.Endpoints, model.Endpoint{
		Method:     name.Text(),
		Path:       path,
		Protocol:   model.ProtoREST,
		Detection:  model.DetectRouter,
		Confidence: model.Confirmed,
	})
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
