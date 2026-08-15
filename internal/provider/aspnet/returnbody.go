package aspnet

import (
	"strings"

	"github.com/farhadamjady/archerik-extractor/internal/model"
	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/csharp"
	"github.com/farhadamjady/archerik-extractor/internal/schema"
)

// inferResponseFromBody types an action whose DECLARED return type says nothing —
// `IActionResult`, `ActionResult`, `Task<IActionResult>` — by reading what it
// actually returns (#67). Two shapes, in order:
//
//	return Ok(new { data = x, total = n });   the anonymous object IS the body
//	return Ok(dto);                           a named type reached through a local
//
// These are not edge cases: `IActionResult` is the idiomatic action return in
// ASP.NET precisely because it lets an action return different status codes, and
// the payload then exists only inside the method. Reading the declared type
// alone leaves every such endpoint with no contract at all.
//
// Only the SUCCESS result is a contract. `NotFound()`, `BadRequest(err)` and
// friends carry the error envelope, which must never be presented as the
// response — the same rule the Go provider learned in #58.
func inferResponseFromBody(method csharp.Node, walker *schema.Walker) *model.Schema {
	body := csharp.ChildByType(method, "block")
	if !body.Valid() {
		// Expression-bodied action: `public IActionResult Get() => Ok(new { … });`
		body = csharp.ChildByType(method, "arrow_expression_clause")
		if !body.Valid() {
			return nil
		}
	}

	arg := successResultArg(body)
	if !arg.Valid() {
		return nil
	}
	if arg.Type() == "anonymous_object_creation_expression" {
		return anonymousObjectSchema(arg, body, walker)
	}
	if t := exprType(arg, body); t != "" {
		s := bodyOrNil(walker.Nested().Type(t))
		if s != nil && s.Confidence == model.Confirmed {
			// Recovered from the body rather than declared: one indirection.
			s.Confidence = model.Likely
		}
		return s
	}
	return nil
}

// successResult are the IActionResult helpers whose argument is the 2xx payload.
// Deliberately excludes NotFound/BadRequest/Problem/Conflict/Unauthorized — those
// carry an error envelope, not the contract.
var successResult = map[string]bool{
	"Ok": true, "Created": true, "CreatedAtAction": true, "CreatedAtRoute": true,
	"Accepted": true, "AcceptedAtAction": true, "AcceptedAtRoute": true,
}

// successResultArg finds the payload argument of the first success result in the
// body: the LAST argument of `Ok(x)` / `CreatedAtRoute(name, values, x)`, since
// the payload always comes last in those overloads. An invalid node when the
// action returns no success payload (`Ok()`, `NoContent()`, only error results).
func successResultArg(body csharp.Node) csharp.Node {
	var found csharp.Node
	body.Walk(func(n csharp.Node) bool {
		if found.Valid() {
			return false
		}
		if n.Type() != "invocation_expression" {
			return true
		}
		fn := n.ChildByFieldName("function")
		if !fn.Valid() || !successResult[lastIdent(fn.Text())] {
			return true
		}
		args := n.ChildByFieldName("arguments")
		if !args.Valid() {
			return true
		}
		kids := csharp.NamedChildren(args)
		if len(kids) == 0 {
			return true // Ok() — no body
		}
		last := kids[len(kids)-1]
		// An argument_list wraps each argument; unwrap to the expression.
		if last.Type() == "argument" {
			if inner := csharp.NamedChildren(last); len(inner) > 0 {
				last = inner[0]
			}
		}
		found = last
		return false
	})
	return found
}

// lastIdent reduces a possibly-qualified callee to its method name
// (`this.Ok` / `base.Ok` -> `Ok`).
func lastIdent(s string) string {
	if i := strings.LastIndexByte(s, '.'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// anonymousObjectSchema reads `new { data = x, total = 1 }` as the response body.
// The member NAMES are written in the source, so the object is confirmed; each
// value is typed where the trail allows and stays an uncertain-but-named field
// otherwise.
func anonymousObjectSchema(obj, body csharp.Node, walker *schema.Walker) *model.Schema {
	fields := walker.Nested()
	s := &model.Schema{Type: "object", Required: model.ReqUnknown, Confidence: model.Confirmed}
	for _, m := range anonymousMembers(obj) {
		f := model.Schema{Name: m.name, Type: "object", Required: model.ReqUnknown, Confidence: model.Uncertain}
		switch {
		case literalKind(m.value) != "":
			f.Type = literalKind(m.value)
			f.Confidence = model.Confirmed
		default:
			if t := exprType(m.value, body); t != "" {
				if vs := bodyOrNil(fields.Type(t)); vs != nil {
					vs.Name = m.name
					if vs.Confidence == model.Confirmed {
						vs.Confidence = model.Likely
					}
					f = *vs
				}
			}
		}
		s.Nested = append(s.Nested, f)
	}
	return s
}

// anonMember is one `name = value` (or shorthand `name`) of an anonymous object.
type anonMember struct {
	name  string
	value csharp.Node
}

// anonymousMembers recovers an anonymous object's members. The grammar gives no
// declarator node and no fields — `new { a = x, b }` is the flat child list
// [a, x, b] — so the SEPARATOR text decides: an `=` between two children binds
// the first (a name) to the second (its value); otherwise the child is a
// shorthand member that names itself (`b` is the field `b`).
func anonymousMembers(obj csharp.Node) []anonMember {
	kids := csharp.NamedChildren(obj)
	var out []anonMember
	for i := 0; i < len(kids); i++ {
		k := kids[i]
		if k.Type() != "identifier" {
			continue // a stray value with no name before it; nothing to call it
		}
		if i+1 < len(kids) && strings.Contains(k.Between(kids[i+1]), "=") {
			out = append(out, anonMember{name: k.Text(), value: kids[i+1]})
			i++ // consume the value
			continue
		}
		// Shorthand: `new { user }` projects the variable under its own name.
		out = append(out, anonMember{name: k.Text(), value: k})
	}
	return out
}

// literalKind maps a C# literal node to its JSON type, "" when not a literal.
func literalKind(n csharp.Node) string {
	if !n.Valid() {
		return ""
	}
	switch n.Type() {
	case "string_literal", "verbatim_string_literal", "interpolated_string_expression", "character_literal":
		return "string"
	case "integer_literal", "real_literal":
		return "number"
	case "boolean_literal":
		return "boolean"
	}
	return ""
}

// exprType resolves an expression to a declared type name within the action
// body: `new Dto(...)` / `new Dto { … }` directly, a cast, or a local declared
// with an explicit type (`Dto d = …`). `var` locals carry no written type, so
// they resolve only when their initializer does.
func exprType(expr, body csharp.Node) string {
	if !expr.Valid() {
		return ""
	}
	switch expr.Type() {
	case "object_creation_expression":
		if t := expr.ChildByFieldName("type"); t.Valid() {
			return t.Text()
		}
	case "cast_expression":
		if t := expr.ChildByFieldName("type"); t.Valid() {
			return t.Text()
		}
	case "identifier":
		return localType(body, expr.Text())
	case "await_expression", "parenthesized_expression":
		for _, c := range csharp.NamedChildren(expr) {
			if t := exprType(c, body); t != "" {
				return t
			}
		}
	}
	return ""
}

// localType finds a local's declared type in the action body, following an
// implicit `var` to its initializer.
func localType(body csharp.Node, name string) string {
	found := ""
	body.Walk(func(n csharp.Node) bool {
		if found != "" || n.Type() != "variable_declaration" {
			return found == ""
		}
		kids := csharp.NamedChildren(n)
		if len(kids) < 2 {
			return true
		}
		declType := kids[0]
		for _, d := range kids[1:] {
			if d.Type() != "variable_declarator" {
				continue
			}
			nm := csharp.NamedChildren(d)
			if len(nm) == 0 || nm[0].Text() != name {
				continue
			}
			if declType.Type() != "implicit_type" {
				found = declType.Text()
				return false
			}
			if len(nm) > 1 {
				found = exprType(nm[1], body) // `var x = new Dto()`
			}
			return false
		}
		return true
	})
	return found
}
