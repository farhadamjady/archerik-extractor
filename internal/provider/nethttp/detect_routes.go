package nethttp

import (
	"strings"

	"github.com/farhadamjady/archerik-extractor/internal/model"
	"github.com/farhadamjady/archerik-extractor/internal/provider"
	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/golang"
	"github.com/farhadamjady/archerik-extractor/internal/schema"
)

// routeDetector extracts REST endpoints from net/http route registrations:
// `mux.HandleFunc(pattern, handler)` and `http.Handle(pattern, handler)`. The
// pattern is a string; under Go 1.22+ it is `[METHOD ][host]/path` with `{name}`
// / `{name...}` wildcards, so the verb and path are parsed out of it (a pattern
// with no method registers any verb, emitted as "*"). Only files importing
// net/http are considered, so unrelated `.Handle`/`.HandleFunc` methods on other
// types don't produce false routes.
type routeDetector struct{}

func (routeDetector) Name() string             { return "nethttp.route" }
func (routeDetector) Protocol() model.Protocol { return model.ProtoREST }

// routeQuery captures every `<recv>.<field>(<args>)` selector call; the handler
// filters to HandleFunc/Handle with a string pattern in a net/http file.
const routeQuery = `(call_expression
  function: (selector_expression
    field: (field_identifier) @field)
  arguments: (argument_list) @args
) @call`

func (d routeDetector) Rules() []provider.Rule {
	return []provider.Rule{{Query: routeQuery, OnMatch: d.onCall}}
}

func (routeDetector) onCall(mc *provider.MatchContext) {
	field, _ := mc.Captures["field"].(golang.Node)
	args, _ := mc.Captures["args"].(golang.Node)
	if !field.Valid() || !args.Valid() {
		return
	}
	switch field.Text() {
	case "HandleFunc", "Handle":
	default:
		return
	}
	// Gate on the file actually being a net/http user, so `x.Handle(...)` on some
	// other type in a non-http file doesn't register a route.
	if jf, ok := mc.File.(*golang.File); !ok || !strings.Contains(string(jf.Src()), `"net/http"`) {
		return
	}
	kids := golang.NamedChildren(args)
	if len(kids) < 2 {
		return // need a pattern and a handler
	}
	pattern, ok := golang.StringLit(kids[0])
	if !ok {
		return // dynamic pattern — could resolve via evaluator in a later round
	}
	verb, path, ok := parseGoPattern(pattern)
	if !ok {
		return
	}
	var req, resp *model.Schema
	if jf, ok := mc.File.(*golang.File); ok {
		req, resp = handlerSchemas(jf, kids[1], schema.NewWalkerDepth(mc.Index.Types, mc.Index.SchemaDepth), mc.Index.GoFuncBodies)
	}
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

var httpMethods = map[string]bool{
	"GET": true, "POST": true, "PUT": true, "DELETE": true, "PATCH": true,
	"HEAD": true, "OPTIONS": true, "CONNECT": true, "TRACE": true,
}

// parseGoPattern parses a net/http route pattern into a verb and normalized path.
// Go 1.22 form: "GET example.com/items/{id}" -> ("GET", "/items/{id}"); a
// method-less "/health" -> ("*", "/health"). Returns ok=false for an empty path.
func parseGoPattern(raw string) (verb, path string, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", false
	}
	verb = "*"
	rest := raw
	if i := strings.IndexByte(raw, ' '); i >= 0 {
		if head := raw[:i]; httpMethods[head] {
			verb = head
			rest = strings.TrimSpace(raw[i+1:])
		}
	}
	// Strip an optional host prefix: the path begins at the first '/'.
	if !strings.HasPrefix(rest, "/") {
		if j := strings.IndexByte(rest, '/'); j >= 0 {
			rest = rest[j:]
		} else {
			rest = "/" + rest
		}
	}
	path = normalizeWildcards(rest)
	if path == "" {
		path = "/"
	}
	return verb, path, true
}

// normalizeWildcards canonicalizes Go 1.22 pattern wildcards: `{name...}` (trailing
// match) -> `{name}`, and `{$}` (exact-match anchor) segments are dropped.
func normalizeWildcards(p string) string {
	if !strings.Contains(p, "{") {
		return p
	}
	segs := strings.Split(p, "/")
	out := segs[:0]
	for _, s := range segs {
		if s == "{$}" {
			continue // end-of-path anchor, not a path segment
		}
		if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
			inner := strings.TrimSuffix(s[1:len(s)-1], "...")
			s = "{" + inner + "}"
		}
		out = append(out, s)
	}
	joined := strings.Join(out, "/")
	if joined == "" {
		return "/"
	}
	return joined
}
