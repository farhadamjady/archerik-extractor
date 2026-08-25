package laravel

import (
	"strings"

	"github.com/farhadamjady/archerik-extractor/internal/model"
	"github.com/farhadamjady/archerik-extractor/internal/provider"
	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/php"
)

// routeDetector extracts REST endpoints from Laravel's Route facade:
// `Route::get('/users/{id}', [UserController::class, 'show'])`,
// `Route::match(['get','post'], '/x', $h)`, the registrars that bind a framework
// controller (`Route::redirect`, `Route::view`), and the resource registrars
// (`Route::apiResource('posts', PostController::class)`).
//
// A route's full path is composed from three sources, none of which is
// necessarily in the same statement — or the same file:
//
//  1. the FILE's mount prefix (routes/api.php is served under `api`), resolved
//     by prefixIndexer into Index.MountPrefixes;
//  2. the enclosing route GROUPS, which nest through closures and read their
//     prefix either from the call chain (`Route::prefix('v1')->group(fn)`) or
//     from the array form (`Route::group(['prefix' => 'v1'], fn)`);
//  3. the path argument of the registration itself.
//
// Both call forms carry a registration: a bare `Route::get(...)` is a
// scoped_call_expression, while `Route::middleware('auth')->get(...)` is a
// member_call_expression whose receiver chain roots at the facade — hence two
// rules over one handler, gated on the chain actually rooting at `Route::`.
//
// A registration whose path is not a literal (`Route::get("/x/{$var}", ...)`)
// is skipped rather than emitted: an endpoint's identity IS its verb + path, so
// there is nothing stable to key an unresolved one on. Resolving those through
// the config/value layer is a later round.
type routeDetector struct{}

func (routeDetector) Name() string             { return "laravel.route" }
func (routeDetector) Protocol() model.Protocol { return model.ProtoREST }

const (
	staticCallQuery = `(scoped_call_expression) @call`
	memberCallQuery = `(member_call_expression) @call`
)

func (d routeDetector) Rules() []provider.Rule {
	return []provider.Rule{
		{Query: staticCallQuery, OnMatch: d.onCall},
		{Query: memberCallQuery, OnMatch: d.onCall},
	}
}

// routeVerb maps a Route facade method to its HTTP verb. `any` registers every
// verb and is emitted as "*", matching the net/http provider's method-less form.
var routeVerb = map[string]string{
	"get": "GET", "post": "POST", "put": "PUT", "patch": "PATCH",
	"delete": "DELETE", "options": "OPTIONS", "head": "HEAD", "any": "*",
}

func (routeDetector) onCall(mc *provider.MatchContext) {
	call, _ := mc.Captures["call"].(php.Node)
	if !call.Valid() || !routeFacade(call) {
		return
	}
	switch name := php.CallName(call); name {
	case "match":
		emitRoutes(mc, call, matchVerbs(php.PositionalArg(call, 0)), php.PositionalArg(call, 1))
	case "redirect", "permanentRedirect":
		// Router::redirect() registers the URI for ANY verb, serving a redirect
		// from a framework controller. It is still an endpoint this service
		// answers on.
		emitRoutes(mc, call, []string{"*"}, php.PositionalArg(call, 0))
	case "view":
		// Router::view() binds GET (and HEAD) to a framework controller that
		// renders a template — emitted as GET, like every other read route.
		emitRoutes(mc, call, []string{"GET"}, php.PositionalArg(call, 0))
	case "resource", "apiResource":
		res, _ := php.StringLit(php.PositionalArg(call, 0))
		emitResource(mc, call, res, name == "apiResource")
	case "resources", "apiResources":
		for _, e := range php.ArrayEntries(php.PositionalArg(call, 0)) {
			emitResource(mc, call, e.Key, name == "apiResources")
		}
	default:
		if verb, ok := routeVerb[name]; ok {
			emitRoutes(mc, call, []string{verb}, php.PositionalArg(call, 0))
		}
	}
}

// routeFacade reports whether a call is a registration ON the Route facade: its
// receiver chain must root at `Route::`, `\Route::`, or the fully-qualified
// `Illuminate\Support\Facades\Route::`, AND every call between the root and this
// one must be a router method. Without the root check, any `X::get(...)` or
// `$repo->delete(...)` in the service would register a phantom endpoint.
//
// The chain check matters just as much, because the facade also exposes
// non-registering methods that return something else entirely:
//
//	Route::getRoutes()->get('GET')   // a RouteCollection LOOKUP
//
// That roots at `Route::` and its outer method is `get` with one string
// argument, so a root-only check read it as a registration and emitted
// `GET /GET` — a phantom endpoint, found in laravel/framework's own
// RedirectIfAuthenticated middleware. `getRoutes` is not a router method, so the
// chain is rejected and nothing is emitted.
func routeFacade(call php.Node) bool {
	for n := call; n.Valid(); {
		switch n.Type() {
		case "member_call_expression", "nullsafe_member_call_expression":
			recv := n.ChildByFieldName("object")
			// Every call below the outermost one is an intermediate link; the
			// outermost call's own name is the caller's business.
			if !n.Equal(call) && !routeChainMethod(php.CallName(n)) {
				return false
			}
			n = recv
		case "scoped_call_expression":
			scope := n.ChildByFieldName("scope").Text()
			if scope != "Route" && !strings.HasSuffix(scope, `\Route`) {
				return false
			}
			return n.Equal(call) || routeChainMethod(php.CallName(n))
		default:
			return false
		}
	}
	return false
}

// routeChainMethod reports whether a method may appear mid-chain in a route
// registration. Two kinds qualify: the RouteRegistrar attribute methods, which
// return a registrar so more links can follow, and the registrations themselves,
// which return a Route that attributes are commonly chained onto
// (`Route::get(...)->name('users.index')`).
//
// Anything else ends the chain: a Router method that returns a RouteCollection,
// the current Route, or the facade root is not building a registration.
func routeChainMethod(name string) bool {
	if _, ok := routeVerb[name]; ok {
		return true
	}
	switch name {
	// RouteRegistrar attributes (Illuminate\Routing\RouteRegistrar).
	case "as", "controller", "domain", "middleware", "missing", "name",
		"namespace", "prefix", "scopeBindings", "where", "withoutMiddleware",
		"withoutScopedBindings", "block", "withoutBlocking", "defaults", "can",
		"group":
		return true
	// Registrars, which also return something chainable.
	case "match", "resource", "apiResource", "resources", "apiResources",
		"redirect", "permanentRedirect", "view", "fallback":
		return true
	}
	return false
}

// matchVerbs reads the verb list of `Route::match(['get','post'], ...)`.
// Non-literal or unrecognized verbs are dropped.
func matchVerbs(arg php.Node) []string {
	var out []string
	for _, s := range php.ArrayStrings(arg) {
		if v, ok := routeVerb[strings.ToLower(s)]; ok {
			out = append(out, v)
		}
	}
	return out
}

// emitRoutes appends one endpoint per verb per resolved path per mount.
func emitRoutes(mc *provider.MatchContext, call php.Node, verbs []string, pathArg php.Node) {
	raws, conf := resolvePath(mc, call, pathArg)
	for _, verb := range verbs {
		for _, raw := range raws {
			for _, full := range composePaths(mc, call, raw) {
				appendEndpoint(mc, verb, full, conf)
			}
		}
	}
}

// resolvePath resolves a registration's path argument to the set of paths it
// declares, with the confidence that resolution earns.
//
// A literal is the path, `confirmed`. Otherwise it is evaluated in the
// registration's scope — enclosing `foreach` bindings, the file's top-level
// variables, `config()`/`env()` — and what comes back is `likely`: every input
// was a literal in the source, but reading them as the iteration set assumes the
// array is not mutated between its declaration and the loop, which is one
// inference more than a declared path needs.
//
// An unresolved path yields nothing. An endpoint's identity IS verb + path, so
// there is no honest way to emit one whose path is unknown — unlike an outbound
// dependency, which can be emitted as an unknown target. This is the same choice
// the Express and net/http providers make.
func resolvePath(mc *provider.MatchContext, call, pathArg php.Node) ([]string, model.Confidence) {
	if raw, ok := php.StringLit(pathArg); ok {
		return []string{raw}, model.Confirmed
	}
	f, ok := mc.File.(*php.File)
	if !ok {
		return nil, model.Uncertain
	}
	vals, ok := php.Eval(pathArg, newRouteScope(f, call, mc.Index))
	if !ok {
		return nil, model.Uncertain
	}
	return dedupe(vals), model.Likely
}

// dedupe removes repeats while preserving order, so output stays byte-stable.
func dedupe(vals []string) []string {
	seen := make(map[string]bool, len(vals))
	out := vals[:0:0]
	for _, v := range vals {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// composePaths joins the file's mount prefix(es), the enclosing group prefixes,
// and the declared path. A file mounted in several places (or with divergent
// mount prefixes) yields one path per mount, like the Express provider.
func composePaths(mc *provider.MatchContext, call php.Node, raw string) []string {
	groups := strings.Join(groupPrefixes(call), "/")
	mounts := mc.Index.MountPrefixes[mc.File.Path()]
	if len(mounts) == 0 {
		mounts = []string{""}
	}
	out := make([]string, 0, len(mounts))
	for _, m := range mounts {
		out = append(out, joinPath(m, groups, raw))
	}
	return out
}

func appendEndpoint(mc *provider.MatchContext, verb, path string, conf model.Confidence) {
	mc.Out.Endpoints = append(mc.Out.Endpoints, model.Endpoint{
		Method:     verb,
		Path:       path,
		Protocol:   model.ProtoREST,
		Detection:  model.DetectRouter,
		Confidence: conf,
	})
}

// groupPrefixes returns the prefixes of the route groups enclosing a
// registration, OUTERMOST first. Groups nest through closures, so this walks
// ancestors and, for every closure that is the argument of a `group(...)` call,
// takes that group's prefix.
func groupPrefixes(call php.Node) []string {
	var prefixes []string
	for n := call.Parent(); n.Valid(); n = n.Parent() {
		switch n.Type() {
		case "anonymous_function_creation_expression", "arrow_function":
			g := enclosingGroupCall(n)
			if !g.Valid() {
				continue
			}
			if p := groupPrefix(g); p != "" {
				prefixes = append(prefixes, p)
			}
		}
	}
	// Collected innermost-first by the upward walk; the URL reads the other way.
	for i, j := 0, len(prefixes)-1; i < j; i, j = i+1, j-1 {
		prefixes[i], prefixes[j] = prefixes[j], prefixes[i]
	}
	return prefixes
}

// enclosingGroupCall returns the `group(...)` call a closure is the argument of,
// or an invalid Node when the closure is something else (a middleware callback,
// a collection map, ...).
func enclosingGroupCall(closure php.Node) php.Node {
	arg := closure.Parent()
	if !arg.Valid() || arg.Type() != "argument" {
		return php.Node{}
	}
	args := arg.Parent()
	if !args.Valid() || args.Type() != "arguments" {
		return php.Node{}
	}
	call := args.Parent()
	if !call.Valid() || !php.IsCall(call) || php.CallName(call) != "group" {
		return php.Node{}
	}
	return call
}

// groupPrefix returns the URL prefix one `group(...)` call contributes, from
// either of Laravel's two forms: the attribute array
// (`Route::group(['prefix' => 'admin'], fn)`) or the fluent chain
// (`Route::prefix('v1')->middleware('auth')->group(fn)`).
func groupPrefix(g php.Node) string {
	for _, e := range php.ArrayEntries(php.PositionalArg(g, 0)) {
		if e.Key == "prefix" {
			if s, ok := php.StringLit(e.Value); ok {
				return s
			}
		}
	}
	return chainPrefix(php.CallReceiver(g))
}

// chainPrefix walks a call chain's receivers collecting `prefix('x')` segments,
// returning them joined in source (outermost-first) order. Every other builder
// method in the chain (middleware, name, domain, controller) contributes
// nothing to the URL path.
func chainPrefix(n php.Node) string {
	var parts []string
	for n.Valid() && php.IsCall(n) {
		if php.CallName(n) == "prefix" {
			if s, ok := php.StringLit(php.PositionalArg(n, 0)); ok {
				parts = append(parts, s)
			}
		}
		n = php.CallReceiver(n)
	}
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	return strings.Join(parts, "/")
}

// joinPath composes path fragments into one normalized absolute path.
func joinPath(parts ...string) string {
	var segs []string
	for _, p := range parts {
		for _, s := range strings.Split(p, "/") {
			if s = strings.TrimSpace(s); s != "" && s != "." {
				segs = append(segs, normalizeSegment(s))
			}
		}
	}
	if len(segs) == 0 {
		return "/"
	}
	return "/" + strings.Join(segs, "/")
}

// normalizeSegment canonicalizes optional path parameters (`{id?}` -> `{id}`).
// Optionality is a routing detail; the graph keys an endpoint on verb + path, and
// a caller's `/users/{id}` must match this service's declaration.
//
// A parameter need not be the whole segment. Laravel appends a format parameter
// directly to a literal (`getLicense{format?}`, koel's Subsonic API), so every
// `{...}` in the segment is normalized, not just a segment that is one.
func normalizeSegment(s string) string {
	var b strings.Builder
	for {
		open := strings.Index(s, "{")
		if open < 0 {
			break
		}
		close := strings.Index(s[open:], "}")
		if close < 0 {
			break
		}
		close += open
		b.WriteString(s[:open])
		name := strings.TrimSpace(strings.TrimSuffix(s[open+1:close], "?"))
		b.WriteString("{" + name + "}")
		s = s[close+1:]
	}
	b.WriteString(s)
	return b.String()
}
