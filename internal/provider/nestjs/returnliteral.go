package nestjs

import (
	"strings"

	"github.com/farhadamjady/archerik-extractor/internal/model"
	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/tsjs"
)

// maxExprHops bounds the value-expression chase (identifier -> initializer ->
// property -> ...) so a pathological or self-referential body can't loop.
const maxExprHops = 6

// objectLiteralSchema types a `return { … }` handler — the envelope pattern,
// where the controller assembles the wire object inline rather than returning a
// DTO (#67). The literal IS the response body, which makes its KEYS the contract:
// they are read straight off the AST, so the object itself is confirmed even when
// no value inside it can be typed.
//
// Each value is then chased to a declared type where one exists. A key whose
// value stays unresolvable still ships as an uncertain object — the honesty rule:
// a named field of unknown shape is a better contract than silence.
//
// depth is the remaining nesting budget for literals inside literals; at zero the
// inner object becomes the standard truncation boundary.
func objectLiteralSchema(obj tsjs.Node, method tsjs.Node, rc respCtx, depth int) *model.Schema {
	if depth <= 0 {
		return &model.Schema{Type: "object", Required: model.ReqUnknown, Truncated: true, Confidence: model.Confirmed}
	}
	s := &model.Schema{Type: "object", Required: model.ReqUnknown, Confidence: model.Confirmed}
	for _, p := range tsjs.NamedChildren(obj) {
		switch p.Type() {
		case "pair":
			name, ok := literalKey(p.ChildByFieldName("key"))
			if !ok {
				// A computed key (`{ [k]: v }`) means the key set isn't static.
				s.Confidence = model.Uncertain
				continue
			}
			s.Nested = append(s.Nested, rc.valueSchema(name, p.ChildByFieldName("value"), method, depth))
		case "shorthand_property_identifier":
			// `{ data }` — the key names its own value.
			s.Nested = append(s.Nested, rc.valueSchema(p.Text(), p, method, depth))
		case "spread_element":
			// `{ ...rest }` merges in keys we cannot enumerate statically, so the
			// field list is no longer known to be complete.
			s.Confidence = model.Uncertain
		}
	}
	return s
}

// literalKey returns a property key's name when it is statically known — a bare
// identifier (`data:`), a quoted key (`'data':`), or a numeric key. A computed
// key is not, and reports ok=false.
func literalKey(key tsjs.Node) (string, bool) {
	if !key.Valid() {
		return "", false
	}
	switch key.Type() {
	case "property_identifier":
		return key.Text(), true
	case "string":
		return tsjs.StringValue(key), true
	case "number":
		return key.Text(), true
	}
	return "", false
}

// valueSchema types one key of a returned object literal. The NAME is always
// confirmed — it is written in the source. The TYPE carries its own confidence:
// an inline literal value is confirmed, a value chased through a declared type is
// `likely` (it took an indirection to get there), and an expression that resolves
// to nothing is an uncertain object.
func (rc respCtx) valueSchema(name string, val tsjs.Node, method tsjs.Node, depth int) model.Schema {
	if s, ok := literalValueSchema(val); ok {
		s.Name = name
		s.Required = model.ReqUnknown
		return s
	}
	if val.Valid() && val.Type() == "object" {
		s := objectLiteralSchema(val, method, rc, depth-1)
		s.Name = name
		return *s
	}
	if t := rc.exprType(val, method, 0); t != "" {
		nt, nullable := normalizeTypeAlias(t, rc.aliases)
		if s := bodyOrNil(rc.fieldWalker.Type(nt)); s != nil {
			s.Name = name
			s.Nullable = s.Nullable || nullable
			// Reached through the code rather than declared at this position, so a
			// resolved type is `likely`, not confirmed. A type the walker itself
			// could not resolve (`Promise<any>` -> opaque object) stays uncertain —
			// chasing an expression to a declaration that says nothing is not
			// evidence, and must not read as though it were.
			if s.Confidence == model.Confirmed {
				s.Confidence = model.Likely
			}
			return *s
		}
	}
	return model.Schema{Name: name, Type: "object", Required: model.ReqUnknown, Confidence: model.Uncertain}
}

// literalValueSchema types an inline literal value — the one case where the
// source states the type outright.
func literalValueSchema(val tsjs.Node) (model.Schema, bool) {
	if !val.Valid() {
		return model.Schema{}, false
	}
	switch val.Type() {
	case "string", "template_string":
		return model.Schema{Type: "string", Confidence: model.Confirmed}, true
	case "number":
		return model.Schema{Type: "number", Confidence: model.Confirmed}, true
	case "true", "false":
		return model.Schema{Type: "boolean", Confidence: model.Confirmed}, true
	case "null", "undefined":
		// A literal null names the field but says nothing about its type.
		return model.Schema{Type: "object", Nullable: true, Confidence: model.Uncertain}, true
	}
	return model.Schema{}, false
}

// exprType chases a value expression to a declared TYPE NAME, or "" when the
// trail runs out. It follows the hops that actually carry type information in a
// NestJS controller:
//
//	await x / (x)          transparent
//	x as Foo               the assertion names the type
//	this.svc.method(...)   the #62 field-type + method-return indexes
//	local                  a `const local = <expr>` binding in the handler body
//	expr.prop              the property's declared type on the resolved owner
func (rc respCtx) exprType(expr tsjs.Node, method tsjs.Node, hops int) string {
	if !expr.Valid() || hops > maxExprHops {
		return ""
	}
	switch expr.Type() {
	case "await_expression", "parenthesized_expression", "non_null_expression":
		for _, c := range tsjs.NamedChildren(expr) {
			if t := rc.exprType(c, method, hops+1); t != "" {
				return t
			}
		}
	case "as_expression":
		// `x as Foo` — the assertion is the most direct type statement available.
		if kids := tsjs.NamedChildren(expr); len(kids) == 2 {
			return kids[1].Text()
		}
	case "call_expression":
		return rc.callReturnType(expr)
	case "identifier", "shorthand_property_identifier":
		declared, init := localBinding(method, expr.Text())
		if declared != "" {
			return declared
		}
		return rc.exprType(init, method, hops+1)
	case "member_expression":
		owner := rc.exprType(expr.ChildByFieldName("object"), method, hops+1)
		prop := expr.ChildByFieldName("property")
		if owner == "" || !prop.Valid() {
			return ""
		}
		return rc.fieldType(owner, prop.Text())
	}
	return ""
}

// localBinding finds a `const/let <name> = <init>` declared in the handler body,
// returning its declared type when it has one (`const u: User = …`, which beats
// chasing the initializer) and the initializer expression otherwise. First
// declaration wins.
func localBinding(method tsjs.Node, name string) (declared string, init tsjs.Node) {
	body := tsjs.ChildByType(method, "statement_block")
	if !body.Valid() {
		return "", tsjs.Node{}
	}
	found := false
	body.Walk(func(n tsjs.Node) bool {
		if found || n.Type() != "variable_declarator" {
			return !found
		}
		if nm := n.ChildByFieldName("name"); nm.Valid() && nm.Text() == name {
			found = true
			if ta := n.ChildByFieldName("type"); ta.Valid() {
				declared = typeText(ta)
			}
			init = n.ChildByFieldName("value")
			return false
		}
		return true
	})
	return declared, init
}

// fieldType looks up a property's declared type on an already-resolved owner
// type: `data.users` where data is a `Page` holding `users: User[]` -> "User[]".
func (rc respCtx) fieldType(owner, prop string) string {
	if rc.types == nil {
		return ""
	}
	td, ok := rc.types.Lookup(unwrapAsync(owner))
	if !ok {
		return ""
	}
	for _, f := range td.Fields {
		if f.Name == prop {
			return f.Type
		}
	}
	return ""
}

// asyncWrapper are the containers a service return is wrapped in that carry no
// structure of their own — stripped so a property lookup lands on the payload.
var asyncWrapper = map[string]bool{"Promise": true, "Observable": true, "Optional": true}

// unwrapAsync strips those wrappers: `Promise<Page>` -> `Page`.
func unwrapAsync(t string) string {
	t = strings.TrimSpace(t)
	for i := 0; i < 4; i++ {
		name, args, ok := splitGeneric(t)
		if !ok || !asyncWrapper[name] {
			return t
		}
		parts := splitTopLevel(args, ',')
		if len(parts) == 0 {
			return t
		}
		t = strings.TrimSpace(parts[0])
	}
	return t
}
