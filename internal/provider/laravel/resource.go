package laravel

import (
	"strings"

	"github.com/farhadamjady/archerik-extractor/internal/provider"
	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/php"
)

// Resource registrars are the one Laravel idiom where endpoints exist that
// appear NOWHERE in the source as paths: `Route::apiResource('posts',
// PostController::class)` is a single statement that registers five routes (six
// with PUT and PATCH both bound to `update`), and `Route::resource` adds the two
// HTML form routes on top. Expanding them is not a guess — the mapping is fixed
// by the framework — so the emitted endpoints stay `confirmed`.

// resourceAction is one endpoint a resource registrar generates. Action is the
// controller method name, which is also what ->only()/->except() filter on.
type resourceAction struct {
	action string
	verb   string
	suffix string // appended to the resource base path; {param} is substituted
}

// apiResourceActions is what Route::apiResource registers. Laravel binds
// `update` to PUT *and* PATCH; the graph keys endpoints on verb + path, so that
// is two endpoints, both filtered by the single `update` action name.
var apiResourceActions = []resourceAction{
	{"index", "GET", ""},
	{"store", "POST", ""},
	{"show", "GET", "/{param}"},
	{"update", "PUT", "/{param}"},
	{"update", "PATCH", "/{param}"},
	{"destroy", "DELETE", "/{param}"},
}

// resourceActions is what Route::resource registers: the API set plus the two
// routes that serve HTML forms.
var resourceActions = append([]resourceAction{
	{"create", "GET", "/create"},
	{"edit", "GET", "/{param}/edit"},
}, apiResourceActions...)

// emitResource expands one resource registration into its endpoints, honouring
// the `->only([...])` / `->except([...])` filters on the registration's chain.
func emitResource(mc *provider.MatchContext, call php.Node, name string, api bool) {
	if name == "" {
		return // dynamic resource name — nothing to expand
	}
	actions := resourceActions
	if api {
		actions = apiResourceActions
	}
	only, except := chainActionFilters(call)
	base, param := resourcePath(name)
	for _, a := range actions {
		if !actionEnabled(a.action, only, except) {
			continue
		}
		suffix := strings.ReplaceAll(a.suffix, "{param}", "{"+param+"}")
		for _, full := range composePaths(mc, call, base+suffix) {
			appendEndpoint(mc, a.verb, full)
		}
	}
}

// resourcePath builds a resource's base path and its own route parameter name.
// A dotted name is a NESTED resource: `posts.comments` is served under
// `posts/{post}/comments`, with `{comment}` identifying the member.
func resourcePath(name string) (base, param string) {
	segs := strings.Split(name, ".")
	var parts []string
	for _, s := range segs[:len(segs)-1] {
		parts = append(parts, s, "{"+routeParam(s)+"}")
	}
	last := segs[len(segs)-1]
	return strings.Join(append(parts, last), "/"), routeParam(last)
}

// actionEnabled applies the registrar's action filters: ->only() is a whitelist,
// ->except() a blacklist.
func actionEnabled(action string, only, except []string) bool {
	if len(only) > 0 && !contains(only, action) {
		return false
	}
	return !contains(except, action)
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// chainActionFilters reads ->only([...]) / ->except([...]) from the chain the
// registration is the receiver of. It stops at the first link that is not a
// method call ON the registration, so an unrelated enclosing call cannot leak
// its arguments into the filter.
func chainActionFilters(call php.Node) (only, except []string) {
	child := call
	for n := call.Parent(); n.Valid(); n = n.Parent() {
		if n.Type() != "member_call_expression" && n.Type() != "nullsafe_member_call_expression" {
			break
		}
		if !n.ChildByFieldName("object").Equal(child) {
			break
		}
		switch php.CallName(n) {
		case "only":
			only = append(only, php.ArrayStrings(php.PositionalArg(n, 0))...)
		case "except":
			except = append(except, php.ArrayStrings(php.PositionalArg(n, 0))...)
		}
		child = n
	}
	return only, except
}

// routeParam is Laravel's resource-parameter naming: the singular of the
// resource name with dashes folded to underscores (`blog-posts` -> `blog_post`).
func routeParam(name string) string {
	return strings.ReplaceAll(singular(name), "-", "_")
}

// irregularSingular covers the plurals Laravel's inflector gets right that a
// suffix rule does not. It is deliberately short: the parameter name is part of
// the endpoint IDENTITY (verb + path), so a wrong singular splits a node in the
// graph — the common API nouns are worth encoding, the long tail is not.
var irregularSingular = map[string]string{
	"people": "person", "children": "child", "men": "man", "women": "woman",
	"teeth": "tooth", "feet": "foot", "geese": "goose", "mice": "mouse",
	"statuses": "status", "aliases": "alias", "addresses": "address",
	"boxes": "box", "taxes": "tax", "matches": "match", "searches": "search",
	"buses": "bus", "quizzes": "quiz", "media": "medium",
	"criteria": "criterion", "analyses": "analysis", "indices": "index",
}

// singular reduces a resource name to the form Laravel uses for its route
// parameter. Resource names are lowercase by convention, and the emitted
// parameter is lowercase regardless.
func singular(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	if v, ok := irregularSingular[s]; ok {
		return v
	}
	switch {
	case strings.HasSuffix(s, "ies") && len(s) > 3:
		return s[:len(s)-3] + "y"
	case strings.HasSuffix(s, "ss"), !strings.HasSuffix(s, "s"):
		return s
	default:
		return s[:len(s)-1]
	}
}
