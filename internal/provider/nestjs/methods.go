package nestjs

import (
	"github.com/farhadamjady/archerik-extractor/internal/model"
	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/tsjs"
	"github.com/farhadamjady/archerik-extractor/internal/schema"
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

// inferResponseFromBody types a handler that has no return-type annotation by
// resolving `return [await] this.<field>.<method>(...)` — the field's declared
// type + that type's method return, walked like a normal response (#62).
func inferResponseFromBody(method tsjs.Node, methodReturns map[string]map[string]string, ctrlFields map[string]string, aliases map[string]tsAlias, walker *schema.Walker) *model.Schema {
	body := tsjs.ChildByType(method, "statement_block")
	if !body.Valid() {
		return nil
	}
	var call tsjs.Node
	body.Walk(func(n tsjs.Node) bool {
		if call.Valid() || n.Type() != "return_statement" {
			return !call.Valid()
		}
		if c := returnCall(n); c.Valid() {
			call = c
			return false
		}
		return true
	})
	field, methodName := thisFieldMethod(call)
	if field == "" || methodName == "" {
		return nil
	}
	ft, ok := ctrlFields[field]
	if !ok {
		return nil
	}
	byMethod, ok := methodReturns[simpleName(ft)]
	if !ok {
		return nil
	}
	rt, ok := byMethod[methodName]
	if !ok {
		return nil
	}
	nt, nullable := normalizeTypeAlias(rt, aliases)
	s := bodyOrNil(walker.Type(nt))
	if s != nil && nullable {
		s.Nullable = true
	}
	return s
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
