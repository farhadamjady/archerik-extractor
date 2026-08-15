package nestjs

import (
	"strings"

	"github.com/farhadamjady/archerik-extractor/internal/model"
	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/tsjs"
	"github.com/farhadamjady/archerik-extractor/internal/provider/tsobj"
)

// maxExprHops bounds the value-expression chase (identifier -> initializer ->
// property -> ...) so a pathological or self-referential body can't loop.
const maxExprHops = 6

// objectLiteralSchema types a `return { … }` handler — the envelope pattern,
// where the controller assembles the wire object inline rather than returning a
// DTO (#67). The literal's structure is read by the shared tsobj reader; what
// this provider contributes is how a VALUE is chased back to a declared type.
func objectLiteralSchema(obj tsjs.Node, method tsjs.Node, rc respCtx, depth int) *model.Schema {
	return tsobj.Schema(obj, depth,
		func(expr tsjs.Node) string { return rc.exprType(expr, method, 0) },
		func(t string) *model.Schema {
			nt, nullable := normalizeTypeAlias(t, rc.aliases)
			s := bodyOrNil(rc.fieldWalker.Type(nt))
			if s != nil && nullable {
				s.Nullable = true
			}
			return s
		})
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
