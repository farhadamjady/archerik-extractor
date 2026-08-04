package quarkus

import (
	"strings"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/java"
	"github.com/farhadamjady/service-discovery/internal/schema"
)

// methodIndexer records every type's method return types into Index.MethodReturns
// so the JAX-RS body-inference path (#64) can type a `service.method(...)` payload
// even when the handler's declared return type is the opaque `Response`. It sees
// the scanned files plus shared modules (same rule as the type index).
type methodIndexer struct{}

func (methodIndexer) Name() string { return "quarkus.methods" }

func (methodIndexer) Index(ic *provider.IndexContext, idx *provider.Index) error {
	m := map[string]map[string]string{}
	index := func(files map[string]provider.ParsedFile) {
		for _, pf := range files {
			jf, ok := pf.(*java.File)
			if !ok {
				continue
			}
			jf.Root().Walk(func(n java.Node) bool {
				switch n.Type() {
				case "class_declaration", "interface_declaration":
					indexTypeMethods(n, m)
				}
				return true
			})
		}
	}
	index(ic.Parsed)
	index(ic.Shared)
	idx.MethodReturns = m
	return nil
}

// indexTypeMethods records typeName.methodName -> returnType for one type decl.
func indexTypeMethods(typeDecl java.Node, m map[string]map[string]string) {
	nameNode := typeDecl.ChildByFieldName("name")
	if !nameNode.Valid() {
		return
	}
	name := nameNode.Text()
	body := typeDecl.ChildByFieldName("body")
	if !body.Valid() {
		return
	}
	for _, member := range java.NamedChildren(body) {
		if member.Type() != "method_declaration" {
			continue
		}
		mn := member.ChildByFieldName("name")
		rt := member.ChildByFieldName("type")
		if !mn.Valid() || !rt.Valid() {
			continue
		}
		byMethod := m[name]
		if byMethod == nil {
			byMethod = map[string]string{}
			m[name] = byMethod
		}
		if _, exists := byMethod[mn.Text()]; !exists { // first declaration wins
			byMethod[mn.Text()] = rt.Text()
		}
	}
}

// responseBodyResolver types the payload of a JAX-RS handler that returns the
// opaque `javax/jakarta.ws.rs.core.Response`. It reads the field types of the
// enclosing resource class and the repo-wide method-return index, so a
// `Response.ok().entity(orderService.findAll(page))` chain resolves to
// OrderService.findAll's return type (#64).
type responseBodyResolver struct {
	fields  map[string]string            // field name -> declared type (enclosing class)
	methods map[string]map[string]string // Index.MethodReturns
}

func newResponseBodyResolver(class java.Node, methods map[string]map[string]string) responseBodyResolver {
	return responseBodyResolver{fields: classFieldTypes(class), methods: methods}
}

// classFieldTypes maps a class's field names to their declared type text.
func classFieldTypes(class java.Node) map[string]string {
	out := map[string]string{}
	body := class.ChildByFieldName("body")
	if !body.Valid() {
		return out
	}
	for _, member := range java.NamedChildren(body) {
		if member.Type() != "field_declaration" {
			continue
		}
		ty := member.ChildByFieldName("type")
		if !ty.Valid() {
			continue
		}
		for _, d := range java.NamedChildren(member) {
			if d.Type() == "variable_declarator" {
				if nm := d.ChildByFieldName("name"); nm.Valid() {
					out[nm.Text()] = ty.Text()
				}
			}
		}
	}
	return out
}

// resolve finds the response body schema for a method returning `Response`: it
// scans the body for the entity/ok payload of a success (ok/created) chain,
// resolves that expression's type, and walks it. Returns nil when no typed body
// is recoverable (e.g. `Response.ok().build()`, or a helper-wrapped payload).
func (r responseBodyResolver) resolve(method java.Node, walker *schema.Walker) *model.Schema {
	body := java.ChildByType(method, "block")
	if !body.Valid() {
		return nil
	}
	var payload java.Node
	body.Walk(func(n java.Node) bool {
		if payload.Valid() || n.Type() != "return_statement" {
			return !payload.Valid()
		}
		if p := successPayload(n); p.Valid() {
			payload = p
			return false
		}
		return true
	})
	if !payload.Valid() {
		return nil
	}
	t := r.exprType(payload, body)
	if t == "" {
		return nil
	}
	return bodyOrNil(walker.Type(unwrapReactive(t)))
}

// successPayload extracts the body expression from a `return Response.…build()`:
// the argument to `.entity(X)`, or the sole non-status argument to `.ok(X)`, when
// the chain roots at Response.ok(...) / Response.created(...) (a 2xx branch).
// Returns an invalid node for status()/error chains or bodyless responses.
func successPayload(ret java.Node) java.Node {
	var payload, okArg java.Node
	rootOK := false
	// Walk the return's method-invocation chain collecting relevant calls.
	ret.Walk(func(n java.Node) bool {
		if n.Type() != "method_invocation" {
			return true
		}
		name := ""
		if nm := n.ChildByFieldName("name"); nm.Valid() {
			name = nm.Text()
		}
		obj := n.ChildByFieldName("object")
		switch {
		case name == "entity":
			if a := firstArg(n); a.Valid() {
				payload = a
			}
		case name == "ok" && obj.Valid() && obj.Text() == "Response":
			rootOK = true
			if a := firstArg(n); a.Valid() {
				okArg = a
			}
		case name == "created" && obj.Valid() && obj.Text() == "Response":
			rootOK = true
		case name == "status" && obj.Valid() && obj.Text() == "Response":
			rootOK = false // an explicit status chain — treat as non-success here
		}
		return true
	})
	if !rootOK {
		return java.Node{}
	}
	if payload.Valid() {
		return payload
	}
	return okArg
}

// firstArg returns a call's first argument expression.
func firstArg(call java.Node) java.Node {
	if args := call.ChildByFieldName("arguments"); args.Valid() {
		if kids := java.NamedChildren(args); len(kids) > 0 {
			return kids[0]
		}
	}
	return java.Node{}
}

// exprType best-effort types a payload expression:
//   - field.method(args): the field's type -> that type's method return;
//   - a local variable: its declared type (searched in the method block);
//   - new X(...): X;
//   - a bare field identifier: the field's declared type.
func (r responseBodyResolver) exprType(expr, block java.Node) string {
	switch expr.Type() {
	case "method_invocation":
		obj := expr.ChildByFieldName("object")
		nm := expr.ChildByFieldName("name")
		if obj.Valid() && obj.Type() == "identifier" && nm.Valid() {
			if ft, ok := r.fields[obj.Text()]; ok {
				if byMethod, ok := r.methods[simpleName(ft)]; ok {
					return byMethod[nm.Text()]
				}
			}
		}
	case "object_creation_expression":
		if ty := expr.ChildByFieldName("type"); ty.Valid() {
			return ty.Text()
		}
	case "identifier":
		if ft, ok := r.fields[expr.Text()]; ok {
			return ft
		}
		if lt := localVarType(block, expr.Text()); lt != "" {
			return lt
		}
	}
	return ""
}

// localVarType finds a local variable's declared type within a method block.
func localVarType(block java.Node, name string) string {
	found := ""
	block.Walk(func(n java.Node) bool {
		if found != "" || n.Type() != "local_variable_declaration" {
			return found == ""
		}
		ty := n.ChildByFieldName("type")
		for _, d := range java.NamedChildren(n) {
			if d.Type() == "variable_declarator" {
				if nm := d.ChildByFieldName("name"); nm.Valid() && nm.Text() == name && ty.Valid() {
					found = ty.Text()
					return false
				}
			}
		}
		return true
	})
	return found
}

// simpleName drops a generic suffix and any qualifier from a type text
// (List<OrderDTO> -> List; io.x.OrderService -> OrderService).
func simpleName(t string) string {
	t = strings.TrimSpace(t)
	if i := strings.IndexByte(t, '<'); i >= 0 {
		t = t[:i]
	}
	if i := strings.LastIndexByte(t, '.'); i >= 0 {
		t = t[i+1:]
	}
	return strings.TrimSpace(t)
}
