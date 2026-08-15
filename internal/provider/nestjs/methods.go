package nestjs

import (
	"github.com/farhadamjady/archerik-extractor/internal/model"
	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/tsjs"
)

// classMethodReturns maps a class's method names to their declared return-type
// text, so a handler that omits its own return annotation can be typed from the
// service method it delegates to (#62). First declaration of a name wins.
func classMethodReturns(class tsjs.Node) map[string]string {
	out := map[string]string{}
	body := tsjs.ChildByType(class, "class_body")
	if !body.Valid() {
		return out
	}
	for _, m := range tsjs.NamedChildren(body) {
		if m.Type() != "method_definition" {
			continue
		}
		name := m.ChildByFieldName("name")
		rt := m.ChildByFieldName("return_type")
		if name.Valid() && rt.Valid() {
			if _, ok := out[name.Text()]; !ok {
				out[name.Text()] = typeText(rt)
			}
		}
	}
	return out
}

// controllerFieldTypes maps a controller's injected dependency names to their
// declared types — constructor parameter-properties (`constructor(private readonly
// svc: ArticleService)`) and plain class fields — so `this.svc.method(...)` can be
// resolved to svc's type (#62).
func controllerFieldTypes(class tsjs.Node) map[string]string {
	out := map[string]string{}
	body := tsjs.ChildByType(class, "class_body")
	if !body.Valid() {
		return out
	}
	for _, m := range tsjs.NamedChildren(body) {
		switch m.Type() {
		case "public_field_definition", "field_definition":
			nm := m.ChildByFieldName("name")
			ta := m.ChildByFieldName("type")
			if nm.Valid() && ta.Valid() {
				out[nm.Text()] = normalizeType(typeText(ta))
			}
		case "method_definition":
			if nm := m.ChildByFieldName("name"); nm.Valid() && nm.Text() == "constructor" {
				addParamProperties(m, out)
			}
		}
	}
	return out
}

// addParamProperties records constructor parameters that are ALSO fields —
// TypeScript makes a parameter a class property when it carries an accessibility
// modifier (public/private/protected) or readonly.
func addParamProperties(ctor tsjs.Node, out map[string]string) {
	params := ctor.ChildByFieldName("parameters")
	if !params.Valid() {
		return
	}
	for _, p := range tsjs.NamedChildren(params) {
		if p.Type() != "required_parameter" || !isParamProperty(p) {
			continue
		}
		pat := p.ChildByFieldName("pattern")
		ta := p.ChildByFieldName("type")
		if pat.Valid() && ta.Valid() {
			out[pat.Text()] = normalizeType(typeText(ta))
		}
	}
}

// isParamProperty reports whether a constructor parameter is promoted to a field
// (has an accessibility_modifier or a readonly modifier child).
func isParamProperty(p tsjs.Node) bool {
	for _, c := range tsjs.NamedChildren(p) {
		if c.Type() == "accessibility_modifier" {
			return true
		}
	}
	// `readonly` shows up as an anonymous child; scan the raw text prefix.
	return len(p.Text()) >= 8 && p.Text()[:8] == "readonly"
}

// inferResponseFromBody types a handler that has no return-type annotation from
// what it actually returns. Two shapes, tried in that order:
//
//   - `return [await] this.<field>.<method>(...)` — the thin controller delegating
//     to a service. The field's declared type + that type's method return, walked
//     like a normal response (#62).
//   - `return { … }` — the controller assembling the wire object inline (#67).
//     The literal IS the body, so it is read as the response shape.
//
// Delegation is checked first across the whole body: it names a declared type,
// which is strictly better evidence than a literal's keys, and checking it first
// leaves #62's behavior untouched on handlers that have both.
func inferResponseFromBody(method tsjs.Node, rc respCtx) *model.Schema {
	body := tsjs.ChildByType(method, "statement_block")
	if !body.Valid() {
		return nil
	}
	if call := firstReturn(body, func(n tsjs.Node) tsjs.Node { return returnCall(n) }); call.Valid() {
		if rt := rc.callReturnType(call); rt != "" {
			nt, nullable := normalizeTypeAlias(rt, rc.aliases)
			s := bodyOrNil(rc.walker.Type(nt))
			if s != nil && nullable {
				s.Nullable = true
			}
			return s
		}
	}
	if obj := firstReturn(body, returnObject); obj.Valid() {
		return objectLiteralSchema(obj, method, rc, rc.fieldWalker.Depth())
	}
	return nil
}

// firstReturn returns the value `pick` extracts from the first return statement
// in the body that yields one — source order, so the same handler always types
// the same way.
func firstReturn(body tsjs.Node, pick func(tsjs.Node) tsjs.Node) tsjs.Node {
	var found tsjs.Node
	body.Walk(func(n tsjs.Node) bool {
		if found.Valid() {
			return false
		}
		if n.Type() != "return_statement" {
			return true
		}
		if v := pick(n); v.Valid() {
			found = v
			return false
		}
		return true
	})
	return found
}

// callReturnType resolves `this.<field>.<method>(...)` to that method's declared
// return type, through the controller's field types and the repo-wide class ->
// method -> return index (#62). "" when any hop is missing.
func (rc respCtx) callReturnType(call tsjs.Node) string {
	field, methodName := thisFieldMethod(call)
	if field == "" || methodName == "" {
		return ""
	}
	ft, ok := rc.ctrlFields[field]
	if !ok {
		return ""
	}
	byMethod, ok := rc.methodReturns[simpleName(ft)]
	if !ok {
		return ""
	}
	return byMethod[methodName]
}

// returnObject returns the object literal a return statement yields, unwrapping a
// leading `await`. An invalid node when the return isn't an object literal.
func returnObject(ret tsjs.Node) tsjs.Node {
	for _, c := range tsjs.NamedChildren(ret) {
		switch c.Type() {
		case "await_expression", "parenthesized_expression":
			for _, cc := range tsjs.NamedChildren(c) {
				if cc.Type() == "object" {
					return cc
				}
			}
		case "object":
			return c
		}
	}
	return tsjs.Node{}
}

// returnCall returns the call_expression a return statement yields, unwrapping a
// leading `await`. An invalid node when the return isn't a call.
func returnCall(ret tsjs.Node) tsjs.Node {
	for _, c := range tsjs.NamedChildren(ret) {
		switch c.Type() {
		case "await_expression":
			for _, cc := range tsjs.NamedChildren(c) {
				if cc.Type() == "call_expression" {
					return cc
				}
			}
		case "call_expression":
			return c
		}
	}
	return tsjs.Node{}
}

// thisFieldMethod extracts (field, method) from a `this.field.method(...)` call:
// the call's function is a member_expression whose property is the method and
// whose object is `this.field`. Returns empty strings otherwise.
func thisFieldMethod(call tsjs.Node) (field, method string) {
	if !call.Valid() {
		return "", ""
	}
	fn := call.ChildByFieldName("function")
	if !fn.Valid() || fn.Type() != "member_expression" {
		return "", ""
	}
	prop := fn.ChildByFieldName("property")
	obj := fn.ChildByFieldName("object")
	if !prop.Valid() || !obj.Valid() || obj.Type() != "member_expression" {
		return "", ""
	}
	inner := obj.ChildByFieldName("object")
	fld := obj.ChildByFieldName("property")
	if !inner.Valid() || inner.Type() != "this" || !fld.Valid() {
		return "", ""
	}
	return fld.Text(), prop.Text()
}
