package express

import (
	"strings"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/tsjs"
)

// routeDetector extracts REST endpoints from Express route registrations:
// `app.get('/users', handler)`, `router.post('/:id', ...)`. It is call-based, so
// it filters method calls to the HTTP-verb methods whose FIRST argument is a
// string path AND that pass at least one handler (2+ args) — this excludes
// Express's one-arg settings getter `app.get('port')` and unrelated `.get()`
// calls (Map.get, etc.). Path params `:id` are normalized to `{id}`. Mounting
// prefixes (`app.use('/api', router)`) are not composed yet (a documented gap),
// so routes are emitted at their declared path.
type routeDetector struct{}

func (routeDetector) Name() string             { return "express.route" }
func (routeDetector) Protocol() model.Protocol { return model.ProtoREST }

// routeQuery captures every `<obj>.<method>(<args>)` call; the handler filters to
// verb methods with a string path and a handler argument.
const routeQuery = `(call_expression
  function: (member_expression
    object: (_) @obj
    property: (property_identifier) @verb)
  arguments: (arguments) @args
) @call`

func (d routeDetector) Rules() []provider.Rule {
	return []provider.Rule{{Query: routeQuery, OnMatch: d.onCall}}
}

var routeVerb = map[string]string{
	"get": "GET", "post": "POST", "put": "PUT", "delete": "DELETE",
	"patch": "PATCH", "options": "OPTIONS", "head": "HEAD", "all": "*",
}

func (routeDetector) onCall(mc *provider.MatchContext) {
	verbNode, _ := mc.Captures["verb"].(tsjs.Node)
	args, _ := mc.Captures["args"].(tsjs.Node)
	obj, _ := mc.Captures["obj"].(tsjs.Node)
	if !verbNode.Valid() || !args.Valid() {
		return
	}
	verb, isVerb := routeVerb[verbNode.Text()]
	if !isVerb {
		return
	}
	kids := tsjs.NamedChildren(args)

	// Chained form: router.route('/path').get(handler) — the path lives on a
	// `.route('/path')` call somewhere in the receiver chain, and this call's
	// arguments are just handlers.
	if raw, ok := chainRoutePath(obj); ok {
		emitRoute(mc, verb, raw)
		return
	}

	// Direct form: app.get('/path', handler) — a string path (starting with "/",
	// which separates a route from app.get('port') / cache.get('key', def)) plus
	// at least one handler argument.
	if len(kids) < 2 || kids[0].Type() != "string" {
		return
	}
	raw := tsjs.StringValue(kids[0])
	if !strings.HasPrefix(raw, "/") || !plausibleAppReceiver(obj) {
		return
	}
	emitRoute(mc, verb, raw)
}

func emitRoute(mc *provider.MatchContext, verb, raw string) {
	mc.Out.Endpoints = append(mc.Out.Endpoints, model.Endpoint{
		Method:     verb,
		Path:       normalizePath("/" + strings.Trim(raw, "/")),
		Protocol:   model.ProtoREST,
		Detection:  model.DetectRouter,
		Confidence: model.Confirmed,
	})
}

// chainRoutePath walks a call receiver chain looking for a `.route('/path')`
// call (Express's `router.route('/path').get(...).post(...)` form) and returns
// its string path. Only paths starting with "/" qualify.
func chainRoutePath(n tsjs.Node) (string, bool) {
	for n.Valid() && n.Type() == "call_expression" {
		fn := n.ChildByFieldName("function")
		if fn.Type() == "member_expression" {
			if fn.ChildByFieldName("property").Text() == "route" {
				a := n.ChildByFieldName("arguments")
				kids := tsjs.NamedChildren(a)
				if len(kids) >= 1 && kids[0].Type() == "string" {
					if raw := tsjs.StringValue(kids[0]); strings.HasPrefix(raw, "/") {
						return raw, true
					}
				}
				return "", false
			}
			n = fn.ChildByFieldName("object") // descend the chain
			continue
		}
		break
	}
	return "", false
}

// plausibleAppReceiver reports whether the call receiver looks like an Express
// app or router: a bare identifier (`app`, `router`), `this.something`, or a
// member like `this.router`. A call-chain receiver (`foo().bar`) is rejected to
// avoid false positives.
func plausibleAppReceiver(obj tsjs.Node) bool {
	switch obj.Type() {
	case "identifier", "this":
		return true
	case "member_expression":
		// this.router / that.app style
		return true
	default:
		return false
	}
}

// normalizePath converts Express `:param` segments to `{param}` for cross-language
// graph uniformity.
func normalizePath(p string) string {
	if !strings.Contains(p, ":") {
		return p
	}
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if strings.HasPrefix(s, ":") {
			name := strings.TrimSuffix(strings.TrimPrefix(s, ":"), "?")
			segs[i] = "{" + name + "}"
		}
	}
	return strings.Join(segs, "/")
}
