package laravel

import (
	"strings"

	"github.com/farhadamjady/archerik-extractor/internal/model"
	"github.com/farhadamjady/archerik-extractor/internal/provider"
	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/php"
)

// handlerSchemas resolves the request and response contracts of a route's
// handler. The handler is named, not written, at the registration
// (`[UserController::class, 'update']`), so everything here starts by following
// that name through the class index into another file.
//
// Request, in priority order:
//
//	a FormRequest parameter's rules()  >  an inline $request->validate([...])
//
// Response, in priority order:
//
//	return new SomeResource(...)  >  response()->json([...])  >  return [...]
//
// The precedence is by directness, not by strength of evidence: all three are
// literal, and the first one present is the one the handler actually returns.
func handlerSchemas(mc *provider.MatchContext, handlerArg php.Node) (req, resp *model.Schema) {
	method, ok := resolveHandler(mc.Index, handlerArg)
	if !ok {
		return nil, nil
	}
	res := newResolver(mc.Index)
	return requestSchema(method, res), responseSchema(method, res)
}

// resolveHandler follows a route's handler argument to the method that serves
// it. Laravel spells this three ways:
//
//	[UserController::class, 'update']   the array form
//	'UserController@update'             the legacy string form
//	InvokableController::class          a single-action controller (__invoke)
func resolveHandler(idx *provider.Index, arg php.Node) (php.Node, bool) {
	if !arg.Valid() {
		return php.Node{}, false
	}
	var class, method string
	switch {
	case arg.Type() == "array_creation_expression":
		entries := php.ArrayEntries(arg)
		// The options-array form: `['uses' => 'CronController@cron', 'as' => ...]`.
		// Legacy, but firefly-iii registers its entire API this way, so without it
		// 635 endpoints resolve no contracts at all.
		for _, e := range entries {
			if e.Key != "uses" {
				continue
			}
			s, ok := php.StringLit(e.Value)
			if !ok {
				return php.Node{}, false
			}
			at := indexByte(s, '@')
			if at < 0 {
				return php.Node{}, false
			}
			class, method = s[:at], s[at+1:]
		}
		if class == "" {
			// The positional form: `[UserController::class, 'update']`.
			if len(entries) != 2 || entries[0].Key != "" {
				return php.Node{}, false
			}
			class = entries[0].Value.Text()
			method, _ = php.StringLit(entries[1].Value)
		}
	case arg.Type() == "class_constant_access_expression":
		class, method = arg.Text(), "__invoke"
	default:
		s, ok := php.StringLit(arg)
		if !ok {
			return php.Node{}, false
		}
		if at := indexByte(s, '@'); at >= 0 {
			class, method = s[:at], s[at+1:]
		} else {
			class, method = s, "__invoke"
		}
	}
	cls, ok := lookupClass(idx, arg, class)
	if !ok || method == "" {
		return php.Node{}, false
	}
	return cls.method(method)
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// requestSchema reads the handler's declared request body.
func requestSchema(method php.Node, res *resolver) *model.Schema {
	if s := formRequestSchema(method, res); s != nil {
		return s
	}
	return inlineValidateSchema(method, res)
}

// formRequestSchema resolves a parameter typed as a FormRequest to the shape its
// rules() describes, honouring validationData() when the class declares one.
func formRequestSchema(method php.Node, res *resolver) *model.Schema {
	params := php.ChildByType(method, "formal_parameters")
	if !params.Valid() {
		return nil
	}
	for _, p := range php.NamedChildren(params) {
		if p.Type() != "simple_parameter" {
			continue
		}
		t := php.ChildByType(p, "named_type")
		if !t.Valid() {
			continue
		}
		cls, ok := lookupClass(res.idx, t, t.Text())
		if !ok || !cls.extends("FormRequest") {
			continue
		}
		rules, ok := cls.method("rules")
		if !ok {
			continue
		}
		s := rulesFromMethod(rules, res.walker.Depth(), res)
		if s == nil {
			continue
		}
		s.Type = simpleClassName(t.Text())
		return wrapIfSubkeyed(cls, s)
	}
	return nil
}

// wrapIfSubkeyed applies a FormRequest's validationData() override. A class that
// validates `$this->input('user')` is validating a SUB-OBJECT: the rules describe
// the inner shape, and the real request body is `{"user": {...}}`. Reporting the
// inner shape as the body would understate the payload by a level.
//
// This is the Laravel instance of the NestJS `@Body('key')` wrapping (#63).
func wrapIfSubkeyed(cls phpClass, inner *model.Schema) *model.Schema {
	m, ok := cls.method("validationData")
	if !ok {
		return inner
	}
	key := ""
	php.ChildByType(m, "compound_statement").Walk(func(n php.Node) bool {
		if key != "" || !php.IsCall(n) || php.CallName(n) != "input" {
			return key == ""
		}
		if s, ok := php.StringLit(php.PositionalArg(n, 0)); ok {
			key = s
		}
		return false
	})
	if key == "" {
		return inner
	}
	field := *inner
	field.Name = key
	return &model.Schema{
		Type: "object", Required: model.ReqUnknown, Confidence: inner.Confidence,
		Nested: []model.Schema{field},
	}
}

// inlineValidateSchema reads `$request->validate([...])` in the handler body —
// the same contract as a FormRequest, written in place.
func inlineValidateSchema(method php.Node, res *resolver) *model.Schema {
	body := php.ChildByType(method, "compound_statement")
	if !body.Valid() {
		return nil
	}
	var found *model.Schema
	body.Walk(func(n php.Node) bool {
		if found != nil || !php.IsCall(n) {
			return found == nil
		}
		switch php.CallName(n) {
		case "validate", "validated":
			found = rulesSchema(php.PositionalArg(n, 0), res.walker.Depth())
		}
		return found == nil
	})
	return found
}

// responseSchema reads what the handler returns on its SUCCESS path.
//
// A Laravel handler routinely returns twice — the guard clause first, the real
// payload last:
//
//	if (empty($attrs = $request->validated())) {
//	    return response()->json(['message' => ..., 'errors' => ...], 422);
//	}
//	return new UserResource($user);
//
// Taking the first return reports `{message, errors}` as the endpoint's
// contract: not a miss but a CONFIDENT WRONG answer, which is worse. This is the
// Laravel instance of #58, fixed there for Go with the same rule — a response
// carrying a 4xx/5xx status is never the contract.
func responseSchema(method php.Node, res *resolver) *model.Schema {
	depth := res.walker.Depth()
	// A DECLARED return type wins, as in every other stack. PHP handlers rarely
	// carry one, but a typed codebase says `: UserResource` outright, and that is
	// more direct evidence than reading the body back. A return type naming a
	// framework base (`ResourceCollection`, `JsonResponse`) resolves to nothing
	// and correctly falls through to the body.
	if t := declaredReturnType(method); t != "" {
		if s := res.byClassName(method, t, depth); s != nil {
			return res.wrapByName(method, t, s)
		}
	}
	for _, ret := range returnedExprs(method) {
		if errorResponse(ret) {
			continue
		}
		if s := returnSchema(ret, depth, res); s != nil {
			return s
		}
	}
	return nil
}

// errorResponse reports whether a returned expression is an error response: a
// response-builder chain carrying a literal 4xx/5xx status, or a `->setStatusCode`
// with one. A status that is not a literal is not judged — an unknown status is
// not evidence of an error.
// Laravel's own signature is json($data, $status = 200, $headers = []), so the
// status is argument 1 — but services wrap it. pixelfed's controllers call a
// custom `$this->json($data, $headers, $status)`, where reading argument 1 finds
// the headers array and the 404 goes unnoticed: three endpoints reported
// `{error: "Record not found"}` as their contract.
//
// Since an arbitrary helper's signature is not knowable, every positional
// argument is scanned for an HTTP error status instead. The trade is deliberate:
// a stray 4xx/5xx integer literal in a response call would cost one missed
// schema, while missing a real one publishes a confidently wrong contract, and
// those are not equally bad.
func errorResponse(ret php.Node) bool {
	for n := ret; n.Valid() && php.IsCall(n); n = php.CallReceiver(n) {
		switch php.CallName(n) {
		case "json", "jsonResponse", "make", "response", "setStatusCode", "status", "abort":
		default:
			continue
		}
		for _, a := range php.CallArgs(n) {
			if a.Value.Type() != "integer" {
				continue
			}
			if code := atoiSafe(a.Value.Text()); code >= 400 && code <= 599 {
				return true
			}
		}
	}
	return false
}

func atoiSafe(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0
		}
		n = n*10 + int(s[i]-'0')
	}
	return n
}

// returnSchema types one returned expression.
func returnSchema(ret php.Node, depth int, res *resolver) *model.Schema {
	if !ret.Valid() {
		return nil
	}

	// `return new UserResource($user)` — the dominant Laravel idiom. The
	// resource's $wrap is applied here and only here: it is a property of the
	// top-level response, and the framework drops it for a nested resource.
	if ret.Type() == "object_creation_expression" {
		if s := res.resourceSchema(ret, depth); s != nil {
			return applyWrap(res, ret, s)
		}
		return nil
	}
	// `return ['data' => ...]` — the shape stated outright.
	if ret.Type() == "array_creation_expression" {
		return arraySchema(ret, depth, res)
	}
	if php.IsCall(ret) {
		if s := res.staticCollectionSchema(ret, depth); s != nil {
			return s
		}
		return responseCallSchema(ret, depth, res)
	}
	return nil
}

// responseCallSchema reads the response-builder chain. `response()->json([...])`
// puts the payload in the first argument; the chain may continue
// (`->json([...], 200)`, `->json($x)->header(...)`), so the whole chain is
// searched for the json() link rather than only its outermost call.
func responseCallSchema(ret php.Node, depth int, res *resolver) *model.Schema {
	for n := ret; n.Valid() && php.IsCall(n); n = php.CallReceiver(n) {
		switch php.CallName(n) {
		case "json", "jsonResponse":
			arg := php.PositionalArg(n, 0)
			switch {
			case arg.Type() == "array_creation_expression":
				return arraySchema(arg, depth, res)
			case arg.Type() == "object_creation_expression":
				return res.resourceSchema(arg, depth)
			}
			return nil
		case "noContent":
			return nil // 204: no body is a real answer, and it is not a schema
		}
		// The chain may be rooted in the payload itself rather than in a
		// response() helper: `(new UserResource($u))->response()->setStatusCode(201)`
		// is how Laravel returns a resource with a custom status, and it is the
		// standard shape of a 201 handler.
		if recv := unwrapParens(php.CallReceiver(n)); recv.Valid() && recv.Type() == "object_creation_expression" {
			if s := res.resourceSchema(recv, depth); s != nil {
				return applyWrap(res, recv, s)
			}
			return nil
		}
	}
	return nil
}

// declaredReturnType returns a method's declared return type, or "". The type
// node sits directly under the method_declaration, while parameter types are
// nested inside formal_parameters, so there is no ambiguity. A nullable `?T`
// yields T — nullability of the whole body is not modeled.
func declaredReturnType(method php.Node) string {
	for _, k := range php.NamedChildren(method) {
		switch k.Type() {
		case "named_type", "optional_type", "primitive_type":
			return simpleClassName(strings.TrimPrefix(k.Text(), "?"))
		case "compound_statement":
			return "" // reached the body without a type
		}
	}
	return ""
}

// unwrapParens strips redundant parentheses, which PHP requires around a `new`
// whose result is immediately called.
func unwrapParens(n php.Node) php.Node {
	for n.Valid() && n.Type() == "parenthesized_expression" {
		kids := php.NamedChildren(n)
		if len(kids) != 1 {
			return n
		}
		n = kids[0]
	}
	return n
}

// applyWrap nests a resource's payload under its `$wrap` key, which is how
// Laravel serializes a wrapped resource (`{"user": {...}}`).
func applyWrap(res *resolver, newExpr php.Node, inner *model.Schema) *model.Schema {
	if newExpr.NamedChildCount() == 0 {
		return inner
	}
	cls, ok := lookupClass(res.idx, newExpr, newExpr.NamedChild(0).Text())
	if !ok {
		return inner
	}
	key, ok := cls.staticString("wrap")
	if !ok || key == "" {
		return inner
	}
	field := *inner
	field.Name = key
	return &model.Schema{
		Type: "object", Required: model.ReqUnknown, Confidence: inner.Confidence,
		Nested: []model.Schema{field},
	}
}
