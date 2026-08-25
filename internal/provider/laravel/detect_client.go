package laravel

import (
	"bytes"
	"strings"

	"github.com/farhadamjady/archerik-extractor/internal/model"
	"github.com/farhadamjady/archerik-extractor/internal/provider"
	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/php"
)

// clientDetector extracts outbound HTTP dependencies from Laravel's Http facade
// — `Http::get($url)` and the chained builder form
// `Http::asForm()->withToken($t)->post('https://api.example.com/oauth2/token')`,
// which is how nearly all modern Laravel code makes an outbound call.
//
// The URL is resolved through the same evaluator the route detector uses, so a
// target assembled from configuration resolves rather than being written off:
//
//	Http::get(config('app.url') . '/api/ping')
//
// is a `.` concatenation of a config value and a literal, and the config layer
// answers `app.url` through `config/app.php` -> `env('APP_URL', ...)` -> `.env`.
// That case is the reason the config layer exists.
//
// Unlike an endpoint, an unresolved dependency IS still emitted — as an
// anonymous `uncertain` edge. The rules differ because the identities differ: an
// endpoint is keyed on verb + path, so one with an unknown path has nothing to
// key on, while a dependency with an unknown target is still the fact that this
// service calls SOMETHING, which the graph should show rather than hide.
type clientDetector struct{}

func (clientDetector) Name() string             { return "laravel.client" }
func (clientDetector) Protocol() model.Protocol { return model.ProtoREST }

func (d clientDetector) Rules() []provider.Rule {
	return []provider.Rule{
		{Query: staticCallQuery, OnMatch: d.onCall},
		{Query: memberCallQuery, OnMatch: d.onCall},
		{Query: `(object_creation_expression) @new`, OnMatch: d.onNew},
	}
}

// httpVerb is the set of Http facade methods that ISSUE a request. Each takes
// the URL as its first argument.
var httpVerb = map[string]bool{
	"get": true, "post": true, "put": true, "patch": true,
	"delete": true, "head": true, "options": true,
}

// onCall handles a request issued on the Http facade. The chain must root at
// `Http::` and the outermost method must be a verb — `Http::getFacadeRoot()` and
// friends are not requests, the same trap that produced a phantom endpoint on the
// Route facade (#70).
func (clientDetector) onCall(mc *provider.MatchContext) {
	call, _ := mc.Captures["call"].(php.Node)
	if !call.Valid() {
		return
	}
	name := php.CallName(call)
	if facadeRoot(call, "Http") {
		urlArg := php.PositionalArg(call, 0)
		switch {
		case httpVerb[name]:
		case name == "send":
			// send($method, $url, ...) — the verb is the first argument.
			urlArg = php.PositionalArg(call, 1)
		default:
			return
		}
		emitHTTPDep(mc, call, urlArg, model.DetectLaravelHTTP)
		return
	}
	guzzleCall(mc, call, name)
}

// guzzleCall handles a request on a Guzzle client held in a variable —
// `$client->get($url)`, `$client->request('GET', $url)`. This is the form
// firefly-iii uses throughout, and without it a service whose only outbound
// calls go through Guzzle reports no dependencies at all.
//
// The receiver must be PROVED to hold a Guzzle client — by an assignment in
// scope for a local variable, or by a declared type for a property. `->get(...)`
// is far too common to claim on the method name alone: it is a collection, a
// cache, and a query builder more often than it is an HTTP client, so an
// unproven receiver is left alone.
func guzzleCall(mc *provider.MatchContext, call php.Node, name string) {
	urlArg := php.PositionalArg(call, 0)
	switch {
	case httpVerb[name]:
	case name == "request":
		urlArg = php.PositionalArg(call, 1)
	default:
		return
	}
	f, ok := mc.File.(*php.File)
	if !ok {
		return
	}
	recv := php.CallReceiver(call)
	switch recv.Type() {
	case "variable_name":
		if !holdsGuzzleClient(f, call, php.VarName(recv)) {
			return
		}
	case "member_access_expression":
		// `$this->client->get(...)`, where the client is injected. This is the
		// form that actually occurs: across five benchmark repos there is not one
		// `$this->client = new Client()` assignment, but koel injects a
		// `private readonly Client $client` and calls GitHub through it.
		if accessObject(recv).Text() != "$this" {
			return
		}
		if !guzzleProperty(mc.Index, f, call, php.ChildByType(recv, "name").Text()) {
			return
		}
	default:
		return
	}
	emitHTTPDep(mc, call, urlArg, model.DetectGuzzle)
}

// guzzleProperty reports whether a property of the class enclosing a call holds
// a Guzzle client, by its DECLARED TYPE — a promoted constructor property
// (`__construct(private readonly Client $client)`), a typed property
// (`private Client $client;`), or an assignment from `new Client(...)`.
//
// The type is what makes this safe: `$this->client->get(...)` is claimed only
// when something in the class says that property is a Guzzle client, so a
// `$this->cache->get(...)` on the next line is untouched.
func guzzleProperty(idx *provider.Index, f *php.File, call php.Node, name string) bool {
	if name == "" {
		return false
	}
	class := enclosingClass(call)
	if !class.Valid() {
		return false
	}
	found := false
	class.Walk(func(n php.Node) bool {
		if found {
			return false
		}
		switch n.Type() {
		case "property_promotion_parameter", "property_declaration":
			if php.VarName(propertyVariable(n)) != name {
				return true
			}
			t := php.ChildByType(n, "named_type")
			if t.Valid() && isGuzzleClientType(idx, t, f.Src()) {
				found = true
			}
		case "assignment_expression":
			left := n.ChildByFieldName("left")
			if left.Type() != "member_access_expression" ||
				accessObject(left).Text() != "$this" ||
				php.ChildByType(left, "name").Text() != name {
				return true
			}
			right := n.ChildByFieldName("right")
			if right.Type() == "object_creation_expression" && guzzleClient(right, f.Src()) {
				found = true
			}
		}
		return !found
	})
	return found
}

// propertyVariable returns the variable_name a property declaration binds,
// wherever the grammar puts it.
func propertyVariable(n php.Node) php.Node {
	if v := php.ChildByType(n, "variable_name"); v.Valid() {
		return v
	}
	if el := php.ChildByType(n, "property_element"); el.Valid() {
		return php.ChildByType(el, "variable_name")
	}
	return php.Node{}
}

// isGuzzleClientType reports whether a type annotation names Guzzle's Client,
// resolved through the file's imports so a same-named class from elsewhere does
// not qualify.
func isGuzzleClientType(idx *provider.Index, t php.Node, src []byte) bool {
	name := strings.TrimPrefix(t.Text(), `\`)
	if strings.HasSuffix(name, `GuzzleHttp\Client`) {
		return true
	}
	if simpleClassName(name) != "Client" {
		return false
	}
	if f := t.File(); f != nil {
		if fqn, ok := f.Imports()["Client"]; ok {
			return fqn == `GuzzleHttp\Client`
		}
	}
	return bytes.Contains(src, []byte(`use GuzzleHttp\Client`))
}

// accessObject returns the object of a property access (`$this` in
// `$this->client`). php.CallReceiver does not apply — a member ACCESS is not a
// call, and asking it for a receiver yields nothing.
func accessObject(n php.Node) php.Node {
	if o := n.ChildByFieldName("object"); o.Valid() {
		return o
	}
	if n.NamedChildCount() > 0 {
		return n.NamedChild(0)
	}
	return php.Node{}
}

// enclosingClass returns the class declaration a node sits in.
func enclosingClass(n php.Node) php.Node {
	for p := n.Parent(); p.Valid(); p = p.Parent() {
		if p.Type() == "class_declaration" {
			return p
		}
	}
	return php.Node{}
}

// holdsGuzzleClient reports whether a variable was assigned a Guzzle client in
// the function enclosing the call, or at file scope. Searching the enclosing
// function rather than the whole file keeps a `$client` in one method from
// vouching for an unrelated `$client` in another.
func holdsGuzzleClient(f *php.File, call php.Node, name string) bool {
	if name == "" {
		return false
	}
	scope := enclosingBody(call)
	if !scope.Valid() {
		scope = f.Root()
	}
	found := false
	scope.Walk(func(n php.Node) bool {
		if found || n.Type() != "assignment_expression" {
			return !found
		}
		left := n.ChildByFieldName("left")
		if left.Type() != "variable_name" || php.VarName(left) != name {
			return true
		}
		right := n.ChildByFieldName("right")
		if right.Type() == "object_creation_expression" && guzzleClient(right, f.Src()) {
			found = true
		}
		return !found
	})
	return found
}

// enclosingBody returns the body of the function, method or closure a node sits
// in, or an invalid Node when it is at file scope.
func enclosingBody(n php.Node) php.Node {
	for p := n.Parent(); p.Valid(); p = p.Parent() {
		switch p.Type() {
		case "method_declaration", "function_definition",
			"anonymous_function_creation_expression", "arrow_function":
			return p
		}
	}
	return php.Node{}
}

// onNew handles `new Client(['base_uri' => 'https://api.example.com'])` — a
// Guzzle client whose target is declared at construction. Only a client carrying
// a base_uri names anything; one configured with timeouts and handlers alone
// (koel's SafeHttp) declares no target and is not an edge.
func (clientDetector) onNew(mc *provider.MatchContext) {
	n, _ := mc.Captures["new"].(php.Node)
	f, ok := mc.File.(*php.File)
	if !n.Valid() || !ok || !guzzleClient(n, f.Src()) {
		return
	}
	for _, e := range php.ArrayEntries(php.PositionalArg(n, 0)) {
		if e.Key == "base_uri" || e.Key == "base_url" {
			emitHTTPDep(mc, n, e.Value, model.DetectGuzzle)
			return
		}
	}
}

// guzzleClient reports whether an object creation names Guzzle's Client. The
// bare `new Client(...)` counts only when the file imports GuzzleHttp\Client —
// `Client` is far too common a class name to claim on its own.
func guzzleClient(n php.Node, src []byte) bool {
	// The instantiated type is the first named child: a `name` for `new Client`,
	// a `qualified_name` for `new \GuzzleHttp\Client`.
	if n.NamedChildCount() == 0 {
		return false
	}
	cls := n.NamedChild(0)
	switch cls.Type() {
	case "name", "qualified_name":
	default:
		return false
	}
	name := strings.TrimPrefix(cls.Text(), `\`)
	switch {
	case strings.HasSuffix(name, `GuzzleHttp\Client`):
		return true
	case name == "Client":
		return strings.Contains(string(src), `use GuzzleHttp\Client`)
	}
	return false
}

// emitHTTPDep resolves a URL expression and appends the resulting edge.
func emitHTTPDep(mc *provider.MatchContext, call, urlArg php.Node, detection model.DetectionMethod) {
	dep := model.Dependency{Protocol: model.ProtoREST, Detection: detection, Confidence: model.Uncertain}
	urls, conf, via := resolveURL(mc, call, urlArg)
	if len(urls) == 0 {
		mc.Out.OutboundDependencies = append(mc.Out.OutboundDependencies, dep)
		return
	}
	dep.ResolvedVia = via
	// One call site that resolves to several URLs is one-of-N, not N calls.
	group := ""
	if len(urls) > 1 {
		group = mc.File.Path() + ":" + urls[0]
	}
	for _, u := range urls {
		d := dep
		d.URL, d.Confidence = u, conf
		d.Conditional, d.CandidateGroup = group != "", group
		if host := authority(u); host != "" {
			d.TargetName, d.Resolved = host, true
		} else {
			// A relative URL names no service — the path is real, the target is not.
			d.Confidence = model.Uncertain
		}
		mc.Out.OutboundDependencies = append(mc.Out.OutboundDependencies, d)
	}
}

// resolveURL evaluates a URL argument in the call's scope. A literal is
// `confirmed`; anything reached through a variable or config is `likely`, with
// the config file that supplied it returned as provenance.
//
// The provenance matters here more than anywhere else so far: a URL resolved
// out of `.env.example` is a placeholder the repo ships, not the address the
// service dials in production, and `resolved_via` is what lets a reader tell
// the two apart.
func resolveURL(mc *provider.MatchContext, call, urlArg php.Node) ([]string, model.Confidence, string) {
	if !urlArg.Valid() {
		return nil, model.Uncertain, ""
	}
	if lit, ok := php.StringLit(urlArg); ok {
		return []string{lit}, model.Confirmed, "" // a literal has nothing to explain
	}
	f, ok := mc.File.(*php.File)
	if !ok {
		return nil, model.Uncertain, ""
	}
	sc := newRouteScope(f, call, mc.Index)
	vals, ok := php.Eval(urlArg, sc)
	if !ok || len(vals) == 0 {
		return nil, model.Uncertain, ""
	}
	return dedupe(vals), model.Likely, sc.sourceUsed()
}

// authority returns host[:port] of an absolute URL, or "" when the string is not
// one. Same rule as the Express provider, so a host node means the same thing in
// every stack's output.
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
