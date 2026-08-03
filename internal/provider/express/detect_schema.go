package express

import (
	"strings"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/tsjs"
	"github.com/farhadamjady/service-discovery/internal/schema"
)

// handlerSchemas best-effort infers a typed Express handler's request and
// response body schemas. Express has no declarative body annotation, so the
// signal is weaker than NestJS's @Body / return type — everything resolved here
// is capped at `likely`. Sources, in priority order:
//
//   request  : Request<_, _, ReqBody> generic (3rd arg) > `req.body as Foo` cast
//   response : Response<Body> generic (1st arg) > Request<_, ResBody, _> (2nd arg)
//              > the local type of the `res.json(x)` / `res.send(x)` argument
//
// Only .ts handlers carry types; plain JS yields (nil, nil). The handler is an
// inline arrow/function expression or a same-file named function.
func handlerSchemas(file *tsjs.File, handler tsjs.Node, walker *schema.Walker) (req, resp *model.Schema) {
	fn := resolveHandler(file, handler)
	if !fn.Valid() {
		return nil, nil
	}
	params := fn.ChildByFieldName("parameters")
	body := fn.ChildByFieldName("body")

	reqType, respType := genericBodyTypes(params)
	if reqType == "" {
		reqType = reqBodyCastType(body)
	}
	if respType == "" {
		respType = resSendArgType(body)
	}

	if reqType != "" {
		req = capLikely(bodyOrNil(walker.Type(normalizeType(reqType))))
	}
	if respType != "" {
		resp = capLikely(bodyOrNil(walker.Type(normalizeType(respType))))
	}
	return req, resp
}

// resolveHandler resolves a route handler argument to its function node: an
// inline arrow/function expression, or a same-file named function referenced by
// identifier (`app.get('/x', getUsers)` -> `function getUsers(...)` or
// `const getUsers = (...) => {...}`).
func resolveHandler(file *tsjs.File, handler tsjs.Node) tsjs.Node {
	switch handler.Type() {
	case "arrow_function", "function_expression", "function":
		return handler
	case "identifier":
		return namedFunction(file, handler.Text())
	}
	return tsjs.Node{}
}

// namedFunction finds a same-file `function name(...)` declaration or a
// `const name = (...) => {...}` / `= function(...)` binding, returning its
// function node.
func namedFunction(file *tsjs.File, name string) tsjs.Node {
	var fn tsjs.Node
	file.Root().Walk(func(n tsjs.Node) bool {
		if fn.Valid() {
			return false
		}
		switch n.Type() {
		case "function_declaration", "function":
			if nm := n.ChildByFieldName("name"); nm.Valid() && nm.Text() == name {
				fn = n
				return false
			}
		case "variable_declarator":
			nm := n.ChildByFieldName("name")
			val := n.ChildByFieldName("value")
			if nm.Valid() && nm.Text() == name && val.Valid() &&
				(val.Type() == "arrow_function" || val.Type() == "function_expression" || val.Type() == "function") {
				fn = val
				return false
			}
		}
		return true
	})
	return fn
}

// genericBodyTypes reads the Express generic parameters off a handler's typed
// params: Request<Params, ResBody, ReqBody> yields the request body (3rd arg) and
// a candidate response body (2nd arg); Response<Body> yields the response body
// (1st arg) and takes precedence for the response.
func genericBodyTypes(params tsjs.Node) (reqType, respType string) {
	if !params.Valid() {
		return "", ""
	}
	for _, p := range tsjs.NamedChildren(params) {
		base, args := paramGeneric(p)
		switch base {
		case "Request":
			if len(args) >= 3 && !emptyType(args[2]) {
				reqType = args[2]
			}
			if respType == "" && len(args) >= 2 && !emptyType(args[1]) {
				respType = args[1]
			}
		case "Response":
			if len(args) >= 1 && !emptyType(args[0]) {
				respType = args[0] // Response<Body> is the authoritative response type
			}
		}
	}
	return reqType, respType
}

// paramGeneric returns the base type name and generic argument texts of a
// parameter's type annotation (`req: Request<A, B, C>` -> "Request", [A,B,C]).
func paramGeneric(param tsjs.Node) (base string, args []string) {
	ta := param.ChildByFieldName("type")
	if !ta.Valid() {
		return "", nil
	}
	gen := tsjs.ChildByType(ta, "generic_type")
	if !gen.Valid() {
		return "", nil
	}
	if id := tsjs.ChildByType(gen, "type_identifier"); id.Valid() {
		base = id.Text()
	}
	if ta := tsjs.ChildByType(gen, "type_arguments"); ta.Valid() {
		for _, a := range tsjs.NamedChildren(ta) {
			args = append(args, strings.TrimSpace(a.Text()))
		}
	}
	return base, args
}

// emptyType reports whether a generic argument carries no real type — Express
// handlers commonly write `Request<{}, ...>` or `Request<any, ...>` to skip a
// slot.
func emptyType(t string) bool {
	switch strings.TrimSpace(t) {
	case "", "{}", "any", "unknown", "never", "void":
		return true
	}
	return false
}

// reqBodyCastType finds a `req.body as Foo` / `request.body as Foo` cast in the
// handler body and returns the asserted type name.
func reqBodyCastType(body tsjs.Node) string {
	found := ""
	body.Walk(func(n tsjs.Node) bool {
		if found != "" || n.Type() != "as_expression" {
			return found == ""
		}
		kids := tsjs.NamedChildren(n)
		if len(kids) == 2 && isReqBody(kids[0]) {
			found = kids[1].Text()
			return false
		}
		return true
	})
	return found
}

// isReqBody reports whether an expression is `<req>.body` (the parameter name is
// conventionally req/request, but any receiver's `.body` member qualifies).
func isReqBody(n tsjs.Node) bool {
	return n.Type() == "member_expression" &&
		n.ChildByFieldName("property").Valid() &&
		n.ChildByFieldName("property").Text() == "body"
}

// resSendArgType finds a `res.json(x)` / `res.send(x)` call in the handler body
// and resolves the argument's type: a local variable's declared/annotated type,
// a `x as Foo` cast, or a `new Foo()` / class instantiation.
func resSendArgType(body tsjs.Node) string {
	locals := localTypes(body)
	found := ""
	body.Walk(func(n tsjs.Node) bool {
		if found != "" || n.Type() != "call_expression" {
			return found == ""
		}
		fn := n.ChildByFieldName("function")
		if fn.Type() != "member_expression" {
			return true
		}
		switch fn.ChildByFieldName("property").Text() {
		case "json", "send":
		default:
			return true
		}
		for _, a := range tsjs.NamedChildren(n.ChildByFieldName("arguments")) {
			if t := exprType(a, locals); t != "" {
				found = t
				return false
			}
		}
		return true
	})
	return found
}

// localTypes maps a handler body's locally-declared variable names to their type
// text: `const x: T = ...`, `const x = expr as T`, and `const x = new T()`.
func localTypes(body tsjs.Node) map[string]string {
	locals := map[string]string{}
	body.Walk(func(n tsjs.Node) bool {
		if n.Type() != "variable_declarator" {
			return true
		}
		name := n.ChildByFieldName("name")
		if !name.Valid() || name.Type() != "identifier" {
			return true
		}
		if ta := n.ChildByFieldName("type"); ta.Valid() {
			locals[name.Text()] = typeText(ta)
			return true
		}
		if val := n.ChildByFieldName("value"); val.Valid() {
			if t := valueType(val); t != "" {
				locals[name.Text()] = t
			}
		}
		return true
	})
	return locals
}

// exprType resolves an expression to a type name: a bare identifier via the
// locals map, a cast/instantiation directly.
func exprType(expr tsjs.Node, locals map[string]string) string {
	if expr.Type() == "identifier" {
		return locals[expr.Text()]
	}
	return valueType(expr)
}

// valueType reads a type off an initializer expression: `expr as T` -> T,
// `new T(...)` -> T.
func valueType(val tsjs.Node) string {
	switch val.Type() {
	case "as_expression":
		if kids := tsjs.NamedChildren(val); len(kids) == 2 {
			return kids[1].Text()
		}
	case "new_expression":
		if c := val.ChildByFieldName("constructor"); c.Valid() {
			return baseTypeName(c)
		}
	}
	return ""
}

// bodyOrNil drops a void schema (no request/response body).
func bodyOrNil(s *model.Schema) *model.Schema {
	if s == nil || s.Type == "void" {
		return nil
	}
	return s
}

// capLikely caps a schema's confidence at `likely`: Express handler typing is a
// weaker signal than a declared contract, so a resolved type is `likely`, not
// `confirmed`; an already-uncertain schema stays uncertain.
func capLikely(s *model.Schema) *model.Schema {
	if s != nil && s.Confidence == model.Confirmed {
		s.Confidence = model.Likely
	}
	return s
}
